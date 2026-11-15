package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// A payment of 500.00 applied to two installments (333.34 and 166.66). Unapplying the first settlement reopens that installment only,
// owes the ledger 333.34 (not the whole 500.00), leaves the payment ON_ACCOUNT with 333.34 unapplied, and refuses a repeat; the second
// can then be unapplied alone and the payment is RECEIVED again.
func TestUnapplySettlement_ReversesOnlyThatSettlementAndItsJournal(t *testing.T) {
	s, ctx, items := creditSetup(t)
	first, second := items["INV-2027-0000003/1"], items["INV-2027-0000003/2"]
	cust := first.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("500.00"), UnappliedAmount: amt("500.00"), Currency: first.Currency, ReceivedDate: "2027-05-01", ValueDate: "2027-05-01"}, "uo-1")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{
		{OpenItemID: first.ID, Amount: amt("333.34")}, {OpenItemID: second.ID, Amount: amt("166.66")}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPaymentSettlements(ctx, pay.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("settlements %+v %v", list, err)
	}
	var firstID, secondID string
	for _, l := range list {
		if l.OpenItemID == first.ID {
			firstID = l.ID
		} else {
			secondID = l.ID
		}
		if l.Reversed {
			t.Fatalf("nothing reversed yet: %+v", l)
		}
	}
	if _, err := s.UnapplySettlement(ctx, pay.ID, firstID, applied.Payment.Version-1, ""); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.UnapplySettlement(ctx, pay.ID, "00000000-0000-0000-0000-00000000dead", applied.Payment.Version, ""); problemCode(err) != CodeSettlementNotFound {
		t.Fatalf("unknown settlement: %v", err)
	}
	out, err := s.UnapplySettlement(ctx, pay.ID, firstID, applied.Payment.Version, "wrong installment")
	if err != nil {
		t.Fatal(err)
	}
	if out.Payment.Status != "ON_ACCOUNT" || !out.Payment.UnappliedAmount.Equal(amt("333.34")) || len(out.Reversed) != 1 ||
		out.Reversed[0].SettlementID != firstID || !out.Reversed[0].OpenAfter.Equal(amt("333.34")) || out.Reversed[0].ItemStatus != "OPEN" {
		t.Fatalf("unapply one %+v", out)
	}
	if got, err := s.GetOpenItem(ctx, second.ID); err != nil || !got.OpenAmount.Equal(amt("166.67")) || got.Status != "PARTIALLY_SETTLED" {
		t.Fatalf("the second installment stays settled by 166.66: %+v %v", got, err)
	}
	if _, err := s.UnapplySettlement(ctx, pay.ID, firstID, out.Payment.Version, ""); problemCode(err) != CodePaymentState {
		t.Fatalf("a repeat: %v", err)
	}
	js, err := s.ListGLJournals(ctx, GLJournalFilter{SourceType: JournalPaymentUnapply})
	if err != nil || len(js) != 1 {
		t.Fatalf("unapply journals %+v %v", js, err)
	}
	j := js[0].Lines
	if len(j) != 2 || j[0].Account != "1200" || j[0].Side != "D" || !j[0].Amount.Equal(amt("333.34")) || j[1].Account != "1205" || j[1].Side != "C" || !j[1].Amount.Equal(amt("333.34")) {
		t.Fatalf("the journal mirrors one settlement: %+v", j)
	}
	if n := countOutbox(t, s, ctx, rcvevents.PaymentUnapplied); n != 1 {
		t.Fatalf("events %d", n)
	}
	last, err := s.UnapplySettlement(ctx, pay.ID, secondID, out.Payment.Version, "")
	if err != nil || last.Payment.Status != "RECEIVED" || !last.Payment.UnappliedAmount.Equal(amt("500.00")) {
		t.Fatalf("the last one: %+v %v", last, err)
	}
	list, _ = s.ListPaymentSettlements(ctx, pay.ID)
	for _, l := range list {
		if !l.Reversed {
			t.Fatalf("all reversed: %+v", l)
		}
	}
}

