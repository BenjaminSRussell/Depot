// Command eventfields type-checks Go modules and prints, as JSON, the CloudEvents data fields their event consumers
// decode and the data fields their producers write, per event type. deploy/scripts/test_event_contracts.py compares
// both with the producers' AsyncAPI schemas (architecture gap D6, B-05).
//
//	go -C deploy/ci/eventfields run . -root "$PWD" services/search services/sales
//
// Consumers: every map[string]eventing.Handler literal (or m[type] = handler assignment) in code a main package of the
// module reaches, through functions, variables and interface methods (every module method of that name whose type has
// the interface's methods). For each entry, the handler expression is followed the same way, and every json.Unmarshal of
// the event's data into a concrete type is a decode: of events.Event.Data, of a local copy of it, or of a []byte or
// json.RawMessage parameter it is passed in (statically, or through a func value whose module implementations match). A
// decode into an `any` parameter takes the type of the argument at each call site, and a further Unmarshal of a field of
// decoded data (directly, through a local copy or through a helper that takes it) adds that field's nested paths. Fail
// closed: every other use of the data in the code a handler reaches (json.NewDecoder, len, a call the check does not
// follow, json.Marshal of the whole event) is a problem of that handler, a decode into a map is reported as open, and a
// decoded json.RawMessage, []byte, map or interface field no followed decode reads is marked dynamic. Every
// eventing.Config the main packages reach is reported with its Name and the handler maps its Handlers field holds.
//
// Producers: every events.New call in the module. The event type is a constant, events.Type of constants, or a parameter
// or local variable bound to one; the payload is the static type of the data argument, followed through parameters to
// each call site of the enclosing function (up to maxDepth calls up). Every outbox.Enqueue call gives (event type,
// aggregate) routes: the event is traced back to its events.New through local variables, parameters, functions returning
// it, callbacks the caller passes and struct fields it was stored in. Calls that cannot be resolved are reported with the
// call that failed to bind them.
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
	// Dynamic marks a decoded field with no fixed shape (json.RawMessage, []byte, a map or an interface) that no nested
	// decode the check follows reads: what the handler takes from it is unknown, so the caller fails on it.
	Dynamic bool `json:"dynamic,omitempty"`
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
	Type string `json:"type"`
	At   string `json:"at"`
	// Map is the position of the handler map literal (or m[type] = h assignment) the entry belongs to.
	Map     string   `json:"map"`
	Decodes []Decode `json:"decodes"`
	// Problems are reads of the event data the check cannot follow: a use of evt.Data other than a json.Unmarshal of it
	// (directly, through a local copy or a []byte/json.RawMessage parameter it is passed in), such as json.NewDecoder or
	// len, or a decode into a value whose type cannot be worked out. The caller fails on them.
	Problems []string `json:"problems"`
}

// Consumer is an eventing.Config the module's main packages reach: its Name (the consumer group for every multi-group
// service) and the handler maps its Handlers field holds, bound through parameters and struct literals.
type Consumer struct {
	At    string   `json:"at"`
	Names []string `json:"names"`
	Maps  []string `json:"maps"`
}

// Route is an event type an outbox.Enqueue call writes under an aggregate type: an outbox connector that routes by
// aggregate_type sends it to depot.<context>.<aggregate>.
type Route struct {
	Type      string `json:"type"`
	Aggregate string `json:"aggregate"`
	At        string `json:"at"`
}

// Unresolved is an events.New or outbox.Enqueue call whose event type, payload or aggregate could not be worked out. Via
// is the call that failed to bind it (when the value comes from a parameter), Func the function that call sits in.
type Unresolved struct {
	At     string `json:"at"`
	Via    string `json:"via,omitempty"`
	Func   string `json:"func"`
	Reason string `json:"reason"`
	// Text is the source of the call that could not be resolved (the binding call when Via is set), so an allowlist names
	// one call, not every unresolved call of a function.
	Text string `json:"text"`
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
	Dir       string     `json:"dir"`
	Module    string     `json:"module"`
	Handlers  []Handler  `json:"handlers"`
	Consumers []Consumer `json:"consumers"`
	// UnresolvedConsumers are eventing.Config literals whose Name or handler maps could not be worked out.
	UnresolvedConsumers []Unresolved `json:"unresolvedConsumers"`
	Emits               []Emit       `json:"emits"`
	// Aggregates are the aggregate types outbox.Enqueue calls write, Routes the (event type, aggregate) pairs of the calls
	// whose event the check traces back to its events.New, and UnresolvedRoutes the calls whose event or aggregate it
	// cannot.
	Aggregates       []string     `json:"aggregates"`
	Routes           []Route      `json:"routes"`
	UnresolvedRoutes []Unresolved `json:"unresolvedRoutes"`
	// Unresolved lists events.New calls whose event type or payload could not be worked out.
	Unresolved []Unresolved `json:"unresolved"`
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
	recv         string       // the receiver's named type (path.Name) of a method, "" for a function
	decodeParams map[int]bool // parameters event data is decoded into (an `any` the caller binds)
	dataParams   map[int]bool // []byte/json.RawMessage parameters some call passes event data in
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
	dyn     []callSite                // calls of a func value (no static callee)
	fields  map[string][]assign       // values stored in a struct field of type events.Event, by "path.Type.Field"
	methods map[string]map[string]*fn // methods of each named type of the module, by receiver key and name
	litData map[types.Object]bool     // []byte parameters of function literals event data is passed in
	// assignedMaps are the m[type] = handler statements of handler maps, a handler map "site" like a literal.
	assignedMaps map[token.Pos]bool
	imp          types.Importer
	deps         map[string]listed // non-standard dependencies, whose package variables are read from source when a key names one
	depDone      map[string]bool
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
	a := &analyzer{fset: token.NewFileSet(), root: root, report: &Report{Dir: filepath.ToSlash(rel), Handlers: []Handler{}, Consumers: []Consumer{}, UnresolvedConsumers: []Unresolved{}, Emits: []Emit{}, Aggregates: []string{}, Routes: []Route{},
		UnresolvedRoutes: []Unresolved{}, Unresolved: []Unresolved{}, Problems: []string{}},
		funcs: map[string]*fn{}, locals: map[types.Object][]assign{}, globals: map[string][]assign{}, calls: map[string][]callSite{},
		fields: map[string][]assign{}, methods: map[string]map[string]*fn{}, litData: map[types.Object]bool{}, assignedMaps: map[token.Pos]bool{}, deps: map[string]listed{}, depDone: map[string]bool{}}
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
	a.solveDataParams()
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
	a.report.Unresolved = sortUnresolved(a.report.Unresolved)
	a.report.UnresolvedRoutes = sortUnresolved(a.report.UnresolvedRoutes)
	a.report.UnresolvedConsumers = sortUnresolved(a.report.UnresolvedConsumers)
	a.report.Aggregates = dedupe(a.report.Aggregates)
	sort.Slice(a.report.Routes, func(i, j int) bool {
		ri, rj := a.report.Routes[i], a.report.Routes[j]
		return ri.Type+ri.Aggregate+ri.At < rj.Type+rj.Aggregate+rj.At
	})
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
					f := &fn{key: funcKey(obj), decl: d, pkg: p, decodeParams: map[int]bool{}, dataParams: map[int]bool{}}
					for i := range sig.Params().Len() {
						f.params = append(f.params, sig.Params().At(i))
					}
					if r := sig.Recv(); r != nil {
						f.recv = typeKey(r.Type())
						if a.methods[f.recv] == nil {
							a.methods[f.recv] = map[string]*fn{}
						}
						a.methods[f.recv][d.Name.Name] = f
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
					// A call through an interface (a.store.SetErsVendor) is a call of every module method that implements it.
					for _, g := range a.implementations(callee) {
						a.calls[g.key] = append(a.calls[g.key], callSite{caller: f, call: n})
					}
				} else if tv, ok := f.pkg.info.Types[n.Fun]; ok && !tv.IsType() {
					if _, ok := tv.Type.Underlying().(*types.Signature); ok {
						a.dyn = append(a.dyn, callSite{caller: f, call: n})
					}
				}
			case *ast.CompositeLit:
				for _, el := range n.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							if v, ok := f.pkg.info.Uses[id].(*types.Var); ok && v.IsField() {
								a.storeField(f.pkg.info.TypeOf(n), id.Name, v, kv.Value, f)
							}
						}
					}
				}
			}
			return true
		})
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
				for i, lhs := range as.Lhs {
					if sel, ok := ast.Unparen(lhs).(*ast.SelectorExpr); ok {
						if s := f.pkg.info.Selections[sel]; s != nil && s.Kind() == types.FieldVal {
							a.storeField(s.Recv(), sel.Sel.Name, s.Obj().(*types.Var), as.Rhs[i], f)
						}
					}
				}
			}
			return true
		})
	}
}

