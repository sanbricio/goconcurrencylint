package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// onceReleaseTarget recognises `once.Do(mu.Unlock)`: the release is delegated
// to a sync.Once, as returnsClosureReleasingLock covers delegating it to a
// returned closure. Reports the mutex and whether it is a read unlock.
func (c *Checker) onceReleaseTarget(call *ast.CallExpr) (string, bool, bool) {
	if call == nil || len(call.Args) != 1 {
		return "", false, false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" || !c.isOnceReceiver(sel.X) {
		return "", false, false
	}

	release, ok := common.UnwrapParenExpr(call.Args[0]).(*ast.SelectorExpr)
	if !ok {
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
// Do on the same lock is already credited, this call cannot release anything
// twice: whichever site runs first fires the Once and the other becomes a
// no-op, so counting it again would report an unlock that never happens.
func (c *Checker) applyOnceRelease(call *ast.CallExpr, stats map[string]*Stats) bool {
	varName, isReadUnlock, ok := c.onceReleaseTarget(call)
	if !ok {
		return false
	}

	st := stats[varName]
	if st == nil {
		return false
	}

	if isReadUnlock {
		if st.deferRUnlock == 0 {
			c.handleRWMutexCall(varName, "RUnlock", call.Pos(), stats)
		}
		return true
	}

	if st.deferUnlock > 0 {
		return true
	}
	if c.rwMutexNames[varName] {
		c.handleRWMutexCall(varName, "Unlock", call.Pos(), stats)
		return true
	}
	c.handleMutexCall(varName, "Unlock", call.Pos(), stats)
	return true
}
