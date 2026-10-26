package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A refined run (see refine.go) knows more than the lock counters: it follows
// the value of the local booleans and nilable variables a function keeps beside
// its locks, the way a reader does.
//
//	held := false
//	if !ok { mu.Lock(); held = true }
//	...
//	if held { mu.Unlock() }
//
// A fact says what one such variable holds at the current point: true or false
// for a bool, non-nil or nil for a pointer, interface, map, channel, function
// or slice. A condition built from known facts has a known value, and the run
// then follows only the branch that executes.
//
// Facts are only kept for variables nothing can change behind the walker's back:
// locals and parameters of the function whose address is never taken and that no
// closure assigns. Everything else is simply unknown.

// factSet maps a tracked variable to its known value: true for true / non-nil,
// false for false / nil. A variable that is absent is unknown.
type factSet map[types.Object]bool

func (f factSet) clone() factSet {
	out := make(factSet, len(f))
	maps.Copy(out, f)
	return out
}

// intersectFacts keeps what both paths agree on.
func intersectFacts(a, b factSet) factSet {
	out := make(factSet)
	for obj, value := range a {
		if other, ok := b[obj]; ok && other == value {
			out[obj] = value
		}
	}
	return out
}

// varScope classifies the variables a function declares (see computeVarScope).
type varScope struct {
	// trackable variables can carry facts.
	trackable map[types.Object]bool
	// invariant variables are trackable ones that take a single value per call:
	// parameters never assigned, and locals assigned only where they are
	// declared, outside any loop or closure. A refined run may assume either
	// value for them up front (see refine.go).
	invariant map[types.Object]bool
}

// computeVarScope walks fn once and classifies the variables it declares.
func computeVarScope(fn *ast.FuncDecl, info *types.Info) *varScope {
	scope := &varScope{trackable: map[types.Object]bool{}, invariant: map[types.Object]bool{}}
	if fn == nil || fn.Body == nil || info == nil {
		return scope
	}

	type declaration struct {
		lit        *ast.FuncLit
		repeated   bool
		assigns    int
		isParam    bool
		disallowed bool
	}
	decls := make(map[types.Object]*declaration)

	declare := func(ident *ast.Ident, lit *ast.FuncLit, repeated, isParam bool) {
		if ident == nil || ident.Name == "_" {
			return
		}
		obj := info.Defs[ident]
		if obj == nil {
			return
		}
		d := &declaration{lit: lit, repeated: repeated, isParam: isParam}
		if !isParam {
			d.assigns = 1
		}
		decls[obj] = d
	}
	assign := func(expr ast.Expr, lit *ast.FuncLit) {
		ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
		if !ok || ident.Name == "_" {
			return
		}
		d := decls[info.ObjectOf(ident)]
		if d == nil {
			return
		}
		d.assigns++
		if d.lit != lit {
			d.disallowed = true
		}
	}
	disallow := func(expr ast.Expr) {
		if ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident); ok {
			if d := decls[info.ObjectOf(ident)]; d != nil {
				d.disallowed = true
			}
		}
	}

	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				declare(name, nil, false, true)
			}
		}
	}

	var walk func(n ast.Node, lit *ast.FuncLit, loopDepth int)
	walk = func(n ast.Node, lit *ast.FuncLit, loopDepth int) {
		ast.Inspect(n, func(node ast.Node) bool {
			if node == nil || node == n {
				return true
			}
			repeated := lit != nil || loopDepth > 0
			switch x := node.(type) {
			case *ast.FuncLit:
				if x.Type.Params != nil {
					for _, field := range x.Type.Params.List {
						for _, name := range field.Names {
							declare(name, x, true, true)
						}
					}
				}
				walk(x.Body, x, 0)
				return false
			case *ast.ForStmt:
				if x.Init != nil {
					walk(x.Init, lit, loopDepth)
				}
				if x.Cond != nil {
					walk(x.Cond, lit, loopDepth+1)
				}
				if x.Post != nil {
					walk(x.Post, lit, loopDepth+1)
				}
				walk(x.Body, lit, loopDepth+1)
				return false
			case *ast.RangeStmt:
				for _, expr := range []ast.Expr{x.Key, x.Value} {
					if expr == nil {
						continue
					}
					if ident, ok := expr.(*ast.Ident); ok && x.Tok == token.DEFINE {
						declare(ident, lit, true, false)
					} else {
						assign(expr, lit)
					}
				}
				walk(x.X, lit, loopDepth)
				walk(x.Body, lit, loopDepth+1)
				return false
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if ok && x.Tok == token.DEFINE && info.Defs[ident] != nil {
						declare(ident, lit, repeated, false)
						continue
					}
					assign(lhs, lit)
				}
			case *ast.ValueSpec:
				for _, name := range x.Names {
					declare(name, lit, repeated, false)
				}
			case *ast.IncDecStmt:
				assign(x.X, lit)
			case *ast.UnaryExpr:
				if x.Op == token.AND {
					disallow(x.X)
				}
			case *ast.SelectorExpr:
				// A pointer method called on an addressable value takes its address.
				if sel, ok := info.Selections[x]; ok && sel.Kind() == types.MethodVal {
					if sig, ok := sel.Obj().Type().(*types.Signature); ok && sig.Recv() != nil {
						if _, ptrRecv := sig.Recv().Type().(*types.Pointer); ptrRecv {
							disallow(x.X)
						}
					}
				}
			}
			return true
		})
	}
	walk(fn.Body, nil, 0)

	for obj, d := range decls {
		if d.disallowed || !factTypeTracked(obj.Type()) {
			continue
		}
		scope.trackable[obj] = true
		single := (d.isParam && d.assigns == 0) || (!d.isParam && d.assigns == 1)
		if single && !d.repeated && d.lit == nil {
			scope.invariant[obj] = true
		}
	}
	return scope
}