// typeKey names a (pointer to a) named type by its package path and name, the same for source and export data objects.
func typeKey(t types.Type) string {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil {
		return n.Obj().Pkg().Path() + "." + n.Obj().Name()
	}
	return ""
}

// storeField records a value stored in a field of type events.Event (or *events.Event) of a named struct, so an
// outbox.Enqueue of outcome.Event can be traced to the events.New that built it.
func (a *analyzer) storeField(owner types.Type, name string, field *types.Var, value ast.Expr, f *fn) {
	if owner == nil || !namedIs(field.Type(), eventsPkg, "Event") {
		return
	}
	if k := typeKey(owner); k != "" {
		a.fields[k+"."+name] = append(a.fields[k+"."+name], assign{expr: value, pkg: f.pkg, fn: f})
	}
}

func sortUnresolved(u []Unresolved) []Unresolved {
	sort.Slice(u, func(i, j int) bool { return u[i].At+u[i].Via+u[i].Text < u[j].At+u[j].Via+u[j].Text })
	out := u[:0]
	for i, x := range u {
		if i == 0 || x != u[i-1] {
			out = append(out, x)
		}
	}
	return out
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

// isEventDataSel reports whether x is evt.Data of an events.Event.
func isEventDataSel(x *ast.SelectorExpr, p *pkg) bool {
	sel := p.info.Selections[x]
	return sel != nil && x.Sel.Name == "Data" && namedIs(sel.Recv(), eventsPkg, "Event")
}

// isBytes reports whether t is []byte (json.RawMessage included).
func isBytes(t types.Type) bool {
	if t == nil {
		return false
	}
	s, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	b, ok := s.Elem().Underlying().(*types.Basic)
	return ok && b.Kind() == types.Byte
}

// dataExpr reports whether e carries an event's data in f: evt.Data, a data parameter of f, a local variable every
// assignment of which is such an expression (data := evt.Data), or a conversion of one.
func (a *analyzer) dataExpr(e ast.Expr, p *pkg, f *fn) bool { return a.dataExprDepth(e, p, f, 0) }

func (a *analyzer) dataExprDepth(e ast.Expr, p *pkg, f *fn, depth int) bool {
	if depth > 8 {
		return false
	}
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return isEventDataSel(x, p)
	case *ast.Ident:
		return a.dataIdent(x, p, f, depth)
	case *ast.CallExpr:
		if tv, ok := p.info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return a.dataExprDepth(x.Args[0], p, f, depth+1)
		}
	}
	return false
}

func (a *analyzer) dataIdent(id *ast.Ident, p *pkg, f *fn, depth int) bool {
	obj := p.info.Uses[id]
	if obj == nil {
		obj = p.info.Defs[id]
	}
	v, ok := obj.(*types.Var)
	if !ok || v.IsField() || isGlobal(v) {
		return false
	}
	if i := paramIndex(f, v); i >= 0 {
		return f.dataParams[i]
	}
	if a.litData[v] {
		return true
	}
	as := a.locals[v]
	if len(as) == 0 {
		return false
	}
	for _, x := range as {
		if !a.dataExprDepth(x.expr, x.pkg, x.fn, depth+1) {
			return false
		}
	}
	return true
}

// solveDataParams marks the []byte/json.RawMessage parameters that event data is passed in: at a static call of a module
// function, or at a call of a func value (every module function of the same signature is then marked, as tax's
// movementOf mappers are).
func (a *analyzer) solveDataParams() {
	for changed := true; changed; {
		changed = false
		for key, sites := range a.calls {
			g := a.funcs[key]
			for _, s := range sites {
				for i, arg := range s.call.Args {
					if i < len(g.params) && !g.dataParams[i] && isBytes(g.params[i].Type()) && a.dataExpr(arg, s.caller.pkg, s.caller) {
						g.dataParams[i] = true
						changed = true
					}
				}
			}
		}
		for _, s := range a.dyn {
			sig, _ := s.caller.pkg.info.TypeOf(s.call.Fun).Underlying().(*types.Signature)
			lit, g, resolved := a.funcValue(s.call.Fun, s.caller.pkg, 0)
			for i, arg := range s.call.Args {
				if sig == nil || i >= sig.Params().Len() || !isBytes(sig.Params().At(i).Type()) || !a.dataExpr(arg, s.caller.pkg, s.caller) {
					continue
				}
				switch {
				case lit != nil:
					// The closure's own []byte parameter is the data: its decodes are read where the closure is walked.
					if obj := litParam(lit, s.caller.pkg, i); obj != nil && !a.litData[obj] {
						a.litData[obj] = true
						changed = true
					}
				case g != nil:
					if !g.dataParams[i] {
						g.dataParams[i] = true
						changed = true
					}
				case !resolved:
					// A func value the check cannot resolve (a parameter, a struct field): every module function of that
					// signature may be it.
					for _, h := range a.funcs {
						if h.recv == "" && !h.dataParams[i] && sameSignature(h, sig) {
							h.dataParams[i] = true
							changed = true
						}
					}
				}
			}
		}
	}
}

