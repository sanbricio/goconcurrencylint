package mutex

import "sync"

// A deferred unlock inside a loop deadlocks when the next iteration takes the
// same lock again. When the lock is named through a variable bound on each
// iteration, each pass may take a different lock, and deferring every unlock
// to return is how a set of locks is held together.

type lockHolder struct {
	lock sync.Mutex
}

type lockSet struct {
	mu      sync.Mutex
	stripes []sync.Mutex
	byKey   map[string]*sync.Mutex
}

// GoodHoldEveryRangeElement must be CLEAN.
func GoodHoldEveryRangeElement(holders []*lockHolder) int {
	for _, h := range holders {
		h.lock.Lock()
		defer h.lock.Unlock()
	}
	return len(holders)
}

// GoodHoldEveryStripeThroughLocal must be CLEAN: l is derived from the loop
// index.
func (s *lockSet) GoodHoldEveryStripeThroughLocal() {
	for i := 0; i < len(s.stripes); i++ {
		l := &s.stripes[i]
		l.Lock()
		defer l.Unlock()
	}
}

// GoodHoldEveryKeyLock must be CLEAN: each key picks its own lock.
func (s *lockSet) GoodHoldEveryKeyLock(keys []string) {
	for _, k := range keys {
		m := s.byKey[k]
		m.Lock()
		defer m.Unlock()
	}
}

// BadSameLockEveryIteration must be flagged: the second pass blocks on the
// lock the first pass still holds.
func (s *lockSet) BadSameLockEveryIteration(items []int) {
	for _, it := range items {
		_ = it
		s.mu.Lock()
		defer s.mu.Unlock() // want "mutex 's.mu' defers unlock inside loop"
	}
}

// BadSameLockThroughLocal must be flagged: the local is rebound each pass, but
// always to the same lock.
func (s *lockSet) BadSameLockThroughLocal(items []int) {
	for range items {
		m := &s.mu
		m.Lock()
		defer m.Unlock() // want "mutex 'm' defers unlock inside loop"
	}
}

type chainNode struct {
	mu     sync.Mutex
	parent *chainNode
	n      int
}

// GoodHoldEveryAncestor must be CLEAN: the loop variable moves to the parent
// each pass.
func (c *chainNode) GoodHoldEveryAncestor() *chainNode {
	for p := c.parent; p != nil; p = p.parent {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.n == 0 {
			return p
		}
	}
	return nil
}

// BadLoopVariableNeverMoves must be flagged: the init variable is never
// reassigned, so every pass takes the same lock.
func (c *chainNode) BadLoopVariableNeverMoves() {
	for p := c.parent; c.n < 3; c.n++ {
		p.mu.Lock()
		defer p.mu.Unlock() // want "mutex 'p.mu' defers unlock inside loop"
	}
}
