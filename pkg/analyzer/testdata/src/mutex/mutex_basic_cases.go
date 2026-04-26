package mutex

import "sync"

// ========== MUTEX TESTS ==========

// ---------- Basic Lock/Unlock ----------

// Basic lock and unlock
func GoodBasicLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}

// Correct usage with short declaration
func GoodLockUnlockShortDecl() {
	mu := sync.Mutex{} // short declaration
	mu.Lock()
	mu.Unlock()
}

// Multiple lock/unlock pairs
func GoodMultipleLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
	mu.Lock()
	mu.Unlock()
}

// Lock without unlock
func BadLockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Double lock (lock called twice) without unlock
func BadDoubleLock() {
	mu := sync.Mutex{}
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock() // want "mutex 'mu' is locked but not unlocked" "mutex 'mu' is re-locked before unlock"
}

// Double lock with direct assignment
func BadDoubleLockDirectAssign() {
	var mu sync.Mutex
	mu = sync.Mutex{} // direct assignment
	mu.Lock()         // want "mutex 'mu' is locked but not unlocked"
	mu.Lock()         // want "mutex 'mu' is locked but not unlocked" "mutex 'mu' is re-locked before unlock"
}

// Imbalanced lock/unlock (more locks than unlocks)
func BadImbalancedLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Lock() // want "mutex 'mu' is locked but not unlocked" "mutex 'mu' is re-locked before unlock"
	mu.Unlock()
}

// Re-entrant lock deadlocks even if the code later unlocks twice.
func BadReentrantLockEventuallyBalanced() {
	var mu sync.Mutex
	mu.Lock()
	mu.Lock() // want "mutex 'mu' is re-locked before unlock"
	mu.Unlock()
	mu.Unlock()
}

// Sequential lock/unlock pairs are fine.
func GoodSequentialRelock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
	mu.Lock()
	mu.Unlock()
}

// Double unlock (unlock called twice)
func BadDoubleUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
	mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
}

// ---------- Defer Patterns ----------

// Deferred anonymous function that performs its own Lock + defer Unlock.
// The outer function releases the lock before registering the closure; the
// closure acquires it again when it executes at function exit.
func GoodDeferClosureWithOwnLockUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	items := []int{1, 2, 3}
	mu.Unlock()

	defer func() {
		mu.Lock()
		defer mu.Unlock()
		_ = items
	}()
}

// Lock and unlock using defer
func GoodDeferUnlock() {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
}

// TryLock success branch owns the lock and may unlock it.
func GoodTryLockUnlocksOnlyOnSuccess() {
	var mu sync.Mutex
	if mu.TryLock() {
		defer mu.Unlock()
	}
}

// TryLock false branch does not own the lock.
func BadTryLockFalseBranchUnlock() {
	var mu sync.Mutex
	if mu.TryLock() {
		mu.Unlock()
	} else {
		mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
	}
}

// Negated TryLock: the fallthrough path owns the lock.
func GoodNegatedTryLockFallthroughUnlock() {
	var mu sync.Mutex
	if !mu.TryLock() {
		return
	}
	mu.Unlock()
}

// Negated TryLock: the true branch is the failed lock path.
func BadNegatedTryLockFalsePathUnlock() {
	var mu sync.Mutex
	if !mu.TryLock() {
		mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
		return
	}
	mu.Unlock()
}

// Lock/unlock with panic recovery
func GoodRecoverWithUnlock() {
	var mu sync.Mutex
	mu.Lock()
	defer func() {
		if r := recover(); r != nil {
			mu.Unlock()
		}
	}()
	panic("fail")
}

func GoodRecoverRUnlockAfterLaterRLock(shouldPanic bool) {
	var mu sync.RWMutex
	defer func() {
		if r := recover(); r != nil {
			mu.RUnlock()
		}
	}()
	mu.RLock()
	if shouldPanic {
		panic("fail")
	}
	mu.RUnlock()
}

func BadRecoverInNestedConditionDoesNotGuardRUnlock(cond bool) {
	var mu sync.RWMutex
	defer func() { // want "rwmutex 'mu' has defer runlock but no corresponding rlock"
		if func() bool {
			_ = recover()
			return cond
		}() {
			mu.RUnlock()
		}
	}()
}

// Defer unlock without prior lock
func BadDeferUnlockWithoutLock() {
	var mu sync.Mutex
	defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
	mu.Lock()
}

// Defer unlock after panic before lock
func BadDeferUnlockAfterPanic() {
	var mu sync.Mutex
	defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
	panic("panic before lock")
	mu.Lock()
}

// ---------- Conditional Logic ----------

// RLock + early-return-with-RUnlock in one branch + defer RUnlock in the other
// Both paths execute exactly one RUnlock — not a double-unlock.
func GoodRLockEarlyReturnDeferRUnlock(enablePersistence bool) error {
	var mu sync.RWMutex
	mu.RLock()
	if !enablePersistence {
		mu.RUnlock()
		return nil
	}
	defer mu.RUnlock()
	return nil
}

// Lock + early-return-with-Unlock in one branch + defer Unlock in the other
func GoodLockEarlyReturnDeferUnlock(f func() error) error {
	var mu sync.RWMutex
	mu.Lock()
	if f == nil {
		mu.Unlock()
		return nil
	}
	defer mu.Unlock()
	return f()
}

// RLock with two mutually exclusive explicit RUnlock branches (no defer)
func GoodRLockMutuallyExclusiveRUnlock(flush bool, p []byte) (int, error) {
	var mu sync.RWMutex
	mu.RLock()
	if flush {
		mu.RUnlock()
		return len(p), nil
	}
	mu.RUnlock()
	return 0, nil
}

// Lock + early-return-Unlock + later Unlock (mutually exclusive, no defer)
func GoodLockMutuallyExclusiveUnlock(cur, size int) error {
	var mu sync.Mutex
	mu.Lock()
	if cur < size {
		mu.Unlock()
		return nil
	}
	mu.Unlock()
	return nil
}

// Conditional: both branches lock and unlock
func GoodConditionalBothBranches() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock()
		defer mu.Unlock()
	} else {
		mu.Lock()
		defer mu.Unlock()
	}
}

// Conditional: one branch missing unlock
func BadConditionalMissingUnlock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in if"
	}
}

// Conditional: one branch missing lock
func BadConditionalMissingLock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
	}
}

// Conditional: one branch with lock/unlock, other only unlock
func BadConditionalOneBranchMissingLock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock()
		defer mu.Unlock()
	} else {
		mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
	}
}

// Conditional: one branch with only lock
func BadConditionalOneBranchMissingUnlock() {
	var mu sync.Mutex
	cond := true
	if cond {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	} else {
		mu.Lock()
		defer mu.Unlock()
	}
}

// else-if with mutex
func EdgeCaseElseIf() {
	var mu sync.Mutex
	cond1 := true
	cond2 := false

	if cond1 {
		mu.Lock()
		mu.Unlock()
	} else if cond2 {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in if"
	}
}
