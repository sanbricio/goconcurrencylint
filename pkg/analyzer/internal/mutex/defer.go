package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func (c *Checker) analyzeReturnStatement(stmt *ast.ReturnStmt, stats map[string]*Stats) {
	for _, result := range stmt.Results {
		call, ok := result.(*ast.CallExpr)
		if ok && !c.commentFilter.ShouldSkipCall(call) {
			// Apply callee effects for `return helper()` forms.
			if !c.applyLocalFunctionLiteralLifecycleEffects(call, stats) {
				c.applyLocalMethodLifecycleEffects(call, stats)
			}
		}

		fnlit, ok := result.(*ast.FuncLit)
		if !ok {
			continue
		}
		c.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
	}

	if !c.rawBodyEffects {
		c.reportUnmatchedLocks(stats)
	}
}

// analyzeDeferStatement handles defer statements
func (c *Checker) analyzeDeferStatement(stmt *ast.DeferStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipCall(stmt.Call) {
		return
	}

	// defer once.Do(mu.Unlock) releases the lock on return through a sync.Once.
	if c.applyDeferredOnceRelease(stmt, stats) {
		return
	}

	// Handle direct defer calls
	if call, ok := stmt.Call.Fun.(*ast.SelectorExpr); ok {
		if c.applyDeferredWrapperRelease(stmt.Call, stmt.Pos(), stats) {
			return
		}
		c.handleDeferCall(call, stmt.Pos(), stats)
		return
	}

	// Handle deferred function literals, including a literal assigned to a
	// local variable before `defer release()`.
	var fnlit *ast.FuncLit
	switch fun := common.UnwrapParenExpr(stmt.Call.Fun).(type) {
	case *ast.FuncLit:
		fnlit = fun
	case *ast.Ident:
		fnlit = c.localFunctionLiteralBefore(fun.Name, stmt.Pos())
	}
	if fnlit != nil {
		c.handleDeferFunctionLiteral(fnlit, stmt.Pos(), stats)
		c.handleDeferredOnceReleases(fnlit, stmt.Pos(), stats)
		c.applyDeferredClosureWrapperRelease(fnlit, stmt.Pos(), stats)
	}
}

// applyDeferredWrapperRelease handles `defer e.unlock()`: deferring a method of
// this package that only releases a lock the function holds releases that lock
// at return, exactly like `defer e.mu.Unlock()`. A lock not held at the defer
// is left alone, so a wrapper can never turn into an unmatched-unlock report.
func (c *Checker) applyDeferredWrapperRelease(call *ast.CallExpr, pos token.Pos, stats map[string]*Stats) bool {
	sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok || isSyncLockerMethod(sel.Sel.Name) {
		return false
	}
	callee := c.resolveCalledFuncDecl(call)
	if callee == nil || callee == c.function || callee.Body == nil {
		return false
	}
	target := handOverCall{callee: callee, calleeReceiver: common.ReceiverName(callee), bases: c.lifecycle.receiverBases(sel)}
	if target.calleeReceiver == "" || len(target.bases) == 0 {
		return false
	}
	return c.applyDeferredReleases(pos, stats, func(name string, rw bool) *Stats {
		calleeLock, ok := calleeLockFor(target, name)
		if !ok {
			return nil
		}
		return c.simulateMethodEffect(callee, calleeLock, rw, &Stats{}, nil)
	})
}

// applyDeferredClosureWrapperRelease is applyDeferredWrapperRelease for
// `defer func() { e.unlock() }()`. Locks the literal names itself are left to
// handleDeferFunctionLiteral and handleDeferredOnceReleases.
func (c *Checker) applyDeferredClosureWrapperRelease(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	mutexNames, rwMutexNames := map[string]bool{}, map[string]bool{}
	pick := func(names, into map[string]bool) {
		for name := range names {
			if st := stats[name]; st != nil && (st.lock > st.deferUnlock || st.rlock > st.deferRUnlock) &&
				!bodyNamesLockMethod(fnlit.Body, name) {
				into[name] = true
			}
		}
	}
	pick(c.mutexNames, mutexNames)
	pick(c.rwMutexNames, rwMutexNames)
	if len(mutexNames) == 0 && len(rwMutexNames) == 0 {
		return
	}

	sub := newSimulationFuncAnalysis(c.function, c.simulationStack, c.localFuncStack)
	if c.refine != nil {
		sub.facts = c.invariantFacts()
	}
	simulated := c.forkForSimulation(sub, mutexNames, rwMutexNames)
	baseline := initialStats(mutexNames, rwMutexNames)
	final := simulated.analyzeBlock(fnlit.Body, baseline)
	simulated.applyFunctionExitDefers(final, baseline)
	c.applyDeferredReleases(pos, stats, func(name string, _ bool) *Stats {
		if !mutexNames[name] && !rwMutexNames[name] {
			return nil
		}
		return final[name]
	})
}

