package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

// ratable12 is a 12-month 2027 obligation of 1,200.00: 100.00 a month, the first `recognized` months recognized.
func ratable12(id string, recognized int) ModObligation {
	o := ModObligation{ID: id, Method: MethodRatable, SSP: dec("1200.00"), SSPSource: "EXPLICIT", Allocated: dec("1200.00")}
	for m := 1; m <= 12; m++ {
		end := time.Date(2027, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC)
		st := ScheduleScheduled
		if m <= recognized {
			st = ScheduleRecognized
		}
		o.Lines = append(o.Lines, ModLine{ID: fmt.Sprintf("%s-%02d", id, m), Date: &end, Amount: dec("100.00"), Status: st})
	}
	return o
}

func amounts(c ObligationChange) map[string]string {
	out := map[string]string{}
	for _, l := range c.Lines {
		out[l.ID] = l.Amount.StringFixed(2)
	}
	return out
}

// Worked example 1 (ASC 606-10-25-13(b)): a 12-month subscription of 1,200.00, three months recognized, the price rises by 120.00 on
// 2027-03-31 and the remaining service is not distinct from what was delivered. New allocation 1,320.00; progress 3/12, so revenue to
// date should be 330.00 against 300.00 recognized: a catch-up of 30.00, and the nine months left carry 990.00 (110.00 each).
func TestModify_CatchUpOnAPriceIncrease(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !res.TransactionPriceAfter.Equal(dec("1320.00")) || !c.AllocatedAfter.Equal(dec("1320.00")) || !c.Progress.Equal(dec("0.25")) {
		t.Fatalf("result = %+v", res)
	}
	if !c.CatchUp.Equal(dec("30.00")) || !res.CatchUp.Equal(dec("30.00")) {
		t.Fatalf("catch-up = %s", c.CatchUp)
	}
	if c.NewLine == nil || c.NewLine.Recognized || !c.NewLine.Amount.Equal(dec("30.00")) || !c.NewLine.Date.Equal(*day("2027-03-31")) {
		t.Fatalf("new line = %+v", c.NewLine)
	}
	got := amounts(c)
	if len(got) != 9 || got["a-04"] != "110.00" || got["a-12"] != "110.00" {
		t.Fatalf("future lines = %v", got)
	}
	// 300.00 recognized + 30.00 catch-up + 9 x 110.00 = 1,320.00 to the cent.
	total := dec("300.00").Add(c.NewLine.Amount)
	for _, l := range c.Lines {
		total = total.Add(l.Amount)
	}
	if !total.Equal(dec("1320.00")) {
		t.Errorf("schedule adds up to %s", total)
	}
}

// Worked example 2 (ASC 606-10-25-13(a)): the same price rise, but the remaining months are distinct, so the 1,020.00 still to be
// recognized (1,320.00 less the 300.00 already recognized) is spread over the nine months left: 113.33 eight times and 113.36 in the
// last (the residual); no catch-up and the recognized 300.00 stays as it is.
func TestModify_ProspectiveSpreadsTheRemainingConsiderationOverTheMonthsLeft(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.CatchUp.IsZero() || c.NewLine != nil || !c.AllocatedAfter.Equal(dec("1320.00")) {
		t.Fatalf("change = %+v", c)
	}
	got := amounts(c)
	if len(got) != 9 || got["a-04"] != "113.33" || got["a-11"] != "113.33" || got["a-12"] != "113.36" {
		t.Fatalf("lines = %v", got)
	}
	total := dec("300.00")
	for _, l := range c.Lines {
		total = total.Add(l.Amount)
	}
	if !total.Equal(dec("1320.00")) {
		t.Errorf("schedule adds up to %s", total)
	}
}

