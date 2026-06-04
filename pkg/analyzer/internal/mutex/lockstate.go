package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// analyzeExpressionStatement handles expression statements (Lock/Unlock calls)
func (ma *Checker) analyzeExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	expr := common.UnwrapParenExpr(stmt.X)

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return
	}

	if ma.commentFilter.ShouldSkipCall(call) {
		return
	}

	if ma.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if sel.Sel.Name == "Cleanup" && len(call.Args) == 1 {
		if fnlit, ok := call.Args[0].(*ast.FuncLit); ok {
			ma.handleDeferFunctionLiteral(fnlit, call.Pos(), stats)
		}
		return
	}

	varName := common.GetVarName(sel.X)

	// When a TryLock/TryRLock return value is ignored, the caller has no way to
	// know whether the lock was actually acquired, so any subsequent operation
	// that assumes the lock is held is racy.
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "mutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryLock return value not checked, lock may not be held")
			return
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			ma.errorCollector.AddError(call.Pos(), category.UncheckedTryLock, "rwmutex '"+varName+"' TryRLock return value not checked, lock may not be held")
			return
		}
	}

	if ma.mutexNames[varName] {
		ma.handleMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	if ma.rwMutexNames[varName] {
		ma.handleRWMutexCall(varName, sel.Sel.Name, call.Pos(), stats)
	}

	ma.applyLocalMethodLifecycleEffects(call, stats)
}

// handleMutexCall processes mutex method calls
func (ma *Checker) handleMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].lock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "mutex '"+varName+"' is re-locked before unlock")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
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
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, WriteLockPattern) {
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

// handleRWMutexCall processes rwmutex method calls
func (ma *Checker) handleRWMutexCall(varName, methodName string, pos token.Pos, stats map[string]*Stats) {
	if ma.wrapper.resolve(varName, methodName) {
		return
	}

	switch methodName {
	case "Lock":
		if stats[varName].rlock > 0 {
			ma.errorCollector.AddError(pos, category.DoubleLock, "rwmutex '"+varName+"' attempts write Lock while read lock is held")
		}
		if stats[varName].borrowedLock > 0 {
			stats[varName].borrowedLock--
			stats[varName].removeFirstBorrowedUnlockPos()
			return
		}
		stats[varName].lock++
		stats[varName].lockPos = append(stats[varName].lockPos, pos)
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
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' Unlock called but only read lock is held, did you mean RUnlock?")
			stats[varName].rlock--
			stats[varName].removeFirstRLockPos()
			return
		}
		if stats[varName].lock == 0 {
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, WriteLockPattern) {
				return
			}
			stats[varName].borrowedLock++
			stats[varName].borrowedUnlockPos = append(stats[varName].borrowedUnlockPos, pos)
		} else {
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
		}
	case "RLock", "TryRLock":
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
			ma.errorCollector.AddError(pos, category.RWMutexAPIMismatch, "rwmutex '"+varName+"' RUnlock called but only write lock is held, did you mean Unlock?")
			stats[varName].lock--
			stats[varName].removeFirstLockPos()
			return
		}
		if stats[varName].rlock == 0 {
			if ma.loopCarry.isCarriedLoopUnlock(varName, pos, ma.function, ReadLockPattern) {
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
func (ma *Checker) analyzeAssignStatement(stmt *ast.AssignStmt, stats map[string]*Stats) {
	ma.panicDetector.recordCollectionLengthsFromAssign(stmt)
	ma.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
	ma.tryLock.recordAssignment(stmt)
}

func (ma *Checker) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	ma.panicDetector.recordCollectionLengthsFromDecl(stmt)
	ma.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
}
