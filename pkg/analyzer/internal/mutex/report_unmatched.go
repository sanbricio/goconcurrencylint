package mutex

import (
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// trailingPositions returns the last count positions from the slice.
func trailingPositions(positions []token.Pos, count int) []token.Pos {
	if count <= 0 {
		return nil
	}
	if count >= len(positions) {
		return positions
	}
	return positions[len(positions)-count:]
}

// reportUnmatchedLocksInBranch reports unmatched locks in conditional branches
func (c *Checker) reportUnmatchedLocksInBranch(initial, final map[string]*Stats, branchType string) {
	if c.rawBodyEffects {
		return
	}

	for mutexName := range c.mutexNames {
		c.reportBranchDelta(mutexName, initial[mutexName], final[mutexName], false, branchType)
	}

	for rwMutexName := range c.rwMutexNames {
		c.reportBranchDelta(rwMutexName, initial[rwMutexName], final[rwMutexName], true, branchType)
	}
}

// reportBranchDelta reports only the extra locks that remain held compared to
// the branch entry state.
func (c *Checker) reportBranchDelta(mutexName string, initial, final *Stats, isRWMutex bool, branchType string) {
	if final == nil {
		return
	}
	if initial == nil {
		initial = &Stats{}
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}

	lockMessage := mutexType + " '" + mutexName + "' is locked but not unlocked in " + branchType
	if delta := remainingLockCount(final.lock, final.deferUnlock) - remainingLockCount(initial.lock, initial.deferUnlock); delta > 0 {
		for _, pos := range trailingPositions(final.lockPos, delta) {
			c.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
		}
	}

	suppressBorrowedUnlock := c.unlockDiagnosticSuppressed(mutexName, WriteLockPattern.LockMethods) ||
		c.terminatingTailUnlockSuppressed(mutexName)
	unlockMessage := mutexType + " '" + mutexName + "' is unlocked but not locked"
	if delta := final.borrowedLock - initial.borrowedLock; delta > 0 && !suppressBorrowedUnlock {
		for _, pos := range trailingPositions(final.borrowedUnlockPos, delta) {
			c.errorCollector.AddError(pos, category.UnlockWithoutLock, unlockMessage)
		}
	}

	if isRWMutex {
		rlockMessage := "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
		if delta := remainingLockCount(final.rlock, final.deferRUnlock) - remainingLockCount(initial.rlock, initial.deferRUnlock); delta > 0 {
			for _, pos := range trailingPositions(final.rlockPos, delta) {
				c.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
			}
		}

		suppressBorrowedRUnlock := c.unlockDiagnosticSuppressed(mutexName, ReadLockPattern.LockMethods) ||
			c.terminatingTailUnlockSuppressed(mutexName)
		runlockMessage := "rwmutex '" + mutexName + "' is runlocked but not rlocked"
		if delta := final.borrowedRLock - initial.borrowedRLock; delta > 0 && !suppressBorrowedRUnlock {
			for _, pos := range trailingPositions(final.borrowedRUnlockPos, delta) {
				c.errorCollector.AddError(pos, category.UnlockWithoutLock, runlockMessage)
			}
		}
	}
}

// unlockDiagnosticSuppressed reports whether ownership is managed outside the
// current lock/unlock pair.
func (c *Checker) unlockDiagnosticSuppressed(mutexName string, acquireMethods []string) bool {
	return c.lifecycle.isReleaseFor(mutexName, acquireMethods) ||
		c.lifecycle.isReturnedFuncReleaseFor(mutexName, acquireMethods) ||
		c.lifecycle.isCallerManagedReleaseFor(mutexName, acquireMethods) ||
		c.functionIsParameterUnlockHelper(mutexName, acquireMethods)
}

// terminatingTailUnlockSuppressed reports caller-owned unlocks before a
// terminating tail.
func (c *Checker) terminatingTailUnlockSuppressed(mutexName string) bool {
	return c.terminatingTailDepth > 0 && c.varRootIsFunctionParameter(mutexName)
}

// reportUnmatchedMutexLocksWithContext reports unmatched locks for a specific mutex with context
func (c *Checker) reportUnmatchedMutexLocksWithContext(mutexName string, stats *Stats, isRWMutex bool, branchType string) {
	if stats == nil {
		return
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}

	// Create context-aware messages
	var lockMessage, rlockMessage string
	if branchType == "" {
		// For function-level reporting (no context)
		lockMessage = mutexType + " '" + mutexName + "' is locked but not unlocked"
		rlockMessage = "rwmutex '" + mutexName + "' is rlocked but not runlocked"
	} else {
		// For branch-level reporting (with context)
		lockMessage = mutexType + " '" + mutexName + "' is locked but not unlocked in " + branchType
		rlockMessage = "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
	}

	suppressFunctionLevelLock := branchType == "" &&
		(c.lifecycle.returnsHandleFor(mutexName, WriteLockPattern.UnlockMethods) ||
			c.lifecycle.returnsFuncFor(mutexName, WriteLockPattern.UnlockMethods))
	for _, pos := range trailingPositions(stats.lockPos, remainingLockCount(stats.lock, stats.deferUnlock)) {
		if suppressFunctionLevelLock {
			continue
		}
		c.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
	}

	suppressFunctionLevelUnlock := branchType == "" && c.unlockDiagnosticSuppressed(mutexName, WriteLockPattern.LockMethods)
	for _, pos := range stats.borrowedUnlockPos {
		if suppressFunctionLevelUnlock {
			continue
		}
		c.errorCollector.AddError(pos, category.UnlockWithoutLock, mutexType+" '"+mutexName+"' is unlocked but not locked")
	}

	if isRWMutex {
		suppressFunctionLevelRLock := branchType == "" &&
			(c.lifecycle.returnsHandleFor(mutexName, ReadLockPattern.UnlockMethods) ||
				c.lifecycle.returnsFuncFor(mutexName, ReadLockPattern.UnlockMethods))
		for _, pos := range trailingPositions(stats.rlockPos, remainingLockCount(stats.rlock, stats.deferRUnlock)) {
			if suppressFunctionLevelRLock {
				continue
			}
			c.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
		}
		suppressFunctionLevelRUnlock := branchType == "" && c.unlockDiagnosticSuppressed(mutexName, ReadLockPattern.LockMethods)
		for _, pos := range stats.borrowedRUnlockPos {
			if suppressFunctionLevelRUnlock {
				continue
			}
			c.errorCollector.AddError(pos, category.UnlockWithoutLock, "rwmutex '"+mutexName+"' is runlocked but not rlocked")
		}
	}
}

// reportUnmatchedMutexLocks reports unmatched locks for a specific mutex
func (c *Checker) reportUnmatchedMutexLocks(mutexName string, stats *Stats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	c.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (c *Checker) reportUnmatchedLocks(stats map[string]*Stats) {
	if c.rawBodyEffects {
		return
	}

	for mutexName := range c.mutexNames {
		if c.deferErrors.badDeferUnlock[mutexName] {
			continue
		}
		c.reportUnmatchedMutexLocks(mutexName, stats[mutexName], false)
	}

	for rwMutexName := range c.rwMutexNames {
		if c.deferErrors.badDeferUnlock[rwMutexName] || c.deferErrors.badDeferRUnlock[rwMutexName] {
			continue
		}
		c.reportUnmatchedMutexLocks(rwMutexName, stats[rwMutexName], true)
	}

	// Report goroutine-parent deadlocks only when the parent exits while still
	// holding the lock, so the goroutine can never acquire it.
	cg := newCrossGoroutineDetector(c.mutexNames, c.rwMutexNames, c.commentFilter, c.typesInfo)
	for _, conflict := range c.goroutineLockConflicts {
		st := stats[conflict.varName]
		if st == nil {
			continue
		}
		if conflict.parentReadLock {
			if remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				c.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
					cg.deadlockMessage(conflict.varName, true, true, conflict.requestMethod, false))
			}
			continue
		}

		if remainingLockCount(st.lock, st.deferUnlock) > 0 {
			c.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
				cg.deadlockMessage(conflict.varName, conflict.isRWMutex, false, conflict.requestMethod, false))
		}
	}
}