// applyDeferredReleases records a deferred release for every held lock whose
// simulated effect, from a state holding nothing, is exactly one release, and
// reports whether it recorded any.
func (c *Checker) applyDeferredReleases(pos token.Pos, stats map[string]*Stats, effectOf func(name string, rw bool) *Stats) bool {
	applied := false
	visit := func(names map[string]bool, rw bool) {
		for name := range names {
			st := stats[name]
			if st == nil {
				continue
			}
			held := st.lock > st.deferUnlock
			readHeld := rw && st.rlock > st.deferRUnlock
			if !held && !readHeld {
				continue
			}
			effect := effectOf(name, rw)
			switch {
			case held && releasesOnly(effect, false):
				c.handleDeferUnlock(name, pos, stats, rw)
				applied = true
			case readHeld && releasesOnly(effect, true):
				c.handleDeferRUnlock(name, pos, stats)
				applied = true
			}
		}
	}
	visit(c.mutexNames, false)
	visit(c.rwMutexNames, true)
	return applied
}

// releasesOnly reports whether effect, simulated from a state holding nothing,
// is exactly one release in the given mode and nothing else.
func releasesOnly(effect *Stats, read bool) bool {
	if effect == nil || effect.lock != 0 || effect.rlock != 0 || effect.deferUnlock != 0 || effect.deferRUnlock != 0 {
		return false
	}
	if read {
		return effect.borrowedRLock == 1 && effect.borrowedLock == 0
	}
	return effect.borrowedLock == 1 && effect.borrowedRLock == 0
}

// isSyncLockerMethod reports whether name is one of the sync.Mutex/RWMutex
// methods handleDeferCall models directly.
func isSyncLockerMethod(name string) bool {
	switch name {
	case "Lock", "Unlock", "RLock", "RUnlock", "TryLock", "TryRLock":
		return true
	default:
		return false
	}
}

// bodyNamesLockMethod reports whether body calls or takes a lock method of
// varName (mu.Unlock() or once.Do(mu.Unlock)).
func bodyNamesLockMethod(body *ast.BlockStmt, varName string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok && isSyncLockerMethod(sel.Sel.Name) && common.GetVarName(sel.X) == varName {
			found = true
		}
		return !found
	})
	return found
}

// handleDeferCall processes direct defer calls
func (c *Checker) handleDeferCall(call *ast.SelectorExpr, pos token.Pos, stats map[string]*Stats) {
	varName := c.resolveLockAlias(common.GetVarName(call.X))

	if call.Sel.Name == "Lock" && c.consumeBorrowedDeferredLock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && c.consumeBorrowedDeferredRLock(varName, stats) {
		return
	}
	if call.Sel.Name == "Lock" && c.deferredRelockBalancesEarlierDeferredUnlock(varName, stats) {
		return
	}
	if call.Sel.Name == "RLock" && c.deferredRRelockBalancesEarlierDeferredRUnlock(varName, stats) {
		return
	}

	// defer Lock / defer RLock re-acquires the lock on
	// function return instead of releasing it, guaranteed deadlock.
	if c.mutexNames[varName] && call.Sel.Name == "Lock" {
		c.errorCollector.AddError(pos, category.DeferLock, "mutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
		return
	}
	if c.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Lock":
			c.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls Lock instead of Unlock, will deadlock on return")
			return
		case "RLock":
			c.errorCollector.AddError(pos, category.DeferLock, "rwmutex '"+varName+"' defer calls RLock instead of RUnlock, will deadlock on return")
			return
		}
	}

	if c.mutexNames[varName] && call.Sel.Name == "Unlock" {
		c.handleDeferUnlock(varName, pos, stats, false)
	}

	if c.rwMutexNames[varName] {
		switch call.Sel.Name {
		case "Unlock":
			c.handleDeferUnlock(varName, pos, stats, true)
		case "RUnlock":
			c.handleDeferRUnlock(varName, pos, stats)
		}
	}
}

func (c *Checker) consumeBorrowedDeferredLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedLock == 0 {
		return false
	}
	st.borrowedLock--
	st.removeFirstBorrowedUnlockPos()
	return true
}

func (c *Checker) consumeBorrowedDeferredRLock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	if st == nil || st.borrowedRLock == 0 {
		return false
	}
	st.borrowedRLock--
	st.removeFirstBorrowedRUnlockPos()
	return true
}

