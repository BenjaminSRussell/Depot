// Command eventfields type-checks Go modules and prints, as JSON, the CloudEvents data fields their event consumers
// decode and the data fields their producers write, per event type. deploy/scripts/test_event_contracts.py compares
// both with the producers' AsyncAPI schemas (architecture gap D6, B-05).
//
//	go -C deploy/ci/eventfields run . -root "$PWD" services/search services/sales
//
// Consumers: every map[string]eventing.Handler literal (or m[type] = handler assignment) in code a main package of the
// module reaches. For each entry, the handler expression is followed through the functions and variables it names, and
// every json.Unmarshal of the event's data (events.Event.Data, or a json.RawMessage parameter it is passed in) into a
// concrete type is a decode; a decode into an `any` parameter takes the type of the argument at each call site.
//
// Producers: every events.New call in the module. The event type is a constant, events.Type of constants, or a parameter
// or local variable bound to one; the payload is the static type of the data argument, followed through parameters to
// each call site of the enclosing function (up to maxDepth calls up).
//
// A field is a JSON path: "lines[].qty" is the qty key of each element of the lines array. A struct field whose type
// marshals itself (json.Marshaler/Unmarshaler or encoding.Text*: time.Time, decimal types, json.RawMessage), a map or an
// interface ends the path. Go reads object keys case-insensitively; the comparison with the schema is left to the caller.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const (
	eventsPkg   = "github.com/BenjaminSRussell/depot/platform/events"
	eventingPkg = "github.com/BenjaminSRussell/depot/platform/eventing"
	outboxPkg   = "github.com/BenjaminSRussell/depot/platform/outbox"
	maxDepth    = 6
)

// Field is one JSON path of a payload.
type Field struct {
	Path      string `json:"path"`
	Omitempty bool   `json:"omitempty,omitempty"`
}

// Decode is a json.Unmarshal of an event's data a handler reaches.
type Decode struct {
	At     string  `json:"at"`
	GoType string  `json:"goType"`
	Open   bool    `json:"open,omitempty"`
	Fields []Field `json:"fields"`
}

// Handler is one entry of a handler map: the event type and what its handler decodes.
type Handler struct {
	Type    string   `json:"type"`
	At      string   `json:"at"`
	Decodes []Decode `json:"decodes"`
}

// Emit is one payload an events.New call writes for an event type.
type Emit struct {
	Type string `json:"type"`
	At   string `json:"at"`
	// Via is the call that bound the type or payload when events.New sits in a helper that takes them as parameters.
	Via    string `json:"via,omitempty"`
	GoType string `json:"goType"`
	// Open: the payload's keys are not known statically (any, json.RawMessage, a map filled at run time).
	Open   bool    `json:"open,omitempty"`
	Fields []Field `json:"fields"`
}

// Report is the result for one module.
type Report struct {
	Dir      string    `json:"dir"`
	Module   string    `json:"module"`
	Handlers []Handler `json:"handlers"`
	Emits    []Emit    `json:"emits"`
	// Aggregates are the aggregate types outbox.Enqueue calls write (an outbox connector that routes by aggregate_type
	// sends each to depot.<context>.<aggregate>), with the calls whose aggregate type could not be worked out.
	Aggregates           []string `json:"aggregates"`
	UnresolvedAggregates []string `json:"unresolvedAggregates"`
	// Unresolved lists events.New calls whose event type or payload could not be worked out, with the reason.
	Unresolved []string `json:"unresolved"`
	// Problems are consumer-side findings the caller must fail on: a handler key that is not a constant, a decode
	// whose target type cannot be worked out, a package that does not type-check.
	Problems []string `json:"problems"`
}

type listed struct {
	ImportPath string
	Name       string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Export     string
	Standard   bool
	DepOnly    bool
	Module     *struct{ Path string }
	Error      *struct{ Err string }
}

type pkg struct {
	path  string
	main  bool
	files []*ast.File
	info  *types.Info
}

type fn struct {
	key          string
	decl         *ast.FuncDecl
	pkg          *pkg
	params       []*types.Var
	decodeParams map[int]bool
}

type assign struct {
	expr ast.Expr
	pkg  *pkg
	fn   *fn
}

type callSite struct {
	caller *fn
	call   *ast.CallExpr
}

type analyzer struct {
	fset    *token.FileSet
	root    string
	report  *Report
	pkgs    []*pkg
	funcs   map[string]*fn
	locals  map[types.Object][]assign
	globals map[string][]assign
	calls   map[string][]callSite
	imp     types.Importer
	deps    map[string]listed // non-standard dependencies, whose package variables are read from source when a key names one
	depDone map[string]bool
}

