package mutex

import (
	"context"
	"errors"
	"sync"
)

// An acquire-or-fail helper may use both halves of an RWMutex: once Lock,
// Unlock, RLock or RUnlock has returned, the goroutine does not hold the other
// half, so a return reached after one of them is judged by it.

type lazyStore struct {
	mu sync.RWMutex
	db *int
}

func openLazyStore(s *lazyStore) error {
	if s.db == nil {
		return errors.New("unavailable")
	}
	return nil
}

// ensureOpen returns nil holding the read lock and an error holding nothing:
// a double-checked lazy open.
func (s *lazyStore) ensureOpen() error {
	for {
		s.mu.RLock()
		if s.db != nil {
			return nil
		}
		s.mu.RUnlock()
		s.mu.Lock()
		if s.db == nil {
			if err := openLazyStore(s); err != nil {
				s.mu.Unlock()
				return err
			}
		}
		s.mu.Unlock()
	}
}

// GoodReadAfterDoubleCheckedOpen must be CLEAN.
func (s *lazyStore) GoodReadAfterDoubleCheckedOpen() int {
	if err := s.ensureOpen(); err != nil {
		return 0
	}
	defer s.mu.RUnlock()
	return *s.db
}

// openWithoutRead reports success after giving the write lock back: it holds
// nothing on any return.
func (s *lazyStore) openWithoutRead() error {
	s.mu.Lock()
	if err := openLazyStore(s); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	return nil
}

// BadReadReleaseAfterWriteOnlyHelper must be flagged: nothing took the read
// lock it releases.
func (s *lazyStore) BadReadReleaseAfterWriteOnlyHelper() int {
	if err := s.openWithoutRead(); err != nil {
		return 0
	}
	defer s.mu.RUnlock() // want "rwmutex 's.mu' has defer runlock but no corresponding rlock"
	return *s.db
}

// A loop `for !mu.TryLock() { ... }` that only leaves by return ends holding
// the lock; the returns in its body hold nothing.

type retryLocked struct {
	mu sync.RWMutex
	n  int
}

func (r *retryLocked) lockUntilDone(ctx context.Context, tick <-chan struct{}) error {
	for !r.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
	}
	return nil
}

// GoodUseRetryLockHelper must be CLEAN.
func (r *retryLocked) GoodUseRetryLockHelper(ctx context.Context, tick <-chan struct{}) error {
	if err := r.lockUntilDone(ctx, tick); err != nil {
		return err
	}
	defer r.mu.Unlock()
	r.n++
	return nil
}

func (r *retryLocked) lockOrGiveUp() error {
	tries := 0
	for !r.mu.TryLock() {
		tries++
		if tries > 3 {
			break
		}
	}
	return nil
}

// BadUseGiveUpHelper must be flagged: the helper's break leaves without the
// lock and still reports success.
func (r *retryLocked) BadUseGiveUpHelper() {
	if err := r.lockOrGiveUp(); err != nil {
		return
	}
	defer r.mu.Unlock() // want "rwmutex 'r.mu' has defer unlock but no corresponding lock"
	r.n++
}

// A guard `if !mu.TryLock() { return false }` hands the lock over on the
// success return after it.

type busyGuard struct {
	mu sync.Mutex
	n  int
}

func (g *busyGuard) tryBegin() bool {
	if !g.mu.TryLock() {
		g.n++
		return false
	}
	return true
}

// GoodUseTryGuardHelper must be CLEAN.
func (g *busyGuard) GoodUseTryGuardHelper() {
	if !g.tryBegin() {
		return
	}
	defer g.mu.Unlock()
	g.n++
}

func (g *busyGuard) tryBeginInverted() bool {
	if !g.mu.TryLock() {
		return true
	}
	return false
}

// BadUseInvertedTryGuardHelper must be flagged: the helper reports success on
// the path where TryLock failed.
func (g *busyGuard) BadUseInvertedTryGuardHelper() {
	if !g.tryBeginInverted() {
		return
	}
	defer g.mu.Unlock() // want "mutex 'g.mu' has defer unlock but no corresponding lock"
	g.n++
}

// A local bound once to sync.OnceFunc(mu.Unlock) releases mu, deferred or
// called early, exactly once.

// GoodOnceFuncReleaseEarlyAndDeferred must be CLEAN.
func (g *busyGuard) GoodOnceFuncReleaseEarlyAndDeferred(wait func()) {
	g.mu.Lock()
	unlock := sync.OnceFunc(g.mu.Unlock)
	defer unlock()
	if g.n > 3 {
		return
	}
	g.n++
	unlock()
	wait()
}

// BadOnceFuncReleasesOtherLock must be flagged: the bound release belongs to
// another lock.
func (g *busyGuard) BadOnceFuncReleasesOtherLock(other *sync.Mutex) {
	g.mu.Lock() // want "mutex 'g.mu' is locked but not unlocked"
	unlock := sync.OnceFunc(other.Unlock)
	defer unlock() // want "mutex 'other' has defer unlock but no corresponding lock"
	g.n++
}

type onceGuarded struct {
	mu sync.RWMutex
	n  int
}

// GoodOnceFuncEarlyReleaseThenReadLock must be CLEAN: the early call releases
// the write lock, so the read lock after it does not wait on this goroutine.
func (o *onceGuarded) GoodOnceFuncEarlyReleaseThenReadLock() int {
	o.mu.Lock()
	unlock := sync.OnceFunc(o.mu.Unlock)
	defer unlock()
	o.n++
	unlock()
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.n
}

// BadReadLockBeforeOnceFuncFires must be flagged: the write lock is still held
// when the read lock is requested.
func (o *onceGuarded) BadReadLockBeforeOnceFuncFires() int {
	o.mu.Lock()
	unlock := sync.OnceFunc(o.mu.Unlock)
	defer unlock()
	o.n++
	o.mu.RLock() // want "rwmutex 'o.mu' attempts read RLock while write lock is held"
	defer o.mu.RUnlock()
	return o.n
}
