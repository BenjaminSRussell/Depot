package store

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// icEntry is a balanced entry whose payable line names a trading partner.
func icEntry(ledgerID, key, amount, partner string) PostInput {
	in := vendorInvoice(ledgerID, key, amount)
	in.Lines[1].TradingPartner = partner
	return in
}

func violationCode(err error) string {
	var rej *domain.RejectedError
	if errors.As(err, &rej) && len(rej.Violations) > 0 {
		return rej.Violations[0].Code
	}
	return ""
}

func TestPostgres_TradingPartner(t *testing.T) {
	e, _ := setup(t)
	// A partner that owns no ledger of the tenant is not a legal entity the ledger knows.
	if _, err := e.store.PostEntry(e.ctx, icEntry(e.ledger.ID, "IC-0", "10.00", ids.New())); violationCode(err) != domain.CodeTradingPartnerInvalid {
		t.Fatalf("an unknown partner must be refused, got %v", err)
	}
	// The ledger's own legal entity is not a partner.
	if _, err := e.store.PostEntry(e.ctx, icEntry(e.ledger.ID, "IC-0b", "10.00", e.le)); violationCode(err) != domain.CodeTradingPartnerInvalid {
		t.Fatalf("the own entity must be refused, got %v", err)
	}
	// Two subsidiaries, each with a ledger; the poster is scoped to its own entity and still sees that they exist.
	subA, subB := ids.New(), ids.New()
	for i, le := range []string{subA, subB} {
		if _, err := e.store.CreateLedger(principalCtx(e.tenant, le), Ledger{LegalEntityID: le, Code: "SUB" + string(rune('A'+i)), Name: "Ledger", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366}); err != nil {
			t.Fatalf("create ledger: %v", err)
		}
	}
	a, err := e.store.PostEntry(e.ctx, icEntry(e.ledger.ID, "IC-A", "300.00", subA))
	if err != nil {
		t.Fatalf("post to partner A: %v", err)
	}
	if got := a.Entries[0].Lines[1].TradingPartnerID; got != subA {
		t.Fatalf("the posted line must carry its partner, got %q", got)
	}
	if _, err := e.store.PostEntry(e.ctx, icEntry(e.ledger.ID, "IC-B", "200.00", subB)); err != nil {
		t.Fatalf("post to partner B: %v", err)
	}
	// An ordinary line without a partner stays out of the partner balances.
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "PLAIN", "50.00")); err != nil {
		t.Fatalf("post plain: %v", err)
	}
	pb, err := e.store.PartnerBalances(e.ctx, PartnerBalanceFilter{LedgerID: e.ledger.ID})
	if err != nil || len(pb.Rows) != 2 {
		t.Fatalf("partner balances: %+v %v", pb, err)
	}
	if r := pb.Rows[0]; r.Account != "2100" || r.Credit != "300.00" && r.Credit != "200.00" {
		t.Fatalf("row: %+v", r)
	}
	byPartner := map[string]PartnerBalanceRow{}
	for _, r := range pb.Rows {
		byPartner[r.TradingPartnerID] = r
	}
	if byPartner[subA].Credit != "300.00" || byPartner[subA].Net != "-300.00" || byPartner[subB].Credit != "200.00" {
		t.Fatalf("balances per partner: %+v", byPartner)
	}
	one, err := e.store.PartnerBalances(e.ctx, PartnerBalanceFilter{LedgerID: e.ledger.ID, TradingPartnerID: subB})
	if err != nil || len(one.Rows) != 1 || one.Rows[0].TradingPartnerID != subB {
		t.Fatalf("filtered balances: %+v %v", one, err)
	}
	// A reversal keeps the partner, so the pair nets to zero.
	hash, _ := domain.HashPayload("reverse ic")
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: a.Entries[0].ID, Key: "REV-IC", Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if got := rev.Entries[0].Lines[1].TradingPartnerID; got != subA {
		t.Fatalf("the reversing line must keep the partner, got %q", got)
	}
	after, err := e.store.PartnerBalances(e.ctx, PartnerBalanceFilter{LedgerID: e.ledger.ID, TradingPartnerID: subA})
	if err != nil || len(after.Rows) != 1 || !decimal.RequireFromString(after.Rows[0].Net).IsZero() {
		t.Fatalf("after the reversal partner A nets to zero: %+v %v", after, err)
	}
	// The line shows in the inquiry and the entry read.
	got, err := e.store.GetEntry(e.ctx, a.Entries[0].ID)
	if err != nil || got.Lines[1].TradingPartnerID != subA {
		t.Fatalf("read back: %+v %v", got.Lines, err)
	}
	// The posted entry is accepted by the validation route too, and a bad partner is refused there.
	res, err := e.store.ValidateEntry(e.ctx, icEntry(e.ledger.ID, "IC-V", "5.00", ids.New()))
	if err != nil || res.Valid() {
		t.Fatalf("validate must refuse an unknown partner: %+v %v", res, err)
	}
}