func main() {
	root := flag.String("root", ".", "directory positions are reported relative to")
	jobs := flag.Int("j", 4, "modules analysed at once")
	flag.Parse()
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	dirs := flag.Args()
	reports := make([]*Report, len(dirs))
	errs := make([]error, len(dirs))
	sem := make(chan struct{}, max(1, *jobs))
	var wg sync.WaitGroup
	for i, d := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reports[i], errs[i] = analyze(absRoot, d)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		fail(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(reports); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "eventfields:", err)
	os.Exit(1)
}

func analyze(root, dir string) (*Report, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("go", "list", "-e", "-export", "-deps",
		"-json=ImportPath,Name,Dir,GoFiles,CgoFiles,Export,Standard,DepOnly,Module,Error", "./...")
	cmd.Dir = abs
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %w: %s", dir, err, stderr.String())
	}
	rel, _ := filepath.Rel(root, abs)
	a := &analyzer{fset: token.NewFileSet(), root: root, report: &Report{Dir: filepath.ToSlash(rel), Handlers: []Handler{}, Emits: []Emit{}, Aggregates: []string{}, UnresolvedAggregates: []string{},
		Unresolved: []string{}, Problems: []string{}},
		funcs: map[string]*fn{}, locals: map[types.Object][]assign{}, globals: map[string][]assign{}, calls: map[string][]callSite{},
		deps: map[string]listed{}, depDone: map[string]bool{}}
	exports := map[string]string{}
	var locals []listed
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var l listed
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("go list in %s: %w", dir, err)
		}
		if l.Export != "" {
			exports[l.ImportPath] = l.Export
		}
		if !l.DepOnly && !l.Standard {
			locals = append(locals, l)
		} else if !l.Standard {
			a.deps[l.ImportPath] = l
		}
	}
	if len(locals) > 0 && locals[0].Module != nil {
		a.report.Module = locals[0].Module.Path
	}
	imp := importer.ForCompiler(a.fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	a.imp = imp
	for _, l := range locals {
		if l.Error != nil {
			a.problem(token.NoPos, "package %s: %s", l.ImportPath, l.Error.Err)
			continue
		}
		if len(l.CgoFiles) > 0 {
			a.problem(token.NoPos, "package %s uses cgo, which this check cannot type-check", l.ImportPath)
			continue
		}
		p := &pkg{path: l.ImportPath, main: l.Name == "main", info: &types.Info{
			Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{}, Instances: map[*ast.Ident]types.Instance{},
		}}
		for _, name := range l.GoFiles {
			f, err := parser.ParseFile(a.fset, filepath.Join(l.Dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			p.files = append(p.files, f)
		}
		var typeErr error
		conf := types.Config{Importer: imp, Error: func(err error) {
			if typeErr == nil {
				typeErr = err
			}
		}}
		_, _ = conf.Check(l.ImportPath, a.fset, p.files, p.info)
		if typeErr != nil {
			a.problem(token.NoPos, "package %s does not type-check: %v", l.ImportPath, typeErr)
		}
		a.pkgs = append(a.pkgs, p)
	}
	a.index()
	a.solveDecodeParams()
	a.consumers()
	a.producers()
	sort.Slice(a.report.Handlers, func(i, j int) bool {
		hi, hj := a.report.Handlers[i], a.report.Handlers[j]
		return hi.Type < hj.Type || hi.Type == hj.Type && hi.At < hj.At
	})
	sort.Slice(a.report.Emits, func(i, j int) bool {
		ei, ej := a.report.Emits[i], a.report.Emits[j]
		return ei.Type < ej.Type || ei.Type == ej.Type && ei.At < ej.At
	})
	sort.Strings(a.report.Unresolved)
	a.report.Aggregates = dedupe(a.report.Aggregates)
	sort.Strings(a.report.UnresolvedAggregates)
	sort.Strings(a.report.Problems)
	return a.report, nil
}

func (a *analyzer) pos(p token.Pos) string {
	if !p.IsValid() {
		return ""
	}
	position := a.fset.Position(p)
	rel, err := filepath.Rel(a.root, position.Filename)
	if err != nil {
		rel = position.Filename
	}
	return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), position.Line)
}

func (a *analyzer) problem(p token.Pos, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if at := a.pos(p); at != "" {
		msg = at + ": " + msg
	}
	a.report.Problems = append(a.report.Problems, msg)
}

func funcKey(f *types.Func) string { return f.Origin().FullName() }

func isGlobal(v *types.Var) bool {
	return v.Pkg() != nil && v.Pkg().Scope().Lookup(v.Name()) == v
}

func globalKey(v *types.Var) string { return v.Pkg().Path() + "." + v.Name() }

