package mutex

import (
	"errors"
	"runtime"
	"sync"
)

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

// Good: repeated platform guards should behave like straight-line code on the
// active target, not like unrelated conditional branches.
func GoodPlatformGuardedLockingFlow(pending bool, err error) error {
	var mu sync.Mutex

	if runtime.GOOS == "linux" {
		mu.Lock()
		if pending {
			mu.Unlock()
			return nil
		}
	}

	if err != nil {
		if runtime.GOOS == "linux" {
			mu.Unlock()
		}
		return err
	}

	if runtime.GOOS == "linux" {
		mu.Unlock()
	}
	return nil
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

type InstrumentedMutex struct {
	realLock    sync.Mutex
	waitersLock sync.Mutex
}

type BrokenInstrumentedMutex struct {
	realLock sync.Mutex
}

type BrokenUnlockInstrumentedMutex struct {
	realLock sync.Mutex
}

type NamedWrapperMutex struct {
	mu sync.RWMutex
}

type LoopExitLocker struct {
	mu sync.Mutex
}

type HelperUnlockedState struct {
	mu sync.Mutex
}

type ResolverLikeClient struct {
	mu sync.RWMutex
}

type NestedResolverWrapper struct {
	cc *ResolverLikeClient
}

type BranchingUnlockClient struct {
	mu    sync.RWMutex
	conns map[int]struct{}
}

type RetryLoopStream struct {
	mu        sync.Mutex
	committed bool
}

type RetryContinueStream struct {
	mu        sync.Mutex
	committed bool
	attempt   *int
}

type RetryLikeAttempt struct{}

type RetryLikeStream struct {
	mu          sync.Mutex
	committed   bool
	replayReady bool
	attempt     *RetryLikeAttempt
}

type MixedCallerManagedRelease struct {
	mu sync.Mutex
}

type iteratorLifecycleStore struct {
	mu sync.Mutex
}

type iteratorLifecycleToken struct {
	owner *iteratorLifecycleStore
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

// Good: wrapper-style mutex methods can lock in Lock() and unlock in Unlock().
func (im *InstrumentedMutex) Lock() {
	im.waitersLock.Lock()
	im.waitersLock.Unlock()
	im.realLock.Lock()
}

func (im *InstrumentedMutex) Unlock() {
	im.waitersLock.Lock()
	im.waitersLock.Unlock()
	im.realLock.Unlock()
}

// Bad: wrapper-style Lock() that acquires an underlying mutex with no sibling release.
func (im *BrokenInstrumentedMutex) Lock() {
	im.realLock.Lock() // want "mutex 'im.realLock' is locked but not unlocked"
}

func (im *BrokenInstrumentedMutex) Unlock() {}

// Bad: wrapper-style Unlock() that releases an underlying mutex with no sibling acquire.
func (im *BrokenUnlockInstrumentedMutex) Lock() {}

func (im *BrokenUnlockInstrumentedMutex) Unlock() {
	im.realLock.Unlock() // want "mutex 'im.realLock' is unlocked but not locked"
}

// Good: wrapper methods do not need to be literally named Lock/Unlock.
func (nwm *NamedWrapperMutex) wLock() {
	nwm.mu.Lock()
}

func (nwm *NamedWrapperMutex) wUnlock() {
	nwm.mu.Unlock()
}

func (nwm *NamedWrapperMutex) rLock() {
	nwm.mu.RLock()
}

func (nwm *NamedWrapperMutex) rUnlock() {
	nwm.mu.RUnlock()
}

// Good: a loop can intentionally break while still holding the lock and release it afterwards.
func (l *LoopExitLocker) GoodLoopBreakWithUnlockAfterLoop(stop bool) {
	for {
		l.mu.Lock()
		if stop {
			break
		}
		l.mu.Unlock()
		stop = true
	}
	l.mu.Unlock()
}

func (h *HelperUnlockedState) releaseHelper() {
	h.mu.Unlock()
}

func (h *HelperUnlockedState) GoodHelperReleasesCallerLock(async bool) {
	h.mu.Lock()
	if async {
		go h.releaseHelper()
		return
	}
	h.releaseHelper()
}

func (cc *ResolverLikeClient) updateAndUnlock() {
	cc.mu.Unlock()
}

func (w *NestedResolverWrapper) GoodNestedHelperUnlock() {
	w.cc.mu.Lock()
	w.cc.updateAndUnlock()
}

func (cc *ResolverLikeClient) updateAndUnlockWithError(err error) error {
	cc.mu.Unlock()
	return err
}

func (w *NestedResolverWrapper) GoodNestedReturnHelperUnlock(err error) error {
	w.cc.mu.Lock()
	return w.cc.updateAndUnlockWithError(err)
}

func (cc *BranchingUnlockClient) updateStateAndUnlock(err error) error {
	if cc.conns == nil {
		cc.mu.Unlock()
		return nil
	}
	if err != nil {
		cc.mu.Unlock()
		return err
	}
	cc.mu.Unlock()
	return nil
}

func (cc *BranchingUnlockClient) GoodBranchingHelperUnlock(err error) error {
	cc.mu.Lock()
	if err != nil {
		cc.updateStateAndUnlock(err)
		return errors.New("wrapped")
	}
	cc.mu.Unlock()
	return nil
}

func (cc *BranchingUnlockClient) GoodExitIdleModeLike(startErr error) error {
	cc.mu.Lock()
	if cc.conns == nil {
		cc.mu.Unlock()
		return errors.New("closed")
	}
	cc.mu.Unlock()

	if startErr != nil {
		cc.mu.Lock()
		cc.updateStateAndUnlock(startErr)
		return errors.New("failed to start")
	}
	return nil
}

func (s *RetryLoopStream) GoodRetryLoopLockCarry() error {
	s.mu.Lock()
	for {
		if s.committed {
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		s.mu.Lock()
		if s.committed {
			s.mu.Unlock()
			return nil
		}
	}
}

func (s *RetryContinueStream) retryLocked() error {
	return nil
}

func (s *RetryContinueStream) GoodRetryLoopWithContinue(switched, fail bool) error {
	s.mu.Lock()
	for {
		if s.committed {
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		a := s.attempt
		_ = a
		s.mu.Lock()
		if switched {
			continue
		}
		if fail {
			s.mu.Unlock()
			return errors.New("failed")
		}
		if err := s.retryLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
}

func (s *RetryLikeStream) newAttemptLocked() (*RetryLikeAttempt, error) {
	return &RetryLikeAttempt{}, nil
}

func (s *RetryLikeStream) retryLocked(_ *RetryLikeAttempt, _ error) error {
	return nil
}

func (s *RetryLikeStream) commitAttemptLocked() {}

func (s *RetryLikeStream) GoodWithRetryLike(switched bool, opErr error, onSuccess func()) error {
	s.mu.Lock()
	for {
		if s.committed {
			s.mu.Unlock()
			return opErr
		}
		if !s.replayReady {
			var err error
			if s.attempt, err = s.newAttemptLocked(); err != nil {
				s.mu.Unlock()
				return err
			}
		}
		a := s.attempt
		s.mu.Unlock()
		err := opErr
		s.mu.Lock()
		if a != s.attempt {
			continue
		}
		if err == nil {
			if onSuccess != nil {
				onSuccess()
			} else {
				s.commitAttemptLocked()
			}
			s.mu.Unlock()
			return nil
		}
		if err := s.retryLocked(a, err); err != nil {
			s.mu.Unlock()
			return err
		}
	}
}

func (m *MixedCallerManagedRelease) releaseHelper() {
	m.mu.Unlock() // want "mutex 'm.mu' is unlocked but not locked"
}

func (m *MixedCallerManagedRelease) GoodCallerManagedRelease() {
	m.mu.Lock()
	m.releaseHelper()
}

func (m *MixedCallerManagedRelease) BadCallerManagedRelease() {
	m.releaseHelper()
}

// Good: a creator can return a token that owns the eventual unlock in Close.
func (s *iteratorLifecycleStore) GoodIteratorReturnsLockedHandle() *iteratorLifecycleToken {
	s.mu.Lock()
	token := &iteratorLifecycleToken{
		owner: s,
	}
	return token
}

func (t *iteratorLifecycleToken) Close() {
	t.owner.mu.Unlock()
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
