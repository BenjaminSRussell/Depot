package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var onhandItems = map[string]struct{}{"BOLT-10": {}, "PUMP-1": {}}

func oh(id, item, site, serial, qty, val string) OnhandLine {
	return OnhandLine{LegacyID: id, Item: item, Site: site, Warehouse: "W1", Location: "A-01", Serial: serial, Unit: "EA", Currency: "USD",
		Quantity: decimal.RequireFromString(qty), Value: decimal.RequireFromString(val)}
}

func validatedOnhandRun() *PackRun {
	return &PackRun{RunID: "r", Pack: PackOnhand, SourceCompany: "USMF", Status: StatusValidated}
}

func heldOnhand(in []OnhandLine) map[string]OnhandLine {
	out := make(map[string]OnhandLine, len(in))
	for _, o := range in {
		out[o.LegacyID] = o
	}
	return out
}

func TestValidateOnhand_RulesQuarantineWithReason(t *testing.T) {
	in := []OnhandLine{
		oh("1", "BOLT-10", "S1", "", "100", "250.50"),
		oh("1", "BOLT-10", "S2", "", "5", "5"),      // duplicate legacy id
		oh("2", "BOLT-10", "S1", "", "7", "9"),      // duplicate natural key
		oh("3", "NOPE", "S1", "", "1", "1"),         // unknown item
		oh("4", "BOLT-10", "S3", "", "-2", "4"),     // negative stock
		oh("5", "BOLT-10", "S4", "", "0", "0"),      // zero stock
		oh("6", "BOLT-10", "S5", "", "3", "-1"),     // negative value
		oh("7", "PUMP-1", "S1", "SN-1", "2", "400"), // serial of two
		oh("8", "PUMP-1", "S1", "SN-2", "1", "200"), // good serial
		oh("", "PUMP-1", "S2", "", "1", "1"),        // blank id
		{LegacyID: "9", Item: "PUMP-1", Warehouse: "W1", Unit: "EA", Currency: "USD", Quantity: decimal.NewFromInt(1)},  // no site
		{LegacyID: "10", Item: "PUMP-1", Site: "S1", Warehouse: "W1", Currency: "USD", Quantity: decimal.NewFromInt(1)}, // no unit
	}
	in = append(in, OnhandLine{LegacyID: "11", Item: "PUMP-1", Site: "S9", Warehouse: "W1", Unit: "EA", Currency: "XXX", Quantity: decimal.NewFromInt(1)})
	ok, q := ValidateOnhand(in, onhandItems)
	if len(ok) != 2 || ok[0].LegacyID != "1" || ok[1].LegacyID != "8" {
		t.Fatalf("accepted %+v", ok)
	}
	want := map[string][2]string{
		"1": {RuleUniqueness, "legacy id appears twice"}, "2": {RuleUniqueness, "appear twice"}, "3": {RuleReferential, "not in the item master"},
		"4": {RuleDomain, "must be positive"}, "5": {RuleDomain, "must be positive"}, "6": {RuleDomain, "must not be negative"},
		"7": {RuleDomain, "a serial is exactly one"}, "": {RuleUniqueness, "blank"}, "9": {RuleDomain, "site and warehouse"},
		"10": {RuleDomain, "unit of measure"}, "11": {RuleDomain, "not ISO 4217"},
	}
	if len(q) != len(want) {
		t.Fatalf("quarantined %d, want %d: %+v", len(q), len(want), q)
	}
	for _, h := range q {
		w := want[h.Record.LegacyID]
		if h.Rule != w[0] || !strings.Contains(h.Reason, w[1]) {
			t.Fatalf("%q: %s %q, want %s %q", h.Record.LegacyID, h.Rule, h.Reason, w[0], w[1])
		}
	}
}

func TestOnhandRecord_CarriesItemDimensionAndQuantity(t *testing.T) {
	r := oh("1", "BOLT-10", "S1", "SN-1", "1", "5").Record()
	if r.Account != "BOLT-10" || r.Dimensions != "S1/W1/A-01//SN-1" || r.Amount.String() != "1" {
		t.Fatalf("record %+v", r)
	}
}

