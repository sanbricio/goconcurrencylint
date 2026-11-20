package mutex

import "sync"

// A mutex is new each iteration only when the iteration builds it, and the
// iteration's critical sections are the windows between its lock and unlock
// calls, wherever they sit, matched by the variable itself rather than by its
// name.

type stripedLocks struct {
	stripes []sync.Mutex
	byKey   map[string]*sync.Mutex
	lock    sync.Mutex
	total   int
}

// GoodPointerToExistingElement must be CLEAN: &s.stripes[i] points at a mutex
// that lives in the slice, not at a new one.
func (s *stripedLocks) GoodPointerToExistingElement() bool {
	for i := range s.stripes {
		l := &s.stripes[i]
		if l.TryLock() {
			l.Unlock()
		} else {
			return true
		}
	}
	return false
}

// GoodPointerFromMap must be CLEAN: the map hands out mutexes that outlive the
// iteration.
func (s *stripedLocks) GoodPointerFromMap(keys []string) {
	for _, k := range keys {
		l := s.byKey[k]
		l.Lock()
		s.total++
		l.Unlock()
	}
}

func onEvent(func() error) {}

// GoodSameNamedFieldElsewhere must be CLEAN: `other.lock` is a field, not the
// loop's `lock`, so it neither escapes the loop mutex nor joins its sections.
func GoodSameNamedFieldElsewhere(other *stripedLocks, n int) {
	for range n {
		var lock sync.Mutex
		var seen bool
		onEvent(func() error {
			lock.Lock()
			defer lock.Unlock()
			if seen {
				return nil
			}
			seen = true
			return nil
		})
		other.lock.Lock()
		other.total++
		other.lock.Unlock()
	}
}

var lastRound int

// GoodGoroutineWindowOnlyTouchesIterationState must be CLEAN: the goroutine
// writes an outer variable, but outside its critical section; inside it only
// iteration state is touched, here and in the select arm.
func GoodGoroutineWindowOnlyTouchesIterationState(stop chan struct{}) {
	for i := 0; ; i++ {
		var (
			mu   sync.Mutex
			set  bool
			done = make(chan struct{})
		)
		go func() {
			defer close(done)
			lastRound = i
			mu.Lock()
			if !set {
				i++
			}
			mu.Unlock()
		}()
		select {
		case <-done:
		case <-stop:
			mu.Lock()
			set = true
			mu.Unlock()
			<-done
			return
		}
	}
}

// BadFreshPointerGuardsOuterState must be flagged: &sync.Mutex{} builds a new
// lock each iteration, and it guards a field shared by every iteration.
func (s *stripedLocks) BadFreshPointerGuardsOuterState(items []int) {
	for _, it := range items {
		mu := &sync.Mutex{} // want "mutex 'mu' declared inside loop, each iteration creates a new mutex that cannot protect shared state"
		go func() {
			mu.Lock()
			s.total += it
			mu.Unlock()
		}()
	}
}

// BadSameNamedFieldDoesNotExcuse must be flagged: the loop's `lock` guards a
// shared field; a same-named field elsewhere changes nothing.
func (s *stripedLocks) BadSameNamedFieldDoesNotExcuse(items []int, other *stripedLocks) {
	for _, it := range items {
		var lock sync.Mutex // want "mutex 'lock' declared inside loop, each iteration creates a new mutex that cannot protect shared state"
		other.lock.Lock()
		other.lock.Unlock()
		go func() {
			lock.Lock()
			s.total += it
			lock.Unlock()
		}()
	}
}

// BadDeferredWindowWritesOuterState must be flagged: the deferred unlock keeps
// the window open to the end of the closure, over a shared write.
func (s *stripedLocks) BadDeferredWindowWritesOuterState(items []int) {
	for _, it := range items {
		var mu sync.Mutex // want "mutex 'mu' declared inside loop, each iteration creates a new mutex that cannot protect shared state"
		onEvent(func() error {
			mu.Lock()
			defer mu.Unlock()
			s.total += it
			return nil
		})
	}
}
