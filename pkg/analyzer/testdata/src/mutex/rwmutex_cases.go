package mutex

import "sync"

// ========== RWMUTEX TESTS ==========

// ---------- Basic Read/Write Operations ----------

// Basic RLock and RUnlock
func GoodBasicRLockRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
}

// Basic Lock and Unlock (write)
func GoodBasicRWLockUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	mu.Unlock()
}

// RLock/RUnlock and Lock/Unlock in sequence
func GoodRWMultipleOperations() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.Lock()
	mu.Unlock()
}

// Upgrading from a read lock to a write lock deadlocks in the same goroutine.
func BadRWMutexPromotion() {
	var mu sync.RWMutex
	mu.RLock()
	mu.Lock() // want "rwmutex 'mu' attempts write Lock while read lock is held"
	mu.Unlock()
	mu.RUnlock()
}

// The write lock is fine after the read lock has been released.
func GoodRWMutexReadThenWriteAfterRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.Lock()
	mu.Unlock()
}

// A deferred RUnlock still leaves the read lock held until the function returns.
func BadRWMutexPromotionWithDeferredRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	defer mu.RUnlock()
	mu.Lock() // want "rwmutex 'mu' attempts write Lock while read lock is held"
	mu.Unlock()
}

// RLock without RUnlock
func BadRLockWithoutRUnlock() {
	var mu sync.RWMutex
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
}

// RWMutex with short declaration
func BadRWLockShortDecl() {
	rwmu := sync.RWMutex{}
	rwmu.Lock() // want "rwmutex 'rwmu' is locked but not unlocked"
}

// RLock without RUnlock with short declaration
func BadRLockShortDecl() {
	rwmu := sync.RWMutex{}
	rwmu.RLock() // want "rwmutex 'rwmu' is rlocked but not runlocked"
}

// RUnlock without RLock
func BadRUnlockWithoutRLock() {
	var mu sync.RWMutex
	mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
}

// Lock without Unlock (write lock)
func BadRWLockWithoutUnlock() {
	var mu sync.RWMutex
	mu.Lock() // want "rwmutex 'mu' is locked but not unlocked"
}

// Unlock without Lock (write lock)
func BadRWUnlockWithoutLock() {
	var mu sync.RWMutex
	mu.Unlock() // want "rwmutex 'mu' is unlocked but not locked"
}

// ---------- RWMutex Defer Patterns ----------

// Defer unlock after write lock
func GoodRWDeferUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	defer mu.Unlock()
}

// Defer runlock after read lock
func GoodRWDeferRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	defer mu.RUnlock()
}

// Defer runlock without prior rlock
func BadRWDeferRUnlockWithoutRLock() {
	var mu sync.RWMutex
	defer mu.RUnlock() // want "rwmutex 'mu' has defer runlock but no corresponding rlock"
	mu.RLock()
}

// Defer unlock without prior lock
func BadRWDeferUnlockWithoutLock() {
	var mu sync.RWMutex
	defer mu.Unlock() // want "rwmutex 'mu' has defer unlock but no corresponding lock"
	mu.Lock()
}

// ---------- RWMutex Conditional Logic ----------

// Both branches use lock/unlock or rlock/runlock
func GoodRWConditionalBothBranches() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.Lock()
		defer mu.Unlock()
	} else {
		mu.RLock()
		defer mu.RUnlock()
	}
}

// Conditional: one branch with rlock, other missing runlock
func BadRWConditionalMissingRUnlock() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked in if"
	}
}

// Conditional: one branch with rlock/runlock, other only runlock
func BadRWConditionalOneBranchMissingRLock() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.RLock()
		defer mu.RUnlock()
	} else {
		mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
	}
}

// ---------- RWMutex Goroutine Patterns ----------

// Goroutine: Lock/Unlock and RLock/RUnlock
func GoodRWGoroutineLockUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.Lock()
		defer mu.Unlock()
	}()
	go func() {
		mu.RLock()
		defer mu.RUnlock()
	}()
}

// Goroutine: rlock without runlock
func BadRWGoroutineRLockWithoutRUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked in goroutine"
	}()
}

// Goroutine: lock without unlock
func BadRWGoroutineLockWithoutUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.Lock() // want "rwmutex 'mu' is locked but not unlocked"
	}()
}

// ---------- RWMutex Balance Issues ----------

// Imbalanced: two rlocks, one runlock
func BadRWImbalancedRLockRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
	mu.RUnlock()
}

// Imbalanced: lock and rlock, only unlock
func BadRWImbalancedMixed() {
	var mu sync.RWMutex
	mu.Lock()
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
	mu.Unlock()
}

// Double unlocks (runlock twice)
func BadRWDoubleRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
}

// Double unlocks (unlock twice)
func BadRWDoubleUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	mu.Unlock()
	mu.Unlock() // want "rwmutex 'mu' is unlocked but not locked"
}
