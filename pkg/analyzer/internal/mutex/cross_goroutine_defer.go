package mutex

import (
	"go/ast"
	"go/token"
)

// detectCrossGoroutineDeferHandoff finds `defer mu.Unlock()` statements inside a
// goroutine that release a lock the parent function acquired before launching
// that goroutine. A sync.Mutex may be unlocked by a goroutine other than the
// one that locked it, so this is a deliberate ownership handoff, not an
// unmatched unlock — the deferred unlock in the child balances the parent's
// Lock/TryLock.
//
// Example that must not be flagged (loki sync_manager.go):
//
//	if !s.runMtx.TryLock() {
//		return false
//	}
//	go func() {
//		defer s.runMtx.Unlock() // releases the lock the parent took above
//		s.run(...)
//	}()
//
// The result is keyed by the defer statement position so handleDeferUnlock /
// handleDeferRUnlock can credit the deferred unlock instead of reporting a
// "defer unlock without lock".
func (c *Checker) detectCrossGoroutineDeferHandoff(fn *ast.FuncDecl) map[token.Pos]bool {
	handoff := make(map[token.Pos]bool)
	if fn == nil || fn.Body == nil {
		return handoff
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok || fnLit.Body == nil {
			return true
		}
		c.markGoroutineHandoffDefers(fn, goStmt.Pos(), fnLit.Body, handoff)
		return true
	})

	return handoff
}

// markGoroutineHandoffDefers records the deferred unlocks in a goroutine body
// whose matching lock was taken by the parent before goPos. It does not descend
// into nested function literals, whose defers belong to a different frame.
func (c *Checker) markGoroutineHandoffDefers(fn *ast.FuncDecl, goPos token.Pos, body *ast.BlockStmt, handoff map[token.Pos]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		varName, lockMethod, ok := c.deferUnlockTarget(deferStmt)
		if !ok {
			return true
		}
		if functionBodyContainsFieldCallBefore(fn.Body, varName, parentAcquireMethods(lockMethod), goPos) {
			handoff[deferStmt.Pos()] = true
		}
		return true
	})
}

// parentAcquireMethods returns the lock methods a parent may use to acquire the
// lock that a child goroutine later releases with the given lock method,
// including the Try* variants.
func parentAcquireMethods(lockMethod string) []string {
	switch lockMethod {
	case "Lock":
		return []string{"Lock", "TryLock"}
	case "RLock":
		return []string{"RLock", "TryRLock"}
	default:
		return []string{lockMethod}
	}
}
