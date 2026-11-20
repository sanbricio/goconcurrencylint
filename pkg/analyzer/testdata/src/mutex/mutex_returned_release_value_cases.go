package mutex

import (
	"errors"
	"sync"
)

// A value handed back holding the bound release of the lock it was returned
// under (`&cursor{release: mu.RUnlock}`) delegates the release to whoever
// closes it, exactly like a closure that calls it.

type heldCursor struct {
	items   []int
	release func()
}

func (c *heldCursor) Close() { c.release() }

type cursorSource struct {
	mu     sync.RWMutex
	other  sync.Mutex
	items  []int
	closed bool
}

// GoodReturnCursorWithBoundRelease must be CLEAN.
func (s *cursorSource) GoodReturnCursorWithBoundRelease() *heldCursor {
	s.mu.RLock()
	return &heldCursor{items: s.items, release: s.mu.RUnlock}
}

// openRead returns nil holding the read lock and an error holding nothing.
func (s *cursorSource) openRead() error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errors.New("closed")
	}
	return nil
}

// GoodReadAfterAcquireHelper must be CLEAN.
func (s *cursorSource) GoodReadAfterAcquireHelper() int {
	if err := s.openRead(); err != nil {
		return 0
	}
	defer s.mu.RUnlock()
	return len(s.items)
}

// GoodCursorAfterAcquireHelper must be CLEAN: the helper takes the lock and
// the cursor carries its release.
func (s *cursorSource) GoodCursorAfterAcquireHelper() (*heldCursor, error) {
	if err := s.openRead(); err != nil {
		return nil, err
	}
	return &heldCursor{items: s.items, release: s.mu.RUnlock}, nil
}

// BadCursorCarriesOtherRelease must be flagged: the cursor releases another
// lock, so nothing gives the read lock back.
func (s *cursorSource) BadCursorCarriesOtherRelease() *heldCursor {
	s.mu.RLock() // want "rwmutex 's.mu' is rlocked but not runlocked"
	s.other.Lock()
	return &heldCursor{items: s.items, release: s.other.Unlock}
}

// BadCursorCarriesAcquire must be flagged: the stored method takes the lock
// again instead of releasing it.
func (s *cursorSource) BadCursorCarriesAcquire() *heldCursor {
	s.mu.RLock() // want "rwmutex 's.mu' is rlocked but not runlocked"
	return &heldCursor{items: s.items, release: s.mu.RLock}
}