// factTypeTracked reports whether a value of typ has a truth a fact can hold.
func factTypeTracked(typ types.Type) bool {
	if typ == nil {
		return false
	}
	switch u := typ.Underlying().(type) {
	case *types.Basic:
		return u.Info()&types.IsBoolean != 0
	case *types.Pointer, *types.Interface, *types.Map, *types.Chan, *types.Signature, *types.Slice:
		return true
	default:
		return false
	}
}

// trackedObject returns the tracked variable expr names, if any.
func (c *Checker) trackedObject(expr ast.Expr) types.Object {
	if c.refine == nil || c.typesInfo == nil {
		return nil
	}
	ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
	if !ok {
		return nil
	}
	obj := c.typesInfo.ObjectOf(ident)
	if obj == nil || !c.refine.scopeOf(c.function, c.typesInfo).trackable[obj] {
		return nil
	}
	return obj
}

// evalCondition returns the value cond has at this point when the run knows
// it: a constant, a combination of known facts, or a TryLock on a lock this
// goroutine already holds, which cannot succeed.
func (c *Checker) evalCondition(cond ast.Expr, stats map[string]*Stats) (bool, bool) {
	if value, ok := common.ConstantBoolValue(cond, c.typesInfo); ok {
		return value, true
	}
	if c.refine == nil {
		return false, false
	}

	switch e := common.UnwrapParenExpr(cond).(type) {
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			value, ok := c.evalCondition(e.X, stats)
			return !value, ok
		}
	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND, token.LOR:
			// The value that decides the operator alone, whichever side has it.
			decisive := e.Op == token.LOR
			left, leftOK := c.evalCondition(e.X, stats)
			if leftOK && left == decisive {
				return decisive, true
			}
			right, rightOK := c.evalCondition(e.Y, stats)
			if rightOK && right == decisive {
				return decisive, true
			}
			if leftOK && rightOK {
				return !decisive, true
			}
		case token.EQL, token.NEQ:
			if obj := c.nilComparedObject(e); obj != nil {
				if nonNil, ok := c.facts[obj]; ok {
					return nonNil == (e.Op == token.NEQ), true
				}
			}
		}
	case *ast.Ident:
		if obj := c.trackedObject(e); obj != nil {
			value, ok := c.facts[obj]
			return value, ok
		}
	case *ast.CallExpr:
		if _, varName, method, ok := c.tryLockConditionTarget(e); ok && stats != nil {
			// sync mutexes are not reentrant: a lock this goroutine holds makes
			// TryLock fail, and a write lock makes TryRLock fail. Holding nothing
			// proves nothing — another goroutine may hold it.
			if st := stats[varName]; st != nil {
				if st.lock > 0 || (method == "TryLock" && st.rlock > 0) {
					return false, true
				}
			}
		}
	}
	return false, false
}

