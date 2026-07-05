package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (c *Checker) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	expr := common.UnwrapParenExpr(stmt.X)

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return
	}

	if c.commentFilter.ShouldSkipCall(call) {
		return
	}

	if c.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
		return
	}

	if c.applyLocalFunctionCallLifecycleEffects(call, stats) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if sel.Sel.Name == "Cleanup" && len(call.Args) == 1 {
		if fnlit, ok := call.Args[0].(*ast.FuncLit); ok {
			c.handleDeferFunctionLiteral(fnlit, call.Pos(), stats)
		}
		return
	}

	varName := common.GetVarName(sel.X)

	// When a TryLock/TryRLock return value is ignored, the caller has no way to
	// know whether the lock was actually acquired, so any subsequent operation
	// that assumes the lock is held is racy.
	switch sel.Sel.Name {
	case "TryLock":
		if c.mutexNames[varName] {
			c.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "mutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
		if c.rwMutexNames[varName] {
			c.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
	case "TryRLock":
		if c.rwMutexNames[varName] {
			c.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryRLock return value not checked, lock may not be held")
			return
		}
	}

	if c.mutexNames[varName] {
		c.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if c.rwMutexNames[varName] {
		c.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	c.applyLocalMethodLifecycleEffects(call, stats)
}

// handleMutexCall processes mutex method calls
func (c *Checker) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if c.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].lock > 0 {
			c.errorCollector.AddError(pos, category.DoubleLock, "mutex '"+varName+"' is re-locked before unlock")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
		c.creditFlagGuardedRelease(varName, stats)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		if stats[varName].lock == 0 {
			if c.unlockIsGuardedByFlag(varName, pos) {
				return
			}
			if c.loopCarry.isCarriedLoopUnlock(varName, pos, c.function, WriteLockPattern) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
		}
	}
}

// creditFlagGuardedRelease records a deferred-unlock credit for a lock whose
// release is delegated to a deferred, flag-guarded unlock (see
// detectFlagGuardedReleases): the acquisition is balanced like
// `mu.Lock(); defer mu.Unlock()`.
func (c *Checker) creditFlagGuardedRelease(varName string, stats map[string]*Stats) {
	if c.isFlagGuarded(varName) {
		stats[varName].deferUnlock++
	}
}

// handleRWMutexCall processes rwmutex method calls
func (c *Checker) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if c.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].rlock > 0 {
			// Read-to-write upgrade on the same goroutine: Lock blocks waiting
			// for this goroutine's own read lock to be released. Guaranteed
			// self-deadlock (RWMutex is not upgradable).
			c.errorCollector.AddError(pos, category.RWMutexRecursiveLock, "rwmutex '"+varName+"' attempts write Lock while read lock is held")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
		c.creditFlagGuardedRelease(varName, stats)
	case "TryLock":
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
	case "Unlock":
		// Unlock called when only a read lock is held.
		// Correct the state as if RUnlock was called to avoid cascading errors.
		if stats[varName].rlock > 0 && stats[varName].lock == 0 {
			c.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' Unlock called but only read lock is held, did you mean RUnlock?")
			stats[varName].rlock--
			stats[varName].removeFirstRLockPos()
			return
		}
		if stats[varName].lock == 0 {
			if c.unlockIsGuardedByFlag(varName, pos) {
				return
			}
			if c.loopCarry.isCarriedLoopUnlock(varName, pos, c.function, WriteLockPattern) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
		}
	case "RLock", "TryRLock":
		// Write-to-read on the same goroutine: a blocking RLock waits for this
		// goroutine's own write lock to be released. Guaranteed self-deadlock.
		// TryRLock is excluded: it returns false instead of blocking, so it does
		// not deadlock.
		if methodName == "RLock" && stats[varName].lock > 0 {
			c.errorCollector.AddError(pos, category.RWMutexRecursiveLock, "rwmutex '"+varName+"' attempts read RLock while write lock is held")
		}
		if stats[varName].borrowedRLock > 0 {
			stats[varName].borrowedRLock--
			stats[varName].removeFirstBorrowedRUnlockPos()
			return
		}
		stats[varName].rlock++
		stats[varName].rlockPos = append(stats[varName].rlockPos, pos)
	case "RUnlock":
		// RUnlock called when only a write lock is held.
		// Correct the state as if Unlock was called to avoid cascading errors.
		if stats[varName].lock > 0 && stats[varName].rlock == 0 {
			c.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' RUnlock called but only write lock is held, did you mean Unlock?")
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
			return
		}
		if stats[varName].rlock == 0 {
			if c.loopCarry.isCarriedLoopUnlock(varName, pos, c.function, ReadLockPattern) {
				return
			}
			stats[varName].borrowedRLock++
			stats[varName].borrowedRUnlockPos = append(stats[varName].borrowedRUnlockPos, pos)
		} else {
			stats[varName].rlock--
			stats[varName].removeFirstRLockPos()
		}
	}
}

// analyzeAssignStatement handles assignments: collection-length bookkeeping,
// potential-panic-while-locked reporting, and TryLock result tracking (the
// latter delegated to the per-function tryLockTracker).
func (c *Checker) analyzeAssignStatement(stmt *ast.AssignStmt, stats map[string]*Stats) {
	c.panicDetector.recordCollectionLengthsFromAssign(stmt)
	c.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
	c.tryLock.recordAssignment(stmt)
}

func (c *Checker) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	c.panicDetector.recordCollectionLengthsFromDecl(stmt)
	c.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
}
