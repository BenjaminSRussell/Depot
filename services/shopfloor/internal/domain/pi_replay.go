package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// The server is authoritative for what a PI execution record says. Everything a kiosk step records is checked against
// the recipe document when the message arrives (CheckStepMessage) and is derived from the document and the stored values
// again when the record is sealed (BuildExecutionSteps): branch decisions, calculation results, limits, expected values
// and tolerances come from the document and the server's own evaluation, never from the message.

// Value names the service itself writes into step messages. A characteristic or var of a sheet may not use them.
var ReservedValueNames = []string{
	"comment", "signature-meaning", "signed-by", "readout-source", "readout-edge", "readout-read-at", "readout-claim", "node",
	"expected", "tolerance", "expression", "timer-seconds", "timer-blocking", "branch-taken", "branch-target",
}

// ReservedValueName reports whether n is one of ReservedValueNames.
func ReservedValueName(n string) bool {
	for _, r := range ReservedValueNames {
		if r == n {
			return true
		}
	}
	return false
}

// Refusal codes of the step checks (422).
const (
	// CodeStepMessage is SF-0198: a kiosk step message disagrees with the recipe or with what the service knows.
	CodeStepMessage = "SF-0198"
	// CodeTimerRunning is SF-0199: a blocking timer is recorded before it has run out.
	CodeTimerRunning = "SF-0199"
)

// Readout sources.
const (
	ReadoutMachine = "machine"
	ReadoutHand    = "entered-by-hand"
)

// stepVisit is one step of the recipe as the walk meets it, with what is known before it.
type stepVisit struct {
	Key        string
	PhasePath  string
	In         Instruction
	PhaseSteps int
	Vars       map[string]decimal.Decimal
	Skipped    bool
	// Jump is the server's evaluation of a branch step; JumpErr says why it could not be decided.
	Jump    bool
	JumpErr string
	// Calc is the server's result of a calculation step; CalcErr says why it could not be worked out.
	Calc    *decimal.Decimal
	CalcErr string
}

