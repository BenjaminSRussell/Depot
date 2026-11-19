package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

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