func TestReconcileOnhand_CleanLoadReconciles(t *testing.T) {
	in := []OnhandLine{oh("1", "BOLT-10", "S1", "", "100", "250.50"), oh("2", "BOLT-10", "S2", "", "40", "100.25"), oh("3", "PUMP-1", "S1", "SN-1", "1", "400")}
	r := validatedOnhandRun()
	if err := ReconcileOnhand(r, in, in, 3, heldOnhand(in), "", nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled || r.RecordsLoaded != 3 || r.AlreadyPresent != 0 {
		t.Fatalf("run %+v", r)
	}
	names := map[string]int{}
	for _, c := range r.Totals {
		names[c.Name]++
		if c.Status != StatusMatch {
			t.Fatalf("total %+v", c)
		}
	}
	if names[OnhandQuantityName] != 3 || names[OnhandValueName] != 3 || names[OnhandEntityName] != 1 || names[OnhandAccountName] != 0 {
		t.Fatalf("totals %v", names)
	}
	e := totalNamed(t, r.Totals, OnhandEntityName, map[string]string{"company": "USMF", "currency": "USD"})
	if e.Source.String() != "750.75" {
		t.Fatalf("value per entity %s", e.Source)
	}
}

func TestReconcileOnhand_HeldBackLineBreaksQuantityValueAndEntity(t *testing.T) {
	in := []OnhandLine{oh("1", "BOLT-10", "S1", "", "100", "250.50"), oh("2", "BOLT-10", "S2", "", "40", "100.25")}
	held := heldOnhand(in)
	delete(held, "2")
	r := validatedOnhandRun()
	if err := ReconcileOnhand(r, in, in, 1, held, "", nil); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("status %s", r.Status)
	}
	e := totalNamed(t, r.Totals, OnhandEntityName, map[string]string{"company": "USMF", "currency": "USD"})
	if e.Status != StatusMismatch || e.Delta.String() != "-100.25" {
		t.Fatalf("entity total %+v", e)
	}
}

func glBalanceTotal(account, cur, amt string) ControlTotal {
	return ControlTotal{Name: "gl_balances.balance", Key: map[string]string{"company": "USMF", "account": account, "currency": cur, "dimensions": ""},
		Target: decimal.RequireFromString(amt)}
}

// BR-49.3: the inventory GL account equals the loaded on-hand value per entity.
func TestReconcileOnhand_InventoryAccountAgainstGL(t *testing.T) {
	in := []OnhandLine{oh("1", "BOLT-10", "S1", "", "100", "250.50"), oh("2", "BOLT-10", "S2", "", "40", "100.25")}
	gl := []ControlTotal{glBalanceTotal("1400", "USD", "300"), glBalanceTotal("1400", "USD", "50.75"), glBalanceTotal("1500", "USD", "9")}
	r := validatedOnhandRun()
	if err := ReconcileOnhand(r, in, in, 2, heldOnhand(in), "1400", gl); err != nil {
		t.Fatal(err)
	}
	a := totalNamed(t, r.Totals, OnhandAccountName, map[string]string{"company": "USMF", "account": "1400", "currency": "USD"})
	if a.Status != StatusMatch || r.Status != StatusReconciled {
		t.Fatalf("account total %+v, run %s", a, r.Status)
	}

	r = validatedOnhandRun()
	gl[1] = glBalanceTotal("1400", "USD", "50.74")
	if err := ReconcileOnhand(r, in, in, 2, heldOnhand(in), "1400", gl); err != nil {
		t.Fatal(err)
	}
	a = totalNamed(t, r.Totals, OnhandAccountName, map[string]string{"company": "USMF", "account": "1400", "currency": "USD"})
	if a.Status != StatusMismatch || r.Status != StatusLoaded {
		t.Fatalf("a one-cent difference must mismatch: %+v, run %s", a, r.Status)
	}
	msg := OnhandAccountMessage(a)
	if !strings.Contains(msg, "1400") || !strings.Contains(msg, "350.74 USD") || !strings.Contains(msg, "350.75 USD") {
		t.Fatalf("message %q", msg)
	}
}

func TestReconcileOnhand_AccountWithNoGLBalanceMismatchesAndOtherCurrencyIsSeparate(t *testing.T) {
	in := []OnhandLine{oh("1", "BOLT-10", "S1", "", "1", "10")}
	r := validatedOnhandRun()
	if err := ReconcileOnhand(r, in, in, 1, heldOnhand(in), "1400", []ControlTotal{glBalanceTotal("1400", "EUR", "10")}); err != nil {
		t.Fatal(err)
	}
	usd := totalNamed(t, r.Totals, OnhandAccountName, map[string]string{"company": "USMF", "account": "1400", "currency": "USD"})
	eur := totalNamed(t, r.Totals, OnhandAccountName, map[string]string{"company": "USMF", "account": "1400", "currency": "EUR"})
	if usd.Status != StatusMismatch || eur.Status != StatusMismatch {
		t.Fatalf("usd %+v eur %+v", usd, eur)
	}
}

