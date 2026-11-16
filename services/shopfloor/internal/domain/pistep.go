package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The step library's rules (round 5, FR-25.15): the fields each step kind may and must carry, the identifiers steps
// share, and what a template holds. A template's spec is one instruction without a position.

// VarName is the form of a step's Var: a lower-case identifier, as an expression reads it.
var VarName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Bounds of the step kinds' own fields.
const (
	MaxTimerSeconds = 86400
)

// checkStepKind checks the fields that belong to one kind and refuses them on any other, and the kinds a destination
// cannot run. Expressions are parsed here; their names are resolved against earlier steps by varScope.
func checkStepKind(f map[string]string, path string, in Instruction, destination string) {
	isCalc := in.Category == InstCalculation
	if in.Var != "" {
		if !VarName.MatchString(in.Var) {
			f[path+".var"] = "must be a lower-case name: letters, digits and '_', starting with a letter, at most 40 characters"
		} else if _, reserved := ExprFunctions[in.Var]; reserved || in.Var == "and" || in.Var == "or" || in.Var == "not" {
			f[path+".var"] = "is a word expressions use; choose another name"
		}
		if ReservedValueName(in.Var) {
			f[path+".var"] = "is a name the service writes into recorded steps; choose another name"
		}
		switch in.Category {
		case InstSetpoint, InstDataRequest, InstInspection, InstReadout, InstCalculation:
			if (in.Category == InstDataRequest || in.Category == InstInspection) && in.Char != nil && in.Char.Type == "text" {
				f[path+".var"] = "applies to a number or yes/no value; a text value cannot be used in a formula"
			}
		default:
			f[path+".var"] = "applies to a step that produces a value: a setpoint, a data request, an inspection, a machine readout or a calculation"
		}
	}
	if in.Char != nil && ReservedValueName(in.Char.Name) {
		f[path+".characteristic.name"] = "is a name the service writes into recorded steps; choose another name"
	}
	if in.Template != nil && (!templateCode.MatchString(in.Template.Code) || in.Template.Version < 1) {
		f[path+".template"] = "must name a step template by its code and a version of 1 or more"
	}
	if in.Expression != "" && !isCalc {
		f[path+".expression"] = "applies to a calculation step only"
	}
	if isCalc && in.Expression != "" {
		if e, err := ParseExpr(in.Expression); err != nil {
			f[path+".expression"] = err.Error()
		} else if e.Type() != ExprNumber {
			f[path+".expression"] = "must compute a number, not a true or false condition"
		}
	}
	if in.Tolerance != nil && in.Category != InstReadout {
		f[path+".tolerance"] = "applies to a machine readout step only"
	}
	if in.Category == InstReadout {
		switch {
		case in.Tolerance != nil && (!finiteNum(*in.Tolerance) || *in.Tolerance < 0):
			f[path+".tolerance"] = "must be zero or more"
		case in.Tolerance != nil && in.Value == nil:
			f[path+".tolerance"] = "needs the expected reading (value) it is the tolerance around"
		case in.Value != nil && !finiteNum(*in.Value):
			f[path+".value"] = "must be a finite number"
		}
	} else if in.Value != nil && in.Category != InstSetpoint {
		f[path+".value"] = "applies to a setpoint or a machine readout only"
	}
	if in.Category == InstTimer {
		if in.DurationSeconds < 1 || in.DurationSeconds > MaxTimerSeconds {
			f[path+".durationSeconds"] = fmt.Sprintf("must be 1 to %d seconds", MaxTimerSeconds)
		}
	} else if in.DurationSeconds != 0 || in.Blocking {
		f[path+".durationSeconds"] = "applies to a timer step only"
	}
	if in.Category == InstBranch {
		if e, err := ParseExpr(in.Condition); err != nil {
			f[path+".condition"] = err.Error()
		} else if e.Type() != ExprBool {
			f[path+".condition"] = "must be a true or false condition, such as temp > 80"
		}
	} else if in.Condition != "" || in.Target != 0 {
		f[path+".condition"] = "applies to a branch step only"
	}
	if destination != DestKiosk {
		switch {
		case in.Category == InstTimer, in.Category == InstBranch, in.Category == InstReadout, isCalc && in.Expression != "":
			f[path+".category"] = "runs at the kiosk only: a process control system reads setpoints"
		}
	}
}

var templateCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)

// varScope tracks the values earlier steps produce while a recipe is checked: the names are unique, a calculation or
// a branch reads only names defined before it, and a name defined in steps a branch may skip is not read after them.
type varScope struct {
	defined map[string]varDef
	phase   string
	skips   []skipRange
}

type varDef struct {
	skippedBy []skipKey
}

type skipRange struct{ from, to int } // steps strictly between from and to are skipped when the branch at from jumps to to
type skipKey struct {
	phase    string
	from, to int
}

func newVarScope() *varScope { return &varScope{defined: map[string]varDef{}} }