// Worked example 3: a bundle of a perpetual licence (milestone, SSP 800.00, satisfied) and a 12-month support plan (SSP 1,200.00, three
// months recognized). The contract price rises by 1,000.00 to 3,000.00. The pool 3,000.00 over SSPs 800 and 1,200 gives 1,200.00 and
// 1,800.00. The licence is fully transferred: a catch-up of 400.00 is recognized at once. The support is 25% transferred: 450.00 to
// date against 300.00 recognized, a catch-up of 150.00, and 1,350.00 over the nine months left (150.00 each).
func TestModify_CatchUpAcrossABundleCatchesUpTheSatisfiedMilestone(t *testing.T) {
	licence := ModObligation{ID: "lic", Method: MethodMilestone, SSP: dec("800.00"), SSPSource: "EXPLICIT", Allocated: dec("800.00"),
		Lines: []ModLine{{ID: "lic-1", Date: day("2027-01-15"), Amount: dec("800.00"), Status: ScheduleRecognized}}}
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		PriceChange: dec("1000.00"), Obligations: []ModObligation{licence, ratable12("sup", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	lic, sup := res.Obligations[0], res.Obligations[1]
	if !lic.AllocatedAfter.Equal(dec("1200.00")) || !sup.AllocatedAfter.Equal(dec("1800.00")) {
		t.Fatalf("allocation = %s / %s", lic.AllocatedAfter, sup.AllocatedAfter)
	}
	if !lic.CatchUp.Equal(dec("400.00")) || lic.NewLine == nil || !lic.NewLine.Recognized || len(lic.Lines) != 0 {
		t.Fatalf("licence = %+v", lic)
	}
	if !sup.CatchUp.Equal(dec("150.00")) || !res.CatchUp.Equal(dec("550.00")) {
		t.Fatalf("support catch-up = %s total %s", sup.CatchUp, res.CatchUp)
	}
	if got := amounts(sup); len(got) != 9 || got["sup-04"] != "150.00" || got["sup-12"] != "150.00" {
		t.Fatalf("support lines = %v", got)
	}
}

// Worked examples 4 to 8 are in testdata/modification-examples.json (TestModify_WorkedExamples4To8), the file the accountant
// reviews in the sign-off packet.

// What revrec cannot take back: a price below what the point-in-time goods were billed at is a credit note (REV-0402), and a scope
// outside 0..1 is refused.
func TestModify_ACutBelowPointInTimeRevenueIsRefused(t *testing.T) {
	goods := ModObligation{ID: "g", Method: MethodPointInTime, SSP: dec("500.00"), SSPSource: "EXPLICIT", Allocated: dec("500.00"),
		Lines: []ModLine{{ID: "g-1", Date: day("2027-01-05"), Amount: dec("500.00"), Status: ScheduleRecognized}}}
	for _, typ := range []string{ModCatchUp, ModProspective} {
		_, err := Modify(ModificationInput{Type: typ, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1700.00"),
			PriceChange: dec("-1300.00"), Obligations: []ModObligation{goods, ratable12("a", 6)}})
		if e, ok := err.(*Error); !ok || e.Code != CodeModBelowRecognized {
			t.Fatalf("%s cut: %v", typ, err)
		}
	}
	if _, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-1"), Scope: map[string]decimal.Decimal{"a": dec("1.5")}, Obligations: []ModObligation{ratable12("a", 6)}}); err == nil {
		t.Fatal("scope 1.5 accepted")
	}
}

// A credited contract: 200.00 of recognized revenue was reversed by a credit note, so the obligation holds 400.00 net of six months. A
// catch-up to 1,000.00 at 6/12 gives 500.00 to date: a catch-up of 100.00 on the net figure.
func TestModify_CatchUpNetsCreditNoteReversals(t *testing.T) {
	o := ratable12("a", 6)
	o.Reversed, o.Allocated = dec("200.00"), dec("1000.00")
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1000.00"),
		PriceChange: dec("0"), NewSSP: map[string]decimal.Decimal{"a": dec("1000.00")}, Obligations: []ModObligation{o}})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Obligations[0]; !c.CatchUp.Equal(dec("100.00")) || !c.Reversal.IsZero() {
		t.Fatalf("change = %+v", c)
	}
}

