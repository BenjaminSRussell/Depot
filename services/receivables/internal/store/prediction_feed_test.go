package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// The feed lists open invoices and, separately, settled ones with the day of their settlement; a change moves updated_at, so an
// updated_since read sees only what changed after it (#47).
func TestListPredictionInvoices_OpenPaidAndDelta(t *testing.T) {
	s := setup(t)
	ctx := scopedCtx(77, legalEntity)
	open := seedItem(t, s, ctx, "PRED-OPEN", custA, "2027-03-01", "100.00", "USD")
	paid := seedItem(t, s, ctx, "PRED-PAID", custA, "2027-03-02", "50.00", "USD")
	if _, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: custA, SourceType: "CREDIT_NOTE", DocumentNumber: "PRED-CN", DocumentDate: "2027-01-01", DueDate: "2027-03-03",
		Currency: "USD", Amount: amt("5.00"), OpenAmount: amt("5.00")}); err != nil {
		t.Fatal(err)
	}
	pay := newPayment(t, s, ctx, ptr(custA), "50.00", "USD", "pred-pay")
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: paid.ID, Amount: amt("50.00")}}}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{}, 10)
	if err != nil || len(got) != 1 || got[0].ID != open.ID || got[0].Amount.String() != "100" || got[0].DueDate != "2027-03-01" || got[0].PaidDate != nil || got[0].Disputes != 0 {
		t.Fatalf("open: %+v %v", got, err)
	}
	done, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{State: "paid"}, 10)
	if err != nil || len(done) != 1 || done[0].ID != paid.ID || done[0].PaidDate == nil || *done[0].PaidDate != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("paid: %+v %v", done, err)
	}
	// Delta: only what changed at or after the time of the last read; the page keys continue after the last row.
	cut := got[0].UpdatedAt.Add(time.Microsecond)
	if later, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{UpdatedSince: cut}, 10); err != nil || len(later) != 0 {
		t.Fatalf("nothing changed since: %+v %v", later, err)
	}
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update open_item set dunning_level = 2, updated_at = clock_timestamp() where id = $1`, open.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	later, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{UpdatedSince: cut}, 10)
	if err != nil || len(later) != 1 || later[0].DunningLevel != 2 {
		t.Fatalf("changed since: %+v %v", later, err)
	}
	first, _ := s.ListPredictionInvoices(ctx, PredictionFeedFilter{}, 1)
	if rest, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{AfterAt: first[0].UpdatedAt, AfterID: first[0].ID}, 10); err != nil || len(rest) != 0 {
		t.Fatalf("after the only row: %+v %v", rest, err)
	}
	// A reversed invoice is in no other state; the feed lists it as reversed.
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update open_item set status = 'REVERSED', open_amount = amount, updated_at = clock_timestamp() where id = $1`, open.ID)
		return err
	}); err == nil {
		if rev, err := s.ListPredictionInvoices(ctx, PredictionFeedFilter{State: "reversed"}, 10); err != nil || len(rev) != 1 || rev[0].ID != open.ID {
			t.Fatalf("reversed: %+v %v", rev, err)
		}
		if still, _ := s.ListPredictionInvoices(ctx, PredictionFeedFilter{State: "open"}, 10); len(still) != 0 {
			t.Fatalf("a reversed invoice is not open: %+v", still)
		}
	} else {
		t.Skipf("cannot reverse the fixture invoice: %v", err)
	}
}
