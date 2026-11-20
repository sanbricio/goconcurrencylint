package mutex

import "sync"

// A function literal called where it is written, `func() { ... }()`, runs
// there once, like a block: what it does to a lock, and what it leaves in the
// variables it assigns, is what the code after the call starts from.

type inlineIndex struct {
	drop sync.RWMutex
}

type inlineRegistry struct {
	mu      sync.RWMutex
	indices map[string]*inlineIndex
}

// GoodLiteralTakesItemLock must be CLEAN: the literal takes the item's read
// lock under the registry lock, and the caller defers its release.
func (r *inlineRegistry) GoodLiteralTakesItemLock(name string) {
	var idx *inlineIndex
	func() {
		r.mu.RLock()
		defer r.mu.RUnlock()
		idx = r.indices[name]
		idx.drop.RLock()
	}()
	defer idx.drop.RUnlock()
}

// GoodLiteralLocksWhenFound must be CLEAN: the item lock is taken only when the
// item exists, and the caller returns early on exactly that condition.
func (r *inlineRegistry) GoodLiteralLocksWhenFound(name string) {
	var (
		idx    *inlineIndex
		exists bool
	)
	func() {
		r.mu.RLock()
		defer r.mu.RUnlock()
		idx, exists = r.indices[name]
		if exists {
			idx.drop.RLock()
		}
	}()
	if !exists {
		return
	}
	defer idx.drop.RUnlock()
}

// GoodLiteralSetsHeldFlag must be CLEAN.
func (r *inlineRegistry) GoodLiteralSetsHeldFlag(ok bool) {
	held := false
	func() {
		if ok {
			r.mu.Lock()
			held = true
		}
	}()
	if held {
		r.mu.Unlock()
	}
}

// BadLiteralWrongPolarity must be flagged: the caller keeps going when the
// item was missing, the one case where no lock was taken.
func (r *inlineRegistry) BadLiteralWrongPolarity(name string) {
	var (
		idx    *inlineIndex
		exists bool
	)
	func() {
		r.mu.RLock()
		defer r.mu.RUnlock()
		idx, exists = r.indices[name]
		if exists {
			idx.drop.RLock()
		}
	}()
	if exists {
		return
	}
	defer idx.drop.RUnlock() // want "rwmutex 'idx.drop' has defer runlock but no corresponding rlock"
}

// BadLiteralLeaksLock must be flagged: nothing releases the lock the literal
// takes.
func (r *inlineRegistry) BadLiteralLeaksLock() {
	func() {
		r.mu.Lock() // want "rwmutex 'r.mu' is locked but not unlocked"
	}()
}

// BadLiteralMayReturnBeforeLock must be flagged: on the early return the
// literal took nothing, and the Unlock after it has nothing to release.
func (r *inlineRegistry) BadLiteralMayReturnBeforeLock(ok bool) {
	func() {
		if !ok {
			return
		}
		r.mu.Lock()
	}()
	r.mu.Unlock() // want "rwmutex 'r.mu' is unlocked but not locked"
}