// A price cut that stays above what is recognized works in both ways: prospective lowers the months left; catch-up here has less to
// recognize at the date than was recognized only if the cut is deep, so a small one is a negative catch-up and is refused, while a
// price rise on a fully recognized obligation is caught up in one line.
func TestModify_ProspectiveCutAndFullyRecognizedCatchUp(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("-300.00"), Obligations: []ModObligation{ratable12("a", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := amounts(res.Obligations[0]); len(got) != 6 || got["a-07"] != "50.00" || got["a-12"] != "50.00" {
		t.Errorf("prospective cut lines = %v", got)
	}
	done, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-12-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("60.00"), Obligations: []ModObligation{ratable12("a", 12)}})
	if err != nil {
		t.Fatal(err)
	}
	if c := done.Obligations[0]; !c.CatchUp.Equal(dec("60.00")) || c.NewLine == nil || len(c.Lines) != 0 {
		t.Errorf("fully recognized catch-up = %+v", c)
	}
	if _, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-12-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("60.00"), Obligations: []ModObligation{ratable12("a", 12)}}); err == nil {
		t.Error("a prospective change with nothing left was accepted")
	}
}

// Obligations booked by the invoice (point in time) are left alone and their price is taken out of the pool.
func TestModify_PointInTimeObligationsAreKept(t *testing.T) {
	goods := ModObligation{ID: "g", Method: MethodPointInTime, SSP: dec("500.00"), SSPSource: "EXPLICIT", Allocated: dec("500.00"),
		Lines: []ModLine{{ID: "g-1", Date: day("2027-01-05"), Amount: dec("500.00"), Status: ScheduleRecognized}}}
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1700.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{goods, ratable12("a", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Obligations) != 1 || res.Obligations[0].ID != "a" || !res.Obligations[0].AllocatedAfter.Equal(dec("1320.00")) {
		t.Fatalf("result = %+v", res)
	}
}

// An SSP revision applies at the modification date: the unsatisfied remainder is weighted by SSP and the share left.
func TestModify_ProspectiveSSPRevisionShiftsTheRemainingConsideration(t *testing.T) {
	a, b := ratable12("a", 0), ratable12("b", 0)
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-01-01"), Currency: "USD", TransactionPrice: dec("2400.00"),
		PriceChange: dec("0"), NewSSP: map[string]decimal.Decimal{"b": dec("3600.00")}, Obligations: []ModObligation{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	// 2,400.00 over SSPs 1,200 : 3,600 = 600.00 : 1,800.00.
	if !res.Obligations[0].AllocatedAfter.Equal(dec("600.00")) || !res.Obligations[1].AllocatedAfter.Equal(dec("1800.00")) || !res.Obligations[1].SSPAfter.Equal(dec("3600.00")) {
		t.Fatalf("allocation = %s / %s", res.Obligations[0].AllocatedAfter, res.Obligations[1].AllocatedAfter)
	}
	if got := amounts(res.Obligations[0]); got["a-01"] != "50.00" {
		t.Errorf("a lines = %v", got)
	}
}

// Earned lines not yet recognized: two months recognized and March (dated on the effective date) still SCHEDULED. Revenue to date is 330.00,
// 200.00 is recognized and March's old 100.00 will be recognized anyway, so the catch-up is 30.00, not 130.00; March's line carries the whole
// 130.00 and no new line is added.
func TestModify_CatchUpDoesNotCountEarnedLinesThatAreNotRecognizedYet(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("1200.00"),
		PriceChange: dec("120.00"), Obligations: []ModObligation{ratable12("a", 2)}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.CatchUp.Equal(dec("30.00")) || !res.CatchUp.Equal(dec("30.00")) || c.NewLine != nil {
		t.Fatalf("catch-up %s total %s new line %+v", c.CatchUp, res.CatchUp, c.NewLine)
	}
	got := amounts(c)
	if got["a-03"] != "130.00" || got["a-04"] != "110.00" || len(got) != 10 {
		t.Fatalf("lines = %v", got)
	}
}

// credited12 is ratable12 with six months recognized and a credit note that took 300.00 of deferred revenue off the six months left
// (50.00 each, credited_amount 50.00 each).
func credited12(id string) ModObligation {
	o := ratable12(id, 6)
	for i := 6; i < 12; i++ {
		o.Lines[i].Amount, o.Lines[i].Credited = dec("50.00"), dec("50.00")
	}
	o.Allocated = dec("900.00")
	return o
}

// Review case: a 1,200.00 ratable obligation, 600.00 recognized over six months, a credit note took 300.00 off the six months left. A
// catch-up of +100.00 at the end of month 6 measures progress by time, 0.5 (not 600/900 from the credited amounts): revenue to date is
// 1,000.00 x 0.5 = 500.00 against 600.00 recognized, so 100.00 comes back out.
func TestModify_CatchUpOnACreditedObligationMeasuresProgressBeforeTheCredit(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("900.00"),
		PriceChange: dec("100.00"), Obligations: []ModObligation{credited12("a")}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Obligations[0]
	if !c.Progress.Equal(dec("0.5")) || !c.AllocatedAfter.Equal(dec("1000.00")) || !c.Reversal.Equal(dec("100.00")) || !c.CatchUp.Equal(dec("-100.00")) {
		t.Fatalf("change = %+v", c)
	}
	// A line credit notes took to zero still counts in the measure.
	o := credited12("b")
	o.Lines[11].Amount, o.Lines[11].Credited, o.Lines[11].Status = dec("0"), dec("100.00"), ScheduleReversed
	if p := Progress(o, *day("2027-06-30")); !p.Equal(dec("0.5")) {
		t.Fatalf("progress with a credit-reversed line = %s", p)
	}
}

// The same credit skews prospective weights too: a credited and an uncredited obligation, both half delivered with SSP 1,200.00, share the
// remaining 1,200.00 equally (600.00 each), not 480.00 / 720.00 by their credited amounts.
func TestModify_ProspectiveWeightsACreditedObligationByItsMeasure(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModProspective, EffectiveDate: *day("2027-06-30"), Currency: "USD", TransactionPrice: dec("2100.00"),
		PriceChange: dec("300.00"), Obligations: []ModObligation{credited12("a"), ratable12("b", 6)}})
	if err != nil {
		t.Fatal(err)
	}
	a, b := res.Obligations[0], res.Obligations[1]
	if !a.AllocatedAfter.Equal(dec("1200.00")) || !b.AllocatedAfter.Equal(dec("1200.00")) {
		t.Fatalf("allocation %s / %s", a.AllocatedAfter, b.AllocatedAfter)
	}
}

