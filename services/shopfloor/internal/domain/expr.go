package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Step expressions (FR-25.15 calculation and branch instructions). A calculation computes a number from the values
// of earlier steps; a branch decides, from the same values, whether the kiosk jumps ahead. The text is parsed by a
// small recursive-descent parser into a tree and evaluated on exact decimals: there is no eval, no function the
// parser does not list, no loop, and the text and the tree are bounded.
//
//	or      := and { ("||" | "or") and }
//	and     := not { ("&&" | "and") not }
//	not     := ("!" | "not") not | compare
//	compare := sum [ ("<" | "<=" | ">" | ">=" | "==" | "!=") sum ]
//	sum     := product { ("+" | "-") product }
//	product := unary { ("*" | "/") unary }
//	unary   := "-" unary | primary
//	primary := number | name | name "(" [ or { "," or } ] ")" | "(" or ")"
//
// Names are lower-case identifiers: the `var` of an earlier step. The functions are abs, min, max, round(x, places),
// floor and ceil. Types are checked when the text is parsed: arithmetic and comparison take numbers, and, or and not
// take truths, so a calculation (a number) or a branch condition (a truth) is known to be one before it runs.

// Bounds of an expression.
const (
	MaxExprLen   = 500
	maxExprNodes = 200
	maxExprDepth = 32
	// MaxRoundPlaces is the most decimal places round() takes (0 to 12), as division is carried to 12.
	MaxRoundPlaces = 12
)

// ExprType is the type an expression has: a number or a truth.
type ExprType int

const (
	ExprNumber ExprType = iota + 1
	ExprBool
)

func (t ExprType) String() string {
	if t == ExprBool {
		return "a true or false condition"
	}
	return "a number"
}

// ExprValue is the result of evaluating an expression.
type ExprValue struct {
	Type ExprType        `json:"type"`
	Num  decimal.Decimal `json:"-"`
	Bool bool            `json:"-"`
}

// String is the value as the kiosk records it: a decimal text or true or false.
func (v ExprValue) String() string {
	if v.Type == ExprBool {
		return fmt.Sprint(v.Bool)
	}
	return v.Num.String()
}

// Expr is a parsed expression.
type Expr struct {
	root  exprNode
	typ   ExprType
	names []string
}

// Type is what the expression evaluates to.
func (e *Expr) Type() ExprType { return e.typ }

// Names lists the identifiers the expression reads, sorted and without repeats.
func (e *Expr) Names() []string { return e.names }

type exprNode interface {
	eval(vars map[string]decimal.Decimal) (ExprValue, error)
}

type numNode struct{ v decimal.Decimal }
type nameNode struct{ name string }
type unaryNode struct {
	op string
	x  exprNode
}
type binNode struct {
	op   string
	l, r exprNode
}
type callNode struct {
	fn   string
	args []exprNode
}

// ErrNoValue is returned by Eval when a name has no value: the step that defines it was not run.
type ErrNoValue struct{ Name string }

func (e ErrNoValue) Error() string { return "the step value " + e.Name + " is not known" }

func (n numNode) eval(map[string]decimal.Decimal) (ExprValue, error) {
	return ExprValue{Type: ExprNumber, Num: n.v}, nil
}

func (n nameNode) eval(vars map[string]decimal.Decimal) (ExprValue, error) {
	v, ok := vars[n.name]
	if !ok {
		return ExprValue{}, ErrNoValue{Name: n.name}
	}
	return ExprValue{Type: ExprNumber, Num: v}, nil
}

func (n unaryNode) eval(vars map[string]decimal.Decimal) (ExprValue, error) {
	x, err := n.x.eval(vars)
	if err != nil {
		return x, err
	}
	if n.op == "!" {
		return ExprValue{Type: ExprBool, Bool: !x.Bool}, nil
	}
	return ExprValue{Type: ExprNumber, Num: x.Num.Neg()}, nil
}

