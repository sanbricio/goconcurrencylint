package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// onceReleaseTarget recognises `once.Do(mu.Unlock)`, and a call of a local
// bound once to `sync.OnceFunc(mu.Unlock)`: the release is delegated to a
// sync.Once, as returnsClosureReleasingLock covers delegating it to a returned
// closure. Reports the mutex and whether it is a read unlock.
func (c *Checker) onceReleaseTarget(call *ast.CallExpr) (string, bool, bool) {
	if call == nil {
		return "", false, false
	}
	if ident, ok := common.UnwrapParenExpr(call.Fun).(*ast.Ident); ok && len(call.Args) == 0 {
		return c.releaseMethodTarget(c.onceFuncRelease(ident))
	}
	if len(call.Args) != 1 {
		return "", false, false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" || !c.isOnceReceiver(sel.X) {
		return "", false, false
	}

	release, _ := common.UnwrapParenExpr(call.Args[0]).(*ast.SelectorExpr)
	return c.releaseMethodTarget(release)
}

// releaseMethodTarget reports the tracked mutex an unlock method value such as
// mu.Unlock releases, and whether it is a read unlock.
func (c *Checker) releaseMethodTarget(release *ast.SelectorExpr) (string, bool, bool) {
	if release == nil {
		return "", false, false
	}
	varName := common.GetVarName(release.X)
	switch release.Sel.Name {
	case "Unlock":
		if c.mutexNames[varName] || c.rwMutexNames[varName] {
			return varName, false, true
		}
	case "RUnlock":
		if c.rwMutexNames[varName] {
			return varName, true, true
		}
	}
	return "", false, false
}

// onceFuncRelease returns the method value m in `f := sync.OnceFunc(m)` when
// ident names such a local f, assigned nowhere else in the function.
func (c *Checker) onceFuncRelease(ident *ast.Ident) *ast.SelectorExpr {
	if c.typesInfo == nil || c.function == nil || c.function.Body == nil {
		return nil
	}
	obj := c.typesInfo.Uses[ident]
	if obj == nil {
		return nil
	}
	var release *ast.SelectorExpr
	assignments := 0
	bind := func(name *ast.Ident, value ast.Expr) {
		if c.typesInfo.ObjectOf(name) != obj {
			return
		}
		assignments++
		call, ok := common.UnwrapParenExpr(value).(*ast.CallExpr)
		if !ok || len(call.Args) != 1 || !isSyncFunc(call.Fun, "OnceFunc", c.typesInfo) {
			return
		}
		release, _ = common.UnwrapParenExpr(call.Args[0]).(*ast.SelectorExpr)
	}
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				if name, ok := lhs.(*ast.Ident); ok && i < len(x.Rhs) {
					bind(name, x.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if i < len(x.Values) {
					bind(name, x.Values[i])
				}
			}
		}
		return true
	})
	if assignments != 1 {
		return nil
	}
	return release
}

// isSyncFunc reports whether fun names the function name of package sync.
func isSyncFunc(fun ast.Expr, name string, info *types.Info) bool {
	sel, ok := common.UnwrapParenExpr(fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "sync"
}

// isOnceReceiver reports whether x is a sync.Once. Without type information it
// answers true so the surrounding guard keeps suppressing; the method value
// argument still has to name a tracked mutex for the idiom to match.
func (c *Checker) isOnceReceiver(x ast.Expr) bool {
	if c.typesInfo == nil {
		return true
	}
	return common.IsOnce(c.typesInfo.TypeOf(x))
}

// applyDeferredOnceRelease credits `defer once.Do(mu.Unlock)` as the deferred
// unlock it performs on return.
func (c *Checker) applyDeferredOnceRelease(stmt *ast.DeferStmt, stats map[string]*Stats) bool {
	varName, isReadUnlock, ok := c.onceReleaseTarget(stmt.Call)
	if !ok {
		return false
	}

	if isReadUnlock {
		c.handleDeferRUnlock(varName, stmt.Pos(), stats)
		return true
	}
	c.handleDeferUnlock(varName, stmt.Pos(), stats, c.rwMutexNames[varName])
	return true
}

// applyOnceRelease handles a plain `once.Do(mu.Unlock)` call. When a deferred
// Do on the same lock is already credited, the lock is not released twice:
// this call runs first, fires the Once and releases it, and the deferred Do
// becomes a no-op.
func (c *Checker) applyOnceRelease(call *ast.CallExpr, stats map[string]*Stats) bool {
	varName, isReadUnlock, ok := c.onceReleaseTarget(call)
	if !ok {
		return false
	}

	st := stats[varName]
	if st == nil {
		return false
	}

	// With a deferred Do already credited, this earlier call is the one that
	// fires the Once: the lock is released now and the deferred one does
	// nothing on return.
	if isReadUnlock {
		if st.deferRUnlock == 0 {
			c.handleRWMutexCall(varName, "RUnlock", call.Pos(), stats)
		} else if st.rlock > 0 {
			st.rlock--
			st.removeFirstRLockPos()
			st.deferRUnlock--
		}
		return true
	}

	if st.deferUnlock > 0 {
		if st.lock > 0 {
			st.lock--
			st.removeFirstLockPos()
			st.deferUnlock--
		}
		return true
	}
	if c.rwMutexNames[varName] {
		c.handleRWMutexCall(varName, "Unlock", call.Pos(), stats)
		return true
	}
	c.handleMutexCall(varName, "Unlock", call.Pos(), stats)
	return true
}

// handleDeferredOnceReleases finds once.Do(mu.Unlock) inside a deferred local
// closure. The bound method is an argument rather than a CallExpr, so the
// ordinary deferred-closure scan cannot see it.
func (c *Checker) handleDeferredOnceReleases(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	if fnlit == nil || fnlit.Body == nil {
		return
	}
	ast.Inspect(fnlit.Body, func(n ast.Node) bool {
		if nested, ok := n.(*ast.FuncLit); ok && nested != fnlit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		varName, isReadUnlock, ok := c.onceReleaseTarget(call)
		if !ok {
			return true
		}
		if isReadUnlock {
			c.handleDeferRUnlock(varName, pos, stats)
		} else {
			c.handleDeferUnlock(varName, pos, stats, c.rwMutexNames[varName])
		}
		return true
	})
}
