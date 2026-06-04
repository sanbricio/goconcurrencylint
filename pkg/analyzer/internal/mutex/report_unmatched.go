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
func (ma *Checker) reportUnmatchedLocksInBranch(initial, final map[string]*Stats, branchType string) {
	if ma.rawBodyEffects {
		return
	}

	for mutexName := range ma.mutexNames {
		ma.reportBranchDelta(mutexName, initial[mutexName], final[mutexName], false, branchType)
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.reportBranchDelta(rwMutexName, initial[rwMutexName], final[rwMutexName], true, branchType)
	}
}

// reportBranchDelta reports only the extra locks that remain held compared to
// the branch entry state.
func (ma *Checker) reportBranchDelta(mutexName string, initial, final *Stats, isRWMutex bool, branchType string) {
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
			ma.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
		}
	}

	suppressBorrowedUnlock := ma.unlockDiagnosticSuppressed(mutexName, WriteLockPattern.LockMethods) ||
		ma.terminatingTailUnlockSuppressed(mutexName)
	unlockMessage := mutexType + " '" + mutexName + "' is unlocked but not locked"
	if delta := final.borrowedLock - initial.borrowedLock; delta > 0 && !suppressBorrowedUnlock {
		for _, pos := range trailingPositions(final.borrowedUnlockPos, delta) {
			ma.errorCollector.AddError(pos, category.UnlockWithoutLock, unlockMessage)
		}
	}

	if isRWMutex {
		rlockMessage := "rwmutex '" + mutexName + "' is rlocked but not runlocked in " + branchType
		if delta := remainingLockCount(final.rlock, final.deferRUnlock) - remainingLockCount(initial.rlock, initial.deferRUnlock); delta > 0 {
			for _, pos := range trailingPositions(final.rlockPos, delta) {
				ma.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
			}
		}

		suppressBorrowedRUnlock := ma.unlockDiagnosticSuppressed(mutexName, ReadLockPattern.LockMethods) ||
			ma.terminatingTailUnlockSuppressed(mutexName)
		runlockMessage := "rwmutex '" + mutexName + "' is runlocked but not rlocked"
		if delta := final.borrowedRLock - initial.borrowedRLock; delta > 0 && !suppressBorrowedRUnlock {
			for _, pos := range trailingPositions(final.borrowedRUnlockPos, delta) {
				ma.errorCollector.AddError(pos, category.UnlockWithoutLock, runlockMessage)
			}
		}
	}
}

// unlockDiagnosticSuppressed reports whether ownership is managed outside the
// current lock/unlock pair.
func (ma *Checker) unlockDiagnosticSuppressed(mutexName string, acquireMethods []string) bool {
	return ma.lifecycle.isReleaseFor(mutexName, acquireMethods) ||
		ma.lifecycle.isCallerManagedReleaseFor(mutexName, acquireMethods) ||
		ma.functionIsParameterUnlockHelper(mutexName, acquireMethods)
}

// terminatingTailUnlockSuppressed reports caller-owned unlocks before a
// terminating tail.
func (ma *Checker) terminatingTailUnlockSuppressed(mutexName string) bool {
	return ma.terminatingTailDepth > 0 && ma.varRootIsFunctionParameter(mutexName)
}

// reportUnmatchedMutexLocksWithContext reports unmatched locks for a specific mutex with context
func (ma *Checker) reportUnmatchedMutexLocksWithContext(mutexName string, stats *Stats, isRWMutex bool, branchType string) {
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

	suppressFunctionLevelLock := branchType == "" && ma.lifecycle.returnsHandleFor(mutexName, WriteLockPattern.UnlockMethods)
	for _, pos := range trailingPositions(stats.lockPos, remainingLockCount(stats.lock, stats.deferUnlock)) {
		if suppressFunctionLevelLock {
			continue
		}
		ma.errorCollector.AddError(pos, category.LockWithoutUnlock, lockMessage)
	}

	suppressFunctionLevelUnlock := branchType == "" && ma.unlockDiagnosticSuppressed(mutexName, WriteLockPattern.LockMethods)
	for _, pos := range stats.borrowedUnlockPos {
		if suppressFunctionLevelUnlock {
			continue
		}
		ma.errorCollector.AddError(pos, category.UnlockWithoutLock, mutexType+" '"+mutexName+"' is unlocked but not locked")
	}

	if isRWMutex {
		suppressFunctionLevelRLock := branchType == "" && ma.lifecycle.returnsHandleFor(mutexName, ReadLockPattern.UnlockMethods)
		for _, pos := range trailingPositions(stats.rlockPos, remainingLockCount(stats.rlock, stats.deferRUnlock)) {
			if suppressFunctionLevelRLock {
				continue
			}
			ma.errorCollector.AddError(pos, category.LockWithoutUnlock, rlockMessage)
		}
		suppressFunctionLevelRUnlock := branchType == "" && ma.unlockDiagnosticSuppressed(mutexName, ReadLockPattern.LockMethods)
		for _, pos := range stats.borrowedRUnlockPos {
			if suppressFunctionLevelRUnlock {
				continue
			}
			ma.errorCollector.AddError(pos, category.UnlockWithoutLock, "rwmutex '"+mutexName+"' is runlocked but not rlocked")
		}
	}
}

// reportUnmatchedMutexLocks reports unmatched locks for a specific mutex
func (ma *Checker) reportUnmatchedMutexLocks(mutexName string, stats *Stats, isRWMutex bool) {
	// Call the context-aware version with empty context for function-level reporting
	ma.reportUnmatchedMutexLocksWithContext(mutexName, stats, isRWMutex, "")
}

// reportUnmatchedLocks reports any remaining unmatched locks at function level
func (ma *Checker) reportUnmatchedLocks(stats map[string]*Stats) {
	if ma.rawBodyEffects {
		return
	}

	for mutexName := range ma.mutexNames {
		if ma.deferErrors.badDeferUnlock[mutexName] {
			continue
		}
		ma.reportUnmatchedMutexLocks(mutexName, stats[mutexName], false)
	}

	for rwMutexName := range ma.rwMutexNames {
		if ma.deferErrors.badDeferUnlock[rwMutexName] || ma.deferErrors.badDeferRUnlock[rwMutexName] {
			continue
		}
		ma.reportUnmatchedMutexLocks(rwMutexName, stats[rwMutexName], true)
	}

	// Report goroutine-parent deadlocks only when the parent exits while still
	// holding the lock, so the goroutine can never acquire it.
	cg := newCrossGoroutineDetector(ma.mutexNames, ma.rwMutexNames, ma.commentFilter, ma.typesInfo)
	for _, conflict := range ma.goroutineLockConflicts {
		st := stats[conflict.varName]
		if st == nil {
			continue
		}
		if conflict.parentReadLock {
			if remainingLockCount(st.rlock, st.deferRUnlock) > 0 {
				ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
					cg.deadlockMessage(conflict.varName, true, true, conflict.requestMethod, false))
			}
			continue
		}

		if remainingLockCount(st.lock, st.deferUnlock) > 0 {
			ma.errorCollector.AddError(conflict.pos, category.GoroutineLockDeadlock,
				cg.deadlockMessage(conflict.varName, conflict.isRWMutex, false, conflict.requestMethod, false))
		}
	}
}
