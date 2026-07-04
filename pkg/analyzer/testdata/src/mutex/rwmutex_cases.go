package mutex

import "sync"

// ========== RWMUTEX TESTS ==========

// ---------- Basic Read/Write Operations ----------

// Basic RLock and RUnlock
func GoodBasicRLockRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
}

// Basic Lock and Unlock (write)
func GoodBasicRWLockUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	mu.Unlock()
}

// RLock/RUnlock and Lock/Unlock in sequence
func GoodRWMultipleOperations() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.Lock()
	mu.Unlock()
}

// Upgrading from a read lock to a write lock deadlocks in the same goroutine.
func BadRWMutexPromotion() {
	var mu sync.RWMutex
	mu.RLock()
	mu.Lock() // want "rwmutex 'mu' attempts write Lock while read lock is held"
	mu.Unlock()
	mu.RUnlock()
}

// The write lock is fine after the read lock has been released.
func GoodRWMutexReadThenWriteAfterRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.Lock()
	mu.Unlock()
}

// A deferred RUnlock still leaves the read lock held until the function returns.
func BadRWMutexPromotionWithDeferredRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	defer mu.RUnlock()
	mu.Lock() // want "rwmutex 'mu' attempts write Lock while read lock is held"
	mu.Unlock()
}

// RLock without RUnlock
func BadRLockWithoutRUnlock() {
	var mu sync.RWMutex
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
}

// RWMutex with short declaration
func BadRWLockShortDecl() {
	rwmu := sync.RWMutex{}
	rwmu.Lock() // want "rwmutex 'rwmu' is locked but not unlocked"
}

// RLock without RUnlock with short declaration
func BadRLockShortDecl() {
	rwmu := sync.RWMutex{}
	rwmu.RLock() // want "rwmutex 'rwmu' is rlocked but not runlocked"
}

// RUnlock without RLock
func BadRUnlockWithoutRLock() {
	var mu sync.RWMutex
	mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
}

// Lock without Unlock (write lock)
func BadRWLockWithoutUnlock() {
	var mu sync.RWMutex
	mu.Lock() // want "rwmutex 'mu' is locked but not unlocked"
}

// Unlock without Lock (write lock)
func BadRWUnlockWithoutLock() {
	var mu sync.RWMutex
	mu.Unlock() // want "rwmutex 'mu' is unlocked but not locked"
}

func BadTryRLockIgnoredReturn() {
	var mu sync.RWMutex
	mu.TryRLock() // want "rwmutex 'mu' TryRLock return value not checked, lock may not be held"
	mu.RUnlock()  // want "rwmutex 'mu' is runlocked but not rlocked"
}

func BadRWTryLockIgnoredReturn() {
	var mu sync.RWMutex
	mu.TryLock() // want "rwmutex 'mu' TryLock return value not checked, lock may not be held"
	mu.Unlock()  // want "rwmutex 'mu' is unlocked but not locked"
}

func GoodTryRLockIfCondition() {
	var mu sync.RWMutex
	if mu.TryRLock() {
		defer mu.RUnlock()
	}
}

// ---------- RWMutex Defer Patterns ----------

// Defer unlock after write lock
func GoodRWDeferUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	defer mu.Unlock()
}

// Defer runlock after read lock
func GoodRWDeferRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	defer mu.RUnlock()
}

// Defer-before-rlock: the deferred RUnlock runs at return, after the adjacent
// RLock, so the pair is balanced (mirrors GoodDeferUnlockBeforeAdjacentLock).
func GoodRWDeferRUnlockBeforeAdjacentRLock() {
	var mu sync.RWMutex
	defer mu.RUnlock()
	mu.RLock()
}

// Defer-before-lock on a write lock: balanced for the same reason.
func GoodRWDeferUnlockBeforeAdjacentLock() {
	var mu sync.RWMutex
	defer mu.Unlock()
	mu.Lock()
}

func BadRWDeferLockInsteadOfUnlock() {
	var mu sync.RWMutex
	mu.Lock()       // want "rwmutex 'mu' is locked but not unlocked"
	defer mu.Lock() // want "rwmutex 'mu' defer calls Lock instead of Unlock, will deadlock on return"
}

func BadDeferRLockInsteadOfRUnlock() {
	var mu sync.RWMutex
	mu.RLock()       // want "rwmutex 'mu' is rlocked but not runlocked"
	defer mu.RLock() // want "rwmutex 'mu' defer calls RLock instead of RUnlock, will deadlock on return"
}

// ---------- RWMutex Conditional Logic ----------

// Both branches use lock/unlock or rlock/runlock
func GoodRWConditionalBothBranches() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.Lock()
		defer mu.Unlock()
	} else {
		mu.RLock()
		defer mu.RUnlock()
	}
}

// Conditional: one branch with rlock, other missing runlock
func BadRWConditionalMissingRUnlock() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked in if"
	}
}

// Conditional: one branch with rlock/runlock, other only runlock
func BadRWConditionalOneBranchMissingRLock() {
	var mu sync.RWMutex
	cond := true
	if cond {
		mu.RLock()
		defer mu.RUnlock()
	} else {
		mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
	}
}

