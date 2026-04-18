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
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Double lock with direct assignment
func BadDoubleLockDirectAssign() {
	var mu sync.Mutex
	mu = sync.Mutex{} // direct assignment
	mu.Lock()         // want "mutex 'mu' is locked but not unlocked"
	mu.Lock()         // want "mutex 'mu' is locked but not unlocked"
}

// Imbalanced lock/unlock (more locks than unlocks)
func BadImbalancedLockUnlock() {
	var mu sync.Mutex
	mu.Lock()
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
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

// ---------- Goroutine Patterns ----------

// Lock/unlock in a goroutine
func GoodGoroutineLockUnlock() {
	var mu sync.Mutex
	go func() {
		mu.Lock()
		defer mu.Unlock()
	}()
}

type borrowedLockManager struct {
	mu sync.Mutex
}

func (m *borrowedLockManager) GoodBorrowedLockHelperCaller() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callWithBorrowedLock()
}

func (m *borrowedLockManager) callWithBorrowedLock() {
	m.mu.Unlock()
	m.mu.Lock()
}

type cleanupRegistrar struct{}

func (cleanupRegistrar) Cleanup(fn func()) {
	fn()
}

func GoodCleanupUnlock() {
	var mu sync.Mutex
	t := cleanupRegistrar{}
	mu.Lock()
	t.Cleanup(func() {
		mu.Unlock()
	})
}

type returnUnlocker struct {
	mu sync.Mutex
}

func (r *returnUnlocker) GoodReturnUnlocker() func() {
	r.mu.Lock()
	return func() {
		r.mu.Unlock()
	}
}

type tryLocker struct {
	mu sync.Mutex
}

func (t *tryLocker) GoodTryLockWithDeferredUnlock() {
	if t.mu.TryLock() {
		// lock acquired in the fast path
	} else {
		t.mu.Lock()
	}
	defer t.mu.Unlock()
}

// Goroutine deadlock (lock, but unlock never called)
func BadGoroutineDeadlock() {
	var mu sync.Mutex
	ch := make(chan struct{})
	go func() {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in goroutine"
		<-ch      // deadlock, never unlocks
	}()
}

// Goroutine defer unlock without lock
func BadGoroutineDeferUnlockWithoutLock() {
	var mu sync.Mutex
	ch := make(chan struct{})
	go func() {
		defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
		<-ch
	}()
}

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

// ========== MIXED SCENARIOS ==========

// Mix of var and short declarations
func MixedDeclarationTypes() {
	var mu1 sync.Mutex
	mu2 := sync.Mutex{}

	mu1.Lock() // want "mutex 'mu1' is locked but not unlocked"
	mu2.Lock() // want "mutex 'mu2' is locked but not unlocked"
}

// Different mutex types in same function
func MixedMutexTypes() {
	var mu sync.Mutex
	var rwmu sync.RWMutex

	// Good usage
	mu.Lock()
	defer mu.Unlock()

	rwmu.RLock()
	defer rwmu.RUnlock()
}

// Nested mutex usage
func MixedNestedMutexUsage() {
	var mu1, mu2 sync.Mutex

	mu1.Lock()
	defer mu1.Unlock()

	func() {
		mu2.Lock()
		defer mu2.Unlock()
	}()
}

// ========== EDGE CASES ==========

// ---------- Declaration Edge Cases ----------

// Short declaration with pointer
func PointerShortDeclaration() {
	mu := &sync.Mutex{} // short declaration with pointer
	mu.Lock()           // want "mutex 'mu' is locked but not unlocked"
}

// Short declaration in nested context
func NestedStructShortDeclaration() {
	type MyStruct struct {
		mu sync.Mutex
	}

	s := MyStruct{} // This should not be detected as a mutex
	_ = s

	// But this should be detected
	mu := sync.Mutex{} // short declaration
	mu.Lock()          // want "mutex 'mu' is locked but not unlocked"
}

// ---------- Control Flow Edge Cases ----------

// switch statement with mutex
func EdgeCaseSwitchStatement() {
	var mu sync.Mutex
	x := 1

	switch x {
	case 1:
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in case"
	case 2:
		mu.Lock()
		mu.Unlock()
	}
}

// type switch with mutex
func EdgeCaseTypeSwitchStatement() {
	var mu sync.Mutex
	var x interface{} = 1

	switch x.(type) {
	case int:
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in case"
	case string:
		mu.Lock()
		mu.Unlock()
	}
}

// select statement with mutex
func EdgeCaseSelectStatement() {
	var mu sync.Mutex
	ch := make(chan int)

	select {
	case <-ch:
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in select"
	default:
		mu.Lock()
		mu.Unlock()
	}
}

// nested blocks with mutex
func EdgeCaseNestedBlocks() {
	var mu sync.Mutex

	{
		mu.Lock() // want "mutex 'mu' is locked but not unlocked"
		{
			// nested block
		}
	}
}

// ---------- For Loop Edge Cases ----------

// Good: Lock/Unlock per iteration in for loop
func GoodForLoopLockUnlock() {
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		mu.Lock()
		mu.Unlock()
	}
}

// Good: Lock with defer Unlock in for loop (defers stack per iteration)
func GoodForLoopDeferUnlock() {
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		mu.Lock()
		defer mu.Unlock()
	}
}

// Lock at top of a block, then a switch where every reachable path
// (each case) calls Unlock before returning or continuing — no path skips it.
func GoodLockThenSwitchAllPathsUnlock(errVal error, idx, minIdx uint64) (uint64, error) {
	var mu sync.Mutex
	for {
		mu.Lock()

		switch {
		case errVal != nil:
			mu.Unlock()
			return idx, errVal
		case idx <= minIdx:
			mu.Unlock()
			continue
		}

		mu.Unlock()
		return idx, nil
	}
}

