package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/depot/services/receivables/internal/events"
)

const reverseLedgerID = "00000000-0000-0000-0000-0000000000b1"

type appliedFixture struct {
	s             *Postgres
	ctx           context.Context
	ledger        *ledgerclient.Fake
	pay           Payment
	applied       ApplyResult
	first, second OpenItem
}

func appliedSetup(t *testing.T) appliedFixture {
	t.Helper()
	s, ctx, items := creditSetup(t)
	fake := ledgerclient.NewFake()
	fake.AddLedger(reverseLedgerID, legalEntity)
	s.WithLedger(fake).WithGLLedgers(map[string]string{legalEntity: reverseLedgerID})
	first, second := items["INV-2027-0000003/1"], items["INV-2027-0000003/2"]
	cust := first.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("500.00"), UnappliedAmount: amt("500.00"), Currency: first.Currency, ReceivedDate: "2027-05-01", ValueDate: "2027-05-01"}, "ra-pay")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Residual: amt("0.50"), Allocations: []domain.Allocation{
		{OpenItemID: first.ID, Amount: amt("333.34")}, {OpenItemID: second.ID, Amount: amt("166.16"), WriteOff: amt("0.50")}}}, "ra-apply")
	if err != nil {
		t.Fatal(err)
	}
	return appliedFixture{s: s, ctx: ctx, ledger: fake, pay: pay, applied: applied, first: first, second: second}
}

const originalPostingDate = "2020-03-10"

func (f appliedFixture) backdate(t *testing.T) {
	t.Helper()
	execAdmin(t, f.s, `update payment_application set posting_date = $2 where id = $1`, f.applied.ApplicationID, originalPostingDate)
}

func (f appliedFixture) reverse(key string, version int) (ReverseApplicationResult, error) {
	return f.s.ReverseApplication(f.ctx, f.pay.ID, version, ReverseApplicationRequest{ApplicationID: f.applied.ApplicationID, Reason: "wrong invoice"}, key)
}

func (f appliedFixture) unchanged(t *testing.T) {
	t.Helper()
	pay, err := f.s.GetPayment(f.ctx, f.pay.ID)
	if err != nil || pay.Version != f.applied.Payment.Version || pay.Status != "ON_ACCOUNT" || !pay.UnappliedAmount.Equal(amt("0.50")) {
		t.Fatalf("payment changed: %+v %v", pay, err)
	}
	if it, err := f.s.GetOpenItem(f.ctx, f.first.ID); err != nil || !it.OpenAmount.IsZero() || it.Status != "SETTLED" {
		t.Fatalf("first item changed: %+v %v", it, err)
	}
	if n := countOutbox(t, f.s, f.ctx, rcvevents.PaymentApplicationReversed); n != 0 {
		t.Fatalf("a refused reversal published %d events", n)
	}
	if n := len(f.ledger.Entries()); n != 0 {
		t.Fatalf("a refused reversal posted %d ledger entries", n)
	}
	if status, _ := f.application(t); status != "APPLIED" {
		t.Fatalf("application status %s", status)
	}
}

func (f appliedFixture) application(t *testing.T) (status string, reason *string) {
	t.Helper()
	err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select status, reversal_reason from payment_application where id = $1`, f.applied.ApplicationID).Scan(&status, &reason)
	})
	if err != nil {
		t.Fatal(err)
	}
	return status, reason
}

func (f appliedFixture) settlementRows(t *testing.T) (total, reversals int) {
	t.Helper()
	err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select count(*), count(*) filter (where reversal_of_id is not null) from settlement where payment_id = $1`, f.pay.ID).Scan(&total, &reversals)
	})
	if err != nil {
		t.Fatal(err)
	}
	return total, reversals
}