func valueNamed(vals []Characteristic, name string) (Characteristic, bool) {
	for _, v := range vals {
		if v.Name == name {
			return v, true
		}
	}
	return Characteristic{}, false
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func ptrStr(f *float64) *string {
	if f == nil {
		return nil
	}
	s := fmtFloat(*f)
	return &s
}

// producedValue is the number a step's recorded values give its var.
func producedValue(in Instruction, vals []Characteristic) (decimal.Decimal, bool) {
	switch in.Category {
	case InstSetpoint:
		if in.Value != nil {
			return decimal.NewFromFloat(*in.Value), true
		}
	case InstDataRequest, InstInspection, InstReadout:
		if in.Char == nil {
			return decimal.Zero, false
		}
		v, ok := valueNamed(vals, in.Char.Name)
		if !ok {
			return decimal.Zero, false
		}
		if v.Type == "boolean" {
			if v.Value == "true" {
				return decimal.NewFromInt(1), true
			}
			return decimal.Zero, true
		}
		d, err := decimal.NewFromString(v.Value)
		return d, err == nil
	}
	return decimal.Zero, false
}

func copyVars(m map[string]decimal.Decimal) map[string]decimal.Decimal {
	out := make(map[string]decimal.Decimal, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// walkRecipe visits every step in order. Before the visit it evaluates the step's calculation or branch from the values
// of the steps before it; after it, it takes the step's value into the vars (a recorded one, or a calculation's own
// result) and applies a jump to the steps it skips. visit returns false to stop.
func walkRecipe(doc RecipeDoc, recorded map[string][]Characteristic, visit func(*stepVisit) bool) {
	vars := map[string]decimal.Decimal{}
	for _, up := range doc.Procedure.UnitProcedures {
		for _, op := range up.Operations {
			for _, ph := range op.Phases {
				path := strings.Join([]string{doc.Procedure.Name, up.Name, op.Name, ph.Name}, "/")
				skipped := map[int]bool{}
				for _, in := range ph.Instructions {
					key := StepKey(up.Name, op.Name, ph.Name, in.Seq)
					v := &stepVisit{Key: key, PhasePath: path, In: in, PhaseSteps: len(ph.Instructions), Vars: copyVars(vars), Skipped: skipped[in.Seq]}
					if !v.Skipped {
						switch {
						case in.Category == InstCalculation && in.Expression != "":
							v.Calc, v.CalcErr = evalNumber(in.Expression, vars)
						case in.Category == InstBranch:
							v.Jump, v.JumpErr = evalTruth(in.Condition, vars)
						}
					}
					if !visit(v) {
						return
					}
					if v.Skipped {
						continue
					}
					vals, rec := recorded[key]
					switch {
					case in.Category == InstCalculation && v.Calc != nil && in.Var != "":
						vars[in.Var] = *v.Calc
					case in.Var != "" && rec:
						if d, ok := producedValue(in, vals); ok {
							vars[in.Var] = d
						}
					}
					if in.Category == InstBranch && v.Jump {
						for n := in.Seq + 1; n < in.Target; n++ {
							skipped[n] = true
						}
					}
				}
			}
		}
	}
}

func evalNumber(src string, vars map[string]decimal.Decimal) (*decimal.Decimal, string) {
	e, err := ParseExpr(src)
	if err != nil {
		return nil, err.Error()
	}
	r, err := e.Eval(vars)
	if err != nil {
		return nil, err.Error()
	}
	if r.Type != ExprNumber {
		return nil, "the formula is not a number"
	}
	return &r.Num, ""
}

func evalTruth(src string, vars map[string]decimal.Decimal) (bool, string) {
	e, err := ParseExpr(src)
	if err != nil {
		return false, err.Error()
	}
	r, err := e.Eval(vars)
	if err != nil {
		return false, err.Error()
	}
	if r.Type != ExprBool {
		return false, "the condition is not true or false"
	}
	return r.Bool, ""
}

// StepContext is what the service knows about a step before a message for it is recorded.
type StepContext struct {
	v *stepVisit
}

// ContextFor walks the recipe up to the step with the key, over the values stored for the steps before it.
func ContextFor(doc RecipeDoc, key string, recorded map[string][]Characteristic) (StepContext, bool) {
	var found *stepVisit
	walkRecipe(doc, recorded, func(v *stepVisit) bool {
		if v.Key == key {
			found = v
			return false
		}
		return true
	})
	return StepContext{v: found}, found != nil
}

// Step is the recipe instruction of the context.
func (c StepContext) Step() Instruction { return c.v.In }

// StepCheck is the outcome of checking a step message: the values to store (the service's own), or a refusal.
type StepCheck struct {
	Values  []Characteristic
	Refusal string
	Code    string
	// NeedsClaim is set for a machine readout reported as read from the machine: the caller must match Primary against the
	// reading the service handed out for the step.
	NeedsClaim bool
	Primary    decimal.Decimal
	// BlockingSeconds is the length of a blocking timer, which may be recorded only after it has run out.
	BlockingSeconds int
}

func refuse(format string, a ...any) StepCheck {
	return StepCheck{Refusal: fmt.Sprintf(format, a...), Code: CodeStepMessage}
}

// onlyNames reports a value of vals whose name is not in allowed.
func onlyNames(vals []Characteristic, allowed ...string) (string, bool) {
next:
	for _, v := range vals {
		for _, a := range allowed {
			if v.Name == a {
				continue next
			}
		}
		return v.Name, false
	}
	return "", true
}

func charValue(c *InstructionCharacteristic, raw string) (Characteristic, error) {
	out := Characteristic{Name: c.Name, Type: c.Type}
	switch c.Type {
	case "numeric":
		d, err := decimal.NewFromString(strings.TrimSpace(raw))
		if err != nil {
			return out, fmt.Errorf("%s must be a decimal number", c.Name)
		}
		out.Value = d.String()
		out.Min, out.Max = ptrStr(c.Min), ptrStr(c.Max)
		if c.Unit != "" {
			u := c.Unit
			out.Unit = &u
		}
	case "boolean":
		if raw != "true" && raw != "false" {
			return out, fmt.Errorf("%s must be true or false", c.Name)
		}
		out.Value = raw
	default:
		if strings.TrimSpace(raw) == "" {
			return out, fmt.Errorf("%s needs a value", c.Name)
		}
		out.Value = raw
	}
	return out, nil
}

// CheckStepMessage checks the values a kiosk sends for the step against the recipe and returns the values to store. The
// limits, unit, expected value, tolerance, node, timer and branch target are the document's; a branch decision and a
// calculation result are the service's evaluation, and a client value that disagrees is refused. A step a branch skips
// takes no message. A signature step is checked by the signature rules and passes through.
func CheckStepMessage(c StepContext, vals []Characteristic) StepCheck {
	v := c.v
	in := v.In
	if v.Skipped {
		return refuse("this step is skipped by a branch decided earlier in its phase, so nothing is recorded for it")
	}
	comment, hasComment := valueNamed(vals, "comment")
	var extra []Characteristic
	if hasComment {
		extra = []Characteristic{{Name: "comment", Type: "text", Value: comment.Value}}
	}
	switch in.Category {
	case InstSetpoint:
		if in.Char == nil || in.Value == nil {
			return refuse("the setpoint step has no value")
		}
		if bad, ok := onlyNames(vals, in.Char.Name, "comment"); !ok {
			return refuse("%s is not a value of this step", bad)
		}
		if got, ok := valueNamed(vals, in.Char.Name); ok {
			if d, err := decimal.NewFromString(got.Value); err != nil || !d.Equal(decimal.NewFromFloat(*in.Value)) {
				return refuse("the setpoint of this step is %s, not %s", fmtFloat(*in.Value), got.Value)
			}
		}
		cv, _ := charValue(in.Char, fmtFloat(*in.Value))
		return StepCheck{Values: append([]Characteristic{cv}, extra...)}
	case InstDataRequest, InstInspection:
		if in.Char == nil {
			return refuse("the step has no characteristic")
		}
		if bad, ok := onlyNames(vals, in.Char.Name, "comment"); !ok {
			return refuse("%s is not a value of this step; it records %s", bad, in.Char.Name)
		}
		got, ok := valueNamed(vals, in.Char.Name)
		if !ok {
			return refuse("the step records %s", in.Char.Name)
		}
		cv, err := charValue(in.Char, got.Value)
		if err != nil {
			return refuse("%s", err.Error())
		}
		return StepCheck{Values: append([]Characteristic{cv}, extra...)}
	case InstReadout:
		if in.Char == nil {
			return refuse("the step has no characteristic")
		}
		if bad, ok := onlyNames(vals, in.Char.Name, "comment", "readout-source"); !ok {
			return refuse("%s is not a value of this step", bad)
		}
		got, ok := valueNamed(vals, in.Char.Name)
		if !ok {
			return refuse("the step records %s", in.Char.Name)
		}
		src, _ := valueNamed(vals, "readout-source")
		if src.Value != ReadoutMachine && src.Value != ReadoutHand {
			return refuse("readout-source must be %s or %s", ReadoutMachine, ReadoutHand)
		}
		cv, err := charValue(in.Char, got.Value)
		if err != nil {
			return refuse("%s", err.Error())
		}
		out := []Characteristic{cv, {Name: "readout-source", Type: "text", Value: src.Value}, {Name: "node", Type: "text", Value: in.Node}}
		if in.Value != nil && in.Tolerance != nil {
			out = append(out, Characteristic{Name: "expected", Type: "numeric", Value: fmtFloat(*in.Value)}, Characteristic{Name: "tolerance", Type: "numeric", Value: fmtFloat(*in.Tolerance)})
		}
		if src.Value == ReadoutHand && strings.TrimSpace(comment.Value) == "" {
			return refuse("a reading entered by hand needs a comment saying why")
		}
		d, _ := decimal.NewFromString(cv.Value)
		return StepCheck{Values: append(out, extra...), NeedsClaim: src.Value == ReadoutMachine, Primary: d}
	case InstCalculation:
		if in.Expression == "" {
			return StepCheck{Values: vals}
		}
		if v.Calc == nil {
			return refuse("this step cannot be calculated: %s", v.CalcErr)
		}
		name := in.Var
		if name == "" {
			name = "calculation"
		}
		if bad, ok := onlyNames(vals, name); !ok {
			return refuse("%s is not a value of this step", bad)
		}
		if got, ok := valueNamed(vals, name); ok {
			if d, err := decimal.NewFromString(got.Value); err != nil || !d.Equal(*v.Calc) {
				return refuse("%s is %s from the values recorded before this step, not %s", name, v.Calc.String(), got.Value)
			}
		}
		return StepCheck{Values: []Characteristic{{Name: name, Type: "numeric", Value: v.Calc.String()}, {Name: "expression", Type: "text", Value: in.Expression}}}
	case InstTimer:
		out := []Characteristic{{Name: "timer-seconds", Type: "numeric", Value: strconv.Itoa(in.DurationSeconds)}, {Name: "timer-blocking", Type: "boolean", Value: strconv.FormatBool(in.Blocking)}}
		sc := StepCheck{Values: out}
		if in.Blocking {
			sc.BlockingSeconds = in.DurationSeconds
		}
		return sc
	case InstBranch:
		if v.JumpErr != "" {
			return refuse("this branch cannot be decided: %s", v.JumpErr)
		}
		if bad, ok := onlyNames(vals, "branch-taken", "branch-target"); !ok {
			return refuse("%s is not a value of this step", bad)
		}
		if got, ok := valueNamed(vals, "branch-taken"); ok && got.Value != strconv.FormatBool(v.Jump) {
			return refuse("the condition of this branch is %t from the values recorded before it, not %s", v.Jump, got.Value)
		}
		if got, ok := valueNamed(vals, "branch-target"); ok && got.Value != strconv.Itoa(in.Target) {
			return refuse("the target of this branch is step %d, not %s", in.Target, got.Value)
		}
		return StepCheck{Values: []Characteristic{{Name: "branch-taken", Type: "boolean", Value: strconv.FormatBool(v.Jump)}, {Name: "branch-target", Type: "numeric", Value: strconv.Itoa(in.Target)}}}
	}
	return StepCheck{Values: vals}
}

// TimerElapsed reports whether a blocking timer of seconds may be recorded now: the steps before it were recorded (or the
// recipe started) at since, and the timer starts after that.
func TimerElapsed(since, now time.Time, seconds int) bool {
	return now.Add(time.Second).Sub(since) >= time.Duration(seconds)*time.Second
}

// StepByKey finds the instruction a step key names (unit procedure/operation/phase/seq).
func (d RecipeDoc) StepByKey(key string) (Instruction, bool) { return d.SignatureStep(key) }
