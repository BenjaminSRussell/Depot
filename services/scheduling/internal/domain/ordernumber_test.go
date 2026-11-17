package domain

import (
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestCompareOrderNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Production numbers have a minimum width (PO-%06d); past it a plain
		// compare would put PO-1000000 before PO-999999.
		{"PO-999999", "PO-1000000", -1},
		{"PO-000001", "PO-000002", -1},
		// Legacy opening-WIP numbers of any width.
		{"WO-87", "WO-100", -1},
		{"WO-100", "WO-87", 1},
		// Different prefixes compare as text first.
		{"PO-2", "WO-1", -1},
		{"A-10", "B-2", -1},
		// Text after the number.
		{"WO-87A", "WO-87B", -1},
		{"WO-87", "WO-87A", -1},
		{"WO-9-2", "WO-9-10", -1},
		// Equal by value, different spelling: plain compare decides, so the
		// order is still total.
		{"PO-007", "PO-7", -1},
		{"PO-7", "PO-007", 1},
		{"PO-7", "PO-7", 0},
		// A digit run longer than any integer type.
		{"N-123456789012345678901234567890", "N-123456789012345678901234567891", -1},
		// A number and text at the same position: digits sort before letters.
		{"10", "A", -1},
		{"", "A", -1},
	}
	for _, c := range cases {
		if got := CompareOrderNumbers(c.a, c.b); sign(got) != c.want {
			t.Errorf("CompareOrderNumbers(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// The compare is a total order: antisymmetric, zero only for equal strings,
// and transitive, so sorting with it is deterministic.
func TestCompareOrderNumbers_TotalOrder(t *testing.T) {
	gen := rapid.StringMatching(`[A-Z0-9-]{0,8}`)
	rapid.Check(t, func(t *rapid.T) {
		a, b, c := gen.Draw(t, "a"), gen.Draw(t, "b"), gen.Draw(t, "c")
		if sign(CompareOrderNumbers(a, b)) != -sign(CompareOrderNumbers(b, a)) {
			t.Fatalf("not antisymmetric for %q, %q", a, b)
		}
		if (CompareOrderNumbers(a, b) == 0) != (a == b) {
			t.Fatalf("zero for different strings %q, %q", a, b)
		}
		if CompareOrderNumbers(a, b) < 0 && CompareOrderNumbers(b, c) < 0 && CompareOrderNumbers(a, c) >= 0 {
			t.Fatalf("not transitive for %q < %q < %q", a, b, c)
		}
	})
}

// The dispatch plan follows the natural order across widths (FR-23.5).
func TestOrderPlan_NaturalOrderNumbers(t *testing.T) {
	due := at(1, 12, 0)
	ops := []Operation{
		{ID: "big", OrderID: "1", OrderNumber: "PO-1000000", OperNumber: 10, DueAt: due},
		{ID: "small", OrderID: "2", OrderNumber: "PO-999999", OperNumber: 10, DueAt: due},
		{ID: "wo100", OrderID: "3", OrderNumber: "WO-100", OperNumber: 10, DueAt: due},
		{ID: "wo87", OrderID: "4", OrderNumber: "WO-87", OperNumber: 10, DueAt: due},
	}
	got := []string{}
	for _, op := range orderPlan(ops) {
		got = append(got, op.ID)
	}
	if want := []string{"small", "big", "wo87", "wo100"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// BR-23.12: equal changeover and due date go to the order number, not the
// order id. o-b has the larger id but the smaller number, so it goes first;
// an order without a number goes after numbered ones.
func TestSequenceNearest_TieFallsToOrderNumber(t *testing.T) {
	due := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	m, _ := NewSetupMatrix(nil)
	orders := []SetupOrder{
		{OrderID: "o-a", OrderNumber: "PO-1000000", SetupGroup: "X", Due: due},
		{OrderID: "o-0", SetupGroup: "X", Due: due},
		{OrderID: "o-b", OrderNumber: "PO-999999", SetupGroup: "X", Due: due},
	}
	seq := SequenceNearest(m, "X", orders)
	got := []string{}
	for _, o := range seq.Orders {
		got = append(got, o.OrderID)
	}
	if want := []string{"o-b", "o-a", "o-0"}; !slices.Equal(got, want) {
		t.Fatalf("sequence = %v, want %v (order number, natural order, no number last)", got, want)
	}
}

// Two proposals of one group with the same planned start come back in the
// same order every time.
func TestProposeCampaigns_SameStartChunksAreDeterministic(t *testing.T) {
	start := monday.Add(8 * time.Hour)
	cands := []ProposalCandidate{}
	for i := range MaxProposalOrders + 2 {
		cands = append(cands, ProposalCandidate{OrderID: string(rune('a'+i%26)) + string(rune('a'+i/26)), ResourceID: "r", SetupGroup: "G",
			Start: start, RunMinutes: 10})
	}
	first := ProposeCampaigns(cands, monday, 7)
	for range 20 {
		rev := slices.Clone(cands)
		slices.Reverse(rev)
		again := ProposeCampaigns(rev, monday, 7)
		if len(again) != len(first) {
			t.Fatalf("got %d proposals, want %d", len(again), len(first))
		}
		for i := range again {
			if again[i].Orders[0].OrderID != first[i].Orders[0].OrderID {
				t.Fatalf("proposal %d starts with %s, earlier with %s", i, again[i].Orders[0].OrderID, first[i].Orders[0].OrderID)
			}
		}
	}
}
