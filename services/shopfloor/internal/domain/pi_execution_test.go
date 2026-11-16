package domain

import (
	"strings"
	"testing"
	"time"
)

func execDoc() RecipeDoc {
	return docOf(
		Instruction{Category: InstText, Text: "Check the vessel."},
		Instruction{Category: InstDataRequest, Text: "Temp.", Var: "temp", Char: numChar("Temp")},
		Instruction{Category: InstBranch, Text: "Hot?", Condition: "temp > 80", Target: 5},
		Instruction{Category: InstText, Text: "Cool."},
		Instruction{Category: InstReadout, Text: "Oven.", Node: "n", Value: fp(180), Tolerance: fp(5), Char: numChar("Oven")},
		Instruction{Category: InstSignature, Text: "Sign.", Meaning: "verified"},
	)
}

func execMessages(at time.Time) []ExecMessage {
	ten := "10"
	return []ExecMessage{
		{SourceMessageID: "R1:U/O/P/2", OccurredAt: at, Values: []Characteristic{{Name: "Temp", Type: "numeric", Value: "90", Max: &ten, OutOfLimit: true}, {Name: "comment", Type: "text", Value: "probe was cold"}}},
		{SourceMessageID: "r1:U/O/P/3", OccurredAt: at, Values: []Characteristic{{Name: "branch-taken", Type: "boolean", Value: "true"}, {Name: "branch-target", Type: "numeric", Value: "5"}}},
		{SourceMessageID: "R1:U/O/P/5", OccurredAt: at, Values: []Characteristic{{Name: "Oven", Type: "numeric", Value: "190"}, {Name: "readout-source", Type: "text", Value: "entered-by-hand"}, {Name: "expected", Type: "numeric", Value: "180"}, {Name: "tolerance", Type: "numeric", Value: "5"}}},
		{SourceMessageID: "other:U/O/P/1", OccurredAt: at},
	}
}

func TestExecutionSteps_StatusesAndDeviations(t *testing.T) {
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	steps := BuildExecutionSteps("r1", execDoc(), execMessages(at))
	var got []string
	for _, s := range steps {
		got = append(got, s.Status)
	}
	if strings.Join(got, ",") != "acknowledged,recorded,recorded,skipped,recorded,missing" {
		t.Fatalf("statuses = %v", got)
	}
	if !steps[1].Deviation || steps[1].Comment != "probe was cold" || !strings.Contains(steps[1].DeviationReasons[0], "outside its limits") {
		t.Fatalf("an out-of-limit entry: %+v", steps[1])
	}
	if steps[0].Deviation || steps[2].Deviation || steps[3].Deviation {
		t.Fatalf("a step without a problem is a deviation: %+v", steps)
	}
	if r := strings.Join(steps[4].DeviationReasons, "; "); !strings.Contains(r, "entered by hand") || !strings.Contains(r, "outside its tolerance") {
		t.Fatalf("a hand-entered reading outside tolerance: %q", r)
	}
	if !steps[5].Deviation || steps[5].DeviationReasons[0] != "the step was not recorded" {
		t.Fatalf("a signature step with no message: %+v", steps[5])
	}
	if steps[1].Seq != 2 || steps[5].Seq != 6 || steps[1].Key != "U/O/P/2" || steps[1].OccurredAt != "2026-11-15T10:00:00Z" {
		t.Fatalf("seq, key or time: %+v", steps[1])
	}
}

func sealed(t *testing.T) (ExecHeader, string, []ExecStep) {
	t.Helper()
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	steps := BuildExecutionSteps("r1", execDoc(), execMessages(at))
	h := ExecHeader{ID: "e1", RecipeID: "r1", OrderID: "o1", ResourceID: "res", SheetCode: "CHARGE", SheetVersion: 2, ExecutedBy: "operator-1", StartedAt: ExecTime(at), CompletedAt: ExecTime(at.Add(time.Hour)), StepCount: len(steps)}
	h.HeadHash = ChainSteps(ChainSeed(h.ID, h.RecipeID), steps)
	return h, RecordHash(h), steps
}

func TestExecutionChain_VerifiesAndDetectsTampering(t *testing.T) {
	h, rec, steps := sealed(t)
	if r := VerifyChain(h, rec, steps); !r.Valid || r.CheckedSteps != 6 {
		t.Fatalf("an untouched record: %+v", r)
	}
	again := BuildExecutionSteps("r1", execDoc(), execMessages(time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)))
	if ChainSteps(ChainSeed("e1", "r1"), again) != h.HeadHash {
		t.Fatal("the chain is not deterministic")
	}
	clone := func() []ExecStep {
		c := make([]ExecStep, len(steps))
		copy(c, steps)
		return c
	}
	mutations := map[string]struct {
		change func([]ExecStep) []ExecStep
		at     int
	}{
		"a value changed": {func(s []ExecStep) []ExecStep {
			s[1].Values = []Characteristic{{Name: "Temp", Type: "numeric", Value: "70"}}
			return s
		}, 2},
		"a comment erased":   {func(s []ExecStep) []ExecStep { s[1].Comment = ""; return s }, 2},
		"a deviation hidden": {func(s []ExecStep) []ExecStep { s[4].Deviation = false; return s }, 5},
		"a signer swapped":   {func(s []ExecStep) []ExecStep { s[5].SignerUser = "someone-else"; return s }, 6},
		"a time moved":       {func(s []ExecStep) []ExecStep { s[0].OccurredAt = "2026-01-01T00:00:00Z"; return s }, 1},
		"a step removed":     {func(s []ExecStep) []ExecStep { return append(s[:2], s[3:]...) }, 3},
		"the last step cut":  {func(s []ExecStep) []ExecStep { return s[:5] }, 6},
		"steps swapped":      {func(s []ExecStep) []ExecStep { s[1], s[2] = s[2], s[1]; return s }, 2},
		"a step appended":    {func(s []ExecStep) []ExecStep { return append(s, ExecStep{Seq: 7}) }, 7},
		"a hash recomputed":  {func(s []ExecStep) []ExecStep { s[1].Values = nil; s[1].Hash = StepHash(s[1].PrevHash, s[1]); return s }, 3},
	}
	for name, m := range mutations {
		r := VerifyChain(h, rec, m.change(clone()))
		if r.Valid || r.BrokenAtSeq == nil || *r.BrokenAtSeq != m.at {
			t.Errorf("%s: %+v (want broken at %d)", name, r, m.at)
		}
	}
	// A header change (who executed it) or a replaced record hash is caught by the record hash.
	h2 := h
	h2.ExecutedBy = "someone-else"
	if r := VerifyChain(h2, rec, steps); r.Valid || r.BrokenAtSeq == nil || *r.BrokenAtSeq != 0 {
		t.Errorf("a changed header: %+v", r)
	}
	if r := VerifyChain(h, "00", steps); r.Valid {
		t.Errorf("a replaced record hash: %+v", r)
	}
}
