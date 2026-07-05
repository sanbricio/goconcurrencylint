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
