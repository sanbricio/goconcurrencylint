package mutex

import "sync"

// A method that returns holding its receiver's lock, released by every caller
// right after the call, is an acquire helper: the callers own the release.
// A method called with the lock held may drop it and take it back on return
// from a deferred literal.

type waitingProc struct {
	mu       sync.Mutex
	deleting chan struct{}
	n        int
}

// lock waits for a running delete to give the lock up, dropping it meanwhile,
// and returns holding it. Must be CLEAN: both callers release.
func (p *waitingProc) lock() {
	p.mu.Lock()
	for p.deleting != nil {
		d := p.deleting
		p.mu.Unlock()
		<-d
		p.mu.Lock()
	}
}

func (p *waitingProc) GoodDeleteUnderLock() {
	p.lock()
	defer p.mu.Unlock()
	p.dropWhileDeleting()
}

func (p *waitingProc) GoodCountUnderLock() int {
	p.lock()
	n := p.n
	p.mu.Unlock()
	return n
}

// dropWhileDeleting is called with mu held and returns with it held. Must be
// CLEAN.
func (p *waitingProc) dropWhileDeleting() {
	done := make(chan struct{})
	p.deleting = done
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.deleting = nil
		close(done)
	}()
	p.n++
}

type leakyProc struct {
	mu sync.Mutex
	n  int
}

// lockAndCount must be flagged: one of its callers never releases.
func (p *leakyProc) lockAndCount() {
	p.mu.Lock() // want "mutex 'p.mu' is locked but not unlocked"
	p.n++
}

func (p *leakyProc) GoodReleasesAfter() {
	p.lockAndCount()
	defer p.mu.Unlock()
	p.n++
}

func (p *leakyProc) ForgetsRelease() {
	p.lockAndCount()
	p.n++
}

// A method called with its receiver's lock held that releases it is a release
// helper wherever the call sits: as a statement, in an if condition or on the
// right of an assignment.

type releasingQueue struct {
	mu      sync.Mutex
	pending []int
	sent    int
}

// enqueueAndUnlock must be CLEAN: every caller holds mu when it calls.
func (q *releasingQueue) enqueueAndUnlock(v int) bool {
	q.pending = append(q.pending, v)
	q.mu.Unlock()
	q.sent++
	return q.sent < 100
}

func (q *releasingQueue) GoodReleaseInCondition(vs []int) bool {
	for _, v := range vs {
		q.mu.Lock()
		if v < 0 {
			q.mu.Unlock()
			continue
		}
		if !q.enqueueAndUnlock(v) {
			return false
		}
	}
	return true
}

func (q *releasingQueue) GoodReleaseInAssignment(v int) bool {
	q.mu.Lock()
	ok := q.enqueueAndUnlock(v)
	return ok
}

type misusedQueue struct {
	mu      sync.Mutex
	pending []int
}

// flushAndUnlock must be flagged: its only caller never took the lock.
func (q *misusedQueue) flushAndUnlock() bool {
	q.pending = nil
	q.mu.Unlock() // want "mutex 'q.mu' is unlocked but not locked"
	return true
}

func (q *misusedQueue) CallsWithoutLock() bool {
	if !q.flushAndUnlock() {
		return false
	}
	return true
}

type callbackPublisher struct {
	mu   sync.Mutex
	errs []error
}

// failAndUnlock must be CLEAN: both callers hold mu where they call it, one of
// them inside a callback that takes mu itself.
func (p *callbackPublisher) failAndUnlock(err error) {
	p.errs = append(p.errs, err)
	p.mu.Unlock()
}

func (p *callbackPublisher) GoodFailNow(err error) {
	p.mu.Lock()
	p.failAndUnlock(err)
}

func (p *callbackPublisher) GoodFailLater(schedule func(func()), err error) {
	schedule(func() {
		p.mu.Lock()
		p.failAndUnlock(err)
	})
}

type lateCallbackPublisher struct {
	mu   sync.Mutex
	errs []error
}

// failLateAndUnlock must be flagged: the callback that calls it runs after the
// lock taken around its registration was released.
func (p *lateCallbackPublisher) failLateAndUnlock(err error) {
	p.errs = append(p.errs, err)
	p.mu.Unlock() // want "mutex 'p.mu' is unlocked but not locked"
}

func (p *lateCallbackPublisher) RegistersUnderLock(schedule func(func()), err error) {
	p.mu.Lock()
	schedule(func() {
		p.failLateAndUnlock(err)
	})
	p.mu.Unlock()
}
