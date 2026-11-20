package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A function can take a lock it is handed as plain function values instead of
// naming it, so one implementation serves any lock and either of its modes:
//
//	func waitLock(ctx context.Context, tryLock func() bool, lock, unlock func()) error {
//		if tryLock() { return nil }
//		... lock() in a goroutine; on ctx.Done(), unlock() once it lands ...
//	}
//
//	if err := waitLock(ctx, mu.TryRLock, mu.RLock, mu.RUnlock); err != nil {
//		return err
//	}
//	defer mu.RUnlock()
//
// Read at the call, the bound methods say which lock and which mode; read in
// the callee, the parameters they land in say what happens to it. The callee
// keeps the acquire-or-fail contract for that lock when its last result
// reports success and:
//
//   - it calls every parameter that receives an acquiring method of the lock,
//     and does nothing else with it;
//   - every call of a parameter that receives a releasing method is reached
//     only under a condition of its own function (an `if` arm, a case), never
//     on a straight path, so no successful return can have given the lock
//     back; and it does nothing else with that parameter either.
//
// A callee that takes the lock and gives it back on its way out (`lock();
// defer unlock(); return fn()`) fails the second rule. One that releases in
// both arms of an `if` would pass it, so the shape is never enough alone: the
// lock is only counted as held after the call when the caller itself releases
// it later, on its own path (see acquiresThroughParamsAt), or, for a helper
// that passes the call's result on, when its own callers take the lock over
// (see handsLockToCaller).

// paramRole is what a callee does with one of its parameters.
type paramRole struct {
	called bool
	// conditional: every call is reached only under a condition of its own
	// function.
	conditional bool
	// escapes: the parameter is used other than by calling it.
	escapes bool
}

// callAcquiresThroughParams reports whether call passes bound acquiring
// methods of mutexName (in pattern's mode) to a function of this package that
// takes the lock through them on success, and how that function reports
// success.
func (l *lifecycleResolver) callAcquiresThroughParams(call *ast.CallExpr, mutexName string, pattern LockPattern) (successResult, bool) {
	if l.typesInfo == nil || call.Ellipsis != token.NoPos {
		return noSuccessResult, false
	}
	callee := l.calledFuncDecl(call)
	if callee == nil || callee.Body == nil || callee.Type.Params == nil {
		return noSuccessResult, false
	}
	kind := l.successResultOf(callee)
	if kind == noSuccessResult {
		return noSuccessResult, false
	}

	params := parameterObjects(l.typesInfo, callee)
	if len(params) != len(call.Args) {
		return noSuccessResult, false
	}

	var acquires, releases []types.Object
	for index, arg := range call.Args {
		sel, ok := common.UnwrapParenExpr(arg).(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if selection := l.typesInfo.Selections[sel]; selection == nil || selection.Kind() != types.MethodVal {
			continue
		}
		if l.lockNameOf(sel.X) != mutexName {
			continue
		}
		switch {
		case slices.Contains(pattern.LockMethods, sel.Sel.Name):
			acquires = append(acquires, params[index])
		case slices.Contains(pattern.UnlockMethods, sel.Sel.Name):
			releases = append(releases, params[index])
		case slices.Contains(anyAcquireMethods, sel.Sel.Name) || slices.Contains(anyReleaseMethods, sel.Sel.Name):
			// The other mode of the same lock: this contract says nothing
			// about how the two interact.
			return noSuccessResult, false
		}
	}
	if len(acquires) == 0 {
		return noSuccessResult, false
	}

	roles := l.paramRoles(callee)
	for _, param := range acquires {
		role := roles[param]
		if param == nil || !role.called || role.escapes {
			return noSuccessResult, false
		}
	}
	for _, param := range releases {
		role := roles[param]
		if param == nil || role.escapes || (role.called && !role.conditional) {
			return noSuccessResult, false
		}
	}
	return kind, true
}

// acquiresThroughParamsAt reports whether call, in caller, takes mutexName
// through its bound methods (see callAcquiresThroughParams) and the caller
// releases the lock later on its own path, which confirms that it expects to
// hold it after a successful call.
func (l *lifecycleResolver) acquiresThroughParamsAt(caller *ast.FuncDecl, call *ast.CallExpr, mutexName string, pattern LockPattern) (successResult, bool) {
	kind, ok := l.callAcquiresThroughParams(call, mutexName, pattern)
	if !ok || caller == nil || !releasedLaterOnOwnPath(caller.Body, call.End(), mutexName, pattern.UnlockMethods) {
		return noSuccessResult, false
	}
	return kind, true
}

// releasedLaterOnOwnPath reports whether body releases mutexName after pos
// outside any function literal: a release inside a closure runs whenever the
// closure is called, which the caller's flow does not see.
func releasedLaterOnOwnPath(body *ast.BlockStmt, pos token.Pos, mutexName string, unlockMethods []string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if node.Pos() <= pos {
				return true
			}
			sel, ok := common.UnwrapParenExpr(node.Fun).(*ast.SelectorExpr)
			if ok && slices.Contains(unlockMethods, sel.Sel.Name) && common.GetVarName(sel.X) == mutexName {
				found = true
			}
		}
		return !found
	})
	return found
}

