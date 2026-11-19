package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

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