// index records every function declaration, every assignment to a variable and every call of a module function.
func (a *analyzer) index() {
	for _, p := range a.pkgs {
		for _, file := range p.files {
			for _, d := range file.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					obj, ok := p.info.Defs[d.Name].(*types.Func)
					if !ok {
						continue
					}
					sig := obj.Signature()
					f := &fn{key: funcKey(obj), decl: d, pkg: p, decodeParams: map[int]bool{}}
					for i := range sig.Params().Len() {
						f.params = append(f.params, sig.Params().At(i))
					}
					a.funcs[f.key] = f
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if vs, ok := spec.(*ast.ValueSpec); ok {
							a.valueSpec(vs, p, nil)
						}
					}
				}
			}
		}
	}
	for _, f := range a.funcs {
		if f.decl.Body == nil {
			continue
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					rhs := n.Rhs[0]
					if len(n.Lhs) == len(n.Rhs) {
						rhs = n.Rhs[i]
					}
					a.assignTo(lhs, rhs, f.pkg, f)
				}
			case *ast.ValueSpec:
				a.valueSpec(n, f.pkg, f)
			case *ast.CallExpr:
				if callee := a.callee(f.pkg, n); callee != nil {
					if _, ok := a.funcs[funcKey(callee)]; ok {
						a.calls[funcKey(callee)] = append(a.calls[funcKey(callee)], callSite{caller: f, call: n})
					}
				}
			}
			return true
		})
	}
}

func (a *analyzer) valueSpec(vs *ast.ValueSpec, p *pkg, f *fn) {
	for i, name := range vs.Names {
		switch {
		case len(vs.Values) == len(vs.Names):
			a.assignTo(name, vs.Values[i], p, f)
		case len(vs.Values) == 1:
			a.assignTo(name, vs.Values[0], p, f)
		}
	}
}

func (a *analyzer) assignTo(lhs, rhs ast.Expr, p *pkg, f *fn) {
	id, ok := ast.Unparen(lhs).(*ast.Ident)
	if !ok {
		return
	}
	obj := p.info.Defs[id]
	if obj == nil {
		obj = p.info.Uses[id]
	}
	v, ok := obj.(*types.Var)
	if !ok {
		return
	}
	as := assign{expr: rhs, pkg: p, fn: f}
	if isGlobal(v) {
		a.globals[globalKey(v)] = append(a.globals[globalKey(v)], as)
	} else {
		a.locals[v] = append(a.locals[v], as)
	}
}

// callee is the function or method a call names statically, or nil (a conversion, a call of a func value).
func (a *analyzer) callee(p *pkg, call *ast.CallExpr) *types.Func {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	var id *ast.Ident
	switch x := fun.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return nil
	}
	f, _ := p.info.Uses[id].(*types.Func)
	return f
}

func isPkgFunc(f *types.Func, path, name string) bool {
	return f != nil && f.Pkg() != nil && f.Pkg().Path() == path && f.Name() == name && f.Signature().Recv() == nil
}

func namedIs(t types.Type, path, name string) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == path && n.Obj().Name() == name
}

func paramIndex(f *fn, obj types.Object) int {
	if f == nil {
		return -1
	}
	for i, p := range f.params {
		if p == obj {
			return i
		}
	}
	return -1
}

// isEventData reports whether e is an event's data: evt.Data of an events.Event, or a json.RawMessage parameter of f.
func (a *analyzer) isEventData(e ast.Expr, p *pkg, f *fn) bool {
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if sel := p.info.Selections[x]; sel != nil && x.Sel.Name == "Data" {
			return namedIs(sel.Recv(), eventsPkg, "Event")
		}
	case *ast.Ident:
		obj := p.info.Uses[x]
		return obj != nil && paramIndex(f, obj) >= 0 && namedIs(obj.Type(), "encoding/json", "RawMessage")
	case *ast.CallExpr:
		if tv, ok := p.info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return a.isEventData(x.Args[0], p, f)
		}
	}
	return false
}

// decodeTarget is the type json.Unmarshal(data, e) decodes into, or the index of f's parameter that holds it.
func (a *analyzer) decodeTarget(e ast.Expr, p *pkg, f *fn) (types.Type, int, bool) {
	e = ast.Unparen(e)
	operand := e
	var t types.Type
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		operand = ast.Unparen(u.X)
		t = p.info.TypeOf(operand)
	} else {
		t = p.info.TypeOf(e)
		if ptr, ok := t.Underlying().(*types.Pointer); ok {
			t = ptr.Elem()
		}
	}
	if t == nil {
		return nil, -1, false
	}
	if types.IsInterface(t) {
		if id, ok := operand.(*ast.Ident); ok {
			if i := paramIndex(f, p.info.Uses[id]); i >= 0 {
				return nil, i, true
			}
		}
		return nil, -1, false
	}
	return t, -1, true
}

