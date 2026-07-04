package cond

import "sync"

type worker struct {
	cond  *sync.Cond
	ready bool
}

// ========== cond-wait-not-in-loop (GCL4001) ==========

// Bad: Wait guarded by a plain if instead of a for.
func BadWaitInIf(ready bool) {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	if !ready {
		c.Wait() // want "cond 'c' Wait called outside a for loop"
	}
	c.L.Unlock()
}

// Bad: Wait with no surrounding condition at all.
func BadWaitNoLoop() {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	c.Wait() // want "cond 'c' Wait called outside a for loop"
	c.L.Unlock()
}

// Bad: Wait inside a goroutine that never loops.
func BadWaitInGoroutine() {
	c := sync.NewCond(&sync.Mutex{})
	go func() {
		c.L.Lock()
		c.Wait() // want "cond 'c' Wait called outside a for loop"
		c.L.Unlock()
	}()
}

// Bad: Wait on a struct field, still not looped.
func (w *worker) BadWaitField() {
	w.cond.L.Lock()
	if !w.ready {
		w.cond.Wait() // want "cond 'w.cond' Wait called outside a for loop"
	}
	w.cond.L.Unlock()
}

// Good: the canonical loop re-checks the condition on every wakeup.
func GoodWaitInForCond(ready bool) {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	for !ready {
		c.Wait()
	}
	c.L.Unlock()
}

// Good: a bare for loop still counts as looping the Wait.
func GoodWaitInForInfinite() {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	for {
		c.Wait()
		break
	}
	c.L.Unlock()
}

// Good: a range loop counts too.
func GoodWaitInRange(items []int) {
	c := sync.NewCond(&sync.Mutex{})
	c.L.Lock()
	for range items {
		c.Wait()
	}
	c.L.Unlock()
}

// Good: the loop lives inside the goroutine that runs the Wait.
func GoodWaitInGoroutineLoop(ready bool) {
	c := sync.NewCond(&sync.Mutex{})
	go func() {
		c.L.Lock()
		for !ready {
			c.Wait()
		}
		c.L.Unlock()
	}()
}

// Good: looped Wait on a struct field.
func (w *worker) GoodWaitField() {
	w.cond.L.Lock()
	for !w.ready {
		w.cond.Wait()
	}
	w.cond.L.Unlock()
}

// Good: sync.WaitGroup also has a Wait(); the type check must keep it out.
func GoodWaitGroupNotFlagged() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done() }()
	wg.Wait()
}

// ========== cond-new-nil-locker (GCL4002) ==========

// Bad: nil Locker makes the first Wait panic.
func BadNewCondNil() {
	_ = sync.NewCond(nil) // want "sync.NewCond called with nil Locker"
}

// Good: a real Locker is passed.
func GoodNewCond() {
	var mu sync.Mutex
	_ = sync.NewCond(&mu)
}
