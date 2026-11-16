package domain

import (
	"github.com/shopspring/decimal"
	"strings"
	"testing"
	"time"
)

func cv(name, typ, val string) Characteristic {
	return Characteristic{Name: name, Type: typ, Value: val}
}

// A phase: 1 temp entry, 2 calculation, 3 branch to 6, 4 readout, 5 timer, 6 signature, 7 text.
func replayDoc() RecipeDoc {
	return docOf(
		Instruction{Category: InstDataRequest, Text: "Temp.", Var: "temp", Char: &InstructionCharacteristic{Name: "Temp", Type: "numeric", Unit: "degC", Min: fp(10), Max: fp(100)}},
		Instruction{Category: InstCalculation, Text: "Double.", Var: "twice", Expression: "temp * 2"},
		Instruction{Category: InstBranch, Text: "Hot?", Condition: "twice > 100", Target: 6},
		Instruction{Category: InstReadout, Text: "Oven.", Node: "ns=2;s=Oven", Value: fp(180), Tolerance: fp(5), Char: &InstructionCharacteristic{Name: "Oven", Type: "numeric", Unit: "degC", Min: fp(150), Max: fp(210)}},
		Instruction{Category: InstTimer, Text: "Cure.", DurationSeconds: 60, Blocking: true},
		Instruction{Category: InstSignature, Text: "Sign.", Meaning: "verified"},
		Instruction{Category: InstText, Text: "Done."},
	)
}

func ctx(t *testing.T, key string, rec map[string][]Characteristic) StepContext {
	t.Helper()
	c, ok := ContextFor(replayDoc(), key, rec)
	if !ok {
		t.Fatalf("no step %s", key)
	}
	return c
}