// funcValue resolves a called func value: a function literal (directly or through the single assignment of a local), or
// a module function or method value. resolved is false when it cannot be worked out (a parameter, a struct field).
func (a *analyzer) funcValue(e ast.Expr, p *pkg, depth int) (*ast.FuncLit, *fn, bool) {
	if depth > 8 {
		return nil, nil, false
	}
	e = ast.Unparen(e)
	if lit, ok := e.(*ast.FuncLit); ok {
		return lit, nil, true
	}
	var id *ast.Ident
	switch x := e.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return nil, nil, false
	}
	switch obj := p.info.Uses[id].(type) {
	case *types.Func:
		if impls := a.implementations(obj); len(impls) == 1 {
			return nil, impls[0], true
		}
	case *types.Var:
		if obj.IsField() || isGlobal(obj) {
			return nil, nil, false
		}
		if as := a.locals[obj]; len(as) == 1 {
			return a.funcValue(as[0].expr, as[0].pkg, depth+1)
		}
	}
	return nil, nil, false
}

// litParam is the object of a function literal's parameter i.
func litParam(lit *ast.FuncLit, p *pkg, i int) types.Object {
	n := 0
	for _, f := range lit.Type.Params.List {
		names := f.Names
		if len(names) == 0 {
			n++
			continue
		}
		for _, name := range names {
			if n == i {
				return p.info.Defs[name]
			}
			n++
		}
	}
	return nil
}

func sameSignature(g *fn, sig *types.Signature) bool {
	obj, ok := g.pkg.info.Defs[g.decl.Name].(*types.Func)
	return ok && shape(obj.Signature()) == shape(sig)
}

