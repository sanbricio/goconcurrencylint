package mutex

import "sync"

// Dropping a held lock to wait and taking it back before going on. What a
// loop break or a merged branch leaves held is measured against what was held
// before: the lock taken back is the one dropped, not a new one.

type relockWaiter struct {
	mu   sync.Mutex
	rw   sync.RWMutex
	ch   chan struct{}
	done bool
	n    int
}

// GoodRelockBeforeBreak must be CLEAN: the loop is entered holding mu and the
// break leaves it holding mu again, which the deferred unlock releases.
func (w *relockWaiter) GoodRelockBeforeBreak() {
	w.mu.Lock()
	defer func() {
		w.mu.Unlock()
	}()
	for {
		w.n++
		w.mu.Unlock()
		if w.done {
			w.mu.Lock()
			break
		}
		<-w.ch
		w.mu.Lock()
	}
}

// GoodReadRelockBeforeBreak must be CLEAN: same shape on the read side.
func (w *relockWaiter) GoodReadRelockBeforeBreak() int {
	w.rw.RLock()
	defer w.rw.RUnlock()
	for {
		w.rw.RUnlock()
		<-w.ch
		w.rw.RLock()
		if w.done {
			break
		}
	}
	return w.n
}

// GoodSelectArmRelocksAndReturns must be CLEAN: the arm that takes mu back
// returns under the deferred unlock; only the other arm reaches the Lock below.
func (w *relockWaiter) GoodSelectArmRelocksAndReturns(stop chan struct{}) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		w.mu.Unlock()
		select {
		case <-stop:
			w.mu.Lock()
			return 0
		case <-w.ch:
		}
		w.mu.Lock()
		if w.done {
			return w.n
		}
	}
}

// GoodSwitchArmRelocksAndReturns must be CLEAN: same as above with a switch.
func (w *relockWaiter) GoodSwitchArmRelocksAndReturns() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mu.Unlock()
	switch {
	case w.done:
		w.mu.Lock()
		return 1
	default:
	}
	w.mu.Lock()
	return 2
}

// GoodThenRelocksAndReturns must be CLEAN: the else path is the one that goes
// on, and it does not hold mu.
func (w *relockWaiter) GoodThenRelocksAndReturns() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mu.Unlock()
	if w.done {
		w.mu.Lock()
		return 1
	} else {
		w.n++
	}
	w.mu.Lock()
	return 2
}

// goodWaitLocked must be CLEAN: called with mu held and returns with it held;
// it drops mu while waiting and takes it back on every way out.
func (w *relockWaiter) goodWaitLocked(stop chan struct{}) {
	w.mu.Unlock()
	for {
		select {
		case <-stop:
			w.mu.Lock()
			return
		case <-w.ch:
		}
		w.mu.Lock()
		if w.done {
			break
		}
		w.mu.Unlock()
	}
	w.n++
}

// BadBreakCarriesLockTakenInLoop must be flagged: mu was not held at the loop
// head, and the break leaves the loop holding it.
func (w *relockWaiter) BadBreakCarriesLockTakenInLoop() {
	for {
		w.mu.Lock() // want "mutex 'w.mu' is locked but not unlocked"
		if w.done {
			break
		}
		w.mu.Unlock()
		<-w.ch
	}
	w.n++
}

// BadRelockTwiceBeforeBreak must be flagged: the arm that breaks takes mu a
// second time while holding it.
func (w *relockWaiter) BadRelockTwiceBeforeBreak() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		w.mu.Unlock()
		<-w.ch
		w.mu.Lock()
		if w.done {
			w.mu.Lock() // want "mutex 'w.mu' is locked but not unlocked" "mutex 'w.mu' is re-locked before unlock"
			break
		}
	}
}

// BadFallThroughArmKeepsLock must be flagged: the arm that returns releases,
// the arm that goes on keeps mu.
func (w *relockWaiter) BadFallThroughArmKeepsLock() {
	w.mu.Lock()
	w.mu.Unlock()
	select {
	case <-w.ch:
		w.mu.Lock()
		w.mu.Unlock()
		return
	default:
		w.mu.Lock() // want "mutex 'w.mu' is locked but not unlocked"
	}
	w.n++
}
