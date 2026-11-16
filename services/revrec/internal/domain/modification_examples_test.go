package domain

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// The contract-modification worked examples 4 to 8 (IFRS 15.18 to 21, ASC 606-10-25-10 to 13) live in
// testdata/modification-examples.json, the file the accountant reviews in the sign-off packet (tools/signoff-packets, #512).
// Each example holds the contract before the modification and the full expected re-allocation.

type exampleLine struct {
	ID     string          `json:"id"`
	Date   string          `json:"date,omitempty"`
	Amount decimal.Decimal `json:"amount"`
	Status string          `json:"status,omitempty"`
}

type exampleObligation struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	SSP       decimal.Decimal `json:"ssp"`
	SSPSource string          `json:"sspSource"`
	Allocated decimal.Decimal `json:"allocated"`
	Lines     []exampleLine   `json:"lines"`
}

type exampleChange struct {
	ID              string          `json:"id"`
	AllocatedBefore decimal.Decimal `json:"allocatedBefore"`
	AllocatedAfter  decimal.Decimal `json:"allocatedAfter"`
	SSPBefore       decimal.Decimal `json:"sspBefore"`
	SSPAfter        decimal.Decimal `json:"sspAfter"`
	Progress        decimal.Decimal `json:"progress"`
	CatchUp         decimal.Decimal `json:"catchUp"`
	Reversal        decimal.Decimal `json:"reversal"`
	Removed         bool            `json:"removed"`
	Lines           []exampleLine   `json:"lines"`
}

type modificationExample struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Standard  string `json:"standard"`
	Narrative string `json:"narrative"`
	Input     struct {
		Type             string                     `json:"type"`
		EffectiveDate    string                     `json:"effectiveDate"`
		Currency         string                     `json:"currency"`
		TransactionPrice decimal.Decimal            `json:"transactionPrice"`
		PriceChange      decimal.Decimal            `json:"priceChange"`
		Scope            map[string]decimal.Decimal `json:"scope,omitempty"`
		Obligations      []exampleObligation        `json:"obligations"`
	} `json:"input"`
	Expected struct {
		TransactionPriceAfter decimal.Decimal `json:"transactionPriceAfter"`
		CatchUp               decimal.Decimal `json:"catchUp"`
		Reversal              decimal.Decimal `json:"reversal"`
		Obligations           []exampleChange `json:"obligations"`
	} `json:"expected"`
}

func loadModificationExamples(t *testing.T) []modificationExample {
	t.Helper()
	raw, err := os.ReadFile("testdata/modification-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Examples []modificationExample `json:"examples"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Examples
}

func (e modificationExample) modificationInput(t *testing.T) ModificationInput {
	t.Helper()
	date := func(s string) *time.Time {
		if s == "" {
			return nil
		}
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("example %d: date %q: %v", e.Number, s, err)
		}
		return &d
	}
	in := ModificationInput{Type: e.Input.Type, EffectiveDate: *date(e.Input.EffectiveDate), Currency: e.Input.Currency,
		TransactionPrice: e.Input.TransactionPrice, PriceChange: e.Input.PriceChange, Scope: e.Input.Scope}
	for _, o := range e.Input.Obligations {
		mo := ModObligation{ID: o.ID, Method: o.Method, SSP: o.SSP, SSPSource: o.SSPSource, Allocated: o.Allocated}
		for _, l := range o.Lines {
			mo.Lines = append(mo.Lines, ModLine{ID: l.ID, Date: date(l.Date), Amount: l.Amount, Status: l.Status})
		}
		in.Obligations = append(in.Obligations, mo)
	}
	return in
}

// changeOf is an obligation change in the file's shape, its lines in id order.
func changeOf(c ObligationChange) exampleChange {
	out := exampleChange{ID: c.ID, AllocatedBefore: c.AllocatedBefore, AllocatedAfter: c.AllocatedAfter, SSPBefore: c.SSPBefore, SSPAfter: c.SSPAfter,
		Progress: c.Progress, CatchUp: c.CatchUp, Reversal: c.Reversal, Removed: c.Removed}
	for _, l := range c.Lines {
		out.Lines = append(out.Lines, exampleLine{ID: l.ID, Amount: l.Amount})
	}
	sort.Slice(out.Lines, func(i, j int) bool { return out.Lines[i].ID < out.Lines[j].ID })
	return out
}

func TestModify_WorkedExamples4To8(t *testing.T) {
	examples := loadModificationExamples(t)
	if len(examples) != 5 {
		t.Fatalf("%d examples, want 5 (worked examples 4 to 8)", len(examples))
	}
	for i, e := range examples {
		if e.Number != 4+i {
			t.Fatalf("example %d is numbered %d", i, e.Number)
		}
		res, err := Modify(e.modificationInput(t))
		if err != nil {
			t.Fatalf("example %d: %v", e.Number, err)
		}
		for _, c := range res.Obligations {
			if c.NewLine != nil {
				t.Errorf("example %d adds a schedule line to %s; the file does not describe one", e.Number, c.ID)
			}
		}
		w := e.Expected
		if !res.TransactionPriceAfter.Equal(w.TransactionPriceAfter) || !res.CatchUp.Equal(w.CatchUp) || !res.Reversal.Equal(w.Reversal) {
			t.Errorf("example %d: price %s catch-up %s reversal %s, want %s %s %s", e.Number, res.TransactionPriceAfter, res.CatchUp, res.Reversal,
				w.TransactionPriceAfter, w.CatchUp, w.Reversal)
		}
		if len(res.Obligations) != len(w.Obligations) {
			t.Fatalf("example %d: %d obligations, want %d", e.Number, len(res.Obligations), len(w.Obligations))
		}
		for j, c := range res.Obligations {
			got, want := changeOf(c), w.Obligations[j]
			if !sameChange(got, want) {
				t.Errorf("example %d obligation %s:\n got  %+v\n want %+v", e.Number, c.ID, got, want)
			}
		}
	}
}

func sameChange(a, b exampleChange) bool {
	if a.ID != b.ID || a.Removed != b.Removed || len(a.Lines) != len(b.Lines) {
		return false
	}
	for _, p := range [][2]decimal.Decimal{{a.AllocatedBefore, b.AllocatedBefore}, {a.AllocatedAfter, b.AllocatedAfter}, {a.SSPBefore, b.SSPBefore},
		{a.SSPAfter, b.SSPAfter}, {a.Progress, b.Progress}, {a.CatchUp, b.CatchUp}, {a.Reversal, b.Reversal}} {
		if !p[0].Equal(p[1]) {
			return false
		}
	}
	for i := range a.Lines {
		if a.Lines[i].ID != b.Lines[i].ID || !a.Lines[i].Amount.Equal(b.Lines[i].Amount) {
			return false
		}
	}
	return true
}