func (n binNode) eval(vars map[string]decimal.Decimal) (ExprValue, error) {
	l, err := n.l.eval(vars)
	if err != nil {
		return l, err
	}
	// && and || stop at the left operand, so "x != 0 && 10 / x > 2" does not divide by zero.
	switch n.op {
	case "&&":
		if !l.Bool {
			return ExprValue{Type: ExprBool}, nil
		}
		return n.r.eval(vars)
	case "||":
		if l.Bool {
			return ExprValue{Type: ExprBool, Bool: true}, nil
		}
		return n.r.eval(vars)
	}
	r, err := n.r.eval(vars)
	if err != nil {
		return r, err
	}
	num := func(d decimal.Decimal) ExprValue { return ExprValue{Type: ExprNumber, Num: d} }
	truth := func(b bool) ExprValue { return ExprValue{Type: ExprBool, Bool: b} }
	switch n.op {
	case "+":
		return num(l.Num.Add(r.Num)), nil
	case "-":
		return num(l.Num.Sub(r.Num)), nil
	case "*":
		return num(l.Num.Mul(r.Num)), nil
	case "/":
		if r.Num.IsZero() {
			return ExprValue{}, fmt.Errorf("division by zero")
		}
		return num(l.Num.DivRound(r.Num, 12)), nil
	case "<":
		return truth(l.Num.LessThan(r.Num)), nil
	case "<=":
		return truth(l.Num.LessThanOrEqual(r.Num)), nil
	case ">":
		return truth(l.Num.GreaterThan(r.Num)), nil
	case ">=":
		return truth(l.Num.GreaterThanOrEqual(r.Num)), nil
	case "==":
		return truth(l.Num.Equal(r.Num)), nil
	default: // "!="
		return truth(!l.Num.Equal(r.Num)), nil
	}
}

func (n callNode) eval(vars map[string]decimal.Decimal) (ExprValue, error) {
	args := make([]decimal.Decimal, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(vars)
		if err != nil {
			return v, err
		}
		args[i] = v.Num
	}
	num := func(d decimal.Decimal) (ExprValue, error) { return ExprValue{Type: ExprNumber, Num: d}, nil }
	switch n.fn {
	case "abs":
		return num(args[0].Abs())
	case "floor":
		return num(args[0].Floor())
	case "ceil":
		return num(args[0].Ceil())
	case "round":
		places := args[1]
		if !places.IsInteger() || places.IsNegative() || places.GreaterThan(decimal.NewFromInt(MaxRoundPlaces)) {
			return ExprValue{}, fmt.Errorf("round takes 0 to %d decimal places", MaxRoundPlaces)
		}
		return num(args[0].Round(int32(places.IntPart())))
	case "min":
		m := args[0]
		for _, a := range args[1:] {
			if a.LessThan(m) {
				m = a
			}
		}
		return num(m)
	default: // "max"
		m := args[0]
		for _, a := range args[1:] {
			if a.GreaterThan(m) {
				m = a
			}
		}
		return num(m)
	}
}

// Eval computes the expression with the values of the named steps. A name without a value is an ErrNoValue.
func (e *Expr) Eval(vars map[string]decimal.Decimal) (ExprValue, error) {
	return e.root.eval(vars)
}

// ExprFunctions are the functions an expression may call, with their argument counts (-1 is one or more).
var ExprFunctions = map[string]int{"abs": 1, "floor": 1, "ceil": 1, "round": 2, "min": -1, "max": -1}

type token struct {
	kind string // num, name, op, end
	text string
	at   int
}

func lexExpr(src string) ([]token, error) {
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			dots := 0
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				if src[j] == '.' {
					dots++
				}
				j++
			}
			if dots > 1 || src[i:j] == "." || len(src[i:j]) > 30 {
				return nil, fmt.Errorf("position %d: %q is not a number", i+1, src[i:j])
			}
			out = append(out, token{"num", src[i:j], i})
			i = j
		case c >= 'a' && c <= 'z' || c == '_':
			j := i
			for j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= '0' && src[j] <= '9' || src[j] == '_') {
				j++
			}
			word := src[i:j]
			switch word {
			case "and":
				out = append(out, token{"op", "&&", i})
			case "or":
				out = append(out, token{"op", "||", i})
			case "not":
				out = append(out, token{"op", "!", i})
			default:
				out = append(out, token{"name", word, i})
			}
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch {
			case two == "<=" || two == ">=" || two == "==" || two == "!=" || two == "&&" || two == "||":
				out = append(out, token{"op", two, i})
				i += 2
			case strings.ContainsRune("+-*/()<>!,", rune(c)):
				out = append(out, token{"op", string(c), i})
				i++
			default:
				return nil, fmt.Errorf("position %d: %q is not allowed in an expression", i+1, string(c))
			}
		}
	}
	return append(out, token{"end", "", len(src)}), nil
}