func TestCheckStepMessage_TheServiceDecides(t *testing.T) {
	rec := map[string][]Characteristic{"U/O/P/1": {cv("Temp", "numeric", "40")}}
	// A data entry takes its limits and unit from the recipe, never from the message.
	got := CheckStepMessage(ctx(t, "U/O/P/1", nil), []Characteristic{{Name: "Temp", Type: "numeric", Value: "40.0", Min: fpStr("-1000"), Max: fpStr("1000")}})
	if got.Refusal != "" || got.Values[0].Value != "40" || *got.Values[0].Max != "100" || *got.Values[0].Min != "10" || *got.Values[0].Unit != "degC" {
		t.Fatalf("data entry: %+v", got)
	}
	for name, vals := range map[string][]Characteristic{
		"another name":  {cv("Pressure", "numeric", "1")},
		"no value":      {},
		"not a number":  {cv("Temp", "numeric", "hot")},
		"an extra name": {cv("Temp", "numeric", "40"), cv("expected", "numeric", "40")},
	} {
		if r := CheckStepMessage(ctx(t, "U/O/P/1", nil), vals); r.Refusal == "" {
			t.Errorf("%s was accepted: %+v", name, r)
		}
	}
	// A calculation is the service's result; a client number that differs is refused, a missing one filled in.
	c2 := ctx(t, "U/O/P/2", rec)
	if r := CheckStepMessage(c2, []Characteristic{cv("twice", "numeric", "80.0")}); r.Refusal != "" || r.Values[0].Value != "80" || r.Values[1].Value != "temp * 2" {
		t.Fatalf("calculation: %+v", r)
	}
	if r := CheckStepMessage(c2, []Characteristic{cv("twice", "numeric", "81")}); r.Refusal == "" || !strings.Contains(r.Refusal, "not 81") {
		t.Fatalf("a forged calculation: %+v", r)
	}
	if r := CheckStepMessage(c2, nil); r.Refusal != "" || r.Values[0].Value != "80" {
		t.Fatalf("calculation without a client value: %+v", r)
	}
	if r := CheckStepMessage(ctx(t, "U/O/P/2", nil), nil); r.Refusal == "" {
		t.Fatalf("a calculation whose input was never recorded: %+v", r)
	}
	// A branch is decided by the service from the stored values, with the document's target.
	rec["U/O/P/2"] = []Characteristic{cv("twice", "numeric", "80")}
	c3 := ctx(t, "U/O/P/3", rec)
	if r := CheckStepMessage(c3, []Characteristic{cv("branch-taken", "boolean", "true"), cv("branch-target", "numeric", "6")}); r.Refusal == "" {
		t.Fatalf("a forged jump: %+v", r)
	}
	if r := CheckStepMessage(c3, []Characteristic{cv("branch-taken", "boolean", "false"), cv("branch-target", "numeric", "999")}); r.Refusal == "" || !strings.Contains(r.Refusal, "target") {
		t.Fatalf("a forged target: %+v", r)
	}
	if r := CheckStepMessage(c3, []Characteristic{cv("branch-taken", "boolean", "false"), cv("branch-target", "numeric", "6")}); r.Refusal != "" || r.Values[0].Value != "false" {
		t.Fatalf("the honest branch: %+v", r)
	}
	// With a hot batch the branch jumps over the readout and the timer, and nothing is recorded for them.
	hot := map[string][]Characteristic{"U/O/P/1": {cv("Temp", "numeric", "80")}}
	hot["U/O/P/2"] = []Characteristic{cv("twice", "numeric", "160")}
	if r := CheckStepMessage(ctx(t, "U/O/P/3", hot), []Characteristic{cv("branch-taken", "boolean", "true")}); r.Refusal != "" || r.Values[1].Value != "6" {
		t.Fatalf("a branch that jumps: %+v", r)
	}
	hot["U/O/P/3"] = []Characteristic{cv("branch-taken", "boolean", "true"), cv("branch-target", "numeric", "6")}
	if r := CheckStepMessage(ctx(t, "U/O/P/4", hot), []Characteristic{cv("Oven", "numeric", "180"), cv("readout-source", "text", "machine")}); r.Refusal == "" || !strings.Contains(r.Refusal, "skipped") {
		t.Fatalf("a message for a skipped step: %+v", r)
	}
	// A readout: expected, tolerance, limits and node are the document's; the source must be named; a hand entry needs a reason.
	c4 := ctx(t, "U/O/P/4", rec)
	r := CheckStepMessage(c4, []Characteristic{cv("Oven", "numeric", "181.5"), cv("readout-source", "text", "machine")})
	names := []string{}
	for _, v := range r.Values {
		names = append(names, v.Name+"="+v.Value)
	}
	if r.Refusal != "" || !r.NeedsClaim || r.Primary.String() != "181.5" || strings.Join(names, ",") != "Oven=181.5,readout-source=machine,node=ns=2;s=Oven,expected=180,tolerance=5" {
		t.Fatalf("machine readout: %v %+v", names, r)
	}
	if r := CheckStepMessage(c4, []Characteristic{cv("Oven", "numeric", "181.5"), cv("readout-source", "text", "guess")}); r.Refusal == "" {
		t.Fatalf("an unknown source: %+v", r)
	}
	if r := CheckStepMessage(c4, []Characteristic{cv("Oven", "numeric", "181.5"), cv("readout-source", "text", ReadoutHand)}); r.Refusal == "" || !strings.Contains(r.Refusal, "comment") {
		t.Fatalf("a hand entry without a reason: %+v", r)
	}
	if r := CheckStepMessage(c4, []Characteristic{cv("Oven", "numeric", "181.5"), cv("readout-source", "text", ReadoutHand), cv("comment", "text", "probe offline")}); r.Refusal != "" || r.NeedsClaim {
		t.Fatalf("a hand entry with a reason: %+v", r)
	}
	// A blocking timer carries the document's duration; the caller enforces the time.
	if r := CheckStepMessage(ctx(t, "U/O/P/5", rec), []Characteristic{cv("timer-seconds", "numeric", "1")}); r.Refusal != "" || r.BlockingSeconds != 60 || r.Values[0].Value != "60" {
		t.Fatalf("timer: %+v", r)
	}
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	if TimerElapsed(at, at.Add(30*time.Second), 60) || !TimerElapsed(at, at.Add(59500*time.Millisecond), 60) || !TimerElapsed(at, at.Add(61*time.Second), 60) {
		t.Fatal("the blocking timer's clock")
	}
}

