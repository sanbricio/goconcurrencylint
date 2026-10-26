package mutex

import (
	"sync"
	"testing"
)

// ===== a local flag that records whether the lock was taken =====

type bucketCache struct {
	mu      sync.RWMutex
	buckets map[int]*int
}

// held says whether the first branch took the lock; every later decision reads
// it, so each path releases exactly what it took.
func (b *bucketCache) GoodLockHeldFlagFollowsAcquisition(id int) *int {
	bucket, ok := b.buckets[id]
	held := false
	if !ok {
		b.mu.Lock()
		held = true
		bucket = new(int)
		b.buckets[id] = bucket
	}
	if bucket == nil {
		if held {
			b.mu.Unlock()
		}
		return nil
	}
	if !held {
		b.mu.Lock()
	}
	b.mu.Unlock()
	return bucket
}

// Forgetting the release on the early return leaks the lock on the path that
// took it, however the flag is used elsewhere.
func (b *bucketCache) BadLockHeldFlagEarlyReturnLeaks(id int) *int {
	bucket, ok := b.buckets[id]
	held := false
	if !ok {
		b.mu.Lock() // want "rwmutex 'b.mu' is locked but not unlocked in if"
		held = true
	}
	if bucket == nil {
		return nil
	}
	if !held {
		b.mu.Lock() // want "rwmutex 'b.mu' is locked but not unlocked in if"
	}
	b.mu.Unlock() // want "rwmutex 'b.mu' is unlocked but not locked"
	return bucket
}

// A parameter decides alone whether the lock is taken: the release that
// depends on it is missing.
func BadParameterDecidesLockOnly(take bool) {
	var mu sync.Mutex
	if take {
		mu.Lock() // want "mutex 'mu' is locked but not unlocked in if"
	}
	mu.Unlock() // want "mutex 'mu' is unlocked but not locked"
}

// ===== closures that lock only when a parameter says so =====

type flagLockedStore struct {
	mu     sync.Mutex
	closed bool
	n      int
}

// lock and unlock read the same parameter, so they take and give back the
// lock together or not at all.
func (s *flagLockedStore) remove(seq int, needLock bool) bool {
	if seq == 0 {
		return false
	}
	lock := func() {
		if needLock {
			s.mu.Lock()
		}
	}
	unlock := func() {
		if needLock {
			s.mu.Unlock()
		}
	}
	lock()
	defer unlock()
	if s.closed {
		return false
	}
	s.n--
	return true
}

func (s *flagLockedStore) removeLocked(seq int) bool {
	return s.remove(seq, false)
}

// The caller already holds the lock and asks remove not to take it: its own
// Unlock releases its own Lock.
func GoodCallerKeepsItsLockAcrossNoLockHelper() {
	s := &flagLockedStore{}
	s.mu.Lock()
	s.removeLocked(1)
	s.mu.Unlock()
}

// Asking the helper to lock while holding the lock is a self-deadlock; the
// borrowed release the main analysis sees there is not explained away.
func BadCallerHoldsLockHelperLocksAgain() {
	s := &flagLockedStore{}
	s.mu.Lock()
	s.remove(1, true)
	s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
}

// The release is deferred unconditionally while the lock depends on the flag.
func (s *flagLockedStore) BadDeferredUnlockWithoutFlag(needLock bool) {
	if needLock {
		s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked in if"
	}
	defer s.mu.Unlock() // want "mutex 's.mu' has defer unlock but no corresponding lock"
	s.n++
}

// ===== TryLock assertions in tests =====

// A goroutine cannot TryLock what it holds, and a failed assertion reports the
// test failed: the branches the main analysis complained about do not run on
// a passing test.
func GoodTryLockAssertions(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	if mu.TryLock() {
		t.Errorf("TryLock succeeded while held")
	}
	mu.Unlock()
	if !mu.TryLock() {
		t.Errorf("TryLock failed after release")
	}
	mu.Unlock()
}

// The release is missing on the passing path too.
func BadTryLockAssertionsLeak(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()         // want "mutex 'mu' is locked but not unlocked"
	if mu.TryLock() { // want "mutex 'mu' is locked but not unlocked in if"
		t.Errorf("TryLock succeeded while held")
	}
}

// ===== a loop whose condition takes the lock =====

// Each iteration gives back what the condition took; the loop ends when the
// condition fails, holding nothing.
func GoodPollWhileReadLockIsFree(mu *sync.RWMutex, yield func()) {
	for mu.TryRLock() {
		mu.RUnlock()
		yield()
	}
}

func BadPollReleasesTwice(mu *sync.RWMutex) {
	for mu.TryRLock() {
		mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
		mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
	}
}

// ===== hand-over-hand traversal =====

type sweepNode struct {
	sync.Mutex
	next    *sweepNode
	deleted bool
}

// The lock of curr becomes the lock of prev when the traversal moves on.
func GoodHandOverHandSweep(head *sweepNode) {
	prev := head
	prev.Lock()
	for curr := prev.next; curr != nil; curr = curr.next {
		curr.Lock()
		if curr.deleted {
			prev.next = curr.next
			curr.Unlock()
		} else {
			prev.Unlock()
			prev = curr
		}
	}
	prev.Unlock()
}

// The last node's lock is never released.
func BadHandOverHandSweepLeaksLast(head *sweepNode) {
	prev := head
	prev.Lock() // want "mutex 'prev' is locked but not unlocked"
	for curr := prev.next; curr != nil; curr = curr.next {
		curr.Lock() // want "mutex 'curr' is locked but not unlocked"
		if curr.deleted {
			prev.next = curr.next
			curr.Unlock()
		} else {
			prev.Unlock()
			prev = curr
		}
	}
}

// ===== a wrapper on a value that is itself a lock =====

type batch struct {
	sync.Mutex
	pending int
}

func (b *batch) lock() { b.Mutex.Lock() }

func (b *batch) Lock() { b.lock() }

func (b *batch) Unlock() {
	b.pending = 0
	b.Mutex.Unlock()
}

// b.Unlock releases b.Mutex, which is what b.lock takes.
func (b *batch) GoodCommitThroughWrapper() {
	b.lock()
	b.pending++
	b.Unlock()
}

// split embeds one mutex and locks another through its own methods: taking
// the embedded one does not take what split.Unlock releases.
type split struct {
	sync.Mutex
	other sync.Mutex
}

func (s *split) lockEmbedded() { s.Mutex.Lock() } // want "mutex 's.Mutex' is locked but not unlocked"

func (s *split) Lock() { s.other.Lock() }

func (s *split) Unlock() { s.other.Unlock() }

func (s *split) BadReleasesOtherLock() {
	s.lockEmbedded()
	s.Unlock() // want "mutex 's' is unlocked but not locked"
}

// ===== TryLock on a lock this goroutine holds =====

// sync mutexes are not reentrant, so the success branch never runs.
func GoodTryLockWhileHeldNeverSucceeds(mu *sync.Mutex, work func()) {
	mu.Lock()
	if mu.TryLock() {
		mu.Unlock()
		return
	}
	work()
	mu.Unlock()
}