// usageBundle is the contract of worked examples 10 to 12: a 12-month platform subscription (RATABLE, SSP 1,200.00, 100.00 a month, three
// months recognized) and an API-calls obligation (USAGE, SSP 800.00, allocation 800.00 over 8,000 calls, a rate of 0.10 a call) of which
// 2,000 calls were consumed by the effective date and 200.00 recognized at the old rate. The price is 2,000.00.
func usageBundle() []ModObligation {
	api := ModObligation{ID: "api", Method: MethodUsage, SSP: dec("800.00"), SSPSource: "EXPLICIT", Allocated: dec("800.00"),
		Lines: []ModLine{{ID: "api-1", Date: day("2027-03-31"), Amount: dec("200.00"), Status: ScheduleRecognized}},
		Usage: &UsageMeasure{ToDate: dec("2000"), Expected: dec("8000")}}
	return []ModObligation{ratable12("plat", 3), api}
}

// Worked example 10 (ASC 606-10-25-13(b), USAGE catch-up): the price rises by 400.00 to 2,400.00 on 2027-03-31. The pool 2,400.00 over
// SSPs 1,200 : 800 gives 1,440.00 to the platform and 960.00 to the API calls. The platform is 25% transferred: 360.00 to date against
// 300.00 recognized, a catch-up of 60.00, and 1,080.00 over nine months (120.00 each). The API calls' reallocated rate is 960.00 / 8,000 =
// 0.12 a call, so 2,000 calls are worth 240.00 against the 200.00 recognized at the old rate: a catch-up of 40.00, recognized at once,
// and no schedule line for the 720.00 the allocation still holds (it is recognized as usage posts).
func TestModify_CatchUpRecognizesUsageToDateAtTheReallocatedRate(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		PriceChange: dec("400.00"), Obligations: usageBundle()})
	if err != nil {
		t.Fatal(err)
	}
	plat, api := res.Obligations[0], res.Obligations[1]
	if !plat.AllocatedAfter.Equal(dec("1440.00")) || !api.AllocatedAfter.Equal(dec("960.00")) {
		t.Fatalf("allocation = %s / %s", plat.AllocatedAfter, api.AllocatedAfter)
	}
	if got := amounts(plat); !plat.CatchUp.Equal(dec("60.00")) || len(got) != 9 || got["plat-04"] != "120.00" || got["plat-12"] != "120.00" {
		t.Fatalf("platform catch-up %s lines %v", plat.CatchUp, got)
	}
	if !api.Progress.Equal(dec("0.25")) || !api.CatchUp.Equal(dec("40.00")) || api.Reversal.IsPositive() || len(api.Lines) != 0 {
		t.Fatalf("api = %+v", api)
	}
	if api.NewLine == nil || !api.NewLine.Recognized || !api.NewLine.Amount.Equal(dec("40.00")) || !api.NewLine.Date.Equal(*day("2027-03-31")) {
		t.Fatalf("api new line = %+v", api.NewLine)
	}
	if !res.CatchUp.Equal(dec("100.00")) || !res.Reversal.IsZero() {
		t.Fatalf("catch-up %s reversal %s", res.CatchUp, res.Reversal)
	}
	// Recognized to date: platform 300.00 + 60.00, API calls 200.00 + 40.00 = 600.00 = 360.00 + 240.00.
	if got := dec("300.00").Add(plat.CatchUp).Add(dec("200.00")).Add(api.CatchUp); !got.Equal(dec("600.00")) {
		t.Errorf("revenue to date = %s", got)
	}
}