// shape is a signature's parameter and result types, without names (a func type and a function declared to match it
// name their parameters differently).
func shape(sig *types.Signature) string {
	q := func(p *types.Package) string { return p.Path() }
	var b strings.Builder
	for _, t := range []*types.Tuple{sig.Params(), sig.Results()} {
		b.WriteString("(")
		for i := range t.Len() {
			b.WriteString(types.TypeString(t.At(i).Type(), q) + ",")
		}
		b.WriteString(")")
	}
	if sig.Variadic() {
		b.WriteString("...")
	}
	return b.String()
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
		if t == nil {
			return nil, -1, false
		}
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
			if call, ok := n.(*ast.CallExpr); ok && a.isUnmarshal(f.pkg, call) && a.dataExpr(call.Args[0], f.pkg, f) {
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

// walker follows an expression through the module functions and variables it names, collecting decodes and the reads
// of the event data it cannot follow.
type walker struct {
	a        *analyzer
	record   bool
	seenFn   map[string]bool
	seenVar  map[types.Object]bool
	seenGlb  map[string]bool
	decodes  map[string]*Decode
	decoded  map[types.Object]decodedVar // local variables a decode filled, for a nested decode of one of their fields
	problems map[string]bool
	maps     map[string]bool // positions of the handler maps the walk meets, when non-nil (handlerSites)
}

type decodedVar struct {
	key  string // of the Decode in walker.decodes
	path string // the JSON path the variable holds ("" for the whole data)
}

func (a *analyzer) newWalker(record bool) *walker {
	return &walker{a: a, record: record, seenFn: map[string]bool{}, seenVar: map[types.Object]bool{}, seenGlb: map[string]bool{},
		decodes: map[string]*Decode{}, decoded: map[types.Object]decodedVar{}, problems: map[string]bool{}}
}

func (w *walker) problem(at token.Pos, format string, args ...any) {
	w.problems[w.a.pos(at)+": "+fmt.Sprintf(format, args...)] = true
}

func (w *walker) node(n ast.Node, p *pkg, f *fn) {
	if n == nil {
		return
	}
	var stack []ast.Node
	ast.Inspect(n, func(x ast.Node) bool {
		if x == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		ancestors := stack
		stack = append(stack, x)
		switch x := x.(type) {
		case *ast.CompositeLit:
			if w.maps != nil && isHandlerMap(p.info.TypeOf(x)) {
				w.maps[w.a.pos(x.Pos())] = true
			}
		case *ast.AssignStmt:
			if w.maps != nil && w.a.assignedMaps[x.Pos()] {
				w.maps[w.a.pos(x.Pos())] = true
			}
		case *ast.CallExpr:
			if w.record {
				w.call(x, p, f)
			}
		case *ast.SelectorExpr:
			// A root with no ancestors is a variable's assigned value, walked from a use of the variable; its own
			// statement is checked where the function body is walked.
			if w.record && len(ancestors) > 0 && isEventDataSel(x, p) {
				w.sink(x, ancestors, p, f)
			}
		case *ast.Ident:
			w.ident(x, p)
			if w.record && len(ancestors) > 0 && p.info.Uses[x] != nil && !isAssigned(x, ancestors) && w.a.dataIdent(x, p, f, 0) {
				w.sink(x, ancestors, p, f)
			}
		}
		return true
	})
}

// isAssigned reports whether id is the left-hand side of the statement it sits in.
func isAssigned(id *ast.Ident, ancestors []ast.Node) bool {
	if len(ancestors) == 0 {
		return false
	}
	if as, ok := ancestors[len(ancestors)-1].(*ast.AssignStmt); ok {
		for _, l := range as.Lhs {
			if l == ast.Expr(id) {
				return true
			}
		}
	}
	return false
}

// sink checks where the event data e flows: into json.Unmarshal, into a data parameter of a module function (or of a
// func value with a []byte parameter), or into a local copy. Anything else is a read the check cannot follow.
func (w *walker) sink(e ast.Expr, ancestors []ast.Node, p *pkg, f *fn) {
	var cur ast.Node = e
	for i := len(ancestors) - 1; i >= 0; i-- {
		switch par := ancestors[i].(type) {
		case *ast.ParenExpr:
			cur = par
			continue
		case *ast.CallExpr:
			if tv, ok := p.info.Types[par.Fun]; ok && tv.IsType() {
				cur = par
				continue
			}
			idx := -1
			for j, arg := range par.Args {
				if ast.Node(arg) == cur {
					idx = j
				}
			}
			if idx < 0 {
				break
			}
			if w.a.isUnmarshal(p, par) && idx == 0 {
				return
			}
			if callee := w.a.callee(p, par); callee != nil {
				if g, ok := w.a.funcs[funcKey(callee)]; ok && g.dataParams[idx] {
					return
				}
				w.problem(e.Pos(), "passes the event data to %s, which the check does not follow; decode it with json.Unmarshal", callee.Name())
				return
			}
			if sig, ok := p.info.TypeOf(par.Fun).Underlying().(*types.Signature); ok && idx < sig.Params().Len() && isBytes(sig.Params().At(idx).Type()) {
				lit, g, resolved := w.a.funcValue(par.Fun, p, 0)
				switch {
				case lit != nil:
					if obj := litParam(lit, p, idx); obj != nil && w.a.litData[obj] {
						return
					}
				case g != nil:
					if g.dataParams[idx] {
						return
					}
				case !resolved:
					for _, g := range w.a.funcs {
						if g.dataParams[idx] && sameSignature(g, sig) {
							return
						}
					}
				}
			}
			if id, ok := ast.Unparen(par.Fun).(*ast.Ident); ok {
				if b, ok := p.info.Uses[id].(*types.Builtin); ok {
					w.problem(e.Pos(), "passes the event data to %s, which the check does not follow; decode it with json.Unmarshal", b.Name())
					return
				}
			}
			w.problem(e.Pos(), "passes the event data to a function value the check cannot follow")
			return
		case *ast.AssignStmt:
			for j, r := range par.Rhs {
				if ast.Node(r) == cur && len(par.Lhs) == len(par.Rhs) {
					if id, ok := par.Lhs[j].(*ast.Ident); ok && w.a.dataIdent(id, p, f, 0) {
						return
					}
				}
			}
		case *ast.ValueSpec:
			for j, v := range par.Values {
				if ast.Node(v) == cur && j < len(par.Names) && w.a.dataIdent(par.Names[j], p, f, 0) {
					return
				}
			}
		}
		break
	}
	w.problem(e.Pos(), "reads the event data in a way the check cannot follow (only json.Unmarshal of it, of a local copy "+
		"or of a []byte/json.RawMessage parameter it is passed in, is checked)")
}

func (w *walker) ident(id *ast.Ident, p *pkg) {
	switch obj := p.info.Uses[id].(type) {
	case *types.Func:
		for _, g := range w.a.implementations(obj) {
			if !w.seenFn[g.key] {
				w.seenFn[g.key] = true
				w.node(g.decl.Body, g.pkg, g)
			}
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

// implementations are the module declarations a reference to obj may run: its own, or for an interface method every
// module method of that name whose type has all the interface's methods (handler maps behind an interface).
func (a *analyzer) implementations(obj *types.Func) []*fn {
	if g, ok := a.funcs[funcKey(obj)]; ok {
		return []*fn{g}
	}
	recv := obj.Signature().Recv()
	if recv == nil {
		return nil
	}
	iface, ok := recv.Type().Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	var out []*fn
	keys := make([]string, 0, len(a.methods))
	for k := range a.methods {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ms := a.methods[k]
		g, ok := ms[obj.Name()]
		if !ok {
			continue
		}
		all := true
		for i := range iface.NumMethods() {
			if _, ok := ms[iface.Method(i).Name()]; !ok {
				all = false
			}
		}
		if all {
			out = append(out, g)
		}
	}
	return out
}

func (w *walker) call(call *ast.CallExpr, p *pkg, f *fn) {
	if w.a.isUnmarshal(p, call) {
		src := call.Args[0]
		if w.a.dataExpr(src, p, f) {
			t, i, ok := w.a.decodeTarget(call.Args[1], p, f)
			switch {
			case !ok:
				w.problem(call.Pos(), "decodes event data into a value whose type the check cannot work out")
			case i < 0:
				w.add(call.Pos(), t, "", call.Args[1], p)
			}
			return
		}
		// A second decode of a json.RawMessage field the first one filled: its fields sit under that field's path.
		if dv, path, ok := w.decodedPath(src, p); ok {
			t, i, ok := w.a.decodeTarget(call.Args[1], p, f)
			if !ok || i >= 0 {
				w.problem(call.Pos(), "decodes a field of the event data into a value whose type the check cannot work out")
				return
			}
			w.nested(dv, joinPath(dv.path, path), t, call.Args[1], p)
		}
		return
	}
	callee := w.a.callee(p, call)
	if callee == nil {
		return
	}
	if callee.Pkg() != nil && callee.Pkg().Path() == "encoding/json" && (callee.Name() == "Marshal" || callee.Name() == "MarshalIndent" ||
		callee.Name() == "Encode") && len(call.Args) > 0 {
		if t := p.info.TypeOf(call.Args[0]); t != nil && namedIs(t, eventsPkg, "Event") {
			w.problem(call.Pos(), "marshals the whole event, whose re-decoded data the check cannot follow; decode evt.Data instead")
		}
	}
	g, ok := w.a.funcs[funcKey(callee)]
	if !ok {
		return
	}
	// A decoded json.RawMessage/[]byte field passed to a helper that decodes it: a nested decode under that field's path.
	for i, arg := range call.Args {
		if i < len(g.params) && isBytes(g.params[i].Type()) {
			if dv, path, ok := w.decodedPath(arg, p); ok {
				w.followNested(g, i, dv, joinPath(dv.path, path), call, p, 0)
			}
		}
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
			w.problem(call.Pos(), "passes a value whose type the check cannot work out to %s, which decodes event data into it", g.decl.Name.Name)
			continue
		}
		w.add(call.Pos(), t, "", arg, p)
	}
}

// followNested records the decodes helper g makes of its parameter i, which holds the decoded field at path: a
// json.Unmarshal of it (or of a local copy) into a concrete type, or into an `any` parameter bound at the call, and the
// same through the module helpers g passes it to.
func (w *walker) followNested(g *fn, i int, dv decodedVar, path string, call *ast.CallExpr, cp *pkg, depth int) {
	if depth > 5 || g.decl.Body == nil {
		return
	}
	ast.Inspect(g.decl.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if w.a.isUnmarshal(g.pkg, c) && w.a.paramRef(c.Args[0], g, i, 0) {
			t, j, ok := w.a.decodeTarget(c.Args[1], g.pkg, g)
			switch {
			case ok && j < 0:
				w.nested(dv, path, t, c.Args[1], g.pkg)
			case ok && j < len(call.Args):
				if at := cp.info.TypeOf(call.Args[j]); at != nil {
					if ptr, isPtr := at.Underlying().(*types.Pointer); isPtr {
						at = ptr.Elem()
					}
					if !types.IsInterface(at) {
						w.nested(dv, path, at, call.Args[j], cp)
					}
				}
			}
			return true
		}
		if callee := w.a.callee(g.pkg, c); callee != nil {
			if h, ok := w.a.funcs[funcKey(callee)]; ok {
				for k, arg := range c.Args {
					if k < len(h.params) && isBytes(h.params[k].Type()) && w.a.paramRef(arg, g, i, 0) {
						w.followNested(h, k, dv, path, c, g.pkg, depth+1)
					}
				}
			}
		}
		return true
	})
}

// paramRef reports whether e is g's parameter i, a conversion of it or a local copy of it.
func (a *analyzer) paramRef(e ast.Expr, g *fn, i int, depth int) bool {
	if depth > 8 {
		return false
	}
	e = ast.Unparen(e)
	if c, ok := e.(*ast.CallExpr); ok {
		if tv, ok := g.pkg.info.Types[c.Fun]; ok && tv.IsType() && len(c.Args) == 1 {
			return a.paramRef(c.Args[0], g, i, depth+1)
		}
		return false
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := g.pkg.info.Uses[id].(*types.Var)
	if !ok {
		return false
	}
	if paramIndex(g, v) == i {
		return true
	}
	if as := a.locals[v]; len(as) == 1 && paramIndex(g, v) < 0 && !isGlobal(v) {
		return a.paramRef(as[0].expr, g, i, depth+1)
	}
	return false
}

// decodedPath is the variable a decode filled and the JSON path below it of a field expression such as d.Extra or
// d.Lines[i].Raw, also through a local copy (raw := d.Raw) or a conversion ([]byte(d.Raw)).
func (w *walker) decodedPath(e ast.Expr, p *pkg) (decodedVar, string, bool) {
	return w.decodedPathDepth(e, p, 0)
}

func (w *walker) decodedPathDepth(e ast.Expr, p *pkg, depth int) (decodedVar, string, bool) {
	var segs []string // innermost first once complete
	e = ast.Unparen(e)
	for {
		if c, ok := e.(*ast.CallExpr); ok {
			if tv, ok := p.info.Types[c.Fun]; ok && tv.IsType() && len(c.Args) == 1 {
				e = ast.Unparen(c.Args[0])
				continue
			}
		}
		switch x := e.(type) {
		case *ast.SelectorExpr:
			sel := p.info.Selections[x]
			if sel == nil || sel.Kind() != types.FieldVal {
				return decodedVar{}, "", false
			}
			name, ok := jsonPath(sel)
			if !ok {
				return decodedVar{}, "", false
			}
			if name != "" {
				segs = append([]string{name}, segs...)
			}
			e = ast.Unparen(x.X)
		case *ast.IndexExpr:
			segs = append([]string{"[]"}, segs...)
			e = ast.Unparen(x.X)
		case *ast.StarExpr:
			e = ast.Unparen(x.X)
		case *ast.Ident:
			dv, ok := w.decoded[p.info.Uses[x]]
			base := ""
			if !ok && depth < 8 {
				// A local copy of a decoded field: raw := d.Raw.
				if v, isVar := p.info.Uses[x].(*types.Var); isVar && !isGlobal(v) && len(w.a.locals[v]) == 1 {
					as := w.a.locals[v][0]
					if adv, apath, aok := w.decodedPathDepth(as.expr, as.pkg, depth+1); aok {
						dv, base, ok = adv, apath, true
					}
				}
			}
			if !ok || (len(segs) == 0 && base == "") {
				return decodedVar{}, "", false
			}
			path := base
			for _, s := range segs {
				switch {
				case s == "[]":
					path += "[]"
				case path == "":
					path = s
				default:
					path += "." + s
				}
			}
			return dv, path, true
		default:
			return decodedVar{}, "", false
		}
	}
}

// jsonPath is the JSON key path of a field selection, through the embedded structs it is promoted from ("" when every
// step is an embedded struct encoding/json flattens).
func jsonPath(sel *types.Selection) (string, bool) {
	t := sel.Recv()
	var parts []string
	for _, i := range sel.Index() {
		if p, ok := types.Unalias(t).(*types.Pointer); ok {
			t = p.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok || i >= st.NumFields() {
			return "", false
		}
		f := st.Field(i)
		name, _, _ := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
		if name == "-" {
			return "", false
		}
		if !f.Embedded() || name != "" {
			if name == "" {
				name = f.Name()
			}
			parts = append(parts, name)
		}
		t = f.Type()
	}
	return strings.Join(parts, "."), true
}

func (w *walker) add(at token.Pos, t types.Type, prefix string, target ast.Expr, p *pkg) {
	c := collector{decode: true, seen: map[*types.TypeName]bool{}}
	c.typ(t, prefix, false)
	d := &Decode{At: w.a.pos(at), GoType: types.TypeString(t, qualifier), Open: c.open, Fields: c.result()}
	key := d.At + "|" + d.GoType
	w.decodes[key] = d
	w.remember(target, decodedVar{key: key, path: prefix}, p)
}

// nested adds the fields of a decode of a field of decoded data, under that field's path.
func (w *walker) nested(dv decodedVar, path string, t types.Type, target ast.Expr, p *pkg) {
	d := w.decodes[dv.key]
	for i := range d.Fields {
		if d.Fields[i].Path == path {
			d.Fields[i].Dynamic = false // read by this nested decode
		}
	}
	c := collector{decode: true, seen: map[*types.TypeName]bool{}}
	c.typ(t, path, false)
	switch t.Underlying().(type) {
	case *types.Map, *types.Interface:
		d.Open = true // a map (or any) reads keys the check cannot list
	}
	d.Fields = append(d.Fields, c.fields...)
	sortFields(d.Fields)
	w.remember(target, decodedVar{key: dv.key, path: path}, p)
}

func (w *walker) remember(target ast.Expr, dv decodedVar, p *pkg) {
	e := ast.Unparen(target)
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = ast.Unparen(u.X)
	}
	if id, ok := e.(*ast.Ident); ok {
		if v := p.info.Uses[id]; v != nil {
			w.decoded[v] = dv
		}
	}
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
	entry := func(key, value ast.Expr, at, mapAt token.Pos, p *pkg, f *fn) {
		typs, err := a.evalStr(key, p, &frame{fn: f, pkg: p}, 0)
		if err != nil || len(typs) == 0 {
			a.problem(at, "a handler map key the check cannot evaluate to a constant event type")
			return
		}
		w := a.newWalker(true)
		w.node(value, p, f)
		decodes := []Decode{}
		for _, d := range w.decodes {
			decodes = append(decodes, *d)
		}
		sort.Slice(decodes, func(i, j int) bool { return decodes[i].At+decodes[i].GoType < decodes[j].At+decodes[j].GoType })
		problems := []string{}
		for p := range w.problems {
			problems = append(problems, p)
		}
		sort.Strings(problems)
		for _, typ := range dedupe(typs) {
			a.report.Handlers = append(a.report.Handlers, Handler{Type: typ, At: a.pos(at), Map: a.pos(mapAt), Decodes: decodes, Problems: problems})
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
							entry(kv.Key, kv.Value, kv.Pos(), x.Pos(), p, f)
						}
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					if ix, ok := ast.Unparen(lhs).(*ast.IndexExpr); ok && isHandlerMap(p.info.TypeOf(ix.X)) && len(x.Lhs) == len(x.Rhs) {
						if id, ok := ast.Unparen(ix.Index).(*ast.Ident); ok && copies[p.info.Uses[id]] {
							continue
						}
						if f != nil {
							a.assignedMaps[x.Pos()] = true
						}
						entry(ix.Index, x.Rhs[i], x.Pos(), x.Pos(), p, f)
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
	for _, k := range keys {
		f, ok := a.funcs[k]
		if !ok || f.decl.Body == nil {
			continue
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || !namedIs(f.pkg.info.TypeOf(cl), eventingPkg, "Config") {
				return true
			}
			var nameE, handlersE ast.Expr
			for _, el := range cl.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*ast.Ident); ok {
						switch id.Name {
						case "Name":
							nameE = kv.Value
						case "Handlers":
							handlersE = kv.Value
						}
					}
				}
			}
			if nameE == nil || handlersE == nil {
				return true
			}
			a.consumerConfig(cl, nameE, handlersE, f)
			return true
		})
	}
	sort.Slice(a.report.Consumers, func(i, j int) bool {
		return a.report.Consumers[i].At+strings.Join(a.report.Consumers[i].Names, ",") < a.report.Consumers[j].At+strings.Join(a.report.Consumers[j].Names, ",")
	})
}

// consumerConfig records the (Name, handler maps) pairs of one eventing.Config literal, binding the parameters both come
// from at the same call sites.
func (a *analyzer) consumerConfig(cl *ast.CompositeLit, nameE, handlersE ast.Expr, f *fn) {
	got, fails := a.expand(&frame{fn: f, pkg: f.pkg}, 0, func(fr *frame) ([]emitSite, error) {
		names, errN := a.evalStr(nameE, f.pkg, fr, 0)
		maps, errH := a.handlerSites(handlersE, f.pkg, fr, 0)
		if errors.Is(errN, errUnbound) || errors.Is(errH, errUnbound) {
			return nil, errUnbound
		}
		if errN != nil {
			return nil, errors.New("the consumer's Name is " + errNotConstant.Error())
		}
		if errH != nil {
			return nil, errors.New("the consumer's Handlers reach no handler map the check finds")
		}
		var out []emitSite
		for _, n := range names {
			out = append(out, emitSite{typ: n, agg: strings.Join(maps, "|")})
		}
		return out, nil
	})
	a.report.UnresolvedConsumers = append(a.report.UnresolvedConsumers, a.unresolved(cl.Pos(), cl, fails)...)
	byMaps := map[string][]string{}
	for _, g := range got {
		byMaps[g.agg] = append(byMaps[g.agg], g.typ)
	}
	keys := make([]string, 0, len(byMaps))
	for k := range byMaps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a.report.Consumers = append(a.report.Consumers, Consumer{At: a.pos(cl.Pos()), Names: dedupe(byMaps[k]), Maps: strings.Split(k, "|")})
	}
}

// handlerSites are the handler map literals (by position) a Handlers expression reaches: through parameters bound at
// call sites, struct literal fields (spec.handlers(s) of a package-level consumerSpec) and the functions it calls.
func (a *analyzer) handlerSites(e ast.Expr, p *pkg, fr *frame, depth int) ([]string, error) {
	if depth > 20 {
		return nil, errNotConstant
	}
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok {
		if i, ok := a.param(fr, p, id); ok {
			if fr.args == nil {
				return nil, errUnbound
			}
			if i >= len(fr.args) {
				return nil, errNotConstant
			}
			return a.handlerSites(fr.args[i], fr.caller.pkg, fr.caller, depth+1)
		}
	}
	root := ast.Node(e)
	rp, rf := p, fr.fn
	if c, ok := e.(*ast.CallExpr); ok {
		if sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr); ok {
			if s := p.info.Selections[sel]; s != nil && s.Kind() == types.FieldVal {
				v, vp, vfr, err := a.structField(sel.X, sel.Sel.Name, p, fr, depth+1)
				if err != nil {
					return nil, err
				}
				root, rp, rf = v, vp, vfr.fn
			}
		}
	}
	w := a.newWalker(false)
	w.maps = map[string]bool{}
	w.node(root, rp, rf)
	var out []string
	for m := range w.maps {
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errNotConstant
	}
	sort.Strings(out)
	return out, nil
}

