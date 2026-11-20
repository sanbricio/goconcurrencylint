package mutex

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A function can hand its caller the release of the lock it returns holding
// as a method of the lock's owner, either directly or through a field that
// caches that method value:
//
//	func (tx *Tx) grab() (*conn, func(error), error) {
//		tx.closemu.RLock()
//		return tx.dc, tx.release, nil
//	}
//	func (tx *Tx) release(error) { tx.closemu.RUnlock() }
//
//	c.once.Do(func() { c.releaseCache = c.release })
//	c.closemu.RLock()
//	return c.dc, c.releaseCache, nil
//
// The returned value releases the lock when the caller calls it, exactly like
// a returned closure that unlocks (see returnsClosureReleasingLock). Seen from
// the releasing method, the same evidence explains its lone unlock: it is
// handed out, as a method value, by a function that takes the lock it gives
// back.

// handsOutReleaseValue reports whether the function returns, beside the lock
// mutexName it holds, a value that releases it: a method value of the lock's
// owner whose body releases it, or a field every assignment of which stores
// such a method value.
func (l *lifecycleResolver) handsOutReleaseValue(mutexName string, unlockMethods []string) bool {
	if l.function == nil || l.function.Body == nil || l.typesInfo == nil {
		return false
	}
	found := false
	ast.Inspect(l.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			sel, ok := common.UnwrapParenExpr(result).(*ast.SelectorExpr)
			if !ok {
				continue
			}
			path, ok := strings.CutPrefix(mutexName, common.GetVarName(sel.X)+".")
			if !ok {
				continue
			}
			if l.releasingMethodValue(sel, path, unlockMethods) || l.fieldHoldsReleasingMethod(sel, path, unlockMethods) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// releasingMethodValue reports whether sel is a bound method of this package
// that releases the lock at path below its receiver.
func (l *lifecycleResolver) releasingMethodValue(sel *ast.SelectorExpr, path string, unlockMethods []string) bool {
	selection := l.typesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal {
		return false
	}
	method := l.declarationOf(selection.Obj())
	if method == nil || method.Body == nil {
		return false
	}
	receiver := common.ReceiverName(method)
	return receiver != "" && functionBodyContainsFieldCall(method.Body, receiver+"."+path, unlockMethods)
}

// fieldHoldsReleasingMethod reports whether sel is a field that the package
// only ever sets to a releasing method value of the same object.
func (l *lifecycleResolver) fieldHoldsReleasingMethod(sel *ast.SelectorExpr, path string, unlockMethods []string) bool {
	selection := l.typesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return false
	}
	field := selection.Obj()
	stores, ok := 0, true
	for _, fn := range l.functions {
		if fn == nil || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, isAssign := n.(*ast.AssignStmt)
			if !ok || !isAssign || len(assign.Lhs) != len(assign.Rhs) {
				return ok
			}
			for index, lhs := range assign.Lhs {
				target, isSel := common.UnwrapParenExpr(lhs).(*ast.SelectorExpr)
				if !isSel {
					continue
				}
				if s := l.typesInfo.Selections[target]; s == nil || s.Obj() != field {
					continue
				}
				value, isSel := common.UnwrapParenExpr(assign.Rhs[index]).(*ast.SelectorExpr)
				if !isSel || common.GetVarName(value.X) != common.GetVarName(target.X) ||
					!l.releasingMethodValue(value, path, unlockMethods) {
					ok = false
					return false
				}
				stores++
			}
			return true
		})
	}
	return ok && stores > 0
}

// isHandedOutReleaseMethod reports whether the method under analysis, which
// releases mutexName (a lock below its receiver) without taking it, is handed
// out as a method value by a function that takes that lock on the same
// object.
func (l *lifecycleResolver) isHandedOutReleaseMethod(mutexName string, acquireMethods []string) bool {
	fn := l.function
	if fn == nil || fn.Name == nil || l.typesInfo == nil {
		return false
	}
	path, ok := receiverRelativePath(mutexName, common.ReceiverName(fn))
	if !ok || path == "" || common.ReceiverName(fn) == "" {
		return false
	}
	method := l.typesInfo.Defs[fn.Name]
	if method == nil {
		return false
	}
	for _, bound := range l.boundSelectorsNamed(fn.Name.Name) {
		if bound.enclosing == fn {
			continue
		}
		selection := l.typesInfo.Selections[bound.sel]
		if selection == nil || selection.Kind() != types.MethodVal || selection.Obj() != method {
			continue
		}
		if functionBodyContainsFieldCall(bound.enclosing.Body, joinMutexPath(common.GetVarName(bound.sel.X), path), acquireMethods) {
			return true
		}
	}
	return false
}

// declarationOf returns the declaration of a function object of this
// package.
func (l *lifecycleResolver) declarationOf(obj types.Object) *ast.FuncDecl {
	for _, fn := range l.functions {
		if fn != nil && fn.Name != nil && l.typesInfo.Defs[fn.Name] == obj {
			return fn
		}
	}
	return nil
}
