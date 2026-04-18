package mutex

import "sync"

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
