package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

func creditSetup(t *testing.T) (*Postgres, context.Context, map[string]OpenItem) {
	t.Helper()
	s := setup(t)
	ctx := scopedCtx(42, legalEntity)
	// 1000.00 in three installments (333.34, 333.33, 333.33), and a credit note of 150.00 against it.
	if _, err := s.ApplyBillingDocument(ctx, invoiceDoc(t, invoiceDocID, "INV-2027-0000003",
		domain.BillingInstallment{No: 1, Amount: amt("333.34"), DueDate: "2027-04-30"},
		domain.BillingInstallment{No: 2, Amount: amt("333.33"), DueDate: "2027-05-30"},
		domain.BillingInstallment{No: 3, Amount: amt("333.33"), DueDate: "2027-06-30"})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyBillingDocument(ctx, creditDoc(t, creditDocID, invoiceDocID, "CN-2027-0000001")); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]OpenItem{}
	for _, it := range items {
		out[it.DocumentNumber] = it
	}
	return s, ctx, out
}

func TestSettleCredit_AgainstOriginalTakesTheOldestInstallmentFirst(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit := items["CN-2027-0000001"]
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, "cs-1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Total.Equal(amt("150.00")) || res.CreditStatus != "SETTLED" || !res.CreditOpenAfter.IsZero() || len(res.Settlements) != 1 ||
		res.Settlements[0].DocumentNumber != "INV-2027-0000003/1" || !res.Settlements[0].OpenAfter.Equal(amt("183.34")) || res.Settlements[0].ItemStatus != "PARTIALLY_SETTLED" {
		t.Fatalf("result %+v", res)
	}
	replay, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, "cs-1")
	if err != nil || replay.BatchID != res.BatchID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditSettled); n != 1 {
		t.Fatalf("events %d", n)
	}
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, ""); problemCode(err) != CodeNotCredit {
		t.Fatalf("a settled credit: %v", err)
	}
	// The net balance of the customer is the same before and after: 1000 - 150 owed.
	if _, err := s.RunAgingSnapshot(ctx, legalEntity, parseDay("2027-07-31")); err != nil {
		t.Fatal(err)
	}
	snaps, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity, AsOf: "2027-07-31"}, 10)
	if err != nil || len(snaps) != 1 || !snaps[0].TotalOpen.Equal(amt("850.00")) {
		t.Fatalf("snapshot %+v %v", snaps, err)
	}
}

func TestSettleCredit_NamedDebitsAreCheckedAndAtomic(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit, i1, i2 := items["CN-2027-0000001"], items["INV-2027-0000003/1"], items["INV-2027-0000003/2"]
	cases := []struct {
		name   string
		debits []CreditDebit
		code   string
	}{
		{"more than the credit holds", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("100.00")}, {OpenItemID: i2.ID, Amount: amt("60.00")}}, CodeCreditSettleInvalid},
		{"more than the debit has open", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("400.00")}}, CodeCreditSettleInvalid},
		{"a credit as a debit", []CreditDebit{{OpenItemID: credit.ID, Amount: amt("10.00")}}, CodeNotDebit},
		{"the same debit twice", []CreditDebit{{OpenItemID: i1.ID, Amount: amt("10.00")}, {OpenItemID: i1.ID, Amount: amt("10.00")}}, CodeCreditSettleInvalid},
	}
	for _, c := range cases {
		if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, Debits: c.debits}, ""); problemCode(err) != c.code {
			t.Fatalf("%s: got %v, want %s", c.name, err, c.code)
		}
	}
	got := itemByID(t, s, ctx, credit.ID)
	if !got.OpenAmount.Equal(amt("-150.00")) || got.Status != "OPEN" {
		t.Fatalf("a refused settlement changed the credit: %+v", got)
	}
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID,
		Debits: []CreditDebit{{OpenItemID: i1.ID, Amount: amt("100.00")}, {OpenItemID: i2.ID, Amount: amt("30.00")}}}, "")
	if err != nil || !res.Total.Equal(amt("130.00")) || res.CreditStatus != "PARTIALLY_SETTLED" || !res.CreditOpenAfter.Equal(amt("-20.00")) {
		t.Fatalf("settle: %+v %v", res, err)
	}
	if again, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, Debits: []CreditDebit{{OpenItemID: i2.ID, Amount: amt("20.00")}}}, ""); err != nil ||
		again.CreditStatus != "SETTLED" {
		t.Fatalf("second settlement: %+v %v", again, err)
	}
}

func TestSettleCredit_AsOfStatementAddsALaterSettlementBack(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit := items["CN-2027-0000001"]
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, ""); err != nil {
		t.Fatal(err)
	}
	execAdmin(t, s, `update credit_settlement set settled_at = '2027-06-01'`)
	// Settled on 2027-06-01: as of an earlier date the credit and the installment are still open in full.
	after, err := s.Statement(ctx, legalEntity, billCustomer, parseDay("2027-06-30"))
	if err != nil || len(after) != 1 || len(after[0].Lines) != 3 {
		t.Fatalf("statement after the settlement %+v %v", after, err)
	}
	secs, err := s.Statement(ctx, legalEntity, billCustomer, parseDay("2027-04-15"))
	if err != nil || len(secs) != 1 {
		t.Fatalf("statement %+v %v", secs, err)
	}
	open := map[string]string{}
	for _, l := range secs[0].Lines {
		open[l.DocumentNumber] = l.OpenAmount.String()
	}
	if open["CN-2027-0000001"] != "-150" || open["INV-2027-0000003/1"] != "333.34" {
		t.Fatalf("as-of lines %+v", open)
	}
}