type exprParser struct {
	toks  []token
	pos   int
	nodes int
	depth int
	names map[string]bool
}

func (p *exprParser) peek() token { return p.toks[p.pos] }
func (p *exprParser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *exprParser) isOp(ops ...string) bool {
	t := p.peek()
	if t.kind != "op" {
		return false
	}
	for _, o := range ops {
		if t.text == o {
			return true
		}
	}
	return false
}

func (p *exprParser) grow() error {
	p.nodes++
	if p.nodes > maxExprNodes {
		return fmt.Errorf("the expression is too long: at most %d terms and operators", maxExprNodes)
	}
	return nil
}

func (p *exprParser) enter() error {
	p.depth++
	if p.depth > maxExprDepth {
		return fmt.Errorf("the expression nests too deeply: at most %d levels", maxExprDepth)
	}
	return nil
}

func want(t ExprType, got ExprType, op string, at int) error {
	if t != got {
		return fmt.Errorf("position %d: %s needs %s, not %s", at+1, op, t, got)
	}
	return nil
}

func (p *exprParser) or() (exprNode, ExprType, error) {
	if err := p.enter(); err != nil {
		return nil, 0, err
	}
	defer func() { p.depth-- }()
	l, lt, err := p.and()
	for err == nil && p.isOp("||") {
		op := p.next()
		if err = want(ExprBool, lt, "or", op.at); err != nil {
			break
		}
		var r exprNode
		var rt ExprType
		if r, rt, err = p.and(); err != nil {
			break
		}
		if err = want(ExprBool, rt, "or", op.at); err != nil {
			break
		}
		l, err = binNode{"||", l, r}, p.grow()
	}
	return l, lt, err
}

func (p *exprParser) and() (exprNode, ExprType, error) {
	l, lt, err := p.not()
	for err == nil && p.isOp("&&") {
		op := p.next()
		if err = want(ExprBool, lt, "and", op.at); err != nil {
			break
		}
		var r exprNode
		var rt ExprType
		if r, rt, err = p.not(); err != nil {
			break
		}
		if err = want(ExprBool, rt, "and", op.at); err != nil {
			break
		}
		l, err = binNode{"&&", l, r}, p.grow()
	}
	return l, lt, err
}

func (p *exprParser) not() (exprNode, ExprType, error) {
	if p.isOp("!") {
		op := p.next()
		if err := p.enter(); err != nil {
			return nil, 0, err
		}
		defer func() { p.depth-- }()
		x, xt, err := p.not()
		if err != nil {
			return nil, 0, err
		}
		if err := want(ExprBool, xt, "not", op.at); err != nil {
			return nil, 0, err
		}
		return unaryNode{"!", x}, ExprBool, p.grow()
	}
	return p.compare()
}

func (p *exprParser) compare() (exprNode, ExprType, error) {
	l, lt, err := p.sum()
	if err != nil || !p.isOp("<", "<=", ">", ">=", "==", "!=") {
		return l, lt, err
	}
	op := p.next()
	if err := want(ExprNumber, lt, op.text, op.at); err != nil {
		return nil, 0, err
	}
	r, rt, err := p.sum()
	if err != nil {
		return nil, 0, err
	}
	if err := want(ExprNumber, rt, op.text, op.at); err != nil {
		return nil, 0, err
	}
	return binNode{op.text, l, r}, ExprBool, p.grow()
}

func (p *exprParser) sum() (exprNode, ExprType, error) {
	l, lt, err := p.product()
	for err == nil && p.isOp("+", "-") {
		op := p.next()
		if err = want(ExprNumber, lt, op.text, op.at); err != nil {
			break
		}
		var r exprNode
		var rt ExprType
		if r, rt, err = p.product(); err != nil {
			break
		}
		if err = want(ExprNumber, rt, op.text, op.at); err != nil {
			break
		}
		l, err = binNode{op.text, l, r}, p.grow()
	}
	return l, lt, err
}

func (p *exprParser) product() (exprNode, ExprType, error) {
	l, lt, err := p.unary()
	for err == nil && p.isOp("*", "/") {
		op := p.next()
		if err = want(ExprNumber, lt, op.text, op.at); err != nil {
			break
		}
		var r exprNode
		var rt ExprType
		if r, rt, err = p.unary(); err != nil {
			break
		}
		if err = want(ExprNumber, rt, op.text, op.at); err != nil {
			break
		}
		l, err = binNode{op.text, l, r}, p.grow()
	}
	return l, lt, err
}