func TestReconcileOnhand_NeedsAValidatedRun(t *testing.T) {
	r := validatedOnhandRun()
	r.Status = StatusLoaded
	if err := ReconcileOnhand(r, nil, nil, 0, nil, "", nil); err == nil {
		t.Fatal("want transition error")
	}
}

func TestHashOnhand_ChangesWithValueAndIgnoresOrder(t *testing.T) {
	a, b := oh("1", "BOLT-10", "S1", "", "1", "10"), oh("2", "PUMP-1", "S1", "", "2", "20")
	if HashOnhand([]OnhandLine{a, b}) != HashOnhand([]OnhandLine{b, a}) {
		t.Fatal("order must not change the hash")
	}
	c := a
	c.Value = decimal.RequireFromString("10.01")
	if HashOnhand([]OnhandLine{a}) == HashOnhand([]OnhandLine{c}) {
		t.Fatal("a changed value must change the hash")
	}
}

// A serial is one unit in one place: the same serial of an item at a second warehouse or location
// is a duplicate even though the full dimension differs, while the same serial text on another
// item is a different unit.
func TestValidateOnhand_SerialOncePerItem(t *testing.T) {
	other := oh("2", "PUMP-1", "S2", "SN-1", "1", "400")
	other.Warehouse = "W9"
	in := []OnhandLine{oh("1", "PUMP-1", "S1", "SN-1", "1", "400"), other, oh("3", "BOLT-10", "S1", "SN-1", "1", "2")}
	ok, q := ValidateOnhand(in, onhandItems)
	if len(ok) != 2 || ok[0].LegacyID != "1" || ok[1].LegacyID != "3" {
		t.Fatalf("accepted %+v", ok)
	}
	if len(q) != 1 || q[0].Record.LegacyID != "2" || q[0].Rule != RuleUniqueness || !strings.Contains(q[0].Reason, `serial "SN-1" of item "PUMP-1" appears twice`) {
		t.Fatalf("quarantined %+v", q)
	}
}

// Legacy codes may hold the separator characters of the display text; two different dimensions
// must still be two keys.
func TestOnhandKey_FieldsCannotCollide(t *testing.T) {
	a := OnhandLine{Item: "A|B", Site: "C", Warehouse: "W"}
	b := OnhandLine{Item: "A", Site: "B|C", Warehouse: "W"}
	if a.Key() == b.Key() {
		t.Fatalf("keys collide: %q", a.Key())
	}
	ok, q := ValidateOnhand([]OnhandLine{
		{LegacyID: "1", Item: "BOLT-10", Site: "S|1", Warehouse: "W", Unit: "EA", Currency: "USD", Quantity: decimal.NewFromInt(1)},
		{LegacyID: "2", Item: "BOLT-10", Site: "S", Warehouse: "1|W", Unit: "EA", Currency: "USD", Quantity: decimal.NewFromInt(1)},
	}, onhandItems)
	if len(ok) != 2 || len(q) != 0 {
		t.Fatalf("accepted %d, quarantined %+v", len(ok), q)
	}
}

// H-S9-1: an onhand run reconciles the factory's staging, not the inventory ledger, so go/no-go
// keeps the packs item open for a plan that includes onhand even when every run is RECONCILED.
func TestEvaluateChecklist_StagedOnhandIsNotLoadedStock(t *testing.T) {
	plan := decisionPlan()
	plan.Packs = append(plan.Packs, PackOnhand)
	packs := allPackFacts(StatusReconciled)
	for _, e := range plan.Entities {
		packs = append(packs, PackFact{Pack: PackOnhand, Company: e, RunID: "onhand-" + e, Status: StatusReconciled, CreatedAt: decisionT0.Add(2 * time.Hour)})
	}
	items, err := EvaluateChecklist(ReadinessFacts{Plan: plan, Packs: packs, Parallel: signedOff()}, allAttested(), glWaived())
	if err != nil {
		t.Fatal(err)
	}
	it := items[0]
	if it.Code != ItemPacksReconciled || it.Status != ItemOpen || !strings.Contains(it.Detail, "onhand/USMF is RECONCILED but staged in the factory only, not posted to the inventory ledger") ||
		strings.Contains(it.Detail, "gl_balances/") || len(it.References) != 6 {
		t.Fatalf("packs item %s %q %v", it.Status, it.Detail, it.References)
	}
	if err := RequireAllMet(items); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a GO must be refused while stock is only staged: %v", err)
	}
}

