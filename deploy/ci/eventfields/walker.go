package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"
)

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