func (a *analyzer) isUnmarshal(p *pkg, call *ast.CallExpr) bool {
	return len(call.Args) == 2 && isPkgFunc(a.callee(p, call), "encoding/json", "Unmarshal")
}

// solveDecodeParams marks each function parameter that event data is decoded into, directly or through calls.
func (a *analyzer) solveDecodeParams() {
	for _, f := range a.funcs {
		if f.decl.Body == nil {
			continue
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && a.isUnmarshal(f.pkg, call) && a.isEventData(call.Args[0], f.pkg, f) {
				if _, i, ok := a.decodeTarget(call.Args[1], f.pkg, f); ok && i >= 0 {
					f.decodeParams[i] = true
				}
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for key, sites := range a.calls {
			g := a.funcs[key]
			for i := range g.decodeParams {
				for _, s := range sites {
					if i >= len(s.call.Args) {
						continue
					}
					arg := ast.Unparen(s.call.Args[i])
					id, ok := arg.(*ast.Ident)
					if !ok || !types.IsInterface(s.caller.pkg.info.TypeOf(arg)) {
						continue
					}
					if j := paramIndex(s.caller, s.caller.pkg.info.Uses[id]); j >= 0 && !s.caller.decodeParams[j] {
						s.caller.decodeParams[j] = true
						changed = true
					}
				}
			}
		}
	}
}

// walker follows an expression through the module functions and variables it names, collecting decodes.
type walker struct {
	a       *analyzer
	record  bool
	seenFn  map[string]bool
	seenVar map[types.Object]bool
	seenGlb map[string]bool
	decodes map[string]Decode
}

func (a *analyzer) newWalker(record bool) *walker {
	return &walker{a: a, record: record, seenFn: map[string]bool{}, seenVar: map[types.Object]bool{}, seenGlb: map[string]bool{},
		decodes: map[string]Decode{}}
}

func (w *walker) node(n ast.Node, p *pkg, f *fn) {
	if n == nil {
		return
	}
	ast.Inspect(n, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.CallExpr:
			if w.record {
				w.call(x, p, f)
			}
		case *ast.Ident:
			w.ident(x, p)
		}
		return true
	})
}

func (w *walker) ident(id *ast.Ident, p *pkg) {
	switch obj := p.info.Uses[id].(type) {
	case *types.Func:
		g, ok := w.a.funcs[funcKey(obj)]
		if ok && !w.seenFn[g.key] {
			w.seenFn[g.key] = true
			w.node(g.decl.Body, g.pkg, g)
		}
	case *types.Var:
		if obj.IsField() {
			return
		}
		if isGlobal(obj) {
			k := globalKey(obj)
			if !w.seenGlb[k] {
				w.seenGlb[k] = true
				for _, as := range w.a.globals[k] {
					w.node(as.expr, as.pkg, as.fn)
				}
			}
			return
		}
		if !w.seenVar[obj] {
			w.seenVar[obj] = true
			for _, as := range w.a.locals[obj] {
				w.node(as.expr, as.pkg, as.fn)
			}
		}
	}
}