// structField is the value a struct literal gives field name, for x a parameter bound at a call site, a variable with one
// assignment, or the literal itself.
func (a *analyzer) structField(x ast.Expr, name string, p *pkg, fr *frame, depth int) (ast.Expr, *pkg, *frame, error) {
	if depth > 20 {
		return nil, nil, nil, errNotConstant
	}
	x = ast.Unparen(x)
	switch e := x.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return a.structField(e.X, name, p, fr, depth+1)
		}
	case *ast.CompositeLit:
		for _, el := range e.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
					return kv.Value, p, fr, nil
				}
			}
		}
	case *ast.Ident:
		if i, ok := a.param(fr, p, e); ok {
			if fr.args == nil {
				return nil, nil, nil, errUnbound
			}
			if i >= len(fr.args) {
				return nil, nil, nil, errNotConstant
			}
			return a.structField(fr.args[i], name, fr.caller.pkg, fr.caller, depth+1)
		}
		v, ok := p.info.Uses[e].(*types.Var)
		if !ok {
			return nil, nil, nil, errNotConstant
		}
		var as []assign
		if isGlobal(v) {
			as = a.globals[globalKey(v)]
		} else {
			as = a.locals[v]
		}
		if len(as) == 1 {
			f2 := fr
			if as[0].fn == nil {
				f2 = &frame{pkg: as[0].pkg}
			}
			return a.structField(as[0].expr, name, as[0].pkg, f2, depth+1)
		}
	}
	return nil, nil, nil, errNotConstant
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
		if sel := p.info.Selections[x]; sel != nil && sel.Kind() == types.FieldVal {
			// spec.name of a struct literal the value is bound to (production's consumerSpec).
			v, vp, vfr, err := a.structField(x.X, x.Sel.Name, p, fr, depth+1)
			if err != nil {
				return nil, err
			}
			return a.evalStr(v, vp, vfr, depth+1)
		}
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
	agg  string
	data dataVal
	via  token.Pos // the outermost call that bound the type or payload, when they come from parameters
}

