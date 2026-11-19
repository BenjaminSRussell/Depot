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
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
