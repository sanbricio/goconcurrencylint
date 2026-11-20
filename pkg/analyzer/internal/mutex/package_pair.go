package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A package can split the lifecycle of one of its own global locks across two
// plain functions, the same way a type splits a field's lock across two
// methods (see isMirrorPairHalf):
//
//	func acquireTable() { table.mu.Lock(); if table.m == nil { table.m = ... } }
//	func releaseTable() { table.mu.Unlock() }
//
//	acquireTable()
//	defer releaseTable()
//
// A package-level variable is the same lock in every function of the package,
// so the two halves need no receiver to agree on it: both name it, and the
// type checker says it is the same object. The rest of the evidence is the
// method rule unchanged: this half touches the lock once and unconditionally,
// another function of the package performs the opposite operation on the same
// lock, and either this half does nothing but the lock call and nobody calls
// it, or some caller really uses the two together in order.

// isPackageLockPairHalf reports whether the lock operation under analysis, in a
// function without a receiver, is one half of such a pair.
func (w *wrapperResolver) isPackageLockPairHalf(varName, methodName string, oppositeMethods []string) bool {
	if w.lifecycle == nil || w.typesInfo == nil || w.function == nil || w.function.Name == nil || w.function.Recv != nil {
		return false
	}

	ref, ok := packageLockRefFor(w.typesInfo, w.function, varName, methodName)
	if !ok {
		return false
	}
	if !ref.callsUnconditionally(w.function, []string{methodName}) {
		return false
	}

	self := w.typesInfo.Defs[w.function.Name]
	if self == nil {
		return false
	}

	sameSide := mutexMethodGroup(methodName)
	var siblings []types.Object
	for _, fn := range w.lifecycle.functions {
		if fn == nil || fn == w.function || fn.Recv != nil || fn.Name == nil || fn.Body == nil {
			continue
		}
		if !ref.callsUnconditionally(fn, oppositeMethods) || ref.callsUnconditionally(fn, sameSide) {
			continue
		}
		if obj := w.typesInfo.Defs[fn.Name]; obj != nil {
			siblings = append(siblings, obj)
		}
	}
	if len(siblings) == 0 {
		return false
	}

	calledInPackage := false
	for _, fn := range w.lifecycle.functions {
		if fn != w.function && len(callsOfFunction(w.typesInfo, fn, self)) > 0 {
			calledInPackage = true
			break
		}
	}
	if !calledInPackage && blockIsPureLockAdapter(w.function.Body, ref.lockRef(), "", []string{methodName}) {
		return true
	}

	acquiresFirst := lockMethodSide(methodName) == acquireLockSide
	for _, sibling := range siblings {
		if w.lifecycle.callersPairFunctions(w.typesInfo, self, sibling, acquiresFirst, w.function) {
			return true
		}
	}

	return false
}

// packageLockRef names a lock rooted at a package-level variable: the spelled
// path the analysis tracks it by, plus the variable it hangs off, so a local
// that happens to share the spelling in another function is not mistaken for
// it.
type packageLockRef struct {
	name string
	root types.Object
	info *types.Info
}

// packageLockRefFor finds the call `<varName>.<methodName>()` in fn and returns
// the lock it operates on when its root is a variable declared at package
// level.
func packageLockRefFor(info *types.Info, fn *ast.FuncDecl, varName, methodName string) (packageLockRef, bool) {
	var root types.Object
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if root != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != methodName || common.GetVarName(sel.X) != varName {
			return true
		}
		root = packageLevelRoot(info, sel.X)
		return root == nil
	})
	if root == nil {
		return packageLockRef{}, false
	}
	return packageLockRef{name: varName, root: root, info: info}, true
}

// packageLevelRoot returns the variable at the root of a selector chain when it
// is declared in its package's scope.
func packageLevelRoot(info *types.Info, expr ast.Expr) types.Object {
	for {
		switch node := common.UnwrapParenExpr(expr).(type) {
		case *ast.SelectorExpr:
			expr = node.X
		case *ast.Ident:
			v, ok := info.Uses[node].(*types.Var)
			if !ok || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
				return nil
			}
			return v
		default:
			return nil
		}
	}
}

// matches reports whether call performs one of methods on this lock.
func (r packageLockRef) matches(call *ast.CallExpr, methods []string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !containsMethod(methods, sel.Sel.Name) || common.GetVarName(sel.X) != r.name {
		return false
	}
	return packageLevelRoot(r.info, sel.X) == r.root
}

// callsUnconditionally is bodyCallsUnconditionally for a package-level lock.
func (r packageLockRef) callsUnconditionally(fn *ast.FuncDecl, methods []string) bool {
	if fn == nil || fn.Body == nil {
		return false
	}

	total, conditional := 0, 0
	var stack []ast.Node
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok || !r.matches(call, methods) {
			return true
		}
		total++
		if pathIsConditional(stack) {
			conditional++
		}
		return true
	})

	return total > 0 && conditional == 0
}

// lockRef is the exact-spelling form blockIsPureLockAdapter reads. The root
// check is redundant there: it only runs on the function the ref came from.
func (r packageLockRef) lockRef() lockRef {
	return lockRef{exact: r.name}
}

// callsOfFunction returns the calls in fn of the package function obj.
func callsOfFunction(info *types.Info, fn *ast.FuncDecl, obj types.Object) []*ast.CallExpr {
	if fn == nil || fn.Body == nil {
		return nil
	}

	var calls []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := common.UnwrapParenExpr(call.Fun).(*ast.Ident); ok && info.Uses[ident] == obj {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// callersPairFunctions reports whether some function of the package other than
// exclude calls first and then second (or second and then first, when
// acquiresFirst is false: the function under analysis is the releasing half
// and its counterpart must come before it).
func (l *lifecycleResolver) callersPairFunctions(info *types.Info, first, second types.Object, acquiresFirst bool, exclude *ast.FuncDecl) bool {
	for _, fn := range l.functions {
		if fn == exclude {
			continue
		}
		seconds := callsOfFunction(info, fn, second)
		if len(seconds) == 0 {
			continue
		}
		for _, call := range callsOfFunction(info, fn, first) {
			if anyCallOnSide(seconds, call.Pos(), acquiresFirst) {
				return true
			}
		}
	}
	return false
}

func anyCallOnSide(calls []*ast.CallExpr, pos token.Pos, after bool) bool {
	for _, call := range calls {
		if after == (call.Pos() > pos) {
			return true
		}
	}
	return false
}