type failure struct {
	via    token.Pos
	fn     *fn
	reason string
	text   string // the binding call's source, when via is set
}

// expand evaluates eval in fr; when the value comes from a parameter of the outermost function, it binds that function's
// parameters at each of its call sites in turn, up to maxDepth calls up.
func (a *analyzer) expand(fr *frame, depth int, eval func(*frame) ([]emitSite, error)) ([]emitSite, []failure) {
	got, err := eval(fr)
	if err == nil {
		return got, nil
	}
	outer := fr
	for outer.caller != nil {
		outer = outer.caller
	}
	switch {
	case !errors.Is(err, errUnbound):
		return nil, []failure{{fn: outer.fn, reason: err.Error()}}
	case depth >= maxDepth:
		return nil, []failure{{fn: outer.fn, reason: fmt.Sprintf("bound more than %d calls up", maxDepth)}}
	}
	sites := a.calls[outer.fn.key]
	if len(sites) == 0 {
		return nil, []failure{{fn: outer.fn, reason: "comes from a parameter of " + outer.fn.decl.Name.Name + ", which nothing in the module calls directly"}}
	}
	var out []emitSite
	var fails []failure
	for _, s := range sites {
		g, f := a.expand(rebind(fr, outer, s), depth+1, eval)
		for i := range g {
			if !g[i].via.IsValid() {
				g[i].via = s.call.Pos()
			}
		}
		for i := range f {
			if !f[i].via.IsValid() {
				f[i].via, f[i].fn, f[i].text = s.call.Pos(), s.caller, types.ExprString(s.call)
			}
		}
		out = append(out, g...)
		fails = append(fails, f...)
	}
	return out, fails
}

