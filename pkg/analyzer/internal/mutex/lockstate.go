package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

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

	if c.applyOnceRelease(call, stats) {
		return
	}

	// require.True(t, mu.TryLock()) only returns when the lock was acquired.
	if c.applyAssertedTryLock(call, stats) {
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

	varName := c.resolveLockAlias(common.GetVarName(sel.X))

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
	c.applyLockedValueHandover(stmt, stats)
}

// applyLockedValueHandover records the lock a callee hands over. A function that
// locks the value it returns — `obj := o.serialize(key)` for a serialize that
// ends in `obj.Lock(); return obj` — gives its caller a value that is already
// held, so the caller's `defer obj.Unlock()` is matched rather than an unlock
// without a lock, and a caller that never releases it is the one reported.
func (c *Checker) applyLockedValueHandover(stmt *ast.AssignStmt, stats map[string]*Stats) {
	if len(stmt.Rhs) == 1 {
		call, ok := common.UnwrapParenExpr(stmt.Rhs[0]).(*ast.CallExpr)
		if !ok {
			return
		}
		c.applyLockedCallResults(stmt, call, stmt.Lhs, stats)
		return
	}

	for index, rhs := range stmt.Rhs {
		if index >= len(stmt.Lhs) {
			continue
		}
		call, ok := common.UnwrapParenExpr(rhs).(*ast.CallExpr)
		if !ok {
			continue
		}
		c.applyLockedCallResults(stmt, call, stmt.Lhs[index:index+1], stats)
	}
}

func (c *Checker) applyLockedCallResults(stmt *ast.AssignStmt, call *ast.CallExpr, lhs []ast.Expr, stats map[string]*Stats) {
	callee := c.resolveCalledFuncDecl(call)
	if callee == nil || callee == c.function {
		return
	}
	pos := stmt.Pos()

	summary := c.lockedReturns(callee)
	for resultIndex, method := range summary.byResult {
		if resultIndex >= len(lhs) {
			continue
		}
		ident, ok := common.UnwrapParenExpr(lhs[resultIndex]).(*ast.Ident)
		if !ok || ident.Name == "_" {
			continue
		}
		stat := stats[ident.Name]
		if stat == nil {
			continue
		}

		if method == "RLock" {
			stat.rlock++
			stat.rlockPos = append(stat.rlockPos, pos)
		} else {
			stat.lock++
			stat.lockPos = append(stat.lockPos, pos)
		}

		if errorIndex, ok := summary.errorResultByLocked[resultIndex]; ok {
			c.recordHandoffErrorGuard(stmt, lhs, resultIndex, errorIndex, method)
		}
	}
}

// handoffErrorGuard ties a lock-bearing result to the terminating error arm
// immediately following its assignment. The call only hands ownership to the
// caller on the success path.
type handoffErrorGuard struct {
	varName       string
	method        string
	failureOnThen bool
}

func (c *Checker) recordHandoffErrorGuard(stmt *ast.AssignStmt, lhs []ast.Expr, lockedIndex, errorIndex int, method string) {
	if c.funcAnalysis == nil || lockedIndex >= len(lhs) || errorIndex >= len(lhs) {
		return
	}
	c.ensureControlFlowStructure()
	lockedIdent, lockedOK := common.UnwrapParenExpr(lhs[lockedIndex]).(*ast.Ident)
	errorIdent, errorOK := common.UnwrapParenExpr(lhs[errorIndex]).(*ast.Ident)
	if !lockedOK || !errorOK || lockedIdent.Name == "_" || errorIdent.Name == "_" {
		return
	}

	guardStmt := c.ifInitOwners[stmt]
	if guardStmt == nil {
		location, ok := c.statementLocations[stmt]
		if !ok || location.index+1 >= len(location.list) {
			return
		}
		guardStmt, ok = location.list[location.index+1].(*ast.IfStmt)
		if !ok || guardStmt.Init != nil {
			return
		}
	}
	failureOnThen, ok := nilComparisonFailureBranch(guardStmt.Cond, errorIdent.Name)
	if !ok {
		return
	}
	if failureOnThen {
		if !c.termination.blockAlwaysTerminates(guardStmt.Body) {
			return
		}
	} else if guardStmt.Else == nil || !c.termination.elseAlwaysTerminates(guardStmt.Else) {
		return
	}

	c.handoffErrorGuards[guardStmt] = append(c.handoffErrorGuards[guardStmt], handoffErrorGuard{
		varName:       lockedIdent.Name,
		method:        method,
		failureOnThen: failureOnThen,
	})
}

func nilComparisonFailureBranch(cond ast.Expr, errorName string) (bool, bool) {
	binary, ok := common.UnwrapParenExpr(cond).(*ast.BinaryExpr)
	if !ok || (binary.Op != token.EQL && binary.Op != token.NEQ) {
		return false, false
	}
	left, right := common.UnwrapParenExpr(binary.X), common.UnwrapParenExpr(binary.Y)
	ident, ok := left.(*ast.Ident)
	if !ok || !isNilIdent(right) {
		ident, ok = right.(*ast.Ident)
		if !ok || !isNilIdent(left) {
			return false, false
		}
	}
	if ident.Name != errorName {
		return false, false
	}
	return binary.Op == token.NEQ, true
}

func (c *Checker) applyHandoffErrorGuard(stmt *ast.IfStmt, thenStats, elseStats map[string]*Stats) {
	for _, guard := range c.handoffErrorGuards[stmt] {
		target := elseStats
		if guard.failureOnThen {
			target = thenStats
		}
		stat := target[guard.varName]
		if stat == nil {
			continue
		}
		if guard.method == "RLock" {
			if stat.rlock > 0 {
				stat.rlock--
			}
			if len(stat.rlockPos) > 0 {
				stat.rlockPos = stat.rlockPos[:len(stat.rlockPos)-1]
			}
			continue
		}
		if stat.lock > 0 {
			stat.lock--
		}
		if len(stat.lockPos) > 0 {
			stat.lockPos = stat.lockPos[:len(stat.lockPos)-1]
		}
	}
}

// resolveCalledFuncDecl returns the declaration of the function or method call
// invokes, when it is declared in this package.
func (c *Checker) resolveCalledFuncDecl(call *ast.CallExpr) *ast.FuncDecl {
	switch fun := common.UnwrapParenExpr(call.Fun).(type) {
	case *ast.Ident:
		return c.topLevelFunctionNamed(fun.Name)
	case *ast.SelectorExpr:
		if c.typesInfo == nil {
			return nil
		}
		// For a promoted method, TypeOf(fun.X) names the outer embedding type,
		// while the declaration belongs to the embedded receiver. Selection.Obj
		// points at that declaring method and therefore resolves both direct and
		// promoted calls correctly.
		receiverType := ""
		if selection := c.typesInfo.Selections[fun]; selection != nil {
			if method, ok := selection.Obj().(*types.Func); ok {
				if sig, ok := method.Type().(*types.Signature); ok && sig.Recv() != nil {
					receiverType = common.BaseTypeNameFromType(sig.Recv().Type())
				}
			}
		}
		if receiverType == "" {
			receiverType = common.BaseTypeNameFromType(c.typesInfo.TypeOf(fun.X))
		}
		if receiverType == "" {
			return nil
		}
		return c.receiverMethods[receiverType][fun.Sel.Name]
	}

	return nil
}

func (c *Checker) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	c.panicDetector.recordCollectionLengthsFromDecl(stmt)
	c.panicDetector.reportPotentialPanicWhileLocked(stmt, stats)
}