func fpStr(s string) *string { return &s }

func TestSeal_ReadsTheRecordFromTheRecipeNotTheMessages(t *testing.T) {
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	signedAt := at
	msg := func(key string, signer string, vals ...Characteristic) ExecMessage {
		m := ExecMessage{SourceMessageID: "R1:U/O/P/" + key, OccurredAt: at, Values: vals, SignerUser: signer}
		if signer != "" {
			m.SignerAuthTime = &signedAt
		}
		return m
	}
	statuses := func(steps []ExecStep) string {
		var out []string
		for _, s := range steps {
			d := ""
			if s.Deviation {
				d = "!"
			}
			out = append(out, s.Status+d)
		}
		return strings.Join(out, ",")
	}
	honest := []ExecMessage{
		msg("1", "", cv("Temp", "numeric", "40")),
		msg("2", "", cv("twice", "numeric", "80"), cv("expression", "text", "temp * 2")),
		msg("3", "", cv("branch-taken", "boolean", "false"), cv("branch-target", "numeric", "6")),
		msg("4", "", cv("Oven", "numeric", "181"), cv("readout-source", "text", "machine"), cv("readout-claim", "text", "c1")),
		msg("5", "", cv("timer-seconds", "numeric", "60"), cv("timer-blocking", "boolean", "true")),
		msg("6", "u1", cv("signed-by", "text", "u1"), cv("signature-meaning", "text", "verified")),
	}
	if got := statuses(BuildExecutionSteps("r1", replayDoc(), honest)); got != "recorded,recorded,recorded,recorded,recorded,recorded,acknowledged" {
		t.Fatalf("an honest run: %s", got)
	}
	forge := func(change func(m []ExecMessage)) string {
		m := append([]ExecMessage(nil), honest...)
		change(m)
		return statuses(BuildExecutionSteps("r1", replayDoc(), m))
	}
	// A forged jump (stored before the ingest check existed) skips nothing: the service evaluates the condition itself.
	if got := forge(func(m []ExecMessage) {
		m[2] = msg("3", "", cv("branch-taken", "boolean", "true"), cv("branch-target", "numeric", "999"))
	}); got != "recorded,recorded,recorded!,recorded,recorded,recorded,acknowledged" {
		t.Fatalf("a forged branch: %s", got)
	}
	// A calculation that is not its inputs' result is a deviation.
	if got := forge(func(m []ExecMessage) { m[1] = msg("2", "", cv("twice", "numeric", "81")) }); !strings.Contains(got, "recorded,recorded!,") {
		t.Fatalf("a forged calculation: %s", got)
	}
	// A message cannot move the limits or the expected value: a reading far from the document's expected value is a
	// deviation whatever the message says, and a "machine" reading with no claim, or a hand entry with no reason, is one too.
	if got := forge(func(m []ExecMessage) {
		m[3] = msg("4", "", cv("Oven", "numeric", "200"), cv("readout-source", "text", "machine"), cv("readout-claim", "text", "c1"), cv("expected", "numeric", "200"), cv("tolerance", "numeric", "50"))
	}); !strings.Contains(got, "recorded,recorded,recorded,recorded!,") {
		t.Fatalf("a readout outside the document's tolerance: %s", got)
	}
	if got := forge(func(m []ExecMessage) {
		m[3] = msg("4", "", cv("Oven", "numeric", "181"), cv("readout-source", "text", "machine"))
	}); !strings.Contains(got, "recorded!,recorded,recorded,recorded,") && !strings.Contains(got, ",recorded!,recorded,recorded") {
		t.Fatalf("a machine reading without a claim: %s", got)
	}
	if got := forge(func(m []ExecMessage) {
		m[0] = msg("1", "", cv("Temp", "numeric", "5"), cv("max", "numeric", "1"))
	}); !strings.HasPrefix(got, "recorded!") {
		t.Fatalf("a value outside the document's limits: %s", got)
	}
	// H2: a signature step needs a signer, a step-up time and the step's meaning, or it is missing with a deviation.
	for name, bad := range map[string]ExecMessage{
		"no signer":       msg("6", "", cv("signature-meaning", "text", "verified")),
		"another meaning": msg("6", "u1", cv("signed-by", "text", "u1"), cv("signature-meaning", "text", "performed")),
		"no values":       msg("6", ""),
	} {
		if got := forge(func(m []ExecMessage) { m[5] = bad }); !strings.Contains(got, "missing!,acknowledged") {
			t.Errorf("%s: %s", name, got)
		}
	}
	// A blank recipe id in any case still matches (L5), and a message for a skipped step makes a deviation.
	skip := append([]ExecMessage(nil), honest...)
	skip[0] = msg("1", "", cv("Temp", "numeric", "80"))
	skip[1] = msg("2", "", cv("twice", "numeric", "160"))
	skip[2] = msg("3", "", cv("branch-taken", "boolean", "true"), cv("branch-target", "numeric", "6"))
	skip[3].SourceMessageID = "r1:U/O/P/4"
	if got := statuses(BuildExecutionSteps("R1", replayDoc(), skip)); got != "recorded,recorded,recorded,recorded!,recorded!,recorded,acknowledged" {
		t.Fatalf("a message for a skipped step: %s", got)
	}
}

