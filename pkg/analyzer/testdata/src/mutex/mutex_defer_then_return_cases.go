package mutex

import "sync"

// A deferred unlock in a loop is only repeated when the loop can start over
// after it. When every way on from the defer leaves the function, it runs at
// most once, even if it sits in a branch nested inside the iteration.

type retryReleaser struct {
	mu    sync.Mutex
	state int
}

// GoodDeferInNestedBranchThenReturn must be CLEAN: after the inner if, the
// outer branch always returns.
func (r *retryReleaser) GoodDeferInNestedBranchThenReturn(cas func(int) bool) {
	for {
		x := r.state
		if cas(x) {
			if x == 1 {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.state = 0
			}
			return
		}
	}
}

// GoodDeferInSwitchCaseThenReturn must be CLEAN.
func (r *retryReleaser) GoodDeferInSwitchCaseThenReturn(next func() int) int {
	for {
		switch v := next(); v {
		case 0:
			continue
		default:
			if v > 10 {
				r.mu.Lock()
				defer r.mu.Unlock()
			}
			return v
		}
	}
}

// BadDeferInNestedBranchThenContinue must be flagged: the outer branch goes
// round again with the lock still held.
func (r *retryReleaser) BadDeferInNestedBranchThenContinue(cas func(int) bool) {
	for {
		x := r.state
		if cas(x) {
			if x == 1 {
				r.mu.Lock()
				defer r.mu.Unlock() // want "mutex 'r.mu' defers unlock inside loop"
				r.state = 0
			}
			continue
		}
		return
	}
}

// BadDeferInNestedBranchThenFallThrough must be flagged: nothing after the
// inner if leaves the function, so the loop starts over.
func (r *retryReleaser) BadDeferInNestedBranchThenFallThrough(cas func(int) bool) {
	for i := 0; i < 3; i++ {
		if cas(i) {
			if i == 1 {
				r.mu.Lock()
				defer r.mu.Unlock() // want "mutex 'r.mu' defers unlock inside loop"
			}
			r.state++
		}
	}
}