func (c *Checker) deferredRelockBalancesEarlierDeferredUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.lock == 0 && st.deferUnlock > 0
}

func (c *Checker) deferredRRelockBalancesEarlierDeferredRUnlock(varName string, stats map[string]*Stats) bool {
	st := stats[varName]
	return st != nil && st.rlock == 0 && st.deferRUnlock > 0
}

// handleDeferFunctionLiteral processes defer with function literals
func (c *Checker) handleDeferFunctionLiteral(fnlit *ast.FuncLit, pos token.Pos, stats map[string]*Stats) {
	guard := newRecoverGuardInspector(c.commentFilter)

	// A refined run knows which branches of the literal can run when the
	// function returns; a release only on branches that cannot is no release.
	exitFacts := c.invariantFacts()
	releases := func(name, method string) bool {
		return exitFacts == nil || c.liveCallCount(fnlit.Body, name, method, exitFacts) > 0
	}

	// Check for mutex unlocks in function literal
	for mutexName := range c.mutexNames {
		target := c.resolveLockAlias(mutexName)
		if !releases(mutexName, "Unlock") {
			continue
		}
		if guard.containsUnlock(fnlit.Body, mutexName) && !guard.containsLock(fnlit.Body, mutexName) {
			if c.isFlagGuarded(target) {
				continue
			}
			if stats[target].lock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, mutexName, "Unlock") {
				continue
			}
			c.handleDeferUnlock(target, pos, stats, c.rwMutexNames[target])
		}
	}

	// Check for rwmutex unlocks in function literal
	for rwMutexName := range c.rwMutexNames {
		target := c.resolveLockAlias(rwMutexName)
		if guard.containsUnlock(fnlit.Body, rwMutexName) && !guard.containsLock(fnlit.Body, rwMutexName) && releases(rwMutexName, "Unlock") {
			if c.isFlagGuarded(target) {
				continue
			}
			if stats[target].lock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "Unlock") {
				continue
			}
			c.handleDeferUnlock(target, pos, stats, true)
		}
		if guard.containsRUnlock(fnlit.Body, rwMutexName) && !guard.containsRLock(fnlit.Body, rwMutexName) && releases(rwMutexName, "RUnlock") {
			if stats[target].rlock == 0 && guard.unlocksOnlyInRecoverGuard(fnlit.Body, rwMutexName, "RUnlock") {
				continue
			}
			c.handleDeferRUnlock(target, pos, stats)
		}
	}
}

// handleDeferUnlock processes defer unlock calls
func (c *Checker) handleDeferUnlock(varName string, pos token.Pos, stats map[string]*Stats, isRWMutex bool) {
	if stats[varName].lock == 0 {
		if c.safeDeferBeforeLock[pos] {
			// defer-before-lock: the matching Lock immediately follows in the
			// same block, so this deferred unlock balances it at return. Credit
			// it; the upcoming Lock increments lock to match.
			stats[varName].deferUnlock++
			return
		}
		if c.crossGoroutineDeferHandoff[pos] {
			// The parent locked this mutex before launching the goroutine; the
			// deferred unlock releases the parent's lock on its behalf.
			stats[varName].deferUnlock++
			return
		}
		if c.isMirrorPairRelease(varName, WriteLockPattern.LockMethods) {
			// The acquiring half lives in another method of the type and the
			// package really uses the two as a pair; nothing was taken here to
			// balance.
			return
		}
		mutexType := "mutex"
		if isRWMutex {
			mutexType = "rwmutex"
		}
		c.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, mutexType+" '"+varName+"' has defer unlock but no corresponding lock")
		c.deferErrors.badDeferUnlock[varName] = true
	} else {
		stats[varName].deferUnlock++
	}
}

// handleDeferRUnlock processes defer runlock calls
func (c *Checker) handleDeferRUnlock(varName string, pos token.Pos, stats map[string]*Stats) {
	if stats[varName].rlock == 0 {
		if c.safeDeferBeforeLock[pos] {
			stats[varName].deferRUnlock++
			return
		}
		if c.crossGoroutineDeferHandoff[pos] {
			// The parent read-locked this rwmutex before launching the
			// goroutine; the deferred runlock releases it on the parent's behalf.
			stats[varName].deferRUnlock++
			return
		}
		if c.isMirrorPairRelease(varName, ReadLockPattern.LockMethods) {
			return
		}
		c.errorCollector.AddError(pos, category.DeferUnlockWithoutLock, "rwmutex '"+varName+"' has defer runlock but no corresponding rlock")
		c.deferErrors.badDeferRUnlock[varName] = true
	} else {
		stats[varName].deferRUnlock++
	}
}