// Lock + defer Unlock with a temporary explicit Unlock/relock mid-function.
// The defer still balances the final state on every exit path.
func GoodDeferUnlockWithTemporaryRelease(buf []byte, bufLen, bufSize int) {
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()

COPY:
	remain := bufSize - bufLen
	if remain > len(buf) {
		bufLen += len(buf)
	} else {
		bufLen += remain
		mu.Unlock()
		// expensive work outside the critical section
		mu.Lock()
		goto COPY
	}
	_ = bufLen
}

// ---------- Switch All Cases Edge Cases ----------

// Good: switch where all cases + default properly lock/unlock
func GoodSwitchAllCases() {
	var mu sync.Mutex
	x := 1
	switch x {
	case 1:
		mu.Lock()
		mu.Unlock()
	case 2:
		mu.Lock()
		defer mu.Unlock()
	default:
		mu.Lock()
		mu.Unlock()
	}
}

// Bad: switch where default has unlock but another case is missing it
func BadSwitchDefaultOnlyUnlock() {
	var mu sync.Mutex
	x := 1
	switch x {
	case 1:
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in case"
	default:
		mu.Lock()
		mu.Unlock()
	}
}

// ---------- RWMutex For Loop Edge Cases ----------

// Good: RLock/RUnlock per iteration
func GoodRWForLoopRLock() {
	var rwmu sync.RWMutex
	for i := 0; i < 10; i++ {
		rwmu.RLock()
		rwmu.RUnlock()
	}
}

// Good: a labeled statement before RLock should still be analyzed.
// Regression for real-world code like consul/agent/cache getWithIndex.
func GoodLabeledRLockRUnlock(retry bool) {
	var mu sync.RWMutex

RETRY:
	mu.RLock()
	_, _ = retry, 1
	mu.RUnlock()

	if retry {
		retry = false
		goto RETRY
	}
}

// ========== FUNCTION PARAMETER TESTS ==========

// Good: function receives mutex parameter, properly locks and unlocks
func GoodMutexParameter(mu *sync.Mutex) {
	mu.Lock()
	mu.Unlock()
}

// Bad: function receives mutex parameter, locks but forgets to unlock
func BadMutexParameter(mu *sync.Mutex) {
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Good: function receives mutex parameter with defer unlock
func GoodMutexParameterDefer(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
}

// Good: function receives rwmutex parameter, properly rlocks and runlocks
func GoodRWMutexParameter(rw *sync.RWMutex) {
	rw.RLock()
	defer rw.RUnlock()
}

// Bad: function receives rwmutex parameter, rlocks but forgets to runlock
func BadRWMutexParameter(rw *sync.RWMutex) {
	rw.RLock() // want "rwmutex 'rw' is rlocked but not runlocked"
}

// ========== PACKAGE-LEVEL VARIABLE TESTS ==========

var pkgMu sync.Mutex
var pkgRWMu sync.RWMutex

// Good: package-level mutex properly locked and unlocked
func GoodPackageLevelMutex() {
	pkgMu.Lock()
	defer pkgMu.Unlock()
}

// Bad: package-level mutex locked but not unlocked
func BadPackageLevelMutex() {
	pkgMu.Lock() // want "mutex 'pkgMu' is locked but not unlocked"
}

// Good: package-level rwmutex properly rlocked and runlocked
func GoodPackageLevelRWMutex() {
	pkgRWMu.RLock()
	defer pkgRWMu.RUnlock()
}

// Bad: package-level rwmutex rlocked but not runlocked
func BadPackageLevelRWMutex() {
	pkgRWMu.RLock() // want "rwmutex 'pkgRWMu' is rlocked but not runlocked"
}

// ========== STRUCT FIELD ACCESS TESTS ==========

type SafeMap struct {
	mu   sync.Mutex
	rwmu sync.RWMutex
	data map[string]string
}

// Good: struct field mutex properly locked and unlocked
func GoodStructFieldMutex() {
	var sm SafeMap
	sm.mu.Lock()
	sm.mu.Unlock()
}

// Good: struct field mutex with defer
func GoodStructFieldMutexDefer() {
	var sm SafeMap
	sm.mu.Lock()
	defer sm.mu.Unlock()
}

// Bad: struct field mutex locked but not unlocked
func BadStructFieldMutex() {
	var sm SafeMap
	sm.mu.Lock() // want "mutex 'sm.mu' is locked but not unlocked"
}

// Good: struct field rwmutex properly rlocked and runlocked
func GoodStructFieldRWMutex() {
	var sm SafeMap
	sm.rwmu.RLock()
	defer sm.rwmu.RUnlock()
}

// Bad: struct field rwmutex rlocked but not runlocked
func BadStructFieldRWMutex() {
	var sm SafeMap
	sm.rwmu.RLock() // want "rwmutex 'sm.rwmu' is rlocked but not runlocked"
}

// Good: method receiver with struct field mutex, properly balanced
func (sm *SafeMap) GoodMethodMutex() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
}

// Bad: method receiver with struct field mutex, locked but not unlocked
func (sm *SafeMap) BadMethodMutex() {
	sm.mu.Lock() // want "mutex 'sm.mu' is locked but not unlocked"
}

// ========== COMMENT FILTERING TESTS ==========

// Test that commented code is properly ignored by the linter.
// The following commented code should NOT trigger any linter warnings.

/*
func CommentedBadMutexUsage() {
    var mu sync.Mutex
    mu.Lock() // This should be ignored
}
*/

// func AnotherCommentedFunction() {
//     var mu sync.Mutex
//     mu.Lock() // This should also be ignored
// }