func TestReconcileOnHandValue_PerAccountAndCurrencyWithinRounding(t *testing.T) {
	d := decimal.RequireFromString
	pump, bolt := "p1", "p2"
	expected := []AccountValue{
		{Account: "1100", Currency: "USD", Value: d("100.00"), Lines: 2, Products: map[string]struct{}{pump: {}}},
		{Account: "1200", Currency: "USD", Value: d("50.00"), Lines: 1, Products: map[string]struct{}{bolt: {}}},
	}
	onHand := map[string]OnHandStock{
		pump: {Qty: d("1"), Value: map[string]decimal.Decimal{"USD": d("100.004")}},
		bolt: {Qty: d("1"), Value: map[string]decimal.Decimal{"USD": d("49.00")}},
	}
	got, err := ReconcileOnHandValue("le", expected, onHand)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Key["account"] != "1100" || got[0].Status != StatusWithinTolerance {
		t.Fatalf("a difference of a rounding is within tolerance: %+v", got[0])
	}
	if got[1].Key["account"] != "1200" || got[1].Status != StatusMismatch || !got[1].Delta.Equal(d("-1")) {
		t.Fatalf("a real difference is a mismatch: %+v", got[1])
	}
	if !OnHandValueRounding("USD", 0).Equal(d("0.01")) || !OnHandValueRounding("USD", 5).Equal(d("0.03")) {
		t.Fatalf("rounding allowance %s %s", OnHandValueRounding("USD", 0), OnHandValueRounding("USD", 5))
	}
}

// The ledger rounds to the currency's own minor unit, so the allowance follows it (JPY none, KWD three decimals).
func TestOnHandValueRounding_FollowsTheCurrencyPrecision(t *testing.T) {
	d := decimal.RequireFromString
	for _, c := range []struct {
		cur   string
		lines int
		want  string
	}{{"JPY", 1, "1"}, {"JPY", 5, "3"}, {"KWD", 1, "0.001"}, {"KWD", 3, "0.002"}, {"USD", 1, "0.01"}} {
		if got := OnHandValueRounding(c.cur, c.lines); !got.Equal(d(c.want)) {
			t.Errorf("%s x %d: %s, want %s", c.cur, c.lines, got, c.want)
		}
	}
	jpy := []AccountValue{{Account: "1100", Currency: "JPY", Value: d("100"), Lines: 1, Products: map[string]struct{}{"p": {}}}}
	got, _ := ReconcileOnHandValue("le", jpy, map[string]OnHandStock{"p": {Value: map[string]decimal.Decimal{"JPY": d("100.4")}}})
	if got[0].Status != StatusWithinTolerance {
		t.Fatalf("a yen of rounding is within tolerance: %+v", got[0])
	}
	kwd := []AccountValue{{Account: "1100", Currency: "KWD", Value: d("10"), Lines: 1, Products: map[string]struct{}{"p": {}}}}
	got, _ = ReconcileOnHandValue("le", kwd, map[string]OnHandStock{"p": {Value: map[string]decimal.Decimal{"KWD": d("10.005")}}})
	if got[0].Status != StatusMismatch {
		t.Fatalf("five fils is not rounding for KWD: %+v", got[0])
	}
}

// A product loaded under two inventory accounts is compared once, for both together, not charged in full to each.
func TestReconcileOnHandValue_AccountsSharingAProductAreComparedTogether(t *testing.T) {
	d := decimal.RequireFromString
	exp := []AccountValue{
		{Account: "1100", Currency: "USD", Value: d("60"), Lines: 1, Products: map[string]struct{}{"p": {}}},
		{Account: "1200", Currency: "USD", Value: d("40"), Lines: 1, Products: map[string]struct{}{"p": {}, "q": {}}},
	}
	got, err := ReconcileOnHandValue("le", exp, map[string]OnHandStock{"p": {Value: map[string]decimal.Decimal{"USD": d("70")}}, "q": {Value: map[string]decimal.Decimal{"USD": d("30")}}})
	if err != nil || len(got) != 1 || got[0].Key["account"] != "1100+1200" || got[0].Status != StatusMatch || !got[0].Target.Equal(d("100")) {
		t.Fatalf("%+v %v", got, err)
	}
}
