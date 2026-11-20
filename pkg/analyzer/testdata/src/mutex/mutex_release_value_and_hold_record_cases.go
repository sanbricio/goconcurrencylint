package mutex

import (
	"errors"
	"sync"
)

// A function returning holding a lock can hand back its release as a method
// of the lock's owner, directly or through a field caching that method.

type releaseValueTx struct {
	closemu sync.RWMutex
	done    bool
}

// GoodReturnsReleaseMethod must be CLEAN.
func (tx *releaseValueTx) GoodReturnsReleaseMethod() (func(error), error) {
	tx.closemu.RLock()
	if tx.done {
		tx.closemu.RUnlock()
		return nil, errors.New("done")
	}
	return tx.releaseRead, nil
}

// releaseRead must be CLEAN.
func (tx *releaseValueTx) releaseRead(error) {
	tx.closemu.RUnlock()
}

type releaseValueConn struct {
	closemu sync.RWMutex
	once    sync.Once
	cache   func(error)
}

// GoodReturnsCachedReleaseMethod must be CLEAN.
func (c *releaseValueConn) GoodReturnsCachedReleaseMethod() func(error) {
	c.once.Do(func() {
		c.cache = c.releaseConn
	})
	c.closemu.RLock()
	return c.cache
}

// releaseConn must be CLEAN.
func (c *releaseValueConn) releaseConn(error) {
	c.closemu.RUnlock()
}

type releaseValueWrong struct {
	closemu sync.RWMutex
	other   sync.Mutex
}

// BadReturnsUnrelatedMethod must be flagged: the returned method releases
// another lock.
func (w *releaseValueWrong) BadReturnsUnrelatedMethod() func() {
	w.closemu.RLock() // want "rwmutex 'w.closemu' is rlocked but not runlocked"
	return w.releaseOther
}

func (w *releaseValueWrong) releaseOther() {
	w.other.Unlock() // want "mutex 'w.other' is unlocked but not locked"
}

// A field can record that the lock stays held between calls.

type holdRecordRows struct {
	closemu sync.RWMutex
	held    bool
	n       int
}

// GoodScanKeepsHold must be CLEAN: the hold is recorded in r.held.
func (r *holdRecordRows) GoodScanKeepsHold(keep bool) {
	r.closemu.RLock()
	r.n++
	if keep {
		r.held = true
	} else {
		r.closemu.RUnlock()
	}
}

// releaseHeld must be CLEAN.
func (r *holdRecordRows) releaseHeld() {
	if r.held {
		r.held = false
		r.closemu.RUnlock()
	}
}

type holdRecordStore struct {
	pendingLock sync.Mutex
	lock        sync.Mutex
	pending     *int
}

// GoodAddPending must be CLEAN: the hold travels with c.pending.
func (c *holdRecordStore) GoodAddPending(v int) error {
	c.pendingLock.Lock()
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.pending != nil {
		c.pendingLock.Unlock()
		return errors.New("busy")
	}
	c.pending = &v
	return nil
}

// GoodCommitPending must be CLEAN.
func (c *holdRecordStore) GoodCommitPending() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.pending == nil {
		return errors.New("nothing pending")
	}
	c.pending = nil
	c.pendingLock.Unlock()
	return nil
}

type holdRecordNoClear struct {
	mu   sync.Mutex
	held bool
}

// BadReleaseWithoutClearing must be flagged: the field is never cleared, so
// it is not a record of the hold.
func (r *holdRecordNoClear) BadReleaseWithoutClearing() {
	if r.held {
		r.mu.Unlock() // want "mutex 'r.mu' is unlocked but not locked"
	}
}

// BadSetWithoutRecord must be flagged.
func (r *holdRecordNoClear) BadSetWithoutRecord() {
	r.mu.Lock() // want "mutex 'r.mu' is locked but not unlocked"
	r.held = true
}