func TestReverseApplication_ReopensItemsMirrorsTheJournalAndDeletesNothing(t *testing.T) {
	f := appliedSetup(t)
	if !ids.Valid(f.applied.ApplicationID) {
		t.Fatalf("apply returned application id %q", f.applied.ApplicationID)
	}
	f.backdate(t)
	out, err := f.reverse("ra-1", f.applied.Payment.Version)
	if err != nil {
		t.Fatal(err)
	}
	if out.ApplicationID != f.applied.ApplicationID || out.ReversalJournalEntryID == "" || len(out.Reopened) != 2 ||
		out.Payment.Status != "RECEIVED" || !out.Payment.UnappliedAmount.Equal(amt("500.00")) || out.Payment.Version != f.applied.Payment.Version+1 {
		t.Fatalf("result %+v", out)
	}
	for _, it := range []OpenItem{f.first, f.second} {
		if got, err := f.s.GetOpenItem(f.ctx, it.ID); err != nil || !got.OpenAmount.Equal(it.OpenAmount) || got.Status != it.Status {
			t.Fatalf("%s not back to %s %s: %+v %v", it.DocumentNumber, it.OpenAmount, it.Status, got, err)
		}
	}
	entries := f.ledger.Entries()
	if len(entries) != 1 {
		t.Fatalf("ledger entries %d", len(entries))
	}
	e := entries[0]
	if e.ID != out.ReversalJournalEntryID || e.IdempotencyKey != "post:receivables:paymentapplication:"+f.applied.ApplicationID+":reversal" || e.PostingDate != originalPostingDate || e.PostingDate == today() {
		t.Fatalf("entry %+v", e.Entry)
	}
	want := map[string]string{"1200/D": "500.00", "1205/C": "499.50", domain.AccountWriteOff + "/C": "0.50"}
	if len(e.Lines) != len(want) {
		t.Fatalf("lines %+v", e.Lines)
	}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		if w, ok := want[l.Account+"/"+side]; !ok || !l.Amount.Equal(amt(w)) {
			t.Fatalf("line %+v, want %v", l, want)
		}
	}
	if status, reason := f.application(t); status != "REVERSED" || reason == nil || *reason != "wrong invoice" {
		t.Fatalf("application %s %v", status, reason)
	}
	if total, reversals := f.settlementRows(t); total != 4 || reversals != 2 {
		t.Fatalf("settlement rows %d, reversals %d: the originals must stay", total, reversals)
	}
	if n := countOutbox(t, f.s, f.ctx, rcvevents.PaymentApplicationReversed); n != 1 {
		t.Fatalf("events %d", n)
	}
	reapplied, err := f.s.ApplyPayment(f.ctx, f.pay.ID, out.Payment.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: f.first.ID, Amount: amt("333.34")}}, Residual: amt("166.66")}, "ra-apply-2")
	if err != nil || reapplied.ApplicationID == f.applied.ApplicationID || reapplied.Settlements[0].ItemStatus != "SETTLED" {
		t.Fatalf("apply after reversal: %+v %v", reapplied, err)
	}
}

func TestReverseApplication_ReplayReturnsTheFirstAnswerAndAnotherBodyIsRefused(t *testing.T) {
	f := appliedSetup(t)
	first, err := f.reverse("ra-key", f.applied.Payment.Version)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.reverse("ra-key", f.applied.Payment.Version)
	if err != nil || replay.ReversalJournalEntryID != first.ReversalJournalEntryID || replay.Payment.Version != first.Payment.Version {
		t.Fatalf("replay %+v %v", replay, err)
	}
	other := ReverseApplicationRequest{ApplicationID: f.applied.ApplicationID, Reason: "another reason"}
	if _, err := f.s.ReverseApplication(f.ctx, f.pay.ID, f.applied.Payment.Version, other, "ra-key"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if n, e := countOutbox(t, f.s, f.ctx, rcvevents.PaymentApplicationReversed), len(f.ledger.Entries()); n != 1 || e != 1 {
		t.Fatalf("events %d, ledger entries %d", n, e)
	}
}

func TestReverseApplication_RefusalsChangeNothing(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		arrange func(t *testing.T, f appliedFixture)
		call    func(f appliedFixture) error
	}{
		{name: "stale If-Match", code: CodeVersionConflict, call: func(f appliedFixture) error {
			_, err := f.reverse("k", f.applied.Payment.Version-1)
			return err
		}},
		{name: "unknown payment", code: CodePaymentNotFound, call: func(f appliedFixture) error {
			_, err := f.s.ReverseApplication(f.ctx, ids.New(), 1, ReverseApplicationRequest{ApplicationID: f.applied.ApplicationID, Reason: "r"}, "k")
			return err
		}},
		{name: "unknown application", code: CodePaymentNotFound, call: func(f appliedFixture) error {
			_, err := f.s.ReverseApplication(f.ctx, f.pay.ID, f.applied.Payment.Version, ReverseApplicationRequest{ApplicationID: ids.New(), Reason: "r"}, "k")
			return err
		}},
		{name: "bank-reconciled", code: CodeReversalReconciled, arrange: func(t *testing.T, f appliedFixture) {
			execAdmin(t, f.s, `update payment set bank_reconciled_at = now() where id = $1`, f.pay.ID)
		}},
		{name: "reconciliation state unreadable", code: CodeReversalReconciled, arrange: func(t *testing.T, f appliedFixture) {
			execAdmin(t, f.s, `alter table payment drop column bank_reconciled_at`)
		}},
		{name: "posting period closed", code: CodeReversalPeriodClosed, arrange: func(t *testing.T, f appliedFixture) {
			f.backdate(t)
			f.ledger.RefuseClosedThrough("2020-03-31")
		}},
		{name: "ledger down", code: CodeReversalLedgerUnavailable, arrange: func(t *testing.T, f appliedFixture) {
			f.ledger.FailPosts(errors.New("connection refused"))
		}},
		{name: "ledger refuses", code: CodeReversalLedgerUnavailable, arrange: func(t *testing.T, f appliedFixture) {
			f.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: 422, Code: "LEDGER_ACCOUNT_INVALID"})
		}},
		{name: "ledger not configured", code: CodeReversalLedgerUnavailable, arrange: func(t *testing.T, f appliedFixture) {
			f.s.WithGLLedgers(nil)
		}},
		{name: "payment without a live application", code: CodePaymentState, arrange: func(t *testing.T, f appliedFixture) {
			execAdmin(t, f.s, `update payment set status = 'RECEIVED' where id = $1`, f.pay.ID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := appliedSetup(t)
			if tt.arrange != nil {
				tt.arrange(t, f)
			}
			call := tt.call
			if call == nil {
				call = func(f appliedFixture) error {
					_, err := f.reverse("k", f.applied.Payment.Version)
					return err
				}
			}
			if err := call(f); problemCode(err) != tt.code {
				t.Fatalf("code %q (%v), want %s", problemCode(err), err, tt.code)
			}
			if tt.name == "ledger not configured" || tt.name == "reconciliation state unreadable" || tt.name == "payment without a live application" {
				return
			}
			f.unchanged(t)
		})
	}
}