func (s *varScope) startPhase(path string, ins []Instruction) {
	s.phase, s.skips = path, nil
	for i, in := range ins {
		if in.Category == InstBranch && in.Target > i+1 && in.Target <= len(ins) {
			s.skips = append(s.skips, skipRange{from: i + 1, to: in.Target})
		}
	}
}

// check applies the cross-step rules to the instruction at position seq (1-based) of a phase of total steps.
func (s *varScope) check(f map[string]string, path string, seq, total int, in Instruction) {
	read := func(field, src string, want ExprType) {
		e, err := ParseExpr(src)
		if err != nil || e.Type() != want {
			return // reported by checkStepKind
		}
		for _, name := range e.Names() {
			d, ok := s.defined[name]
			switch {
			case !ok:
				f[path+"."+field] = fmt.Sprintf("reads %s, which no earlier step produces: give that step the var %s", name, name)
			default:
				for _, k := range d.skippedBy {
					// A step skipped by a branch is not a value the steps after the branch can count on, unless they are
					// skipped with it.
					if !(k.phase == s.phase && k.from < seq && seq < k.to) {
						f[path+"."+field] = fmt.Sprintf("reads %s, whose step a branch may skip", name)
					}
				}
			}
		}
	}
	switch in.Category {
	case InstCalculation:
		if in.Expression != "" {
			read("expression", in.Expression, ExprNumber)
		}
	case InstBranch:
		read("condition", in.Condition, ExprBool)
		if in.Target <= seq || in.Target > total {
			f[path+".target"] = fmt.Sprintf("must be a later step of this phase: %d to %d", seq+1, total)
		}
	}
	if in.Var != "" && VarName.MatchString(in.Var) {
		if _, dup := s.defined[in.Var]; dup {
			f[path+".var"] = "is used by an earlier step; every var is unique in the recipe"
			return
		}
		var by []skipKey
		for _, r := range s.skips {
			if r.from < seq && seq < r.to {
				by = append(by, skipKey{phase: s.phase, from: r.from, to: r.to})
			}
		}
		s.defined[in.Var] = varDef{skippedBy: by}
	}
}

// StepTemplateKinds are the instruction categories a step template may be of: every kind of the recipe document.
func StepTemplateKinds() []string {
	return []string{InstText, InstSetpoint, InstDataRequest, InstCalculation, InstInspection, InstMaterials, InstSignature, InstSequence, InstReadout, InstTimer, InstBranch}
}

// ValidateStepTemplate checks the spec of a step template: one instruction that would be valid as a step of a kiosk
// sheet, except for what only a place in a sheet decides, the target of a branch and the earlier steps an expression
// reads. Keys are JSON paths under "spec".
func ValidateStepTemplate(spec Instruction) map[string]string {
	f := map[string]string{}
	spec.Seq = 1
	if spec.Template != nil {
		f["spec.template"] = "a template is not made from another template"
	}
	if spec.Category == InstBranch && spec.Target != 0 {
		f["spec.target"] = "is set where the step is used in a sheet, not in the template"
		spec.Target = 0
	}
	if spec.Category == InstCalculation && strings.TrimSpace(spec.Expression) == "" {
		f["spec.expression"] = "is required: the formula over earlier step values"
	}
	inner := map[string]string{}
	checkInstruction(inner, "spec", 1, spec, DestKiosk)
	for k, v := range inner {
		f[k] = v
	}
	return f
}

// TemplateRefs lists the template references of a document, in order, without repeats.
func (d RecipeDoc) TemplateRefs() []TemplateRef {
	var out []TemplateRef
	seen := map[TemplateRef]bool{}
	d.WalkInstructions(func(_ string, in Instruction) {
		if in.Template != nil && !seen[*in.Template] {
			seen[*in.Template] = true
			out = append(out, *in.Template)
		}
	})
	return out
}

// WalkInstructions calls fn for every instruction of the document in order, with its JSON path.
func (d RecipeDoc) WalkInstructions(fn func(path string, in Instruction)) {
	for ui, up := range d.Procedure.UnitProcedures {
		for oi, op := range up.Operations {
			for pi, ph := range op.Phases {
				for ii, in := range ph.Instructions {
					fn(fmt.Sprintf("recipe.procedure.unitProcedures[%d].operations[%d].phases[%d].instructions[%d]", ui, oi, pi, ii), in)
				}
			}
		}
	}
}

// TemplateDrift reports whether a step that names a template differs from the template's spec in a field it copied. The
// fields a sheet sets where it uses the step are left out: its position, a branch's target, the var, a setpoint's value
// and the material lines. A step that drifted is no longer the template's step, so the reference is refused.
func TemplateDrift(step, spec Instruction) bool {
	norm := func(in Instruction) string {
		in.Seq, in.Target, in.Var, in.Template = 0, 0, "", nil
		if in.Category == InstSetpoint {
			in.Value = nil
		}
		in.Materials = nil
		b, _ := json.Marshal(in)
		return string(b)
	}
	return norm(step) != norm(spec)
}
