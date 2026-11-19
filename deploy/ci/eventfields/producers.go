package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
)

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
