package domain

import (
	"strings"
	"testing"
)

var partnerMapping = []AccountMapping{
	{MainAccount: "1500", ConsolidationAccount: "IC_REC", Type: AccountBalanceSheet, Role: RoleICReceivable},
	{MainAccount: "2500", ConsolidationAccount: "IC_PAY", Type: AccountBalanceSheet, Role: RoleICPayable},
	{MainAccount: "1400", ConsolidationAccount: "IC_INV", Type: AccountBalanceSheet, Role: RoleICInventory},
	{MainAccount: "4100", ConsolidationAccount: "IC_REVENUE", Type: AccountIncome, Role: RoleICSales},
	{MainAccount: "5100", ConsolidationAccount: "IC_COGS", Type: AccountIncome, Role: RoleICCost},
	{MainAccount: "5200", ConsolidationAccount: "IC_PURCHASES", Type: AccountIncome, Role: RoleICPurchases},
	{MainAccount: "5300", ConsolidationAccount: "IC_COGS_BUYER", Type: AccountIncome, Role: RoleICCOGS},
	{MainAccount: "4000", ConsolidationAccount: "REVENUE", Type: AccountIncome},
}

// heldByParent makes the parent P the holder of a subsidiary's shares.
func heldByParent(u Unit) Unit {
	for i := range u.Holdings {
		u.Holdings[i].HolderUnitID = "P"
	}
	return u
}

func prow(account, partner, net string) PartnerRow {
	return PartnerRow{Account: account, CounterpartyUnitID: partner, Net: dec(net)}
}

// threeUnitGroup is the group of the IFRS 10 B86(c) worked example: parent P (USD) holds 100 percent of S1 (USD) and S2 (EUR).
func threeUnitGroup() ([]Unit, []CollectLine, map[string]CollectRate, map[string]UnitTerms) {
	units := []Unit{{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}, heldByParent(subsidiary("S1", "USD", "100")), heldByParent(subsidiary("S2", "EUR", "100"))}
	lines := []CollectLine{
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-1000000"),
		collected("S1", "4000", AccountIncome, "", "-100000"),
		collected("S2", "3000", AccountBalanceSheet, RoleEquity, "-800000"),
		collected("S2", "4000", AccountIncome, "", "-50000"),
		collected("P", "3000", AccountBalanceSheet, RoleEquity, "-9999999"),
	}
	rates := map[string]CollectRate{"EUR": {Average: dec("1.10"), Closing: dec("1.20")}}
	terms := map[string]UnitTerms{
		"S1": {InvestmentCost: dec("1000000"), ShareCapital: dec("1000000"), HistoricalRate: dec("1")},
		"S2": {InvestmentCost: dec("1000000"), ShareCapital: dec("800000"), HistoricalRate: dec("1.25")},
	}
	return units, lines, rates, terms
}