// Worked example: credit note CN-2027-0000001 of 150.00 settled against installment 1 (333.34 to 183.34). Reversing the settlement
// puts 150.00 back on the installment (OPEN, 333.34) and on the credit (OPEN, -150.00), nothing moves in the ledger, a repeat is
// refused, the as-of statement before the reversal still shows the settlement and the credit can be settled again.
func TestReverseCreditSettlement_ReopensBothSidesOnce(t *testing.T) {
	s, ctx, items := creditSetup(t)
	credit, inst := items["CN-2027-0000001"], items["INV-2027-0000003/1"]
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReverseCreditSettlement(ctx, "00000000-0000-0000-0000-00000000dead", ""); problemCode(err) != CodeCreditSettlementNotFound {
		t.Fatalf("unknown batch: %v", err)
	}
	listed, err := s.ListCreditSettlements(ctx, CreditSettlementFilter{OpenItemID: inst.ID}, 10)
	if err != nil || len(listed) != 1 || listed[0].BatchID != res.BatchID || listed[0].Reversed || !listed[0].Total.Equal(amt("150.00")) {
		t.Fatalf("listed %+v %v", listed, err)
	}
	out, err := s.ReverseCreditSettlement(ctx, res.BatchID, "credit applied to the wrong invoice")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Total.Equal(amt("150.00")) || out.CreditStatus != "OPEN" || !out.CreditOpenAfter.Equal(amt("-150.00")) || len(out.Reopened) != 1 ||
		!out.Reopened[0].OpenAfter.Equal(amt("333.34")) || out.Reopened[0].ItemStatus != "OPEN" || !out.RealizedFX.IsZero() {
		t.Fatalf("reversal %+v", out)
	}
	if got, _ := s.GetOpenItem(ctx, credit.ID); !got.OpenAmount.Equal(amt("-150.00")) || got.Status != "OPEN" {
		t.Fatalf("credit %+v", got)
	}
	if _, err := s.ReverseCreditSettlement(ctx, res.BatchID, ""); problemCode(err) != CodeCreditSettlementReversed {
		t.Fatalf("a repeat: %v", err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditSettlementReversed); n != 1 {
		t.Fatalf("events %d", n)
	}
	if listed, _ = s.ListCreditSettlements(ctx, CreditSettlementFilter{CreditItemID: credit.ID}, 10); len(listed) != 1 || !listed[0].Reversed {
		t.Fatalf("listed after %+v", listed)
	}
	if _, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, AgainstOriginal: true}, ""); err != nil {
		t.Fatalf("settle again: %v", err)
	}
	// Open sums stay whole: the as-of statement with both rows (settlement 150.00, reversal -150.00, settlement 150.00) nets to 183.34.
	secs, err := s.Statement(ctx, legalEntity, inst.CustomerID, parseDay("2027-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sec := range secs {
		for _, l := range sec.Lines {
			if l.DocumentNumber == "INV-2027-0000003/1" && l.OpenAmount.Equal(amt("183.34")) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("statement %+v", secs)
	}
}

// Worked example (as TestSettleCredit_CrossCurrency...): 100.00 EUR credit against a USD invoice priced at 108.00 USD, a 2.00 USD gain
// booked Dr 1200 / Cr 7100. Reversing gives the invoice its 108.00 back (1000.00) and the credit its 100.00, and the gain is mirrored
// Dr 7100 / Cr 1200 for 2.00, so the realized difference nets to zero in the ledger.
func TestReverseCreditSettlement_CrossCurrencyMirrorsTheRealizedGain(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	credit, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: item.CustomerID, SourceType: "CREDIT_NOTE",
		DocumentNumber: "CN-30001", DocumentDate: "2027-02-01", DueDate: "2027-02-01", Currency: "EUR", Amount: amt("-100.00"), OpenAmount: amt("-100.00")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true, FunctionalCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	s.WithRates(fakeRates{"EUR>USD@2027-02-01": "1.10", "EUR>USD@" + today(): "1.08"})
	res, err := s.SettleCredit(ctx, CreditSettlementRequest{LegalEntityID: legalEntity, CreditItemID: credit.ID, Debits: []CreditDebit{{OpenItemID: item.ID, Amount: amt("100.00")}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ReverseCreditSettlement(ctx, res.BatchID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !out.RealizedFX.Equal(amt("2.00")) || !out.CreditOpenAfter.Equal(amt("-100.00")) || out.CreditStatus != "OPEN" || len(out.Reopened) != 1 ||
		!out.Reopened[0].OpenAfter.Equal(amt("1000.00")) || out.Reopened[0].ItemStatus != "OPEN" || out.Reopened[0].ItemAmount == nil || !out.Reopened[0].ItemAmount.Equal(amt("108.00")) {
		t.Fatalf("reversal %+v", out)
	}
	fx, err := s.ListGLJournals(ctx, GLJournalFilter{SourceType: JournalRealizedFX})
	if err != nil || len(fx) != 2 {
		t.Fatalf("fx journals %+v %v", fx, err)
	}
	var mirrored bool
	for _, j := range fx {
		if j.Lines[0].Account == "7100" && j.Lines[0].Side == "D" && j.Lines[0].Amount.Equal(amt("2.00")) &&
			j.Lines[1].Account == "1200" && j.Lines[1].Side == "C" && j.Lines[1].Amount.Equal(amt("2.00")) {
			mirrored = true
		}
	}
	if !mirrored {
		t.Fatalf("the 2.00 gain was not mirrored: %+v", fx)
	}
	// A loss reverses the other way: a credit worth 108.00 carried at 1.05 loses nothing here, but the same row negated must net zero.
	secs, err := s.Statement(ctx, legalEntity, item.CustomerID, parseDay("2027-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sec := range secs {
		for _, l := range sec.Lines {
			if l.DocumentNumber == "INV-30001" && l.OpenAmount.Equal(amt("1000.00")) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("statement after the reversal %+v", secs)
	}
}