func (w *walker) call(call *ast.CallExpr, p *pkg, f *fn) {
	if w.a.isUnmarshal(p, call) && w.a.isEventData(call.Args[0], p, f) {
		t, i, ok := w.a.decodeTarget(call.Args[1], p, f)
		switch {
		case !ok:
			w.a.problem(call.Pos(), "decodes event data into a value whose type the check cannot work out")
		case i < 0:
			w.add(call.Pos(), t)
		}
		return
	}
	callee := w.a.callee(p, call)
	if callee == nil {
		return
	}
	g, ok := w.a.funcs[funcKey(callee)]
	if !ok {
		return
	}
	for i := range g.decodeParams {
		if i >= len(call.Args) {
			continue
		}
		arg := ast.Unparen(call.Args[i])
		t := p.info.TypeOf(arg)
		if t == nil {
			continue
		}
		if ptr, ok := t.Underlying().(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if types.IsInterface(t) {
			if id, ok := arg.(*ast.Ident); ok && paramIndex(f, p.info.Uses[id]) >= 0 && f.decodeParams[paramIndex(f, p.info.Uses[id])] {
				continue // the caller of f binds it
			}
			w.a.problem(call.Pos(), "passes a value whose type the check cannot work out to %s, which decodes event data into it", g.decl.Name.Name)
			continue
		}
		w.add(call.Pos(), t)
	}
}

func (w *walker) add(at token.Pos, t types.Type) {
	c := collector{decode: true, seen: map[*types.TypeName]bool{}}
	c.typ(t, "", false)
	d := Decode{At: w.a.pos(at), GoType: types.TypeString(t, qualifier), Open: c.open, Fields: c.result()}
	w.decodes[d.At+"|"+d.GoType] = d
}

func qualifier(p *types.Package) string { return p.Name() }

func isHandlerMap(t types.Type) bool {
	if t == nil {
		return false
	}
	m, ok := t.Underlying().(*types.Map)
	if !ok {
		return false
	}
	k, ok := m.Key().Underlying().(*types.Basic)
	return ok && k.Kind() == types.String && namedIs(m.Elem(), eventingPkg, "Handler")
}

func (a *analyzer) constString(p *pkg, e ast.Expr) (string, bool) {
	tv, ok := p.info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// consumers finds the handler maps the module's main packages reach and what each entry decodes.
func (a *analyzer) consumers() {
	reach := a.newWalker(false)
	var roots []struct {
		n ast.Node
		p *pkg
		f *fn
	}
	for _, p := range a.pkgs {
		if !p.main {
			continue
		}
		for _, file := range p.files {
			for _, d := range file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					if obj, ok := p.info.Defs[fd.Name].(*types.Func); ok {
						reach.seenFn[funcKey(obj)] = true
						roots = append(roots, struct {
							n ast.Node
							p *pkg
							f *fn
						}{fd.Body, p, a.funcs[funcKey(obj)]})
					}
				} else {
					roots = append(roots, struct {
						n ast.Node
						p *pkg
						f *fn
					}{d, p, nil})
				}
			}
		}
	}
	for _, r := range roots {
		reach.node(r.n, r.p, r.f)
	}
	keys := make([]string, 0, len(reach.seenFn))
	for k := range reach.seenFn {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entry := func(key, value ast.Expr, at token.Pos, p *pkg, f *fn) {
		typs, err := a.evalStr(key, p, &frame{fn: f, pkg: p}, 0)
		if err != nil || len(typs) == 0 {
			a.problem(at, "a handler map key the check cannot evaluate to a constant event type")
			return
		}
		w := a.newWalker(true)
		w.node(value, p, f)
		decodes := []Decode{}
		for _, d := range w.decodes {
			decodes = append(decodes, d)
		}
		sort.Slice(decodes, func(i, j int) bool { return decodes[i].At+decodes[i].GoType < decodes[j].At+decodes[j].GoType })
		for _, typ := range dedupe(typs) {
			a.report.Handlers = append(a.report.Handlers, Handler{Type: typ, At: a.pos(at), Decodes: decodes})
		}
	}
	scan := func(n ast.Node, p *pkg, f *fn) {
		copies := map[types.Object]bool{} // keys of `for t, h := range other { m[t] = h }`: the entries of other, scanned where it is built
		ast.Inspect(n, func(x ast.Node) bool {
			switch x := x.(type) {
			case *ast.RangeStmt:
				if id, ok := x.Key.(*ast.Ident); ok && isHandlerMap(p.info.TypeOf(x.X)) {
					copies[p.info.Defs[id]] = true
				}
			case *ast.CompositeLit:
				if isHandlerMap(p.info.TypeOf(x)) {
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							entry(kv.Key, kv.Value, kv.Pos(), p, f)
						}
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					if ix, ok := ast.Unparen(lhs).(*ast.IndexExpr); ok && isHandlerMap(p.info.TypeOf(ix.X)) && len(x.Lhs) == len(x.Rhs) {
						if id, ok := ast.Unparen(ix.Index).(*ast.Ident); ok && copies[p.info.Uses[id]] {
							continue
						}
						entry(ix.Index, x.Rhs[i], x.Pos(), p, f)
					}
				}
			}
			return true
		})
	}
	for _, k := range keys {
		if f, ok := a.funcs[k]; ok && f.decl.Body != nil {
			scan(f.decl.Body, f.pkg, f)
		}
	}
	for _, p := range a.pkgs {
		if !p.main {
			continue
		}
		for _, file := range p.files {
			for _, d := range file.Decls {
				if gd, ok := d.(*ast.GenDecl); ok {
					scan(gd, p, nil)
				}
			}
		}
	}
}

// frame binds a function's parameters to the arguments of one call, evaluated in the caller's frame.
type frame struct {
	fn     *fn
	pkg    *pkg
	args   []ast.Expr
	caller *frame
}

var (
	errUnbound = errors.New("a parameter of the outermost function")
	errUnknown = errors.New("not a constant")
)

func (a *analyzer) param(fr *frame, p *pkg, id *ast.Ident) (int, bool) {
	if fr == nil || fr.fn == nil {
		return -1, false
	}
	i := paramIndex(fr.fn, p.info.Uses[id])
	return i, i >= 0
}