// ---------- RWMutex Goroutine Patterns ----------

// Goroutine: Lock/Unlock and RLock/RUnlock
func GoodRWGoroutineLockUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.Lock()
		defer mu.Unlock()
	}()
	go func() {
		mu.RLock()
		defer mu.RUnlock()
	}()
}

// Goroutine: rlock without runlock
func BadRWGoroutineRLockWithoutRUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked in goroutine"
	}()
}

// Goroutine: lock without unlock
func BadRWGoroutineLockWithoutUnlock() {
	var mu sync.RWMutex
	go func() {
		mu.Lock() // want "rwmutex 'mu' is locked but not unlocked"
	}()
}

func BadRWGoroutineDeadlockParentNeverUnlocksWrite() {
	var mu sync.RWMutex
	mu.Lock()   // want "rwmutex 'mu' is locked but not unlocked"
	go func() { // want "rwmutex 'mu' goroutine started while write lock is held and also tries to acquire read lock, will deadlock if parent never releases"
		mu.RLock()
		mu.RUnlock()
	}()
}

func BadRWGoroutineDeadlockParentNeverRUnlocksRead() {
	var mu sync.RWMutex
	mu.RLock()  // want "rwmutex 'mu' is rlocked but not runlocked"
	go func() { // want "rwmutex 'mu' goroutine started while read lock is held and also tries to acquire write lock, will deadlock if parent never runlocks"
		mu.Lock()
		mu.Unlock()
	}()
}

func BadRWGoroutineWaitsBeforeParentRUnlocks() {
	var mu sync.RWMutex
	done := make(chan struct{})
	mu.RLock()
	go func() { // want "rwmutex 'mu' goroutine started while read lock is held and also tries to acquire write lock before parent runlocks"
		mu.Lock()
		mu.Unlock()
		close(done)
	}()
	<-done
	mu.RUnlock()
}

func GoodRWGoroutineLocksAfterParentRUnlocks() {
	var mu sync.RWMutex
	mu.RLock()
	go func() {
		mu.Lock()
		mu.Unlock()
	}()
	mu.RUnlock()
}

// ---------- RWMutex Balance Issues ----------

// Imbalanced: two rlocks, one runlock
func BadRWImbalancedRLockRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
	mu.RUnlock()
}

// Imbalanced: lock and rlock, only unlock
func BadRWImbalancedMixed() {
	var mu sync.RWMutex
	mu.Lock()
	mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
	mu.Unlock()
}

func BadUnlockWhenReadLocked() {
	var mu sync.RWMutex
	mu.RLock()
	mu.Unlock() // want "rwmutex 'mu' Unlock called but only read lock is held, did you mean RUnlock\\?"
}

func BadRUnlockWhenWriteLocked() {
	var mu sync.RWMutex
	mu.Lock()
	mu.RUnlock() // want "rwmutex 'mu' RUnlock called but only write lock is held, did you mean Unlock\\?"
}

func GoodRWMutexCorrectReadAPI() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
}

func GoodRWMutexCorrectWriteAPI() {
	var mu sync.RWMutex
	mu.Lock()
	mu.Unlock()
}

type delegatedRWUnlockShard struct {
	mu    sync.RWMutex
	qlen  int
	qsize int
}

type delegatedRWUnlockStore struct{}

func (s *delegatedRWUnlockStore) setEntry(shard *delegatedRWUnlockShard, cost int) {
	if cost > shard.qsize {
		shard.mu.Unlock()
		return
	}

	shard.qlen += cost
	s.processDeque(shard)
}

func (s *delegatedRWUnlockStore) processDeque(shard *delegatedRWUnlockShard) {
	if shard.qlen <= shard.qsize {
		shard.mu.Unlock()
		return
	}

	shard.qlen = shard.qsize
	shard.mu.Unlock()
}

func (s *delegatedRWUnlockStore) GoodRWMutexWriteUnlockDelegatedThroughHelpers(shard *delegatedRWUnlockShard, exists bool, cost int) {
	shard.mu.Lock()
	if exists {
		shard.mu.Unlock()
		return
	}

	s.setEntry(shard, cost)
}

type oneWayLatchPendingResult struct {
	executing sync.RWMutex
}

func (r *oneWayLatchPendingResult) GoodCreateLocksUntilBroadcast() {
	r.executing.Lock()
}

func (r *oneWayLatchPendingResult) GoodBroadcastUnlocksCreateLock() {
	r.executing.Unlock()
}

func (r *oneWayLatchPendingResult) GoodWaitUsesReadLockAsOneWayLatch() {
	r.executing.RLock()
}

// Boundary cases: the one-way latch/barrier heuristics must only suppress when a
// sibling method on the same type actually releases the field. A method whose
// name merely matches the hints, with no releasing sibling, must still report.
type lonelyOneWayLatch struct {
	gate    sync.RWMutex
	barrier sync.Mutex
}

func (l *lonelyOneWayLatch) WorkerLoopNeverRunlocks() {
	l.gate.RLock() // want "rwmutex 'l.gate' is rlocked but not runlocked"
}