func TestReverseApplication_LedgerOutageIsSafeToRepeatWithTheSameKey(t *testing.T) {
	f := appliedSetup(t)
	f.ledger.FailPosts(errors.New("timeout"))
	if _, err := f.reverse("ra-retry", f.applied.Payment.Version); problemCode(err) != CodeReversalLedgerUnavailable {
		t.Fatalf("outage: %v", err)
	}
	f.unchanged(t)
	f.ledger.FailPosts(nil)
	out, err := f.reverse("ra-retry", f.applied.Payment.Version)
	if err != nil || len(f.ledger.Entries()) != 1 || f.ledger.Entries()[0].ID != out.ReversalJournalEntryID {
		t.Fatalf("retry: %+v %v, entries %d", out, err, len(f.ledger.Entries()))
	}
}

func TestReverseApplication_ARepeatWithANewKeyIsAPaymentStateConflict(t *testing.T) {
	f := appliedSetup(t)
	out, err := f.reverse("ra-a", f.applied.Payment.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.reverse("ra-b", out.Payment.Version); problemCode(err) != CodePaymentState {
		t.Fatalf("second reversal: %v", err)
	}
	if n, e := countOutbox(t, f.s, f.ctx, rcvevents.PaymentApplicationReversed), len(f.ledger.Entries()); n != 1 || e != 1 {
		t.Fatalf("events %d, ledger entries %d", n, e)
	}
}

func TestReverseApplication_ALockedItemIsRefusedWithAR0002(t *testing.T) {
	f := appliedSetup(t)
	admin, _ := adminPools.Load(f.s.pool)
	tx, err := admin.(*pgxpool.Pool).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `select 1 from open_item where id = $1 for update`, f.second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reverse("ra-lock", f.applied.Payment.Version); problemCode(err) != domain.CodeItemLocked {
		t.Fatalf("locked item: %v", err)
	}
}

func TestReverseApplication_TwoReversalsOfOneApplicationPostOnce(t *testing.T) {
	f := appliedSetup(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, key := range []string{"race-a", "race-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.reverse(key, f.applied.Payment.Version)
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case problemCode(err) != CodeVersionConflict && problemCode(err) != CodePaymentState:
			t.Fatalf("loser: %v", err)
		}
	}
	if n, e := countOutbox(t, f.s, f.ctx, rcvevents.PaymentApplicationReversed), len(f.ledger.Entries()); wins != 1 || n != 1 || e != 1 {
		t.Fatalf("winners %d, events %d, ledger entries %d", wins, n, e)
	}
}