func (a *analyzer) evalStr(e ast.Expr, p *pkg, fr *frame, depth int) ([]string, error) {
	if depth > 20 {
		return nil, errUnknown
	}
	e = ast.Unparen(e)
	if s, ok := a.constString(p, e); ok {
		return []string{s}, nil
	}
	switch x := e.(type) {
	case *ast.CallExpr:
		if isPkgFunc(a.callee(p, x), eventsPkg, "Type") && len(x.Args) == 4 {
			var parts [3][]string
			for i := range 3 {
				v, err := a.evalStr(x.Args[i], p, fr, depth+1)
				if err != nil {
					return nil, err
				}
				parts[i] = v
			}
			tv := p.info.Types[x.Args[3]]
			if tv.Value == nil || tv.Value.Kind() != constant.Int {
				return nil, errUnknown
			}
			n, _ := constant.Int64Val(tv.Value)
			var out []string
			for _, c := range parts[0] {
				for _, en := range parts[1] {
					for _, v := range parts[2] {
						out = append(out, fmt.Sprintf("dev.depot.%s.%s.%s.v%d", c, en, v, n))
					}
				}
			}
			return out, nil
		}
	case *ast.Ident:
		if i, ok := a.param(fr, p, x); ok {
			if fr.args == nil {
				return nil, errUnbound
			}
			if i >= len(fr.args) {
				return nil, errUnknown
			}
			return a.evalStr(fr.args[i], fr.caller.pkg, fr.caller, depth+1)
		}
		return a.evalVar(p.info.Uses[x], fr, depth)
	case *ast.SelectorExpr:
		return a.evalVar(p.info.Uses[x.Sel], fr, depth)
	}
	return nil, errUnknown
}

func (a *analyzer) evalVar(obj types.Object, fr *frame, depth int) ([]string, error) {
	v, ok := obj.(*types.Var)
	if !ok || v.IsField() {
		return nil, errUnknown
	}
	var assigns []assign
	if isGlobal(v) {
		assigns = a.globals[globalKey(v)]
		if len(assigns) == 0 && a.loadDep(v.Pkg().Path()) {
			assigns = a.globals[globalKey(v)]
		}
	} else {
		assigns = a.locals[v]
	}
	if len(assigns) == 0 {
		return nil, errUnknown
	}
	var out []string
	for _, as := range assigns {
		f := fr
		if as.fn == nil {
			f = &frame{pkg: as.pkg}
		}
		vals, err := a.evalStr(as.expr, as.pkg, f, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, vals...)
	}
	return out, nil
}

// loadDep type-checks a dependency from source to read its package-level variables (an event type such as
// reimbursement.ReportApprovedType = events.Type(...) is a variable, so export data does not carry its value).
func (a *analyzer) loadDep(path string) bool {
	l, ok := a.deps[path]
	if !ok || a.depDone[path] || len(l.CgoFiles) > 0 {
		return false
	}
	a.depDone[path] = true
	p := &pkg{path: path, info: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}}
	for _, name := range l.GoFiles {
		f, err := parser.ParseFile(a.fset, filepath.Join(l.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return false
		}
		p.files = append(p.files, f)
	}
	conf := types.Config{Importer: a.imp, Error: func(error) {}}
	_, _ = conf.Check(path, a.fset, p.files, p.info)
	for _, file := range p.files {
		for _, d := range file.Decls {
			if gd, ok := d.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						a.valueSpec(vs, p, nil)
					}
				}
			}
		}
	}
	return true
}

type dataVal struct {
	t    types.Type
	expr ast.Expr
	pkg  *pkg
}

func (a *analyzer) evalData(e ast.Expr, p *pkg, fr *frame, depth int) ([]dataVal, error) {
	e = ast.Unparen(e)
	t := p.info.TypeOf(e)
	if t != nil && !types.IsInterface(t) {
		return []dataVal{{t, e, p}}, nil
	}
	if depth > 20 {
		return []dataVal{{nil, e, p}}, nil
	}
	switch x := e.(type) {
	case *ast.Ident:
		if i, ok := a.param(fr, p, x); ok {
			if fr.args == nil {
				return nil, errUnbound
			}
			if i >= len(fr.args) {
				return []dataVal{{nil, e, p}}, nil
			}
			return a.evalData(fr.args[i], fr.caller.pkg, fr.caller, depth+1)
		}
		if v, ok := p.info.Uses[x].(*types.Var); ok && !isGlobal(v) && len(a.locals[v]) > 0 {
			var out []dataVal
			for _, as := range a.locals[v] {
				vals, err := a.evalData(as.expr, as.pkg, fr, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, vals...)
			}
			return out, nil
		}
	}
	return []dataVal{{nil, e, p}}, nil
}

