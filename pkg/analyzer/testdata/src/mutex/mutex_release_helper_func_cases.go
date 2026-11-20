package mutex

import "sync"

// Plain functions that release a lock their caller took: either a lock the
// caller hands over by address, or a package-level lock both sides name. The
// method form (h.mu.Lock(); h.releaseHelper()) was already modelled; these are
// the same split without a receiver.

// --- lock handed over by address ---

func unlockGivenMutex(m *sync.Mutex) {
	m.Unlock()
}

func unlockGivenLocker(l sync.Locker) {
	l.Unlock()
}

func runlockGivenRWMutex(rw *sync.RWMutex) {
	rw.RUnlock()
}

// GoodLocalMutexReleasedByAddress must be CLEAN.
func GoodLocalMutexReleasedByAddress(items []int) int {
	var mu sync.Mutex
	mu.Lock()
	n := len(items)
	unlockGivenMutex(&mu)
	return n
}

// GoodPointerMutexReleasedByHelper must be CLEAN: mu is already a pointer.
func GoodPointerMutexReleasedByHelper(mu *sync.Mutex, out *int) {
	mu.Lock()
	*out++
	unlockGivenMutex(mu)
}

type addressReleasedCounter struct {
	mu sync.Mutex
	rw sync.RWMutex
	n  int
}

// GoodFieldMutexReleasedByAddress must be CLEAN.
func GoodFieldMutexReleasedByAddress(c *addressReleasedCounter) {
	c.mu.Lock()
	c.n++
	unlockGivenMutex(&c.mu)
}

// GoodMutexReleasedThroughLocker must be CLEAN: the interface receives the
// address, so it is the caller's lock.
func GoodMutexReleasedThroughLocker(c *addressReleasedCounter) {
	c.mu.Lock()
	c.n--
	unlockGivenLocker(&c.mu)
}

// GoodReadLockReleasedByAddressOnEveryPath must be CLEAN.
func GoodReadLockReleasedByAddressOnEveryPath(c *addressReleasedCounter, limit int) bool {
	c.rw.RLock()
	if c.n > limit {
		runlockGivenRWMutex(&c.rw)
		return false
	}
	runlockGivenRWMutex(&c.rw)
	return true
}

func unlockOtherMutex(_ *sync.Mutex, other *sync.Mutex) {
	other.Unlock()
}

// BadAddressPassedToNonReleasingParam: the helper releases its other
// parameter, not the lock it was given.
func BadAddressPassedToNonReleasingParam(held, spare *sync.Mutex) {
	held.Lock() // want "mutex 'held' is locked but not unlocked"
	spare.Lock()
	unlockOtherMutex(held, spare)
}

// BadAddressReleasedOnOnePathOnly: the early return skips the helper.
func BadAddressReleasedOnOnePathOnly(c *addressReleasedCounter, skip bool) {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
	if skip {
		return
	}
	unlockGivenMutex(&c.mu)
}

// --- package-level lock named by both sides ---

var helperRegistry struct {
	mu   sync.RWMutex
	byID map[string]int
}

func releaseHelperRegistry() {
	helperRegistry.mu.Unlock()
}

func releaseHelperRegistryRead() {
	helperRegistry.mu.RUnlock()
}

// GoodPackageLockReleasedByHelper must be CLEAN, and so must
// releaseHelperRegistry: every caller takes the lock first.
func GoodPackageLockReleasedByHelper(id string, v int) {
	helperRegistry.mu.Lock()
	helperRegistry.byID[id] = v
	releaseHelperRegistry()
}

// GoodPackageLockReleasedByHelperOnEveryPath must be CLEAN.
func GoodPackageLockReleasedByHelperOnEveryPath(id string) bool {
	helperRegistry.mu.Lock()
	if _, ok := helperRegistry.byID[id]; ok {
		releaseHelperRegistry()
		return false
	}
	helperRegistry.byID[id] = 0
	releaseHelperRegistry()
	return true
}

// GoodPackageReadLockReleasedByHelper must be CLEAN.
func GoodPackageReadLockReleasedByHelper(id string) int {
	helperRegistry.mu.RLock()
	v := helperRegistry.byID[id]
	releaseHelperRegistryRead()
	return v
}

var helperSequence sync.Mutex

func releaseHelperSequence() {
	helperSequence.Unlock()
}

// BadPackageLockHelperSkippedOnError: the error path returns with the lock
// held.
func BadPackageLockHelperSkippedOnError(fail bool) error {
	helperSequence.Lock() // want "mutex 'helperSequence' is locked but not unlocked"
	if fail {
		return errHelperSequence
	}
	releaseHelperSequence()
	return nil
}

var errHelperSequence = errorString("sequence failed")

type errorString string

func (e errorString) Error() string { return string(e) }

var helperUnheld sync.Mutex

// releaseHelperUnheld must be flagged: one of its callers never takes the
// lock, so the helper is not a release on the callers' behalf.
func releaseHelperUnheld() {
	helperUnheld.Unlock() // want "mutex 'helperUnheld' is unlocked but not locked"
}

// GoodPackageLockReleasedByUnheldHelper must be CLEAN itself.
func GoodPackageLockReleasedByUnheldHelper() {
	helperUnheld.Lock()
	releaseHelperUnheld()
}

// BadCallerReleasesWithoutLock calls the helper without the lock. It never
// names the lock itself, so the defect is reported once, in the helper above.
func BadCallerReleasesWithoutLock() {
	releaseHelperUnheld()
}

var helperShadowed sync.Mutex

// releaseShadowedHelper unlocks a local that shadows the package variable, so
// calling it releases nothing the caller holds.
func releaseShadowedHelper() {
	var helperShadowed sync.Mutex
	helperShadowed.Unlock() // want "mutex 'helperShadowed' is unlocked but not locked"
}

// BadPackageLockHelperReleasesShadow keeps the package lock held.
func BadPackageLockHelperReleasesShadow() {
	helperShadowed.Lock() // want "mutex 'helperShadowed' is locked but not unlocked"
	releaseShadowedHelper()
}

// --- helpers that hand the lock on ---

// unlockGivenMutexVia must be CLEAN: it releases through unlockGivenMutex.
func unlockGivenMutexVia(m *sync.Mutex) {
	unlockGivenMutex(m)
}

// unlockCounterVia must be CLEAN: it hands on the value holding the lock.
func unlockCounterVia(c *addressReleasedCounter) {
	releaseCounterLock(c)
}

func releaseCounterLock(c *addressReleasedCounter) {
	c.mu.Unlock()
}

// GoodLockReleasedThroughForwardingHelpers must be CLEAN.
func GoodLockReleasedThroughForwardingHelpers(c *addressReleasedCounter) {
	c.mu.Lock()
	c.n++
	unlockCounterVia(c)

	var local sync.Mutex
	local.Lock()
	unlockGivenMutexVia(&local)
}