var errNotConstant = errors.New("not a constant the check can evaluate")

// resolve works out the (type, payload) pairs of one events.New call.
func (a *analyzer) resolve(fr *frame, typE, dataE ast.Expr, p *pkg) ([]emitSite, []failure) {
	return a.expand(fr, 0, func(fr *frame) ([]emitSite, error) {
		typs, errT := a.evalStr(typE, p, fr, 0)
		datas, errD := a.evalData(dataE, p, fr, 0)
		if errors.Is(errT, errUnbound) || errors.Is(errD, errUnbound) {
			return nil, errUnbound
		}
		if errT != nil {
			return nil, errNotConstant
		}
		var out []emitSite
		for _, t := range typs {
			for _, d := range datas {
				out = append(out, emitSite{typ: t, data: d})
			}
		}
		return out, nil
	})
}

// resolveRoute works out the (event type, aggregate type) pairs of one outbox.Enqueue call: the aggregate is evaluated
// like an event type, and the event is traced back to the events.New that built it, both in the same bound frame.
func (a *analyzer) resolveRoute(fr *frame, aggE, evtE ast.Expr, p *pkg) ([]emitSite, []failure) {
	return a.expand(fr, 0, func(fr *frame) ([]emitSite, error) {
		aggs, errA := a.evalStr(aggE, p, fr, 0)
		typs, errT := a.evalEvent(evtE, p, fr, 0)
		if errors.Is(errA, errUnbound) || errors.Is(errT, errUnbound) {
			return nil, errUnbound
		}
		if errA != nil {
			return nil, errors.New("the aggregate type is " + errNotConstant.Error())
		}
		if errT != nil {
			return nil, errors.New("the event cannot be traced to the events.New that built it")
		}
		var out []emitSite
		for _, t := range dedupe(typs) {
			for _, g := range dedupe(aggs) {
				out = append(out, emitSite{typ: t, agg: g})
			}
		}
		return out, nil
	})
}

// evalEvent is the event types an events.Event expression may hold: the type of the events.New that built it, through
// local variables, parameters (errUnbound when unbound), module functions that return it and struct fields it was
// stored in (outcome.Event).
func (a *analyzer) evalEvent(e ast.Expr, p *pkg, fr *frame, depth int) ([]string, error) {
	if depth > 20 {
		return nil, errNotConstant
	}
	e = ast.Unparen(e)
	switch x := e.(type) {
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return a.evalEvent(x.X, p, fr, depth+1)
		}
	case *ast.StarExpr:
		return a.evalEvent(x.X, p, fr, depth+1)
	case *ast.CallExpr:
		callee := a.callee(p, x)
		if isPkgFunc(callee, eventsPkg, "New") && len(x.Args) == 5 {
			return a.evalStr(x.Args[0], p, fr, depth+1)
		}
		if callee == nil {
			// A callback the caller passes (store.SetErsVendor(..., func(old bool) (events.Event, error) {...})).
			lit, lp, lfr, err := a.funcLit(x.Fun, p, fr, depth)
			if err != nil {
				return nil, err
			}
			var out []string
			var bad error
			inspectShallow(lit.Body, func(n ast.Node) {
				if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) > 0 && bad == nil {
					if _, zero := ast.Unparen(r.Results[0]).(*ast.CompositeLit); zero {
						return
					}
					vals, err := a.evalEvent(r.Results[0], lp, lfr, depth+1)
					if err != nil {
						bad = err
						return
					}
					out = append(out, vals...)
				}
			})
			if bad != nil {
				return nil, bad
			}
			if len(out) == 0 {
				return nil, errNotConstant
			}
			return out, nil
		}
		g, ok := a.funcs[funcKey(callee)]
		if !ok || g.decl.Body == nil {
			return nil, errNotConstant
		}
		inner := &frame{fn: g, pkg: g.pkg, args: x.Args, caller: fr}
		var out []string
		var bad error
		inspectShallow(g.decl.Body, func(n ast.Node) {
			if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) > 0 {
				t := g.pkg.info.TypeOf(r.Results[0])
				if tup, ok := t.(*types.Tuple); ok && tup.Len() > 0 {
					t = tup.At(0).Type() // return newEvent(...) of a function returning (events.Event, error)
				}
				if t == nil || !namedIs(t, eventsPkg, "Event") {
					return
				}
				vals, err := a.evalEvent(r.Results[0], g.pkg, inner, depth+1)
				if err != nil {
					// An `events.Event{}` returned with an error is not an event.
					if _, zero := ast.Unparen(r.Results[0]).(*ast.CompositeLit); !zero && bad == nil {
						bad = err
					}
					return
				}
				out = append(out, vals...)
			}
		})
		if bad != nil {
			return nil, bad
		}
		if len(out) == 0 {
			return nil, errNotConstant
		}
		return out, nil
	case *ast.Ident:
		if i, ok := a.param(fr, p, x); ok {
			if fr.args == nil {
				return nil, errUnbound
			}
			if i >= len(fr.args) {
				return nil, errNotConstant
			}
			return a.evalEvent(fr.args[i], fr.caller.pkg, fr.caller, depth+1)
		}
		v, ok := p.info.Uses[x].(*types.Var)
		if !ok || isGlobal(v) || len(a.locals[v]) == 0 {
			return nil, errNotConstant
		}
		var out []string
		for _, as := range a.locals[v] {
			vals, err := a.evalEvent(as.expr, as.pkg, fr, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, vals...)
		}
		return out, nil
	case *ast.SelectorExpr:
		sel := p.info.Selections[x]
		if sel == nil || sel.Kind() != types.FieldVal {
			return nil, errNotConstant
		}
		stores := a.fields[typeKey(sel.Recv())+"."+x.Sel.Name]
		var out []string
		for _, as := range stores {
			// The store sits in another function: resolve it there, binding that function's parameters at its own callers.
			got, fails := a.expand(&frame{fn: as.fn, pkg: as.pkg}, 0, func(f2 *frame) ([]emitSite, error) {
				vals, err := a.evalEvent(as.expr, as.pkg, f2, depth+1)
				var sites []emitSite
				for _, v := range vals {
					sites = append(sites, emitSite{typ: v})
				}
				return sites, err
			})
			// A store whose event cannot be traced is reported, though the other stores of the field may resolve.
			for _, u := range a.unresolved(as.expr.Pos(), as.expr, fails) {
				u.Reason = "an event stored in field " + x.Sel.Name + ": " + u.Reason
				a.report.UnresolvedRoutes = append(a.report.UnresolvedRoutes, u)
			}
			for _, s := range got {
				out = append(out, s.typ)
			}
		}
		if len(out) == 0 {
			return nil, errNotConstant
		}
		return out, nil
	}
	return nil, errNotConstant
}

