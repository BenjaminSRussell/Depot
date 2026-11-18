package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

func foreignInvoice(e env, key, currency string, lines ...string) PostInput {
	in := vendorInvoice(e.ledger.ID, key, "1.00")
	in.Lines = nil
	for i := 0; i < len(lines); i += 3 {
		in.Lines = append(in.Lines, domain.LineInput{Account: lines[i], Side: lines[i+1], Amount: decimal.RequireFromString(lines[i+2]), Currency: currency,
			Dimensions: map[string]string{"DEPT": "D100"}})
	}
	return in
}

func entryCountOf(t *testing.T, e env) int {
	t.Helper()
	var n int
	if err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from journal_entry`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func setReporting(t *testing.T, e env, currency string) {
	t.Helper()
	if _, err := e.store.UpdateLedgerSettings(e.ctx, e.ledger.ID, 0, LedgerSettings{ReportingCurrency: strPtr(currency)}); err != nil {
		t.Fatalf("reporting currency: %v", err)
	}
}

func TestPostgres_ForeignInvoiceStoresThreeAmountsAtTheLookedUpRate(t *testing.T) {
	e, _ := setup(t)
	setReporting(t, e, "GBP")
	rates := &stubRates{rates: map[string]string{"EURUSD": "1.085", "USDGBP": "0.79"}}
	ctx := WithRateSource(e.ctx, rates)
	out, err := e.store.PostEntry(ctx, foreignInvoice(e, "FX-3", "EUR", "6010", "D", "100.00", "2100", "C", "100.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if rates.calls != 2 {
		t.Errorf("one lookup per pair before the transaction: %d", rates.calls)
	}
	reloaded, err := e.store.GetEntry(e.ctx, out.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, lines := range [][]Line{out.Entries[0].Lines, reloaded.Lines} {
		for _, l := range lines {
			if l.Amount != "108.50" || l.TransactionAmount != "100.00" || l.TransactionCurrency != "EUR" || l.TransactionRate != "1.085000000000" ||
				l.ReportingAmount != "85.72" || l.ReportingCurrency != "GBP" || l.ReportingRate != "0.790000000000" || l.RateOverrideReason != "" {
				t.Errorf("line %s: %+v", l.Account, l)
			}
		}
	}
	if out.Entries[0].Totals.Debit != "108.50" || out.Entries[0].Totals.Credit != "108.50" {
		t.Errorf("totals %+v", out.Entries[0].Totals)
	}
	var txn, acct, rpt string
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select amount_txn::text, amount_acct::text, amount_rpt::text from journal_line where entry_id = $1 and line_number = 1`,
			out.Entries[0].ID).Scan(&txn, &acct, &rpt)
	})
	if err != nil || txn != "100.000000" || acct != "108.500000" || rpt != "85.720000" {
		t.Fatalf("stored %s %s %s, %v", txn, acct, rpt, err)
	}
}