// reclassJournals returns the lines of every credit reclass journal, oldest first.
func reclassJournals(t *testing.T, s *Postgres, ctx context.Context) [][]journalLine {
	t.Helper()
	var out [][]journalLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select lines from gl_journal where source_type = $1 order by created_at, source_key`, JournalCreditReclass)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			var lines []journalLine
			if err := json.Unmarshal(raw, &lines); err != nil {
				return err
			}
			out = append(out, lines)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// #40: a credit settled against an item of another trading partner (one tagged and one untagged, either way) moves the receivable
// between the partners: debited under the credit's partner, credited under the debit's, for the settled amount, once, and reversed with
// the settlement. Items of the same partner need none.
func TestSettleCredit_ReclassesTheReceivableBetweenPartners(t *testing.T) {
	for _, c := range []struct {
		name                        string
		creditPartner, debitPartner string // "" is untagged
	}{
		{"credit tagged, debit untagged", ids.New(), ""},
		{"credit untagged, debit tagged", "", ids.New()},
		{"two different partners", ids.New(), ids.New()},
	} {
		s := setup(t)
		ctx := scopedCtx(42, legalEntity)
		// The credit note credits its own invoice, whose partner it inherits; the debit is another invoice of the same customer.
		origin, debit, credit := ids.New(), ids.New(), ids.New()
		od := invoiceDoc(t, origin, "INV-O", domain.BillingInstallment{No: 1, Amount: amt("1000.00"), DueDate: "2027-04-30"})
		od.TradingPartnerID = c.creditPartner
		dd := invoiceDoc(t, debit, "INV-D", domain.BillingInstallment{No: 1, Amount: amt("1000.00"), DueDate: "2027-04-30"})
		dd.TradingPartnerID = c.debitPartner
		for _, d := range []BillingDocument{od, dd, creditDoc(t, credit, origin, "CN-1")} {
			if _, err := s.ApplyBillingDocument(ctx, d); err != nil {
				t.Fatalf("%s: document %s: %v", c.name, d.Number, err)
			}
		}
		items, err := s.ListOpenItems(ctx, OpenItemFilter{}, 50)
		if err != nil {
			t.Fatal(err)
		}
		byNumber := map[string]OpenItem{}
		for _, it := range items {
			byNumber[it.DocumentNumber] = it
		}
		req := CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: byNumber["CN-1"].ID, Debits: []CreditDebit{{OpenItemID: byNumber["INV-D"].ID, Amount: amt("150.00")}}}
		res, err := s.SettleCredit(ctx, req, "cs-reclass")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, err := s.SettleCredit(ctx, req, "cs-reclass"); err != nil {
			t.Fatalf("%s replay: %v", c.name, err)
		}
		js := reclassJournals(t, s, ctx)
		if len(js) != 1 || len(js[0]) != 2 {
			t.Fatalf("%s: reclass journals = %+v, want one with two lines", c.name, js)
		}
		var dr, cr journalLine
		for _, l := range js[0] {
			if l.Credit {
				cr = l
			} else {
				dr = l
			}
		}
		if dr.Account != "1200" || cr.Account != "1200" || !dr.Amount.Equal(amt("150.00")) || !cr.Amount.Equal(amt("150.00")) ||
			dr.TradingPartnerID != c.creditPartner || cr.TradingPartnerID != c.debitPartner {
			t.Fatalf("%s: Dr %+v Cr %+v, want 150.00 debited under the credit's partner %q and credited under the debit's %q", c.name, dr, cr, c.creditPartner, c.debitPartner)
		}
		if _, err := s.ReverseCreditSettlement(ctx, res.BatchID, "mistake"); err != nil {
			t.Fatalf("%s reverse: %v", c.name, err)
		}
		if js := reclassJournals(t, s, ctx); len(js) != 2 || js[1][0].Credit == js[0][0].Credit || js[1][0].TradingPartnerID != js[0][0].TradingPartnerID || js[1][1].TradingPartnerID != js[0][1].TradingPartnerID {
			t.Fatalf("%s: the reversal must mirror the reclass: %+v", c.name, js)
		}
	}
	// The same partner on both items owes nothing.
	s := setup(t)
	ctx := scopedCtx(42, legalEntity)
	p, origin, debit, credit := ids.New(), ids.New(), ids.New(), ids.New()
	od := invoiceDoc(t, origin, "INV-O", domain.BillingInstallment{No: 1, Amount: amt("1000.00"), DueDate: "2027-04-30"})
	od.TradingPartnerID = p
	dd := invoiceDoc(t, debit, "INV-D", domain.BillingInstallment{No: 1, Amount: amt("1000.00"), DueDate: "2027-04-30"})
	dd.TradingPartnerID = p
	for _, d := range []BillingDocument{od, dd, creditDoc(t, credit, origin, "CN-1")} {
		if _, err := s.ApplyBillingDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.ListOpenItems(ctx, OpenItemFilter{}, 50)
	by := map[string]OpenItem{}
	for _, it := range items {
		by[it.DocumentNumber] = it
	}
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: by["CN-1"].ID, Debits: []CreditDebit{{OpenItemID: by["INV-D"].ID, Amount: amt("150.00")}}}, "cs-same"); err != nil {
		t.Fatal(err)
	}
	if js := reclassJournals(t, s, ctx); len(js) != 0 {
		t.Fatalf("same partner owes no reclass: %+v", js)
	}
}
