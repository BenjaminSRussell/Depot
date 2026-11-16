package domain

import (
	"strings"
	"testing"
)

func fp(f float64) *float64 { return &f }

func docOf(ins ...Instruction) RecipeDoc {
	for i := range ins {
		ins[i].Seq = i + 1
	}
	return RecipeDoc{Procedure: Procedure{Name: "B", UnitProcedures: []UnitProcedure{{Name: "U", Operations: []Operation{{Name: "O", Phases: []Phase{{Name: "P", Instructions: ins}}}}}}}}
}

func numChar(name string) *InstructionCharacteristic {
	return &InstructionCharacteristic{Name: name, Type: "numeric", Unit: "kg", Min: fp(0), Max: fp(100)}
}

func entry(v string) Instruction {
	return Instruction{Category: InstDataRequest, Text: "Weigh.", Var: v, Char: numChar(v)}
}

func TestStepKinds_ValidSheetWithEveryNewKind(t *testing.T) {
	d := docOf(
		entry("gross"),
		Instruction{Category: InstReadout, Text: "Read the scale.", Var: "tare", Char: numChar("Tare"), Node: "ns=2;s=Scale.Tare", Value: fp(0.3), Tolerance: fp(0.05)},
		Instruction{Category: InstCalculation, Text: "Net weight.", Var: "net", Expression: "gross - tare", Char: nil},
		Instruction{Category: InstBranch, Text: "Heavy batch?", Condition: "net > 50 and gross < 99", Target: 5},
		Instruction{Category: InstTimer, Text: "Let it settle.", DurationSeconds: 300, Blocking: true},
		Instruction{Category: InstText, Text: "Continue."},
	)
	if f := d.FieldErrors(DestKiosk); len(f) != 0 {
		t.Fatalf("a valid sheet has errors: %v", f)
	}
	if f := d.FieldErrors(DestPCSOPCUA); f["recipe.procedure.unitProcedures[0].operations[0].phases[0].instructions[1].category"] == "" {
		t.Fatalf("a readout for a process control system must be refused: %v", f)
	}
}

