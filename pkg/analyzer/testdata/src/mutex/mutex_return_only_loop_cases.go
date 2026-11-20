package mutex

import "sync"

// A helper whose loop is left only by return ends where its returns end: what
// it does to a lock its caller holds is what holds at those returns, not an
// empty state.

type returnOnlyLoop struct {
	mu    sync.Mutex
	ch    chan int
	queue []int
	n     int
}

func (r *returnOnlyLoop) drain() {
	for {
		select {
		case v := <-r.ch:
			r.n += v
		default:
			return
		}
	}
}

func (r *returnOnlyLoop) popReady() {
	for {
		if len(r.queue) == 0 {
			return
		}
		if r.queue[0] < 0 {
			return
		}
		r.queue = r.queue[1:]
	}
}

// GoodCallsSelectReturnLoop must be CLEAN: drain never touches mu.
func (r *returnOnlyLoop) GoodCallsSelectReturnLoop() {
	r.mu.Lock()
	r.drain()
	r.mu.Unlock()
}

// GoodCallsIfReturnLoop must be CLEAN: popReady never touches mu either.
func (r *returnOnlyLoop) GoodCallsIfReturnLoop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.popReady()
	r.n++
}

// BadNeverUnlocksAfterReturnOnlyLoop must be flagged: the helper keeps mu
// held, so nothing ever releases it.
func (r *returnOnlyLoop) BadNeverUnlocksAfterReturnOnlyLoop() {
	r.mu.Lock() // want "mutex 'r.mu' is locked but not unlocked"
	r.drain()
	r.n++
}

// BadUnlocksTwiceAfterReturnOnlyLoop must be flagged: only the first Unlock
// has a Lock to match.
func (r *returnOnlyLoop) BadUnlocksTwiceAfterReturnOnlyLoop() {
	r.mu.Lock()
	r.popReady()
	r.mu.Unlock()
	r.mu.Unlock() // want "mutex 'r.mu' is unlocked but not locked"
}