func (p *exprParser) unary() (exprNode, ExprType, error) {
	if p.isOp("-") {
		op := p.next()
		if err := p.enter(); err != nil {
			return nil, 0, err
		}
		defer func() { p.depth-- }()
		x, xt, err := p.unary()
		if err != nil {
			return nil, 0, err
		}
		if err := want(ExprNumber, xt, "-", op.at); err != nil {
			return nil, 0, err
		}
		return unaryNode{"-", x}, ExprNumber, p.grow()
	}
	return p.primary()
}

func (p *exprParser) primary() (exprNode, ExprType, error) {
	t := p.next()
	switch {
	case t.kind == "num":
		d, err := decimal.NewFromString(t.text)
		if err != nil {
			return nil, 0, fmt.Errorf("position %d: %q is not a number", t.at+1, t.text)
		}
		return numNode{d}, ExprNumber, p.grow()
	case t.kind == "name" && p.isOp("("):
		return p.call(t)
	case t.kind == "name":
		p.names[t.text] = true
		return nameNode{t.text}, ExprNumber, p.grow()
	case t.kind == "op" && t.text == "(":
		if err := p.enter(); err != nil {
			return nil, 0, err
		}
		defer func() { p.depth-- }()
		x, xt, err := p.or()
		if err != nil {
			return nil, 0, err
		}
		if c := p.next(); c.kind != "op" || c.text != ")" {
			return nil, 0, fmt.Errorf("position %d: a closing bracket is missing", c.at+1)
		}
		return x, xt, nil
	case t.kind == "end":
		return nil, 0, fmt.Errorf("the expression ends too early: a value is missing")
	}
	return nil, 0, fmt.Errorf("position %d: %q is not expected here", t.at+1, t.text)
}

func (p *exprParser) call(name token) (exprNode, ExprType, error) {
	arity, known := ExprFunctions[name.text]
	if !known {
		return nil, 0, fmt.Errorf("position %d: %s is not a function; the functions are abs, min, max, round, floor and ceil", name.at+1, name.text)
	}
	p.next() // (
	var args []exprNode
	for !p.isOp(")") {
		if len(args) > 0 {
			if c := p.next(); c.kind != "op" || c.text != "," {
				return nil, 0, fmt.Errorf("position %d: a comma or closing bracket is expected", c.at+1)
			}
		}
		if err := p.enter(); err != nil {
			return nil, 0, err
		}
		a, at, err := p.or()
		p.depth--
		if err != nil {
			return nil, 0, err
		}
		if err := want(ExprNumber, at, name.text, name.at); err != nil {
			return nil, 0, err
		}
		args = append(args, a)
		if p.peek().kind == "end" {
			return nil, 0, fmt.Errorf("position %d: a closing bracket is missing", p.peek().at+1)
		}
	}
	p.next() // )
	if (arity >= 0 && len(args) != arity) || (arity < 0 && len(args) == 0) {
		return nil, 0, fmt.Errorf("position %d: %s takes %s", name.at+1, name.text, map[int]string{1: "one value", 2: "two values", -1: "at least one value"}[arity])
	}
	if name.text == "round" {
		lit, isLit := args[1].(numNode)
		_, negated := args[1].(unaryNode)
		if negated || (isLit && (!lit.v.IsInteger() || lit.v.GreaterThan(decimal.NewFromInt(MaxRoundPlaces)))) {
			return nil, 0, fmt.Errorf("position %d: round takes 0 to %d decimal places", name.at+1, MaxRoundPlaces)
		}
	}
	return callNode{name.text, args}, ExprNumber, p.grow()
}

// ParseExpr parses and type-checks an expression.
func ParseExpr(src string) (*Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("the expression is empty")
	}
	if len(src) > MaxExprLen {
		return nil, fmt.Errorf("the expression is too long: at most %d characters", MaxExprLen)
	}
	toks, err := lexExpr(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, names: map[string]bool{}}
	root, typ, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != "end" {
		return nil, fmt.Errorf("position %d: %q is not expected here", t.at+1, t.text)
	}
	names := make([]string, 0, len(p.names))
	for n := range p.names {
		names = append(names, n)
	}
	sort.Strings(names)
	return &Expr{root: root, typ: typ, names: names}, nil
}