type emitSite struct {
	typ  string
	data dataVal
	via  token.Pos // the outermost call that bound the type or payload, when they come from parameters
}

// resolve works out the (type, payload) pairs of one events.New call, binding the parameters they come from at each call
// site of the outermost function, up to maxDepth calls up.
func (a *analyzer) resolve(fr *frame, typE, dataE ast.Expr, p *pkg, depth int) ([]emitSite, string) {
	typs, errT := a.evalStr(typE, p, fr, 0)
	datas, errD := []dataVal{{}}, error(nil)
	if dataE != nil {
		datas, errD = a.evalData(dataE, p, fr, 0)
	}
	if errors.Is(errT, errUnbound) || errors.Is(errD, errUnbound) {
		outer := fr
		for outer.caller != nil {
			outer = outer.caller
		}
		if depth >= maxDepth {
			return nil, "bound more than " + fmt.Sprint(maxDepth) + " calls up"
		}
		sites := a.calls[outer.fn.key]
		if len(sites) == 0 {
			return nil, "comes from a parameter of " + outer.fn.decl.Name.Name + ", which nothing in the module calls directly"
		}
		var out []emitSite
		var why []string
		for _, s := range sites {
			nfr := rebind(fr, outer, s)
			got, reason := a.resolve(nfr, typE, dataE, p, depth+1)
			for i := range got {
				if !got[i].via.IsValid() {
					got[i].via = s.call.Pos()
				}
			}
			out = append(out, got...)
			if reason != "" {
				why = append(why, reason)
			}
		}
		return out, strings.Join(dedupe(why), "; ")
	}
	if errT != nil {
		return nil, "not a constant the check can evaluate"
	}
	var out []emitSite
	for _, t := range typs {
		for _, d := range datas {
			out = append(out, emitSite{typ: t, data: d})
		}
	}
	return out, ""
}

func rebind(fr, outer *frame, s callSite) *frame {
	top := &frame{fn: s.caller, pkg: s.caller.pkg}
	var cp func(*frame) *frame
	cp = func(f *frame) *frame {
		if f == outer {
			return &frame{fn: f.fn, pkg: f.pkg, args: s.call.Args, caller: top}
		}
		return &frame{fn: f.fn, pkg: f.pkg, args: f.args, caller: cp(f.caller)}
	}
	return cp(fr)
}

func dedupe(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// producers records the payload every events.New call writes.
func (a *analyzer) producers() {
	keys := make([]string, 0, len(a.funcs))
	for k := range a.funcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, k := range keys {
		f := a.funcs[k]
		if f.decl.Body == nil {
			continue
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && isPkgFunc(a.callee(f.pkg, call), outboxPkg, "Enqueue") && len(call.Args) == 5 {
				sites, reason := a.resolve(&frame{fn: f, pkg: f.pkg}, call.Args[2], nil, f.pkg, 0)
				if reason != "" {
					a.report.UnresolvedAggregates = append(a.report.UnresolvedAggregates, a.pos(call.Pos())+": "+reason)
				}
				for _, s := range sites {
					a.report.Aggregates = append(a.report.Aggregates, s.typ)
				}
				return true
			}
			if !ok || !isPkgFunc(a.callee(f.pkg, call), eventsPkg, "New") || len(call.Args) != 5 {
				return true
			}
			sites, reason := a.resolve(&frame{fn: f, pkg: f.pkg}, call.Args[0], call.Args[4], f.pkg, 0)
			if reason != "" {
				a.report.Unresolved = append(a.report.Unresolved, a.pos(call.Pos())+": "+reason)
			}
			for _, s := range sites {
				e := Emit{Type: s.typ, At: a.pos(call.Pos()), Via: a.pos(s.via), Fields: []Field{}}
				switch {
				case s.data.t == nil:
					e.Open, e.GoType = true, "any"
				case mapLiteral(s.data):
					e.GoType = types.TypeString(s.data.t, qualifier)
					e.Fields, e.Open = a.mapFields(s.data.expr.(*ast.CompositeLit), s.data.pkg, "")
				default:
					c := collector{decode: false, seen: map[*types.TypeName]bool{}}
					c.typ(s.data.t, "", false)
					e.GoType, e.Open, e.Fields = types.TypeString(s.data.t, qualifier), c.open, c.result()
				}
				id := e.Type + "|" + e.At + "|" + e.Via + "|" + e.GoType
				if !seen[id] {
					seen[id] = true
					a.report.Emits = append(a.report.Emits, e)
				}
			}
			return true
		})
	}
}