// Worked example, IFRS 10 B86(c): a parent sells goods to two subsidiaries at different margins and each holds part of them at the
// period end.
//
//	P to S1: sales 1,000, cost 600, margin 40.00 percent. S1 bought 1,000, sold on goods it bought for 700, holds 300.
//	         Unrealised profit 300 x 40 percent = 120.00. P is owed 400, S1 owes 400.
//	P to S2: sales 2,200 USD, cost 1,650, margin 25.00 percent. S2 (EUR) bought 2,000 EUR (2,200 USD at the average rate 1.10), sold on
//	         goods it bought for 1,200 EUR (1,320 USD), holds 880 USD. Unrealised profit 880 x 25 percent = 220.00. P is owed 600 USD,
//	         S2 owes 498 EUR = 597.60 USD at the closing rate 1.20, so 2.40 is an exchange difference.
func TestDeriveRun_ParentSellingToTwoSubsidiaries_B86c(t *testing.T) {
	units, lines, rates, terms := threeUnitGroup()
	partners, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{
		{UnitID: "P", Currency: "USD", Rows: []PartnerRow{
			prow("4100", "S1", "-1000"), prow("5100", "S1", "600"), prow("1500", "S1", "400"),
			prow("4100", "S2", "-2200"), prow("5100", "S2", "1650"), prow("1500", "S2", "600")}},
		{UnitID: "S1", Currency: "USD", Rows: []PartnerRow{prow("5200", "P", "1000"), prow("5300", "P", "700"), prow("2500", "P", "-400")}},
		{UnitID: "S2", Currency: "EUR", Rows: []PartnerRow{prow("5200", "P", "2000"), prow("5300", "P", "1200"), prow("2500", "P", "-498")}},
	}, rates)
	if err != nil {
		t.Fatal(err)
	}
	in := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: units, Lines: lines, Rates: rates, Terms: terms, PartnerLines: partners,
		Opening: map[string]OpeningBasis{"S1": {NetAssets: dec("900000"), Rate: dec("1")}, "S2": {NetAssets: dec("750000"), Rate: dec("1.15")}}}
	d, err := DeriveRun(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Units) != 2 {
		t.Fatalf("units = %+v", d.Units)
	}
	want := map[string]struct{ sales, cost, receivable, payableUnit, profit string }{
		"S1": {"1000", "600", "400", "400", "120"},
		"S2": {"2200", "1650", "600", "498", "220"},
	}
	for _, u := range d.Units {
		w := want[u.ID]
		p := u.Pair
		if p.ICSalesGroup == nil || p.ICCostGroup == nil || p.ReceivableGroup == nil || p.PayableUnit == nil || p.UnrealizedProfitGroup == nil {
			t.Fatalf("%s: partner amounts missing: %+v", u.ID, p)
		}
		if !p.ICSalesGroup.Equal(dec(w.sales)) || !p.ICCostGroup.Equal(dec(w.cost)) || !p.ReceivableGroup.Equal(dec(w.receivable)) || !p.PayableUnit.Equal(dec(w.payableUnit)) || !p.UnrealizedProfitGroup.Equal(dec(w.profit)) {
			t.Errorf("%s: sales %s cost %s receivable %s payable %s profit %s; want %+v", u.ID, p.ICSalesGroup, p.ICCostGroup, p.ReceivableGroup, p.PayableUnit, p.UnrealizedProfitGroup, w)
		}
	}
	if len(d.Warnings) != 2 || !strings.Contains(d.Warnings[0], CodeProfitEstimated) {
		t.Errorf("warnings = %v, want one CNS-0933 estimate per subsidiary", d.Warnings)
	}

	res, err := ComputeRun(RunInput{GroupID: "g", Period: "2027-03", Currency: "USD", Units: d.Units})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBalanced(entriesOf(res)); err != nil {
		t.Fatalf("elimination journal unbalanced: %v", err)
	}
	got := map[string]string{}
	for _, e := range res.Entries {
		if e.RuleCode == RuleE1 || e.RuleCode == RuleE2 || e.RuleCode == RuleE3 {
			net := e.Debit.Sub(e.Credit)
			got[e.UnitID+" "+e.RuleCode+" "+e.Account] = net.String()
		}
	}
	for k, v := range map[string]string{
		"S1 E1 IC_REVENUE": "1000", "S1 E1 IC_COGS": "-1000",
		"S2 E1 IC_REVENUE": "2200", "S2 E1 IC_COGS": "-2200",
		"S1 E2 IC_PAYABLE": "400", "S1 E2 IC_RECEIVABLE": "-400",
		"S2 E2 IC_PAYABLE": "597.6", "S2 E2 IC_RECEIVABLE": "-600", "S2 E2 FX_GAIN_LOSS": "2.4",
		"S1 E3 COGS": "120", "S1 E3 INVENTORY": "-120",
		"S2 E3 COGS": "220", "S2 E3 INVENTORY": "-220",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %s (all %v)", k, got[k], v, got)
		}
	}
	// Each subsidiary's unrealised profit is eliminated on its own: 120.00 + 220.00 = 340.00 off consolidated income.
	var e3 string
	for _, l := range res.Statements {
		if l.Code == LineUnrealizedProfitE3 {
			e3 = l.Amount.String()
		}
	}
	if e3 != "340" && e3 != "-340" {
		t.Errorf("E3 statement line = %q, want 340", e3)
	}
	// A rerun with the same inputs gives the same journal, entry for entry.
	d2, err := DeriveRun(in)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := ComputeRun(RunInput{GroupID: "g", Period: "2027-03", Currency: "USD", Units: d2.Units})
	if err != nil || len(res2.Entries) != len(res.Entries) {
		t.Fatalf("rerun: %v entries %d vs %d", err, len(res2.Entries), len(res.Entries))
	}
	for i := range res.Entries {
		if res.Entries[i] != res2.Entries[i] && (res.Entries[i].UnitID != res2.Entries[i].UnitID || res.Entries[i].RuleCode != res2.Entries[i].RuleCode ||
			res.Entries[i].Account != res2.Entries[i].Account || !res.Entries[i].Debit.Equal(res2.Entries[i].Debit) || !res.Entries[i].Credit.Equal(res2.Entries[i].Credit)) {
			t.Fatalf("rerun entry %d differs: %+v vs %+v", i, res.Entries[i], res2.Entries[i])
		}
	}
}

