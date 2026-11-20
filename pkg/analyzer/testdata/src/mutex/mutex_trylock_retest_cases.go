package mutex

import "sync"

// A TryLock result kept in a variable takes the lock once, where the call
// ran. The first condition that reads it splits the paths into "taken" and
// "not taken"; any later test of the same variable only says which of those
// paths is running, it does not take the lock again.

type retestedProbe struct {
	mu   sync.RWMutex
	n    int
	done chan struct{}
}

// GoodRetestAfterRelease must be CLEAN: the probe gives the lock back at
// once and the result is read again only to report it.
func (p *retestedProbe) GoodRetestAfterRelease(fail func(string)) {
	free := p.mu.TryLock()
	if free {
		p.mu.Unlock()
	}
	close(p.done)
	if !free {
		fail("lock was held")
	}
}

// GoodRetestReadProbe must be CLEAN: same shape on the read half.
func (p *retestedProbe) GoodRetestReadProbe() bool {
	canRead := p.mu.TryRLock()
	if canRead {
		p.mu.RUnlock()
	}
	p.n++
	if !canRead {
		return false
	}
	return true
}

// GoodRetestToRelease must be CLEAN: the lock is used under one test and
// released under a second test of the same result.
func (p *retestedProbe) GoodRetestToRelease() {
	ok := p.mu.TryLock()
	if ok {
		p.n++
	}
	if ok {
		p.mu.Unlock()
	}
}

// GoodNegatedRetest must be CLEAN: the variable holds the negation.
func (p *retestedProbe) GoodNegatedRetest() int {
	busy := !p.mu.TryLock()
	if !busy {
		p.mu.Unlock()
	}
	if busy {
		return 0
	}
	return 1
}

// BadRetestNeverReleases must be flagged: the second test leaves on failure,
// but the success path still never gives the lock back.
func (p *retestedProbe) BadRetestNeverReleases() {
	ok := p.mu.TryLock() // want "rwmutex 'p.mu' is locked but not unlocked"
	if ok {
		p.n++
	}
	if !ok {
		return
	}
	p.n++
}

// BadRetestReleasesOnFailure must be flagged: the second test releases on
// the path where TryLock failed.
func (p *retestedProbe) BadRetestReleasesOnFailure() {
	ok := p.mu.TryLock()
	if ok {
		p.n++
		p.mu.Unlock()
	}
	if !ok {
		p.mu.Unlock() // want "rwmutex 'p.mu' is unlocked but not locked"
	}
}