func mapLiteral(d dataVal) bool {
	cl, ok := ast.Unparen(d.expr).(*ast.CompositeLit)
	if !ok {
		return false
	}
	_, isMap := d.t.Underlying().(*types.Map)
	return isMap && cl != nil
}

// mapFields are the keys of a map literal payload (nested map literals followed); open when a key is not a constant.
func (a *analyzer) mapFields(cl *ast.CompositeLit, p *pkg, prefix string) ([]Field, bool) {
	var out []Field
	open := false
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return out, true
		}
		k, ok := a.constString(p, kv.Key)
		if !ok {
			open = true
			continue
		}
		path := joinPath(prefix, k)
		if inner, ok := ast.Unparen(kv.Value).(*ast.CompositeLit); ok {
			if _, isMap := p.info.TypeOf(inner).Underlying().(*types.Map); isMap {
				sub, o := a.mapFields(inner, p, path)
				out = append(out, sub...)
				open = open || o
				if len(sub) == 0 {
					out = append(out, Field{Path: path})
				}
				continue
			}
		}
		if t := p.info.TypeOf(kv.Value); t != nil && !types.IsInterface(t) {
			c := collector{seen: map[*types.TypeName]bool{}}
			c.typ(t, path, false)
			out = append(out, c.fields...)
			continue
		}
		out = append(out, Field{Path: path})
	}
	sortFields(out)
	return out, open
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// collector lists the JSON paths of a Go type as encoding/json reads (decode) or writes it.
type collector struct {
	decode bool
	open   bool
	seen   map[*types.TypeName]bool
	fields []Field
}

func (c *collector) result() []Field {
	sortFields(c.fields)
	out := c.fields[:0]
	for i, f := range c.fields {
		if i == 0 || f.Path != c.fields[i-1].Path {
			out = append(out, f)
		}
	}
	if out == nil {
		out = []Field{}
	}
	return out
}

func sortFields(f []Field) { sort.Slice(f, func(i, j int) bool { return f[i].Path < f[j].Path }) }

func (c *collector) leaf(path string, omit bool) {
	c.fields = append(c.fields, Field{Path: path, Omitempty: omit})
}

// codec reports whether t marshals or unmarshals itself, which ends the path.
func (c *collector) codec(t types.Type) bool {
	names := []string{"MarshalJSON", "MarshalText"}
	if c.decode {
		names = []string{"UnmarshalJSON", "UnmarshalText"}
	}
	ms := types.NewMethodSet(types.NewPointer(t))
	for _, n := range names {
		for i := range ms.Len() {
			if ms.At(i).Obj().Name() == n {
				return true
			}
		}
	}
	return false
}

func (c *collector) typ(t types.Type, path string, omit bool) {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if path != "" && c.codec(t) {
		c.leaf(path, omit)
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		if n, ok := t.(*types.Named); ok {
			if c.seen[n.Obj()] {
				if path != "" {
					c.leaf(path, omit)
				}
				return
			}
			c.seen[n.Obj()] = true
			defer delete(c.seen, n.Obj())
		}
		before := len(c.fields)
		c.structFields(u, path, omit)
		if path != "" && len(c.fields) == before {
			c.leaf(path, omit)
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			c.leafOrOpen(path, omit)
			return
		}
		if path == "" {
			c.open = true
			return
		}
		c.typ(u.Elem(), path+"[]", omit)
	case *types.Array:
		if path == "" {
			c.open = true
			return
		}
		c.typ(u.Elem(), path+"[]", omit)
	default:
		c.leafOrOpen(path, omit)
	}
}

func (c *collector) leafOrOpen(path string, omit bool) {
	if path == "" {
		c.open = true
		return
	}
	c.leaf(path, omit)
}

func (c *collector) structFields(st *types.Struct, prefix string, omit bool) {
	for i := range st.NumFields() {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fieldOmit := omit || strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		if f.Embedded() && name == "" {
			ft := types.Unalias(f.Type())
			if ptr, ok := ft.(*types.Pointer); ok {
				ft = types.Unalias(ptr.Elem())
			}
			if est, ok := ft.Underlying().(*types.Struct); ok {
				if n, ok := ft.(*types.Named); ok {
					if c.seen[n.Obj()] {
						continue
					}
					c.seen[n.Obj()] = true
					c.structFields(est, prefix, fieldOmit)
					delete(c.seen, n.Obj())
				} else {
					c.structFields(est, prefix, fieldOmit)
				}
				continue
			}
		}
		if !f.Exported() {
			continue
		}
		if name == "" {
			name = f.Name()
		}
		c.typ(f.Type(), joinPath(prefix, name), fieldOmit)
	}
}
