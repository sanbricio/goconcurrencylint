package mutex

import "sync"

// A goroutine can take a lock for the function that started it and signal it
// on a channel; the function releases the lock after receiving the signal.

type signalHandoff struct {
	mu sync.Mutex
	n  int
}

// GoodCloseThenRelease must be CLEAN.
func (s *signalHandoff) GoodCloseThenRelease() {
	locked := make(chan struct{})
	go func() {
		s.mu.Lock()
		close(locked)
		s.n++
	}()
	<-locked
	s.mu.Unlock()
}

// GoodDeferredCloseThenRelease must be CLEAN.
func (s *signalHandoff) GoodDeferredCloseThenRelease(stop chan struct{}) {
	wait := make(chan struct{})
	go func() {
		defer close(wait)
		s.mu.Lock()
		s.n--
	}()
	select {
	case <-wait:
	case <-stop:
		<-wait
	}
	s.mu.Unlock()
}

// GoodSendInLoopThenRelease must be CLEAN.
func GoodSendInLoopThenRelease(wl *sync.RWMutex, n int) {
	wlocked := make(chan bool, 1)
	go func() {
		for i := 0; i < n; i++ {
			wl.Lock()
			wlocked <- true
		}
	}()
	for i := 0; i < n; i++ {
		<-wlocked
		wl.Unlock()
	}
}

// BadSignalBeforeLock must be flagged: the goroutine signals before it takes
// the lock, so the receive says nothing about it.
func (s *signalHandoff) BadSignalBeforeLock() {
	started := make(chan struct{})
	go func() {
		close(started)
		s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked in goroutine"
		s.n++
	}()
	<-started
	s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
}

// BadReleaseBeforeReceive must be flagged: the release comes before the
// signal is waited for.
func (s *signalHandoff) BadReleaseBeforeReceive() {
	locked := make(chan struct{})
	go func() {
		s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked in goroutine"
		close(locked)
	}()
	s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
	<-locked
}