func (l *lonelyOneWayLatch) StartProcessingNeverUnlocks() {
	l.barrier.Lock() // want "mutex 'l.barrier' is locked but not unlocked"
}

type nonTrivialConsolidator struct {
	mu      sync.Mutex
	queries map[string]*nonTrivialPendingResult
}

type nonTrivialPendingResult struct {
	executing    sync.RWMutex
	consolidator *nonTrivialConsolidator
	query        string
}

func (co *nonTrivialConsolidator) GoodCreateLocksPendingResultUntilBroadcast(query string) (*nonTrivialPendingResult, bool) {
	co.mu.Lock()
	defer co.mu.Unlock()

	if r, ok := co.queries[query]; ok {
		return r, false
	}

	r := &nonTrivialPendingResult{consolidator: co, query: query}
	r.executing.Lock()
	co.queries[query] = r
	return r, true
}

func (r *nonTrivialPendingResult) GoodBroadcastUnlocksPendingResult() {
	r.consolidator.mu.Lock()
	defer r.consolidator.mu.Unlock()

	delete(r.consolidator.queries, r.query)
	r.executing.Unlock()
}

type crossMethodBarrier struct {
	lock    sync.RWMutex
	wg      sync.WaitGroup
	threads []crossMethodBarrierThread
	count   int
}

type crossMethodBarrierThread struct {
	parent *crossMethodBarrier
	index  int
}

func (b *crossMethodBarrier) GoodCreateThreadsHoldsBarrier() {
	b.lock.Lock()

	logBarrierEvent("starting")
	for i := range b.threads {
		b.wg.Add(1)
		go b.threads[i].GoodClientLoopUsesReadLockAsOneWayStartGate()
	}

	logBarrierEvent("waiting")
	b.wg.Wait()
	b.wg.Add(len(b.threads))
}

func (b *crossMethodBarrier) GoodRunTestReleasesBarrier() {
	logBarrierEvent("running")
	b.lock.Unlock()

	b.wg.Wait()
}

func (bt *crossMethodBarrierThread) GoodClientLoopUsesReadLockAsOneWayStartGate() {
	b := bt.parent

	b.wg.Done()
	logBarrierEvent("waiting for barrier")
	b.lock.RLock()
	logBarrierEvent("started")

	for i := 0; i < b.count; i++ {
		_ = i + bt.index
	}
}

func logBarrierEvent(string) {}

// Double unlocks (runlock twice)
func BadRWDoubleRUnlock() {
	var mu sync.RWMutex
	mu.RLock()
	mu.RUnlock()
	mu.RUnlock() // want "rwmutex 'mu' is runlocked but not rlocked"
}

// Double unlocks (unlock twice)
func BadRWDoubleUnlock() {
	var mu sync.RWMutex
	mu.Lock()
	mu.Unlock()
	mu.Unlock() // want "rwmutex 'mu' is unlocked but not locked"
}

// --- Locks acquired inside synchronous callbacks ---------------------------

type rwCallbackStream struct {
	chunkMtx sync.RWMutex
}

type rwCallbackStreamMap struct{}

// LoadOrStoreNew runs exactly one of its callbacks synchronously and returns
// the stream with chunkMtx already held, for the caller to release.
func (rwCallbackStreamMap) LoadOrStoreNew(key string, newFn func() (*rwCallbackStream, error), loadFn func(*rwCallbackStream) error) (*rwCallbackStream, bool, error) {
	return nil, false, nil
}

// The write lock is taken inside a callback passed to LoadOrStoreNew and
// released by the caller. The lock isn't visible to flow analysis, so the
// unlock must NOT be reported as "unlocked but not locked" (loki
// pkg/ingester/instance.go pattern). A goroutine lock, by contrast, still
// fires — see BadLockInGoroutineThenParentUnlock.
func GoodRWUnlockAfterLockInCallbackArgument(m rwCallbackStreamMap, keys []string) error {
	var appendErr error
	for _, key := range keys {
		s, _, err := m.LoadOrStoreNew(key,
			func() (*rwCallbackStream, error) {
				s := &rwCallbackStream{}
				s.chunkMtx.Lock()
				return s, nil
			},
			func(s *rwCallbackStream) error {
				s.chunkMtx.Lock()
				return nil
			},
		)
		if err != nil {
			appendErr = err
			continue
		}
		_ = s
		s.chunkMtx.Unlock()
	}
	return appendErr
}

type rwLazyReader struct {
	readerMx sync.RWMutex
	loaded   bool
}

// Temporary lock upgrade: release the caller-held read lock, take the write
// lock, restore the read lock in a defer. The RUnlock must NOT be flagged
// (thanos pkg/block/indexheader/lazy_binary_reader.go load()).
func (r *rwLazyReader) GoodRWReadLockUpgrade() {
	r.readerMx.RUnlock()
	r.readerMx.Lock()
	defer func() {
		r.readerMx.Unlock()
		r.readerMx.RLock()
	}()
	r.loaded = true
}
