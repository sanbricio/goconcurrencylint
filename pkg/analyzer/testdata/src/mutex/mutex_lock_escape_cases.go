package mutex

import "sync"

// ========== lock released by a returned closure (guard/RAII pattern) ==========

type escapeGuard struct {
	release func()
}

func (g escapeGuard) Unlock() {
	if g.release != nil {
		g.release()
	}
}

type closureLocker struct {
	mu sync.Mutex
}

// Good: the unlock lives in a closure stored in the returned guard, so the
// caller owns the release. This is the sync.RWMutex-wrapper pattern (e.g. loki
// obslock) and must not be flagged as a leak.
func (c *closureLocker) LockGuard() escapeGuard {
	c.mu.Lock()
	return escapeGuard{
		release: func() {
			c.mu.Unlock()
		},
	}
}

// Good: the unlocker closure is returned directly.
func (c *closureLocker) LockFunc() func() {
	c.mu.Lock()
	return func() {
		c.mu.Unlock()
	}
}

// Good: the bound unlock method is returned directly, so the caller owns the
// release. Mirrors minio cmd/local-locker.go getMutex() (`return l.mutex.Unlock`).
func (c *closureLocker) LockReturningUnlockMethod() func() {
	c.mu.Lock()
	return c.mu.Unlock
}

// Good: the bound unlock method handed back through a local variable.
func (c *closureLocker) LockReturningUnlockViaLocal() func() {
	c.mu.Lock()
	unlock := c.mu.Unlock
	return unlock
}

// Bad: the closure that unlocks is never returned or called, so the lock still
// leaks and must be flagged.
func (c *closureLocker) BadLockClosureNotReturned() {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
	_ = func() {
		c.mu.Unlock()
	}
}

// ========== lock released by a child goroutine (ownership handoff) ==========

// Good: the parent locks, then hands the release to a goroutine. A sync.Mutex
// may be unlocked by a goroutine other than the one that locked it, so the
// deferred unlock in the child balances the parent's Lock.
func GoodGoroutineDeferUnlockReleasesParentLock() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		defer mu.Unlock()
	}()
}

// Good: same handoff, but the parent acquires with a checked TryLock guard.
func GoodGoroutineDeferUnlockReleasesParentTryLock() bool {
	var mu sync.Mutex
	if !mu.TryLock() {
		return false
	}
	go func() {
		defer mu.Unlock()
	}()
	return true
}

// ========== release delegated to a sync.Once ==========

type onceLocker struct {
	mu      sync.Mutex
	waiters []chan struct{}
}

// Good: the deferred Once releases the lock on every return path, and the
// early call inside the body only moves that same release earlier.
func (o *onceLocker) LockReleasedByDeferredOnce(ok bool) chan struct{} {
	o.mu.Lock()
	unlock := sync.Once{}
	defer unlock.Do(o.mu.Unlock)

	if !ok {
		return nil
	}

	waiter := make(chan struct{})
	o.waiters = append(o.waiters, waiter)

	unlock.Do(o.mu.Unlock)
	return waiter
}

// Good: the Once fires the release inline, without a deferred counterpart.
func (o *onceLocker) LockReleasedByInlineOnce() {
	o.mu.Lock()
	unlock := sync.Once{}
	o.waiters = nil
	unlock.Do(o.mu.Unlock)
}

// Bad: the Once runs unrelated cleanup, so nothing releases the lock.
func (o *onceLocker) BadLockWithOnceNotReleasing() {
	o.mu.Lock() // want "mutex 'o.mu' is locked but not unlocked"
	done := sync.Once{}
	defer done.Do(func() {
		o.waiters = nil
	})
}

// ========== lock parked in a Locker variable ==========

type quotaChecker struct {
	quotaLock sync.RWMutex
	engines   int
}

// Good: the lock is taken only once the check decides it is needed, recorded in
// a Locker variable, and released by the deferred closure that tests it. The
// alias is the guard, so acquisition and release stay paired.
func (q *quotaChecker) GoodLockParkedInLockerVariable() {
	go func() {
		var locker sync.Locker
		defer func() {
			if locker != nil {
				locker.Unlock()
			}
		}()

		for {
			if q.engines == 0 {
				return
			}
			if locker == nil {
				q.quotaLock.Lock()
				locker = &q.quotaLock
			}
			q.engines--
		}
	}()
}

// Bad: the lock is taken but never recorded in the alias, so the deferred
// release never fires for it and the lock leaks.
func (q *quotaChecker) BadLockNeverRecordedInLocker() {
	var locker sync.Locker
	defer func() { // want "rwmutex 'q.quotaLock' has defer unlock but no corresponding lock"
		if locker != nil {
			locker.Unlock()
		}
	}()

	if q.engines > 0 {
		q.quotaLock.Lock() // want "rwmutex 'q.quotaLock' is locked but not unlocked in if"
	}
	locker = &q.quotaLock
}