// parameterObjects returns the callee's parameters in order, nil for a blank
// or unnamed one.
func parameterObjects(info *types.Info, fn *ast.FuncDecl) []types.Object {
	var params []types.Object
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			params = append(params, nil)
			continue
		}
		for _, name := range field.Names {
			params = append(params, info.Defs[name])
		}
	}
	return params
}

// paramRoles reads what fn does with each of its parameters.
func (l *lifecycleResolver) paramRoles(fn *ast.FuncDecl) map[types.Object]paramRole {
	if cached, ok := l.scanCache.paramRoles[fn]; ok {
		return cached
	}

	params := make(map[types.Object]bool)
	for _, param := range parameterObjects(l.typesInfo, fn) {
		if param != nil {
			params[param] = true
		}
	}

	roles := make(map[types.Object]paramRole)
	unconditional := make(map[types.Object]bool)
	calledIdents := make(map[*ast.Ident]bool)
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		switch node := n.(type) {
		case *ast.CallExpr:
			ident, ok := common.UnwrapParenExpr(node.Fun).(*ast.Ident)
			if !ok {
				return true
			}
			obj := l.typesInfo.Uses[ident]
			if !params[obj] {
				return true
			}
			calledIdents[ident] = true
			role := roles[obj]
			role.called = true
			roles[obj] = role
			if !pathIsConditional(stack[innermostFuncLit(stack):]) {
				unconditional[obj] = true
			}
		case *ast.Ident:
			obj := l.typesInfo.Uses[node]
			if params[obj] && !calledIdents[node] {
				role := roles[obj]
				role.escapes = true
				roles[obj] = role
			}
		}
		return true
	})
	for obj, role := range roles {
		role.conditional = role.called && !unconditional[obj]
		roles[obj] = role
	}

	if l.scanCache.paramRoles == nil {
		l.scanCache.paramRoles = make(map[*ast.FuncDecl]map[types.Object]paramRole)
	}
	l.scanCache.paramRoles[fn] = roles
	return roles
}

// innermostFuncLit returns the index in stack just below the innermost
// function literal, or 0 when there is none: a condition is read within the
// function the node belongs to.
func innermostFuncLit(stack []ast.Node) int {
	for index, node := range slices.Backward(stack) {
		if _, ok := node.(*ast.FuncLit); ok {
			return index + 1
		}
	}
	return 0
}

// lockNameOf names the lock expr stands for, seeing through a local variable
// that is set once to it and never reassigned (`mu := &g.mu`).
func (l *lifecycleResolver) lockNameOf(expr ast.Expr) string {
	name := common.GetVarName(expr)
	ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
	if !ok || l.typesInfo == nil {
		return name
	}
	obj, ok := l.typesInfo.Uses[ident].(*types.Var)
	if !ok || obj.Pkg() == nil || obj.Parent() == obj.Pkg().Scope() || obj.IsField() {
		return name
	}
	fn := l.functionAt(obj.Pos())
	if fn == nil {
		return name
	}

	var target ast.Expr
	assignments := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || (l.typesInfo.Defs[id] != obj && l.typesInfo.Uses[id] != obj) {
				continue
			}
			assignments++
			if assign.Tok == token.DEFINE && len(assign.Lhs) == len(assign.Rhs) {
				target = assign.Rhs[index]
			}
		}
		return true
	})
	if assignments != 1 || target == nil {
		return name
	}
	if unary, ok := common.UnwrapParenExpr(target).(*ast.UnaryExpr); ok && unary.Op == token.AND {
		target = unary.X
	}
	if resolved := common.GetVarName(target); resolved != "?" {
		return resolved
	}
	return name
}

// functionAt returns the package function whose body contains pos.
func (l *lifecycleResolver) functionAt(pos token.Pos) *ast.FuncDecl {
	for _, fn := range l.functions {
		if fn != nil && fn.Body != nil && fn.Body.Pos() <= pos && pos < fn.Body.End() {
			return fn
		}
	}
	return nil
}

// passesBoundAcquire reports whether body calls a function that takes
// mutexName through bound methods of it (see callAcquiresThroughParams).
func (l *lifecycleResolver) passesBoundAcquire(body *ast.BlockStmt, mutexName string, pattern LockPattern) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			_, found = l.callAcquiresThroughParams(call, mutexName, pattern)
		}
		return !found
	})
	return found
}