// funcLit is the function literal a called func value holds: a parameter bound at the call site or a local variable,
// with the package and frame its body is evaluated in.
func (a *analyzer) funcLit(e ast.Expr, p *pkg, fr *frame, depth int) (*ast.FuncLit, *pkg, *frame, error) {
	if depth > 20 {
		return nil, nil, nil, errNotConstant
	}
	e = ast.Unparen(e)
	if lit, ok := e.(*ast.FuncLit); ok {
		return lit, p, fr, nil
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return nil, nil, nil, errNotConstant
	}
	if i, ok := a.param(fr, p, id); ok {
		if fr.args == nil {
			return nil, nil, nil, errUnbound
		}
		if i >= len(fr.args) {
			return nil, nil, nil, errNotConstant
		}
		return a.funcLit(fr.args[i], fr.caller.pkg, fr.caller, depth+1)
	}
	if v, ok := p.info.Uses[id].(*types.Var); ok && !isGlobal(v) && len(a.locals[v]) == 1 {
		return a.funcLit(a.locals[v][0].expr, p, fr, depth+1)
	}
	return nil, nil, nil, errNotConstant
}

// inspectShallow visits n without descending into function literals.
func inspectShallow(n ast.Node, visit func(ast.Node)) {
	ast.Inspect(n, func(x ast.Node) bool {
		if _, ok := x.(*ast.FuncLit); ok {
			return false
		}
		if x != nil {
			visit(x)
		}
		return true
	})
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
				routes, fails := a.resolveRoute(&frame{fn: f, pkg: f.pkg}, call.Args[2], call.Args[4], f.pkg)
				a.report.UnresolvedRoutes = append(a.report.UnresolvedRoutes, a.unresolved(call.Pos(), call, fails)...)
				for _, r := range routes {
					a.report.Aggregates = append(a.report.Aggregates, r.agg)
					rt := Route{Type: r.typ, Aggregate: r.agg, At: a.pos(call.Pos())}
					if !seen["route|"+rt.Type+rt.Aggregate+rt.At] {
						seen["route|"+rt.Type+rt.Aggregate+rt.At] = true
						a.report.Routes = append(a.report.Routes, rt)
					}
				}
				if len(fails) > 0 {
					// The aggregate alone (as before) still names a topic the connector writes.
					aggs, _ := a.resolve(&frame{fn: f, pkg: f.pkg}, call.Args[2], call.Args[2], f.pkg)
					for _, g := range aggs {
						a.report.Aggregates = append(a.report.Aggregates, g.typ)
					}
				}
				return true
			}
			if !ok || !isPkgFunc(a.callee(f.pkg, call), eventsPkg, "New") || len(call.Args) != 5 {
				return true
			}
			sites, fails := a.resolve(&frame{fn: f, pkg: f.pkg}, call.Args[0], call.Args[4], f.pkg)
			a.report.Unresolved = append(a.report.Unresolved, a.unresolved(call.Pos(), call, fails)...)
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

func (a *analyzer) unresolved(at token.Pos, call ast.Expr, fails []failure) []Unresolved {
	var out []Unresolved
	seen := map[string]bool{}
	for _, f := range fails {
		u := Unresolved{At: a.pos(at), Via: a.pos(f.via), Reason: f.reason, Text: f.text}
		if u.Text == "" {
			u.Text = types.ExprString(call)
		}
		if f.fn != nil {
			u.Func = f.fn.decl.Name.Name
		}
		if k := u.Via + u.Func + u.Reason + u.Text; !seen[k] {
			seen[k] = true
			out = append(out, u)
		}
	}
	return out
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
	if c.decode && path != "" && dynamicType(t) {
		c.fields = append(c.fields, Field{Path: path, Omitempty: omit, Dynamic: true})
		return
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

// dynamicType reports whether a decoded value of type t has no fixed shape: json.RawMessage or []byte (decoded again
// later, if at all), a map without its own UnmarshalJSON, or an interface.
func dynamicType(t types.Type) bool {
	if namedIs(t, "encoding/json", "RawMessage") {
		return true
	}
	if _, named := types.Unalias(t).(*types.Named); !named && isBytes(t) {
		return true
	}
	switch t.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Map:
		c := collector{decode: true}
		return !c.codec(t)
	}
	return false
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