func TestStepKinds_FieldRules(t *testing.T) {
	good := entry("gross")
	cases := map[string]struct {
		in    Instruction
		field string
	}{
		"calc bad expression":            {Instruction{Category: InstCalculation, Text: "x", Expression: "gross +"}, "expression"},
		"calc truth":                     {Instruction{Category: InstCalculation, Text: "x", Expression: "gross > 1"}, "expression"},
		"calc unknown name":              {Instruction{Category: InstCalculation, Text: "x", Expression: "gross * missing"}, "expression"},
		"expression on a text step":      {Instruction{Category: InstText, Text: "x", Expression: "1"}, "expression"},
		"readout without node":           {Instruction{Category: InstReadout, Char: numChar("T")}, "node"},
		"readout text type":              {Instruction{Category: InstReadout, Node: "n", Char: &InstructionCharacteristic{Name: "T", Type: "text"}}, "characteristic.type"},
		"readout without characteristic": {Instruction{Category: InstReadout, Node: "n"}, "characteristic"},
		"tolerance without value":        {Instruction{Category: InstReadout, Node: "n", Char: numChar("T"), Tolerance: fp(1)}, "tolerance"},
		"negative tolerance":             {Instruction{Category: InstReadout, Node: "n", Char: numChar("T"), Value: fp(1), Tolerance: fp(-1)}, "tolerance"},
		"tolerance on a data request":    {Instruction{Category: InstDataRequest, Char: numChar("T"), Tolerance: fp(1)}, "tolerance"},
		"value on a text step":           {Instruction{Category: InstText, Text: "x", Value: fp(1)}, "value"},
		"timer without duration":         {Instruction{Category: InstTimer, Text: "wait"}, "durationSeconds"},
		"timer too long":                 {Instruction{Category: InstTimer, Text: "wait", DurationSeconds: MaxTimerSeconds + 1}, "durationSeconds"},
		"timer fields on a text step":    {Instruction{Category: InstText, Text: "x", DurationSeconds: 5}, "durationSeconds"},
		"blocking on a text step":        {Instruction{Category: InstText, Text: "x", Blocking: true}, "durationSeconds"},
		"branch without condition":       {Instruction{Category: InstBranch, Text: "q", Target: 3}, "condition"},
		"branch number condition":        {Instruction{Category: InstBranch, Text: "q", Condition: "gross + 1", Target: 3}, "condition"},
		"branch back":                    {Instruction{Category: InstBranch, Text: "q", Condition: "gross > 1", Target: 1}, "target"},
		"branch to itself":               {Instruction{Category: InstBranch, Text: "q", Condition: "gross > 1", Target: 2}, "target"},
		"branch past the phase":          {Instruction{Category: InstBranch, Text: "q", Condition: "gross > 1", Target: 9}, "target"},
		"condition on a text step":       {Instruction{Category: InstText, Text: "x", Condition: "gross > 1"}, "condition"},
		"bad var":                        {Instruction{Category: InstDataRequest, Char: numChar("T"), Var: "Gross Weight"}, "var"},
		"reserved var":                   {Instruction{Category: InstDataRequest, Char: numChar("T"), Var: "min"}, "var"},
		"var on a text step":             {Instruction{Category: InstText, Text: "x", Var: "t"}, "var"},
		"duplicate var":                  {Instruction{Category: InstDataRequest, Char: numChar("T"), Var: "gross"}, "var"},
		"bad template reference":         {Instruction{Category: InstText, Text: "x", Template: &TemplateRef{Code: "bad code", Version: 1}}, "template"},
		"template version zero":          {Instruction{Category: InstText, Text: "x", Template: &TemplateRef{Code: "WEIGH", Version: 0}}, "template"},
	}
	for name, tc := range cases {
		f := docOf(good, tc.in, Instruction{Category: InstText, Text: "end"}, Instruction{Category: InstText, Text: "end"}).FieldErrors(DestKiosk)
		found := false
		for k := range f {
			if strings.HasSuffix(k, "instructions[1]."+tc.field) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no error on %s: %v", name, tc.field, f)
		}
	}
	// What a step may carry stays valid: a legacy calculation has only its text.
	if f := docOf(Instruction{Category: InstCalculation, Text: "Work out the net weight."}).FieldErrors(DestKiosk); len(f) != 0 {
		t.Fatalf("a calculation with text only stays valid: %v", f)
	}
}

func TestStepKinds_ScopeOfStepValues(t *testing.T) {
	calc := func(e string) Instruction { return Instruction{Category: InstCalculation, Text: "c", Expression: e} }
	branch := func(c string, to int) Instruction {
		return Instruction{Category: InstBranch, Text: "b", Condition: c, Target: to}
	}
	text := Instruction{Category: InstText, Text: "t"}
	errs := func(d RecipeDoc) string {
		var out []string
		for k, v := range d.FieldErrors(DestKiosk) {
			out = append(out, k[strings.LastIndex(k, "instructions"):]+" "+v)
		}
		return strings.Join(out, "; ")
	}
	// A value is read after the step that produces it, not before it.
	if e := errs(docOf(calc("late * 2"), entry("late"))); !strings.Contains(e, "no earlier step produces") {
		t.Errorf("reading a later step: %q", e)
	}
	// The same name cannot be produced twice, even in another phase.
	d := docOf(entry("a"))
	d.Procedure.UnitProcedures[0].Operations[0].Phases = append(d.Procedure.UnitProcedures[0].Operations[0].Phases, Phase{Name: "Q", Instructions: []Instruction{{Seq: 1, Category: InstDataRequest, Text: "again", Var: "a", Char: numChar("A")}}})
	if e := errs(d); !strings.Contains(e, "used by an earlier step") {
		t.Errorf("a repeated var across phases: %q", e)
	}
	// A value from an earlier phase can be read.
	d = docOf(entry("a"))
	d.Procedure.UnitProcedures[0].Operations[0].Phases = append(d.Procedure.UnitProcedures[0].Operations[0].Phases, Phase{Name: "Q", Instructions: []Instruction{{Seq: 1, Category: InstCalculation, Text: "c", Expression: "a + 1"}}})
	if e := errs(d); e != "" {
		t.Errorf("reading an earlier phase: %q", e)
	}
	// A branch skips the steps between it and its target: a value produced there cannot be read after them...
	skipped := docOf(entry("a"), branch("a > 5", 4), entry("only_if_small"), calc("only_if_small + 1"))
	if e := errs(skipped); !strings.Contains(e, "a branch may skip") {
		t.Errorf("reading a skipped value: %q", e)
	}
	// ...but a step skipped together with it may read it,
	together := docOf(entry("a"), branch("a > 5", 5), entry("b"), calc("b + 1"), text)
	if e := errs(together); e != "" {
		t.Errorf("a step skipped with its source: %q", e)
	}
	// and a step that cannot be skipped may read what is before the branch.
	if e := errs(docOf(entry("a"), branch("a > 5", 3), text, calc("a * 2"))); e != "" {
		t.Errorf("reading before the branch: %q", e)
	}
	// A branch cannot read a value it jumps over.
	if e := errs(docOf(entry("a"), branch("later > 5", 4), entry("later"), text)); !strings.Contains(e, "condition") {
		t.Errorf("branch reading a later step: %q", e)
	}
}

