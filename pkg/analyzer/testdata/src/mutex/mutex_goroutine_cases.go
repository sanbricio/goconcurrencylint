package mutex

import "sync"

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
