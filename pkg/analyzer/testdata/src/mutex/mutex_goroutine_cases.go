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

func GoodGoroutineLocksAfterParentReleases() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		mu.Lock()
		mu.Unlock()
	}()
	mu.Unlock()
}

func BadGoroutineDeadlockParentNeverUnlocks() {
	var mu sync.Mutex
	mu.Lock()   // want "mutex 'mu' is locked but not unlocked"
	go func() { // want "mutex 'mu' goroutine started while lock is held and also tries to acquire it, will deadlock if parent never releases"
		mu.Lock()
		mu.Unlock()
	}()
}

func BadGoroutineWaitsBeforeParentUnlocks() {
	var mu sync.Mutex
	done := make(chan struct{})
	mu.Lock()
	go func() { // want "mutex 'mu' goroutine started while lock is held and also tries to acquire it before parent unlocks"
		mu.Lock()
		mu.Unlock()
		close(done)
	}()
	<-done
	mu.Unlock()
}

func BadGoroutineWaitGroupWaitsBeforeParentUnlocks() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	mu.Lock()
	go func() { // want "mutex 'mu' goroutine started while lock is held and also tries to acquire it before parent unlocks"
		defer wg.Done()
		mu.Lock()
		mu.Unlock()
	}()
	wg.Wait()
	mu.Unlock()
}

func BadCrossGoroutineUnlockCompletedBeforeLaterLockOrder() {
	var fsMu, blockMu sync.RWMutex
	var ready sync.WaitGroup
	var wg sync.WaitGroup

	ready.Add(1)
	wg.Add(1)
	blockMu.Lock()
	go func() {
		ready.Done()
		defer wg.Done()
		fsMu.Lock()
		fsMu.Unlock()
		blockMu.Unlock() // want "mutex 'blockMu' is unlocked in a different goroutine than it was locked"
	}()

	ready.Wait()
	wg.Wait()

	fsMu.RLock()
	blockMu.RLock()
	blockMu.RUnlock()
	fsMu.RUnlock()
}

func BadFutureWaitGroupReleaseDoesNotBreakCurrentLockOrder() {
	var a, b sync.Mutex
	var wg sync.WaitGroup

	b.Lock()
	a.Lock()
	a.Unlock()
	b.Unlock()

	wg.Add(1)
	a.Lock()
	wg.Wait()

	b.Lock() // want "mutex lock order cycle between 'a' and 'b'"
	b.Unlock()

	go func() {
		defer wg.Done()
		a.Unlock() // want "mutex 'a' is unlocked in a different goroutine than it was locked"
	}()
}

type customWaiter struct{}

func (customWaiter) Wait() {}

func GoodGoroutineParentCallsCustomWaitBeforeUnlock() {
	var mu sync.Mutex
	var waiter customWaiter
	mu.Lock()
	go func() {
		mu.Lock()
		mu.Unlock()
	}()
	waiter.Wait()
	mu.Unlock()
}

func GoodConsistentLockOrderAcrossGoroutines() {
	var muA, muB sync.Mutex
	go func() {
		muA.Lock()
		muB.Lock()
		muB.Unlock()
		muA.Unlock()
	}()
	go func() {
		muA.Lock()
		muB.Lock()
		muB.Unlock()
		muA.Unlock()
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

func BadGoroutineLockWithoutUnlockAfterParentReleases() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in goroutine"
	}()
	mu.Unlock()
}

func BadCrossGoroutineUnlock() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		mu.Unlock() // want "mutex 'mu' is unlocked in a different goroutine than it was locked"
	}()
}

func BadCrossGoroutineUnlockThenParentUnlock() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		mu.Unlock() // want "mutex 'mu' is unlocked in a different goroutine than it was locked"
	}()
	mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
}

func BadGoroutineLockOrderCycle() {
	var muA, muB sync.Mutex
	go func() {
		muA.Lock()
		muB.Lock()
		muB.Unlock()
		muA.Unlock()
	}()
	go func() {
		muB.Lock()
		muA.Lock() // want "mutex lock order cycle between 'muA' and 'muB'"
		muA.Unlock()
		muB.Unlock()
	}()
}

func BadTryRLockOrderCycle() {
	var mu sync.Mutex
	var rw sync.RWMutex
	go func() {
		if rw.TryRLock() {
			mu.Lock()
			mu.Unlock()
			rw.RUnlock()
		}
	}()
	go func() {
		mu.Lock()
		rw.Lock() // want "mutex lock order cycle between 'mu' and 'rw'"
		rw.Unlock()
		mu.Unlock()
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