// Worked example 11 (USAGE catch-up, negative): the price falls by 400.00 to 1,600.00. The pool 1,600.00 over SSPs 1,200 : 800 gives
// 960.00 and 640.00. The platform's 240.00 to date is below the 300.00 recognized: 60.00 comes back out of revenue. The API calls'
// rate falls to 640.00 / 8,000 = 0.08, so 2,000 calls are worth 160.00 against 200.00 recognized: 40.00 comes back out, Dr 4000 / Cr 2400.
func TestModify_CatchUpTakesBackUsageRevenueWhenTheRateFalls(t *testing.T) {
	res, err := Modify(ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		PriceChange: dec("-400.00"), Obligations: usageBundle()})
	if err != nil {
		t.Fatal(err)
	}
	plat, api := res.Obligations[0], res.Obligations[1]
	if !api.AllocatedAfter.Equal(dec("640.00")) || !api.CatchUp.Equal(dec("-40.00")) || !api.Reversal.Equal(dec("40.00")) || api.NewLine != nil {
		t.Fatalf("api = %+v", api)
	}
	if !plat.Reversal.Equal(dec("60.00")) || !res.Reversal.Equal(dec("100.00")) {
		t.Fatalf("platform reversal %s total %s", plat.Reversal, res.Reversal)
	}
}