func TestPostgres_ForeignSplitRoundsTheDifferenceOntoOneLineInEveryCurrency(t *testing.T) {
	e, _ := setup(t)
	setReporting(t, e, "GBP")
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"EURUSD": "1.085", "USDGBP": "0.785"}})
	in := foreignInvoice(e, "FX-ROUND", "EUR", "6010", "D", "100.00", "2100", "C", "33.33", "2100", "C", "33.33", "2100", "C", "33.34")
	out, err := e.store.PostEntry(ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if t0 := out.Entries[0].Totals; t0.Debit != "108.49" || t0.Credit != "108.49" {
		t.Errorf("accounting totals %+v", t0)
	}
	flagged := 0
	for _, l := range out.Entries[0].Lines {
		if l.RoundingAdjustment {
			flagged++
		}
		if l.TransactionCurrency != "EUR" || l.ReportingCurrency != "GBP" {
			t.Errorf("line %s: %+v", l.Account, l)
		}
	}
	if flagged != 1 {
		t.Errorf("one line absorbs the cent: %d", flagged)
	}
	var debit, credit string
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select sum(amount_rpt) filter (where debit_credit = 'D')::text, sum(amount_rpt) filter (where debit_credit = 'C')::text
			from journal_line where entry_id = $1`, out.Entries[0].ID).Scan(&debit, &credit)
	})
	if err != nil || debit != credit {
		t.Fatalf("reporting amounts must balance: %s %s %v", debit, credit, err)
	}
}

func TestPostgres_RateOverrideNeedsTheDuty(t *testing.T) {
	e, _ := setup(t)
	rates := &stubRates{rates: map[string]string{"EURUSD": "1.085"}}
	ctx := WithRateSource(e.ctx, rates)
	override := decimal.RequireFromString("1.2")
	attempt := func(key string, duty bool, reason string) (EntryResult, error) {
		in := foreignInvoice(e, key, "EUR", "6010", "D", "100.00", "2100", "C", "100.00")
		in.RateOverride, in.RateOverrideReason, in.CanOverrideRate = &override, reason, duty
		return e.store.PostEntry(ctx, in)
	}
	if _, err := attempt("OVR-NODUTY", false, "contract rate"); problemCode(err) != "AUTH-0003" {
		t.Fatalf("an override without the duty: %v", err)
	}
	_, err := attempt("OVR-NOREASON", true, "  ")
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != CodeRateOverrideReason {
		t.Fatalf("an override without a reason: %v", err)
	}
	if n := entryCountOf(t, e); n != 0 {
		t.Fatalf("a refused override wrote %d entries", n)
	}
	out, err := attempt("OVR-OK", true, "contract rate")
	if err != nil {
		t.Fatalf("override with duty and reason: %v", err)
	}
	for _, l := range out.Entries[0].Lines {
		if l.Amount != "120.00" || l.TransactionRate != "1.200000000000" || l.RateOverrideReason != "contract rate" {
			t.Errorf("line %s: %+v", l.Account, l)
		}
	}
	if rates.calls != 0 {
		t.Errorf("an override replaces the lookup: %d calls", rates.calls)
	}
}

func TestPostgres_ForeignReversalCopiesThreeAmounts(t *testing.T) {
	e, _ := setup(t)
	setReporting(t, e, "GBP")
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"EURUSD": "1.085", "USDGBP": "0.79"}})
	orig, err := e.store.PostEntry(ctx, foreignInvoice(e, "FX-REV", "EUR", "6010", "D", "100.00", "2100", "C", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "FX-REV-R", Hash: make([]byte, 32), Actor: "test-user", TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	reloaded, err := e.store.GetEntry(e.ctx, rev.Entries[0].ID)
	if err != nil || len(reloaded.Lines) != 2 {
		t.Fatalf("reload: %v", err)
	}
	for i, l := range reloaded.Lines {
		o := orig.Entries[0].Lines[i]
		if l.DebitCredit == o.DebitCredit || l.Amount != o.Amount || l.TransactionAmount != o.TransactionAmount || l.TransactionCurrency != o.TransactionCurrency ||
			l.ReportingAmount != o.ReportingAmount || l.TransactionRate != o.TransactionRate || l.ReportingRate != o.ReportingRate {
			t.Errorf("reversal line %d %+v does not negate %+v", i, l, o)
		}
	}
}

func TestPostgres_ForeignPostFailsClosed(t *testing.T) {
	e, _ := setup(t)
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"no rate for the date": {WithRateSource(e.ctx, &stubRates{rates: map[string]string{}}), CodeRateMissing},
		"no rate source":       {e.ctx, CodeRateMissing},
		"fx refuses the call":  {WithRateSource(e.ctx, &stubRates{err: &RateRefused{Status: 403}}), CodeFXMisconfigure},
		"fx unreachable":       {WithRateSource(e.ctx, &stubRates{err: errors.New("connection refused")}), CodeFXUnavailable},
	} {
		_, err := e.store.PostEntry(tc.ctx, foreignInvoice(e, "FX-FAIL-"+name, "EUR", "6010", "D", "100.00", "2100", "C", "100.00"))
		if problemCode(err) != tc.want {
			t.Errorf("%s: want %s, got %v", name, tc.want, err)
		}
	}
	if n := entryCountOf(t, e); n != 0 {
		t.Fatalf("a failed rate must leave nothing posted, got %d entries", n)
	}
}

func TestPostgres_ForeignLinesMustBalanceInTheirOwnCurrency(t *testing.T) {
	e, _ := setup(t)
	ctx := WithRateSource(e.ctx, &stubRates{rates: map[string]string{"EURUSD": "1.0"}})
	for name, in := range map[string]PostInput{
		"foreign lines off by ten": foreignInvoice(e, "FX-UNB-1", "EUR", "6010", "D", "100.00", "2100", "C", "90.00"),
		"foreign debit, local credit": func() PostInput {
			in := foreignInvoice(e, "FX-UNB-2", "EUR", "6010", "D", "100.00")
			in.Lines = append(in.Lines, domain.LineInput{Account: "2100", Side: domain.SideCredit, Amount: decimal.RequireFromString("100.00"), Currency: "USD"})
			return in
		}(),
	} {
		_, err := e.store.PostEntry(ctx, in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_UNBALANCED" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := entryCountOf(t, e); n != 0 {
		t.Fatalf("got %d entries", n)
	}
}

func TestPostgres_DatabaseRejectsEntryUnbalancedInOneTransactionCurrency(t *testing.T) {
	e, _ := setup(t)
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "DB-TXN", "10.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	debit, credit := posted.Entries[0].Lines[0], posted.Entries[0].Lines[1]
	entryID := ids.New()
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(e.ctx, `insert into journal_entry (tenant_id, id, legal_entity_id, ledger_id, document_id, voucher, posting_sequence,
				posting_date, period_key, source_system, source_type, source_id, idempotency_key, payload_hash, trace_id)
			values ($1, $2, $3, $4, $5, 'GL-Y', 998, '2026-03-10', '2026-03', 'test', 'raw', 'y', 'raw-txn-key', $6, $7)`,
			e.tenant, entryID, e.le, e.ledger.ID, ids.New(), make([]byte, 32), testTrace); err != nil {
			return err
		}
		_, err := tx.Exec(e.ctx, `insert into journal_line (tenant_id, id, legal_entity_id, entry_id, line_number, main_account_id,
				dimension_combination_id, debit_credit, currency_txn, amount_txn, amount_acct)
			select tenant_id, $1::uuid, legal_entity_id, $2::uuid, 1, main_account_id, dimension_combination_id, 'D', 'EUR', 10, 10 from journal_line where id = $3
			union all
			select tenant_id, $4::uuid, legal_entity_id, $2::uuid, 2, main_account_id, dimension_combination_id, 'C', 'USD', 10, 10 from journal_line where id = $5`,
			ids.New(), entryID, debit.ID, ids.New(), credit.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "LEDGER_UNBALANCED") {
		t.Fatalf("balanced accounting amounts in two transaction currencies must fail at commit, got %v", err)
	}
}

func TestPostgres_DomesticPostFailsWhenTheReportingRateCannotBeResolved(t *testing.T) {
	e, _ := setup(t)
	setReporting(t, e, "GBP")
	for name, ctx := range map[string]context.Context{
		"source has no pair": WithRateSource(e.ctx, &stubRates{rates: map[string]string{}}),
		"fx unreachable":     WithRateSource(e.ctx, &stubRates{err: errors.New("connection refused")}),
	} {
		if _, err := e.store.PostEntry(ctx, vendorInvoice(e.ledger.ID, "FX-DOM-"+name, "10.00")); err == nil {
			t.Errorf("%s: an entry without its reporting amount must not be posted", name)
		}
	}
	if n := entryCountOf(t, e); n != 0 {
		t.Fatalf("got %d entries", n)
	}
}

func TestPostgres_PostBatchLooksRatesUpOncePerPairBeforeTheTransaction(t *testing.T) {
	e, _ := setup(t)
	setReporting(t, e, "GBP")
	putName(t, e, "FXB", JournalName{})
	in := batchOf(e, "FXB", "100.00")
	for i := range in.Lines {
		in.Lines[i].Currency = "EUR"
	}
	b, err := e.store.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rates := &stubRates{rates: map[string]string{"EURUSD": "1.085", "USDGBP": "0.79"}}
	out, err := postBatch(e, WithRateSource(e.ctx, rates), b, "FX-BATCH", true)
	if err != nil {
		t.Fatalf("post batch: %v", err)
	}
	if rates.calls != 2 {
		t.Errorf("one lookup per pair: %d", rates.calls)
	}
	for _, l := range out.Entries[0].Lines {
		if l.Amount != "108.50" || l.TransactionCurrency != "EUR" || l.ReportingAmount != "85.72" {
			t.Errorf("line %s: %+v", l.Account, l)
		}
	}
}