// Without a share of the buyer's inventory to read the estimate stands, and what the request, the layers or the ledger say wins in
// that order: layers, then a stated unsold share, then the estimate.
func TestDeriveRun_UnsoldShareSources(t *testing.T) {
	units, lines, rates, terms := threeUnitGroup()
	partners, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{
		{UnitID: "P", Currency: "USD", Rows: []PartnerRow{prow("4100", "S1", "-1000"), prow("5100", "S1", "600")}},
		{UnitID: "S1", Currency: "USD", Rows: []PartnerRow{prow("5200", "P", "1000"), prow("5300", "P", "700")}},
	}, rates)
	if err != nil {
		t.Fatal(err)
	}
	base := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: units[:2], Lines: lines[:2], Rates: rates, Terms: terms, PartnerLines: partners,
		Opening: map[string]OpeningBasis{"S1": {NetAssets: dec("900000"), Rate: dec("1")}}}
	profit := func(in DeriveInput) (string, []string) {
		t.Helper()
		d, err := DeriveRun(in)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ComputeRun(RunInput{GroupID: "g", Period: "2027-03", Currency: "USD", Units: d.Units})
		if err != nil {
			t.Fatal(err)
		}
		got := "0"
		for _, e := range res.Entries {
			if e.RuleCode == RuleE3 && e.Account == AcctCOGS {
				got = e.Debit.String()
			}
		}
		return got, d.Warnings
	}
	if got, w := profit(base); got != "120" || len(w) != 1 {
		t.Fatalf("estimate = %s warnings %v, want 120 and one CNS-0933", got, w)
	}
	// A stated unsold share of 0.5 applies to the margin of the parent's sales: (1000 - 600) x 0.5 = 200.
	half := dec("0.5")
	stated := base
	stated.IC = map[string]ICOverride{"S1": {UnsoldShare: &half}}
	if got, w := profit(stated); got != "200" || len(w) != 0 {
		t.Fatalf("stated share = %s warnings %v, want 200 and no estimate warning", got, w)
	}
	// Receipt layers win over both: 100 units at a legal cost of 5 and a group cost of 3 = 200 in the unit currency, 200 USD here.
	layered := stated
	layered.Layers = map[string][]ICLayer{"S1": {{ID: "l1", Quantity: dec("100"), LegalUnitCost: dec("5"), GroupUnitCost: dec("3"), GroupCostKnown: true}}}
	if got, _ := profit(layered); got != "200" {
		t.Fatalf("layers = %s, want 200", got)
	}
	layered.IC = nil
	if got, w := profit(layered); got != "200" || len(w) != 0 {
		t.Fatalf("layers without a stated share = %s warnings %v", got, w)
	}
	// The partner amounts may not be overridden from the request.
	sales := dec("5")
	bad := base
	bad.IC = map[string]ICOverride{"S1": {ICSales: &sales}}
	if _, err := DeriveRun(bad); err == nil || !strings.Contains(err.Error(), "icSales") {
		t.Fatalf("an icSales override next to partner balances: %v", err)
	}
}