// Worked example 12 (USAGE catch-up, SSP revision and the cap): the price stays 2,000.00 and the API calls' SSP is revised to 1,200.00:
// the pool 2,000.00 over SSPs 1,200 : 1,200 gives 1,000.00 each, a rate of 0.125 a call, so 2,000 calls are worth 250.00 (catch-up
// 50.00). Had 9,000 calls been consumed against the 8,000 the allocation covers, the revenue to date is capped at the allocation 1,000.00,
// a catch-up of 800.00; the overage belongs to the usage invoices.
func TestModify_CatchUpSSPRevisionOfAUsageObligationAndTheCap(t *testing.T) {
	in := ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"),
		NewSSP: map[string]decimal.Decimal{"api": dec("1200.00")}, Obligations: usageBundle()}
	res, err := Modify(in)
	if err != nil {
		t.Fatal(err)
	}
	if api := res.Obligations[1]; !api.AllocatedAfter.Equal(dec("1000.00")) || !api.CatchUp.Equal(dec("50.00")) || !api.SSPAfter.Equal(dec("1200.00")) {
		t.Fatalf("api = %+v", api)
	}
	if plat := res.Obligations[0]; !plat.AllocatedAfter.Equal(dec("1000.00")) {
		t.Fatalf("platform = %+v", plat)
	}
	in.Obligations = usageBundle()
	in.Obligations[1].Usage = &UsageMeasure{ToDate: dec("9000"), Expected: dec("8000")}
	res, err = Modify(in)
	if err != nil {
		t.Fatal(err)
	}
	if api := res.Obligations[1]; !api.Progress.Equal(dec("1")) || !api.CatchUp.Equal(dec("800.00")) {
		t.Fatalf("capped api = %+v", api)
	}
}

// Usage rules: a USAGE obligation without usage to date keeps its allocation (the pool is what is left of the price), usage is refused
// for a prospective modification and for an obligation that is not USAGE, and the expected usage must be positive.
func TestModify_UsageToDateRules(t *testing.T) {
	base := ModificationInput{Type: ModCatchUp, EffectiveDate: *day("2027-03-31"), Currency: "USD", TransactionPrice: dec("2000.00"), PriceChange: dec("400.00")}
	// Without usage to date the API calls keep 800.00: the platform takes the whole 1,600.00 and 1,200.00 + 400.00 is its catch-up base.
	fixed := usageBundle()
	fixed[1].Usage = nil
	in := base
	in.Obligations = fixed
	res, err := Modify(in)
	if err != nil || len(res.Obligations) != 1 || !res.Obligations[0].AllocatedAfter.Equal(dec("1600.00")) {
		t.Fatalf("usage left as it is: %+v %v", res, err)
	}
	prospective := base
	prospective.Type = ModProspective
	prospective.Obligations = usageBundle()
	if _, err := Modify(prospective); errCode(err) != CodeModUsage {
		t.Fatalf("prospective with usage: %v", err)
	}
	wrong := base
	wrong.Obligations = usageBundle()
	wrong.Obligations[0].Usage = &UsageMeasure{ToDate: dec("1"), Expected: dec("2")}
	if _, err := Modify(wrong); errCode(err) != CodeModUsage {
		t.Fatalf("usage on a ratable obligation: %v", err)
	}
	zero := base
	zero.Obligations = usageBundle()
	zero.Obligations[1].Usage = &UsageMeasure{ToDate: dec("1"), Expected: dec("0")}
	if _, err := Modify(zero); errCode(err) != CodeModUsage {
		t.Fatalf("zero expected usage: %v", err)
	}
	scoped := base
	scoped.Obligations = usageBundle()
	scoped.Scope = map[string]decimal.Decimal{"api": dec("0.5")}
	if _, err := Modify(scoped); errCode(err) != CodeModUsage {
		t.Fatalf("scope on a usage obligation: %v", err)
	}
}

func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
