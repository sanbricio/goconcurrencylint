package mutex

import "sync"

// sync.Cond Wait releases the cond's lock while it waits, so a goroutine that
// needs that lock can take it while the parent waits on the cond.

// GoodGoroutineSignalsWhileParentWaitsOnCond must be CLEAN.
func GoodGoroutineSignalsWhileParentWaitsOnCond() {
	var mu sync.Mutex
	ready := sync.NewCond(&mu)
	mu.Lock()
	go func() {
		mu.Lock()
		ready.Signal()
		mu.Unlock()
	}()
	ready.Wait()
	mu.Unlock()
}

type condQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []int
}

// GoodGoroutinePushesWhileParentWaitsOnFieldCond must be CLEAN: the cond is
// built elsewhere, with the lock its waiters hold.
func (q *condQueue) GoodGoroutinePushesWhileParentWaitsOnFieldCond() int {
	q.mu.Lock()
	go func() {
		q.mu.Lock()
		q.items = append(q.items, 1)
		q.cond.Signal()
		q.mu.Unlock()
	}()
	for len(q.items) == 0 {
		q.cond.Wait()
	}
	v := q.items[0]
	q.mu.Unlock()
	return v
}

// BadGoroutineWaitsWhileParentWaitsOnOtherCond must be flagged: the cond waits
// on another lock, so mu stays held while the parent waits.
func BadGoroutineWaitsWhileParentWaitsOnOtherCond() {
	var mu, other sync.Mutex
	ready := sync.NewCond(&other)
	mu.Lock()
	other.Lock()
	go func() { // want "mutex 'mu' goroutine started while lock is held and also tries to acquire it before parent unlocks"
		mu.Lock()
		other.Lock()
		ready.Signal()
		other.Unlock()
		mu.Unlock()
	}()
	ready.Wait()
	other.Unlock()
	mu.Unlock()
}
