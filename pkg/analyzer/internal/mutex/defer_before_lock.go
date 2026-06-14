package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// detectSafeDeferBeforeLock finds deferred unlocks that are immediately
// balanced by a matching lock later in the same block, with no statement that
// can exit the function in between. In that shape — `defer mu.Unlock()`
// followed by `mu.Lock()`, whether the defer is direct or closure-wrapped (the
// `withLock` helper idiom) — the deferred unlock runs at function return, i.e.
// AFTER the lock, so the pair is balanced and must not be reported as
// "defer unlock without lock".
//
// A statement that can return or panic between the defer and the lock (e.g.
// BadDeferUnlockAfterPanic) leaves the unlock reachable while the mutex is
// still unlocked — a real bug — so it stays flagged. So does a defer whose
// matching lock never follows at all (the mutex is simply never locked).
//
// The result is keyed by the defer statement position so handleDeferUnlock /
// handleDeferRUnlock can credit the deferred unlock instead of reporting it.
func (c *Checker) detectSafeDeferBeforeLock(fn *ast.FuncDecl) map[token.Pos]bool {
	safe := make(map[token.Pos]bool)
	if fn == nil || fn.Body == nil {
		return safe
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if block, ok := n.(*ast.BlockStmt); ok {
			c.markSafeDeferBeforeLockInBlock(block.List, safe)
		}
		return true
	})
	return safe
}

func (c *Checker) markSafeDeferBeforeLockInBlock(stmts []ast.Stmt, safe map[token.Pos]bool) {
	for i, stmt := range stmts {
		deferStmt, ok := stmt.(*ast.DeferStmt)
		if !ok {
			continue
		}
		varName, lockMethod, ok := c.deferUnlockTarget(deferStmt)
		if !ok {
			continue
		}
		if c.matchingLockFollowsSafely(stmts[i+1:], varName, lockMethod) {
			safe[deferStmt.Pos()] = true
		}
	}
}

// matchingLockFollowsSafely reports whether rest reaches varName.lockMethod()
// before any statement that can exit the function.
func (c *Checker) matchingLockFollowsSafely(rest []ast.Stmt, varName, lockMethod string) bool {
	for _, stmt := range rest {
		if statementIsMethodCall(stmt, varName, lockMethod) {
			return true
		}
		if c.statementMayExit(stmt) {
			return false
		}
	}
	return false
}

// deferUnlockTarget returns the mutex released by a defer statement (direct
// `defer mu.Unlock()` or closure-wrapped `defer func(){ mu.Unlock() }()`) and
// the lock method that would balance it. ok is false when the defer does not
// release a tracked mutex.
func (c *Checker) deferUnlockTarget(deferStmt *ast.DeferStmt) (varName, lockMethod string, ok bool) {
	switch fun := deferStmt.Call.Fun.(type) {
	case *ast.SelectorExpr:
		return c.unlockSelectorTarget(fun)
	case *ast.FuncLit:
		var found bool
		ast.Inspect(fun.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			if v, m, matched := c.unlockSelectorTarget(sel); matched {
				varName, lockMethod, found = v, m, true
				return false
			}
			return true
		})
		return varName, lockMethod, found
	}
	return "", "", false
}

// unlockSelectorTarget maps an unlock selector (mu.Unlock / mu.RUnlock on a
// tracked mutex) to the variable and the lock method that balances it.
func (c *Checker) unlockSelectorTarget(sel *ast.SelectorExpr) (string, string, bool) {
	if !isUnlockMethod(sel.Sel.Name) {
		return "", "", false
	}
	varName := common.GetVarName(sel.X)
	if !c.mutexNames[varName] && !c.rwMutexNames[varName] {
		return "", "", false
	}
	lockMethod := matchingLockMethod(sel.Sel.Name)
	if lockMethod == "" {
		return "", "", false
	}
	return varName, lockMethod, true
}

// statementIsMethodCall reports whether stmt is exactly `varName.methodName()`.
func statementIsMethodCall(stmt ast.Stmt, varName, methodName string) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := common.UnwrapParenExpr(exprStmt.X).(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == methodName && common.GetVarName(sel.X) == varName
}