// nilComparedObject returns the tracked variable of `x == nil` / `x != nil`.
func (c *Checker) nilComparedObject(e *ast.BinaryExpr) types.Object {
	switch {
	case isNilIdent(common.UnwrapParenExpr(e.Y)):
		return c.trackedObject(e.X)
	case isNilIdent(common.UnwrapParenExpr(e.X)):
		return c.trackedObject(e.Y)
	default:
		return nil
	}
}

// assumeCondition records the facts that hold once cond evaluated to truth,
// as on entry to the branch that runs then.
func (c *Checker) assumeCondition(cond ast.Expr, truth bool) {
	if c.refine == nil {
		return
	}
	switch e := common.UnwrapParenExpr(cond).(type) {
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			c.assumeCondition(e.X, !truth)
		}
	case *ast.BinaryExpr:
		switch {
		case e.Op == token.LAND && truth, e.Op == token.LOR && !truth:
			c.assumeCondition(e.X, truth)
			c.assumeCondition(e.Y, truth)
		case e.Op == token.EQL || e.Op == token.NEQ:
			if obj := c.nilComparedObject(e); obj != nil {
				c.facts[obj] = truth == (e.Op == token.NEQ)
			}
		}
	case *ast.Ident:
		if obj := c.trackedObject(e); obj != nil {
			c.facts[obj] = truth
		}
	}
}

// valueOf returns the truth expr is known to have: a known condition, nil, or
// an expression that always yields a non-nil value.
func (c *Checker) valueOf(expr ast.Expr) (bool, bool) {
	switch e := common.UnwrapParenExpr(expr).(type) {
	case *ast.Ident:
		if isNilIdent(e) {
			return false, true
		}
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return true, true
		}
	case *ast.CompositeLit, *ast.FuncLit:
		return true, true
	case *ast.CallExpr:
		if ident, ok := common.UnwrapParenExpr(e.Fun).(*ast.Ident); ok && c.typesInfo != nil {
			if builtin, ok := c.typesInfo.ObjectOf(ident).(*types.Builtin); ok && (builtin.Name() == "new" || builtin.Name() == "make") {
				return true, true
			}
		}
	}
	return c.evalCondition(expr, nil)
}

// applyAssignmentFacts updates the facts of the variables stmt assigns.
func (c *Checker) applyAssignmentFacts(stmt *ast.AssignStmt) {
	if c.refine == nil {
		return
	}
	pairwise := len(stmt.Lhs) == len(stmt.Rhs) && (stmt.Tok == token.ASSIGN || stmt.Tok == token.DEFINE)
	values := make([]*bool, len(stmt.Lhs))
	if pairwise {
		for i, rhs := range stmt.Rhs {
			if value, ok := c.valueOf(rhs); ok {
				values[i] = &value
			}
		}
	}
	for i, lhs := range stmt.Lhs {
		c.setFact(lhs, values[i])
	}
}