func TestStepTemplateSpec(t *testing.T) {
	ok := map[string]Instruction{
		"text":        {Category: InstText, Text: "Check the vessel."},
		"setpoint":    {Category: InstSetpoint, Text: "Set.", Value: fp(70), Char: &InstructionCharacteristic{Name: "J", Type: "numeric", Min: fp(60), Max: fp(90)}},
		"calculation": {Category: InstCalculation, Text: "Net.", Expression: "gross - tare", Var: "net"},
		"readout":     {Category: InstReadout, Node: "ns=2;s=Oven.Temp", Char: numChar("Temp"), Value: fp(180), Tolerance: fp(2)},
		"timer":       {Category: InstTimer, Text: "Cure.", DurationSeconds: 600, Blocking: true},
		"branch":      {Category: InstBranch, Text: "Too hot?", Condition: "temp > 200"},
		"signature":   {Category: InstSignature, Text: "Sign.", Meaning: "verified"},
	}
	for name, spec := range ok {
		if f := ValidateStepTemplate(spec); len(f) != 0 {
			t.Errorf("%s: %v", name, f)
		}
	}
	bad := map[string]struct {
		spec  Instruction
		field string
	}{
		"calculation without expression": {Instruction{Category: InstCalculation, Text: "Net."}, "spec.expression"},
		"calculation syntax":             {Instruction{Category: InstCalculation, Text: "Net.", Expression: "a +"}, "spec.expression"},
		"branch with a target":           {Instruction{Category: InstBranch, Text: "q", Condition: "a > 1", Target: 3}, "spec.target"},
		"branch without condition":       {Instruction{Category: InstBranch, Text: "q"}, "spec.condition"},
		"timer without duration":         {Instruction{Category: InstTimer, Text: "w"}, "spec.durationSeconds"},
		"readout without node":           {Instruction{Category: InstReadout, Char: numChar("T")}, "spec.node"},
		"unknown kind":                   {Instruction{Category: "script", Text: "x"}, "spec.category"},
		"template of a template":         {Instruction{Category: InstText, Text: "x", Template: &TemplateRef{Code: "A", Version: 1}}, "spec.template"},
	}
	for name, tc := range bad {
		if f := ValidateStepTemplate(tc.spec); f[tc.field] == "" {
			t.Errorf("%s: no error on %s: %v", name, tc.field, f)
		}
	}
	if len(StepTemplateKinds()) != len(instructionCategories) {
		t.Fatalf("the template kinds %v differ from the instruction categories", StepTemplateKinds())
	}
}

func TestTemplateRefsOfADocument(t *testing.T) {
	a, b := &TemplateRef{Code: "WEIGH", Version: 2}, &TemplateRef{Code: "CURE", Version: 1}
	d := docOf(Instruction{Category: InstText, Text: "x", Template: a}, Instruction{Category: InstText, Text: "y", Template: b}, Instruction{Category: InstText, Text: "z", Template: a}, Instruction{Category: InstText, Text: "w"})
	refs := d.TemplateRefs()
	if len(refs) != 2 || refs[0] != *a || refs[1] != *b {
		t.Fatalf("refs = %v", refs)
	}
}
