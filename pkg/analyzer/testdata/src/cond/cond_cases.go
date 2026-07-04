package cond

import "sync"

// ========== cond-new-nil-locker (GCL4001) ==========

// Bad: nil Locker makes the first Wait panic.
func BadNewCondNil() {
	_ = sync.NewCond(nil) // want "sync.NewCond called with nil Locker"
}

// Good: a real Locker is passed.
func GoodNewCond() {
	var mu sync.Mutex
	_ = sync.NewCond(&mu)
}

// Regression guard: a Wait outside a loop must NOT be flagged. The not-in-loop
// heuristic was removed because it fired on legitimate one-shot patterns
// (stateful conds, one-shot signals, Wait wrappers, test helpers).
func WaitOutsideLoopNotFlagged(ready bool) {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	if !ready {
		c.Wait()
	}
	c.L.Unlock()
}