// The buyer's ic_inventory balance is the closing value when its inventory lines name the seller; ic_purchases less ic_cogs is the
// fallback, capped at what the seller sold.
func TestUnrealizedFromPartners_InventoryBalanceAndCap(t *testing.T) {
	f := pairFlows{found: true, salesGroup: dec("1000"), costGroup: dec("600"), purchasesGroup: dec("1000"), cogsGroup: dec("700")}
	if p, inv, m := UnrealizedFromPartners(f, "USD"); !p.Equal(dec("120")) || !inv.Equal(dec("300")) || !m.Equal(dec("40")) {
		t.Errorf("purchases less cogs: %s %s %s", p, inv, m)
	}
	f.hasInventory, f.inventoryGroup = true, dec("250")
	if p, inv, _ := UnrealizedFromPartners(f, "USD"); !p.Equal(dec("100")) || !inv.Equal(dec("250")) {
		t.Errorf("inventory balance wins: %s %s", p, inv)
	}
	f.inventoryGroup = dec("4000")
	if _, inv, _ := UnrealizedFromPartners(f, "USD"); !inv.Equal(dec("1000")) {
		t.Errorf("inventory is capped at the seller's sales: %s", inv)
	}
	f.hasInventory, f.cogsGroup = false, dec("1500")
	if p, inv, _ := UnrealizedFromPartners(f, "USD"); !p.IsZero() || !inv.IsZero() {
		t.Errorf("more cogs than purchases leaves no inventory: %s %s", p, inv)
	}
	if p, _, _ := UnrealizedFromPartners(pairFlows{purchasesGroup: dec("5")}, "USD"); !p.IsZero() {
		t.Errorf("no sales, no margin: %s", p)
	}
	// A seller that sold below cost has no margin to eliminate.
	if p, _, m := UnrealizedFromPartners(pairFlows{salesGroup: dec("500"), costGroup: dec("800"), purchasesGroup: dec("500")}, "USD"); !p.IsZero() || !m.IsZero() {
		t.Errorf("loss-making sale: %s %s", p, m)
	}
}

// A subsidiary that sells to its parent (upstream): the subsidiary's sales are in its own currency, the parent's purchases in the group
// currency, and the NCI bears its share of the unrealised profit.
func TestDeriveRun_UpstreamSaleFromPartnerBalances(t *testing.T) {
	units := []Unit{{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}, heldByParent(subsidiary("S2", "EUR", "80"))}
	lines := []CollectLine{collected("S2", "3000", AccountBalanceSheet, RoleEquity, "-800000"), collected("S2", "4000", AccountIncome, "", "-50000")}
	rates := map[string]CollectRate{"EUR": {Average: dec("1.08"), Closing: dec("1.20")}}
	terms := map[string]UnitTerms{"S2": {InvestmentCost: dec("900000"), ShareCapital: dec("800000"), HistoricalRate: dec("1.25"), SellerIsSubsidiary: true}}
	partners, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{
		{UnitID: "S2", Currency: "EUR", Rows: []PartnerRow{prow("4100", "P", "-1000"), prow("5100", "P", "700"), prow("1500", "P", "250")}},
		{UnitID: "P", Currency: "USD", Rows: []PartnerRow{prow("5200", "S2", "1080"), prow("5300", "S2", "756"), prow("2500", "S2", "-300")}},
	}, rates)
	if err != nil {
		t.Fatal(err)
	}
	d, err := DeriveRun(DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: units, Lines: lines, Rates: rates, Terms: terms, PartnerLines: partners,
		Opening: map[string]OpeningBasis{"S2": {NetAssets: dec("750000"), Rate: dec("1.15")}}})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Units[0].Pair
	if !p.ICSales.Equal(dec("1000")) || !p.ICCost.Equal(dec("700")) || !p.ICReceivable.Equal(dec("250")) || !p.PayableGroup.Equal(dec("300")) || p.ICSalesGroup != nil {
		t.Fatalf("upstream pair = %+v", p)
	}
	// Inventory 1,080 - 756 = 324 USD at the seller's margin 30 percent = 97.20; the NCI (20 percent) bears 19.44.
	if p.UnrealizedProfitGroup == nil || !p.UnrealizedProfitGroup.Equal(dec("97.2")) {
		t.Fatalf("unrealised profit = %v, want 97.20", p.UnrealizedProfitGroup)
	}
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	var nci string
	for _, e := range entries {
		if e.RuleCode == RuleE3 && e.Account == AcctNCIEquity {
			nci = e.Debit.String()
		}
	}
	if nci != "19.44" {
		t.Errorf("NCI share of the unrealised profit = %q, want 19.44", nci)
	}
}