// applyDeclarationFacts updates the facts of the variables a var declaration
// introduces: the zero value when it has no initializer.
func (c *Checker) applyDeclarationFacts(stmt *ast.DeclStmt) {
	if c.refine == nil {
		return
	}
	gen, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return
	}
	for _, spec := range gen.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			var value *bool
			switch {
			case len(vs.Values) == 0:
				zero := false
				value = &zero
			case len(vs.Values) == len(vs.Names):
				if v, ok := c.valueOf(vs.Values[i]); ok {
					value = &v
				}
			}
			c.setFact(name, value)
		}
	}
}

// setFact records value for the variable lhs names, or forgets it when value
// is nil. An invariant variable the run assumes keeps its assumed value.
func (c *Checker) setFact(lhs ast.Expr, value *bool) {
	obj := c.trackedObject(lhs)
	if obj == nil {
		return
	}
	if assumed, ok := c.refine.vars[obj]; ok {
		c.facts[obj] = assumed
		return
	}
	if value == nil {
		delete(c.facts, obj)
		return
	}
	c.facts[obj] = *value
}

// forgetFactsAssignedIn drops the facts of every variable n may assign: a loop
// body runs any number of times, so nothing it assigns is known at its top.
func (c *Checker) forgetFactsAssignedIn(n ast.Node) {
	if c.refine == nil || n == nil || len(c.facts) == 0 {
		return
	}
	forget := func(expr ast.Expr) {
		if obj := c.trackedObject(expr); obj != nil {
			delete(c.facts, obj)
		}
	}
	ast.Inspect(n, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				forget(lhs)
			}
		case *ast.ValueSpec:
			for _, name := range x.Names {
				forget(name)
			}
		case *ast.RangeStmt:
			forget(x.Key)
			forget(x.Value)
		}
		return true
	})
}

// invariantFacts returns the facts about invariant variables only: the ones
// still true when the function returns, which is when a deferred call runs.
func (c *Checker) invariantFacts() factSet {
	if c.refine == nil {
		return nil
	}
	invariant := c.refine.scopeOf(c.function, c.typesInfo).invariant
	out := make(factSet)
	for obj, value := range c.facts {
		if invariant[obj] {
			out[obj] = value
		}
	}
	return out
}

// liveCallCount counts the calls of method on varName in body that can run
// under facts: a branch whose condition the facts decide against is skipped.
func (c *Checker) liveCallCount(body *ast.BlockStmt, varName, method string, facts factSet) int {
	saved := c.facts
	c.facts = facts
	defer func() { c.facts = saved }()

	count := 0
	var visit func(n ast.Node)
	visit = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.FuncLit:
				return false
			case *ast.IfStmt:
				if x.Init != nil {
					visit(x.Init)
				}
				value, known := c.evalCondition(x.Cond, nil)
				if !known || value {
					visit(x.Body)
				}
				if x.Else != nil && (!known || !value) {
					visit(x.Else)
				}
				return false
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method && common.GetVarName(sel.X) == varName {
					count++
				}
			}
			return true
		})
	}
	visit(body)
	return count
}

// callArgumentFacts returns the facts a call establishes for the invariant
// parameters of callee: removeMsg(seq, false) runs its body with needLock false.
func (c *Checker) callArgumentFacts(call *ast.CallExpr, callee *ast.FuncDecl) factSet {
	seed := make(factSet)
	if c.refine == nil || call == nil || callee == nil || callee.Type.Params == nil || call.Ellipsis.IsValid() {
		return seed
	}
	invariant := c.refine.scopeOf(callee, c.typesInfo).invariant
	index := 0
	for _, field := range callee.Type.Params.List {
		if _, variadic := field.Type.(*ast.Ellipsis); variadic {
			break
		}
		names := field.Names
		if len(names) == 0 {
			index++
			continue
		}
		for _, name := range names {
			if index >= len(call.Args) {
				return seed
			}
			if obj := c.typesInfo.Defs[name]; obj != nil && invariant[obj] {
				if value, ok := c.valueOf(call.Args[index]); ok {
					seed[obj] = value
				}
			}
			index++
		}
	}
	return seed
}