func TestDesignRules_ReservedNamesSlashesAndTemplateDrift(t *testing.T) {
	for name, in := range map[string]Instruction{
		"var comment":         {Category: InstDataRequest, Char: numChar("T"), Var: "comment"},
		"var expression":      {Category: InstCalculation, Text: "c", Expression: "1", Var: "expression"},
		"characteristic node": {Category: InstReadout, Node: "n", Char: &InstructionCharacteristic{Name: "node", Type: "numeric"}},
		"characteristic tol":  {Category: InstDataRequest, Char: &InstructionCharacteristic{Name: "tolerance", Type: "numeric"}},
		"var on text":         {Category: InstDataRequest, Char: &InstructionCharacteristic{Name: "Colour", Type: "text"}, Var: "colour"},
	} {
		f := docOf(in).FieldErrors(DestKiosk)
		if len(f) == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
	d := docOf(Instruction{Category: InstText, Text: "x"})
	d.Procedure.UnitProcedures[0].Name = "Mix/Heat"
	if f := d.FieldErrors(DestKiosk); f["recipe.procedure.unitProcedures[0].name"] == "" {
		t.Fatalf("a slash in a name: %v", f)
	}
	spec := Instruction{Category: InstSetpoint, Text: "Set.", Value: fp(70), Char: numChar("Jacket")}
	step := spec
	step.Seq, step.Value, step.Var, step.Template = 3, fp(75), "sp", &TemplateRef{Code: "A", Version: 1}
	if TemplateDrift(step, spec) {
		t.Fatal("a setpoint value, position and var are the sheet's own")
	}
	step.Text = "Set it harder."
	if !TemplateDrift(step, spec) {
		t.Fatal("changed text must count as drift")
	}
	for _, src := range []string{"round(1, 13)", "round(1, 1.5)", "round(1, -1)"} {
		if _, err := ParseExpr(src); err == nil {
			t.Errorf("%s was accepted", src)
		}
	}
	e, err := ParseExpr("round(x, y)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Eval(map[string]decimal.Decimal{"x": dec("1.234"), "y": dec("400")}); err == nil {
		t.Fatal("round with 400 places was evaluated")
	}
}