// Intercompany balances between two subsidiaries are not a holder-subsidiary pair; the run says they stay uneliminated.
func TestDeriveRun_SiblingBalancesAreReported(t *testing.T) {
	units, lines, rates, terms := threeUnitGroup()
	partners, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{
		{UnitID: "P", Currency: "USD", Rows: []PartnerRow{prow("4100", "S1", "-1000"), prow("5100", "S1", "600")}},
		{UnitID: "S1", Currency: "USD", Rows: []PartnerRow{prow("4100", "S2", "-300"), prow("5100", "S2", "200")}},
	}, rates)
	if err != nil {
		t.Fatal(err)
	}
	d, err := DeriveRun(DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: units, Lines: lines, Rates: rates, Terms: terms, PartnerLines: partners,
		Opening: map[string]OpeningBasis{"S1": {NetAssets: dec("900000"), Rate: dec("1")}, "S2": {NetAssets: dec("750000"), Rate: dec("1.15")}}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range d.Warnings {
		found = found || strings.Contains(w, CodeICOutsidePairs) && strings.Contains(w, "S1") && strings.Contains(w, "S2")
	}
	if !found {
		t.Fatalf("warnings = %v, want CNS-0934 for S1 and S2", d.Warnings)
	}
}

func TestCollectPartners_TranslatesByAccountTypeAndKeepsOnlyICRoles(t *testing.T) {
	rates := map[string]CollectRate{"EUR": {Average: dec("1.10"), Closing: dec("1.20")}}
	got, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{{UnitID: "S2", Currency: "EUR", Rows: []PartnerRow{
		prow("5200", "P", "2000"), prow("2500", "P", "-498"), prow("4000", "P", "-5"), prow("9999", "P", "7")}}}, rates)
	if err != nil || len(got) != 2 {
		t.Fatalf("lines = %+v, %v; want the two role accounts only", got, err)
	}
	if got[0].MainAccount != "2500" || !got[0].GroupAmount.Equal(dec("-597.6")) || got[1].MainAccount != "5200" || !got[1].GroupAmount.Equal(dec("2200")) {
		t.Fatalf("translation = %+v", got)
	}
	if _, err := CollectPartners("USD", partnerMapping, []UnitPartnerLedger{{UnitID: "S9", Currency: "GBP"}}, rates); err == nil {
		t.Fatal("a foreign unit without a rate was accepted")
	}
	if err := ValidateMapping([]AccountMapping{{MainAccount: "5200", ConsolidationAccount: "X", Type: AccountBalanceSheet, Role: RoleICPurchases}}); err == nil {
		t.Fatal("ic_purchases on a balance sheet account was accepted")
	}
	if err := ValidateMapping([]AccountMapping{{MainAccount: "1400", ConsolidationAccount: "X", Type: AccountIncome, Role: RoleICInventory}}); err == nil {
		t.Fatal("ic_inventory on an income account was accepted")
	}
	if !HasICRoles(partnerMapping) || HasICRoles(partnerMapping[len(partnerMapping)-1:]) {
		t.Fatal("HasICRoles")
	}
}
