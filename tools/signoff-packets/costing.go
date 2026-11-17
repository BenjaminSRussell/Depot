package main

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const costingSource = "services/costing/internal/domain/testdata/cost_reference.json"

// costMethods are the seven valuation methods of the reference, in the order the workbook shows them.
var costMethods = []struct{ key, label string }{
	{"FIFO", "FIFO"},
	{"LIFO", "LIFO"},
	{"LIFO_DATE", "LIFO by date"},
	{"WEIGHTED_AVG_PERIODIC", "Weighted average, periodic"},
	{"WEIGHTED_AVG_RUNNING", "Weighted average, running"},
	{"MOVING_AVG", "Moving average"},
	{"STANDARD", "Standard cost"},
}

type costReference struct {
	Sequences []struct {
		ID               string `json:"id"`
		Note             string `json:"note"`
		StandardUnitCost string `json:"standardUnitCost"`
		Transactions     []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Qty   string `json:"qty"`
			Value string `json:"value"`
			Date  string `json:"date"`
		} `json:"transactions"`
		Expected map[string]struct {
			Issues         map[string]string `json:"issues"`
			RemainingQty   string            `json:"remainingQty"`
			RemainingValue string            `json:"remainingValue"`
		} `json:"expected"`
	} `json:"sequences"`
}

func buildCosting(root string) ([]*Table, error) {
	var ref costReference
	if err := readJSON(root, costingSource, &ref); err != nil {
		return nil, err
	}
	sequences := newTable("Sequences", "Inputs: each receipt and issue sequence with the standard unit cost and what it exercises",
		"Sequence", "Standard unit cost", "Receipts", "Issues", "Note")
	txns := newTable("Transactions", "Inputs: every receipt (with its value) and issue (quantity only) in date order per sequence",
		"Sequence", "Transaction", "Kind", "Date", "Quantity", "Receipt value")
	expHeader := []string{"Sequence", "Line", "Date", "Quantity"}
	for _, m := range costMethods {
		expHeader = append(expHeader, m.label)
	}
	expected := newTable("Expected", "Expected: the value of each issue and of the stock left at the end, per method side by side", expHeader...)

	type sums struct{ received, issued, onHand decimal.Decimal }
	totals := make([]sums, len(costMethods))
	qtyIn, qtyOut, qtyLeft := decimal.Zero, decimal.Zero, decimal.Zero
	for _, s := range ref.Sequences {
		receipts, issues := 0, 0
		for _, t := range s.Transactions {
			q, err := dec(t.Qty)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", s.ID, t.ID, err)
			}
			switch t.Kind {
			case "R":
				receipts++
				qtyIn = qtyIn.Add(q)
				v, err := dec(t.Value)
				if err != nil {
					return nil, fmt.Errorf("%s %s: %w", s.ID, t.ID, err)
				}
				for i := range totals {
					totals[i].received = totals[i].received.Add(v)
				}
				txns.add(str(s.ID), str(t.ID), str("Receipt"), str(t.Date), num(t.Qty), num(t.Value))
			case "I":
				issues++
				qtyOut = qtyOut.Add(q)
				txns.add(str(s.ID), str(t.ID), str("Issue"), str(t.Date), num(t.Qty), xlsx.Cell{})
				row := []xlsx.Cell{str(s.ID), str(t.ID), str(t.Date), num(t.Qty)}
				for i, m := range costMethods {
					v, ok := s.Expected[m.key].Issues[t.ID]
					if !ok {
						return nil, fmt.Errorf("%s %s has no expected value under %s", s.ID, t.ID, m.key)
					}
					d, err := dec(v)
					if err != nil {
						return nil, fmt.Errorf("%s %s %s: %w", s.ID, t.ID, m.key, err)
					}
					totals[i].issued = totals[i].issued.Add(d)
					row = append(row, num(v))
				}
				expected.add(row...)
			default:
				return nil, fmt.Errorf("%s %s: unknown kind %q", s.ID, t.ID, t.Kind)
			}
		}
		sequences.add(str(s.ID), num(s.StandardUnitCost), integer(receipts), integer(issues), str(s.Note))
		left := s.Expected[costMethods[0].key].RemainingQty
		row := []xlsx.Cell{str(s.ID), str("On hand at the end"), str(""), num(left)}
		for i, m := range costMethods {
			e, ok := s.Expected[m.key]
			if !ok {
				return nil, fmt.Errorf("%s has no expected result for %s", s.ID, m.key)
			}
			if e.RemainingQty != left {
				return nil, fmt.Errorf("%s: %s leaves %s on hand, FIFO %s", s.ID, m.key, e.RemainingQty, left)
			}
			v, err := dec(e.RemainingValue)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", s.ID, m.key, err)
			}
			totals[i].onHand = totals[i].onHand.Add(v)
			row = append(row, num(e.RemainingValue))
		}
		q, err := dec(left)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.ID, err)
		}
		qtyLeft = qtyLeft.Add(q)
		expected.add(row...)
	}

	recon := newTable("Reconciliation", "Tie-out per method: receipts less issues less stock on hand; standard cost leaves the purchase price variance",
		"Method", "Receipts value", "Issued value", "On hand value", "Difference", "Quantity received", "Quantity issued", "Quantity on hand", "Quantity difference")
	for i, m := range costMethods {
		t := totals[i]
		recon.add(str(m.label), money(t.received), money(t.issued), money(t.onHand), money(t.received.Sub(t.issued).Sub(t.onHand)),
			num(qtyIn.String()), num(qtyOut.String()), num(qtyLeft.String()), num(qtyIn.Sub(qtyOut).Sub(qtyLeft).String()))
	}
	return []*Table{sequences, txns, expected, recon}, nil
}
