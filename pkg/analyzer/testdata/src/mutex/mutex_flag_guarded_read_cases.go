package mutex

import "sync"

// A deferred release guarded by a flag that every acquisition sets works the
// same on both halves of an RWMutex:
//
//	defer func() { if held { mu.RUnlock() } }()
//	mu.RLock()
//	held = true

type flagGuardedReader struct {
	mu   sync.Mutex
	data sync.RWMutex
	n    int
}

// GoodReadFlagGuardInIf must be CLEAN.
func (r *flagGuardedReader) GoodReadFlagGuardInIf(v int) {
	var held bool
	defer func() {
		if held {
			r.data.RUnlock()
		}
	}()
	if v > 0 {
		r.data.RLock()
		held = true
	}
	r.n++
}

// GoodReadFlagGuardInSelectWithRetry must be CLEAN: the read lock is dropped
// and the flag cleared before going back to wait again.
func (r *flagGuardedReader) GoodReadFlagGuardInSelectWithRetry(ch chan int) {
	var held bool
	defer func() {
		if held {
			r.data.RUnlock()
		}
	}()
WAIT:
	select {
	case v := <-ch:
		r.data.RLock()
		held = true
		r.mu.Lock()
		if v > 0 {
			r.mu.Unlock()
			r.data.RUnlock()
			held = false
			goto WAIT
		}
		r.n = v
		r.mu.Unlock()
	}
}

// GoodWriteFlagGuardExplicitRelease must be CLEAN: the write half, released
// early with the flag cleared.
func (r *flagGuardedReader) GoodWriteFlagGuardExplicitRelease(v int) {
	var held bool
	defer func() {
		if held {
			r.data.Unlock()
		}
	}()
	r.data.Lock()
	held = true
	if v > 0 {
		r.data.Unlock()
		held = false
		return
	}
	r.n++
}

// GoodReadFlagGuardExplicitRelease must be CLEAN: the read half, released
// early with the flag cleared.
func (r *flagGuardedReader) GoodReadFlagGuardExplicitRelease(v int) {
	var held bool
	defer func() {
		if held {
			r.data.RUnlock()
		}
	}()
	r.data.RLock()
	held = true
	if v > 0 {
		r.data.RUnlock()
		held = false
		return
	}
	r.n++
}

// BadReadFlagNeverSet must be flagged: the read lock is taken without
// setting the flag, so the deferred release never runs for it.
func (r *flagGuardedReader) BadReadFlagNeverSet(v int) {
	var held bool
	defer func() { // want "rwmutex 'r.data' has defer runlock but no corresponding rlock"
		if held {
			r.data.RUnlock()
		}
	}()
	if v > 0 {
		r.data.RLock() // want "rwmutex 'r.data' is rlocked but not runlocked"
	}
	r.n++
}

// GoodReadFlagGuardInsideGoroutine must be CLEAN: the goroutine registers the
// guarded release in its own frame.
func (r *flagGuardedReader) GoodReadFlagGuardInsideGoroutine(ch chan int) {
	go func() {
		var held bool
		defer func() {
			if held {
				r.data.RUnlock()
			}
		}()
		select {
		case v := <-ch:
			r.data.RLock()
			held = true
			r.n = v
		}
	}()
}

// BadReadFlagNeverSetInsideGoroutine must be flagged.
func (r *flagGuardedReader) BadReadFlagNeverSetInsideGoroutine(ch chan int) {
	go func() {
		var held bool
		defer func() { // want "rwmutex 'r.data' has defer runlock but no corresponding rlock"
			if held {
				r.data.RUnlock()
			}
		}()
		select {
		case v := <-ch:
			r.data.RLock() // want "rwmutex 'r.data' is rlocked but not runlocked in goroutine"
			r.n = v
		}
	}()
}
