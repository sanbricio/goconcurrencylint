package mutex

import "sync"

// A TryLock result stored negated (`busy := !mu.TryLock()`) holds the lock
// when the variable is false: the branch that sees it false owns the lock.

type negatedTry struct {
	mu   sync.Mutex
	rw   sync.RWMutex
	seen bool
	n    int
}

// GoodNegatedStoredReleasedWhenFalse must be CLEAN.
func (s *negatedTry) GoodNegatedStoredReleasedWhenFalse() bool {
	held := !s.mu.TryLock()
	s.seen = held
	if !held {
		s.mu.Unlock()
	}
	return held
}

// GoodNegatedReadLockEarlyReturn must be CLEAN: the busy branch returns, the
// other one holds the read lock and releases it.
func (s *negatedTry) GoodNegatedReadLockEarlyReturn() {
	busy := !s.rw.TryRLock()
	if busy {
		return
	}
	s.n++
	s.rw.RUnlock()
}

// BadUnlockWhenNotAcquired must be flagged: busy means TryLock failed.
func (s *negatedTry) BadUnlockWhenNotAcquired() {
	busy := !s.mu.TryLock()
	if busy {
		s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
	}
}

// BadNegatedLeakOnSuccess must be flagged: the path that got the lock never
// gives it back.
func (s *negatedTry) BadNegatedLeakOnSuccess() {
	busy := !s.mu.TryLock() // want "mutex 's.mu' is locked but not unlocked"
	if busy {
		return
	}
	s.n++
}
