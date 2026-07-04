package waitgroup

import (
	"context"
	"runtime"
	"sync"
	"time"
)

// ---------- Loop Patterns ----------

// Add and Done inside a loop (typical worker pattern)
func GoodLoopAddDone() {
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func BadAddCountMismatchForLoopGoroutines() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' Add count 1 does not match 5 goroutines launched"
	for i := 0; i < 5; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func GoodAddCountMatchesForLoopGoroutines() {
	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 5; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func GoodConstCountMatchesMultipleForLoopGoroutines() {
	const goroutines = 32

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for w := 0; w < goroutines; w++ {
		go func() {
			defer wg.Done()
			doSomething()
		}()
	}

	for r := 0; r < goroutines; r++ {
		go func() {
			defer wg.Done()
			doSomething()
		}()
	}

	for d := 0; d < goroutines; d++ {
		go func() {
			defer wg.Done()
			doSomething()
		}()
	}

	wg.Wait()
}

// GoodReuseWaitGroupAcrossWaitPhases reuses one WaitGroup across two
// Add/launch/Wait phases separated by a Wait(). The loop-count check (GCL2007)
// must stop counting worker goroutines at the intermediate wg.Wait(): the
// second phase is a new lifecycle, so its goroutines must not be summed against
// the first phase's Add. Regression for a false positive in kubernetes
// pkg/controller/job/tracking_utils_test.go, which reported
// "Add count 3 does not match 9 goroutines launched".
func GoodReuseWaitGroupAcrossWaitPhases(extra int) {
	items := []int{1, 2, 3}

	var wg sync.WaitGroup
	wg.Add(len(items))
	for range items {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()

	for range items {
		wg.Add(extra)
		go func() {
			defer wg.Done()
		}()
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func GoodRangeLoopAddWithDeferredGoroutineDone(shardsByKeyspace map[string][]string) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	mu.Lock()
	for keyspace, shards := range shardsByKeyspace {
		wg.Add(1)
		go func(keyspace string, shards []string) {
			defer wg.Done()

			if len(shards) == 0 {
				mu.Lock()
				defer mu.Unlock()
				shardsByKeyspace[keyspace] = []string{"0"}
				return
			}

			mu.Lock()
			defer mu.Unlock()
			shardsByKeyspace[keyspace] = shards[:1]
		}(keyspace, shards)
	}
	mu.Unlock()

	wg.Wait()
}

func GoodAddLenThenLaunchRangeWorkers(trace []int) {
	var wg sync.WaitGroup

	wg.Add(len(trace))
	for _, req := range trace {
		go func(req int) {
			defer wg.Done()
			_ = req
		}(req)
	}

	wg.Wait()
}

// Add inside a loop but Done may be missing in some paths
func BadLoopAddMissingDone() {
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
		if i == 0 {
			go func() {
				wg.Done()
			}()
		}
	}
	wg.Wait()
}

// ---------- Goroutine Patterns ----------

// Multiple goroutines with defer Done (should NOT trigger error)
func GoodMultipleGoroutinesWithDeferDone() {
	var wg sync.WaitGroup
	wg.Add(2)
	var errOrderConsumer, errReturnConsumer any
	go func() {
		defer wg.Done()
		errOrderConsumer = doSomething()
	}()
	go func() {
		defer wg.Done()
		errReturnConsumer = doSomething()
	}()
	wg.Wait()

	_ = errOrderConsumer
	_ = errReturnConsumer
}

func BadAddInsideGoroutine() {
	var wg sync.WaitGroup
	go func() {
		wg.Add(1) // want "waitgroup 'wg' Add called inside goroutine, may race with Wait"
		defer wg.Done()
	}()
	wg.Wait()
}

func BadAddInsideGoroutineNoExternalAdd() {
	var wg sync.WaitGroup
	go func() {
		wg.Add(1) // want "waitgroup 'wg' Add called inside goroutine, may race with Wait"
		wg.Done()
	}()
	wg.Wait()
}

func BadAddInsideNestedGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		go func() {
			wg.Add(1) // want "waitgroup 'wg' Add called inside goroutine, may race with Wait"
			wg.Done()
		}()
		wg.Done()
	}()
	wg.Wait()
}

func GoodSwitchWithDefault() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		x := 1
		switch x {
		case 2:
			wg.Done()
		default:
			wg.Done()
		}
	}()
	wg.Wait()
}

func GoodUnconditionalDone() {
	var wg sync.WaitGroup
	condition := false
	wg.Add(1)
	go func() {
		if condition {
			// some work
		}
		wg.Done() // This is outside the if, so it's unconditional
	}()
	wg.Wait()
}

// Not flagged: a Done is present in the goroutine, reached after a blocking
// receive. A present-but-unguaranteed goroutine Done means the counter is not
// provably orphaned, so Add-without-Done stays silent (see
// hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedDoneAfterBlockingReceive() {
	var wg sync.WaitGroup
	ch := make(chan struct{})
	wg.Add(1)
	go func() {
		<-ch
		wg.Done()
	}()
	wg.Wait()
}

// Add without Done in a goroutine that returns prematurely
func BadAddDonePrematureReturn() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		return // forgot to call Done!
		wg.Done()
	}()
	wg.Wait()
}

// Add without Done in a goroutine that panics
func BadPanicWithoutRecover() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		panic("error") // Done is never called
		wg.Done()
	}()
	wg.Wait()
}

// Non-deferred Done after a possible panic is not guaranteed to run.
func BadDoneAfterConditionalPanic() {
	var wg sync.WaitGroup
	shouldPanic := true
	wg.Add(1)
	go func() {
		if shouldPanic {
			panic("error")
		}
		wg.Done() // want "waitgroup 'wg' Done should be deferred so it runs on panic or runtime.Goexit"
	}()
	wg.Wait()
}

func GoodNonDeferredDoneAfterOrdinaryCall() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		doSomething()
		wg.Done()
	}()
	wg.Wait()
}

func GoodNonDeferredDoneAfterMultipleOrdinaryCalls() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		doSomething()
		doSomething()
		wg.Done()
	}()
	wg.Wait()
}

func GoodShadowedPanicFunctionBeforeDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		panic := func(any) {}
		panic("not the builtin")
		wg.Done()
	}()
	wg.Wait()
}

func BadDoneAfterRuntimeGoexit() {
	var wg sync.WaitGroup
	shouldExit := true
	wg.Add(1)
	go func() {
		if shouldExit {
			runtime.Goexit()
		}
		wg.Done() // want "waitgroup 'wg' Done should be deferred so it runs on panic or runtime.Goexit"
	}()
	wg.Wait()
}

// Defer keeps Done guaranteed even if work panics.
func GoodDeferDoneBeforeConditionalPanic() {
	var wg sync.WaitGroup
	shouldPanic := true
	wg.Add(1)
	go func() {
		defer wg.Done()
		if shouldPanic {
			panic("error")
		}
	}()
	wg.Wait()
}

func GoodDoneOnlyStatement() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		wg.Done()
	}()
	wg.Wait()
}

// A defer registers the call for execution at function exit; the deferred
// call itself does not run inline, so it cannot panic before Done.
func GoodDoneAfterDeferRegistration() {
	var wg sync.WaitGroup
	wg.Add(1)
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		wg.Done()
		<-ch
	}()
	wg.Wait()
}

func GoodDoneDeferredInsideFuncWrapper() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			wg.Done()
		}()
		doSomething()
	}()
	wg.Wait()
}

func BadMultipleDoneSameWorkerBranch() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		wg.Done() // want "waitgroup 'wg' Done called multiple times in the same worker branch"
	}()
	wg.Wait()
}

func BadMultipleDoneSameWorkerBranchAfterDone(cond bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		wg.Done()
		if cond {
			wg.Done() // want "waitgroup 'wg' Done called multiple times in the same worker branch"
		}
	}()
	wg.Wait()
}

func GoodSingleDonePerWorkerBranch(cond bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		if cond {
			wg.Done()
		} else {
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: each goroutine contains a Done (one guaranteed, one reached only
// after a conditional return). A present-but-unguaranteed goroutine Done means
// the counter is not provably orphaned, so Add-without-Done stays silent (see
// hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedDeferAfterConditionalReturn() {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
	}()

	go func() {
		if true {
			return
		}
		defer wg.Done()
	}()

	wg.Wait()
}

// Not flagged: the goroutine's Done is guarded by a runtime condition. The
// linter cannot prove the branch is never taken, and flagging every conditional
// Done would false-positive on correct code, so Add-without-Done stays silent
// (see hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedConditionalDone() {
	var wg sync.WaitGroup
	condition := false
	wg.Add(1)
	go func() {
		if condition {
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: an event-driven Done inside a loop. Whether every Add is matched
// depends on runtime events the linter cannot see; this is the canonical
// producer/consumer shape used by correct code (e.g. the kubernetes and vitess
// test helpers this guard is derived from), so reporting Add-without-Done here
// would be a false positive (see hasUnguaranteedGoroutineDone in balance.go).
func UnflaggedConditionalEventDone(events <-chan string) {
	shards := []string{"-40", "40-80", "80-"}
	seen := make(map[string]bool, len(shards))
	var wg sync.WaitGroup

	for _, shard := range shards {
		seen[shard] = false
		wg.Add(1)
	}

	go func() {
		for shard := range events {
			if !seen[shard] {
				seen[shard] = true
				wg.Done()
			}
		}
	}()

	wg.Wait()
}

// Not flagged: a conditional Done. The linter does not constant-fold the guard,
// and a present-but-unguaranteed goroutine Done means the counter is not provably
// orphaned, so Add-without-Done stays silent (see hasUnguaranteedGoroutineDone in
// balance.go). Regression guard.
func UnflaggedConditionalDoneComplex() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		x := 1
		if x > 5 {
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: a Done that lives in one switch case. The linter cannot prove the
// case is never selected, and a present-but-unguaranteed goroutine Done means the
// counter is not provably orphaned, so Add-without-Done stays silent (see
// hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedConditionalDoneSwitch() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		x := 1
		switch x {
		case 2:
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: Done lives in switch cases with no default. The linter cannot
// prove no case is selected, and a present-but-unguaranteed goroutine Done means
// the counter is not provably orphaned, so Add-without-Done stays silent (see
// hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedSwitchNoDefault() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		x := 1
		switch x {
		case 2:
			wg.Done()
		case 3:
			wg.Done()
		}
	}()
	wg.Wait()
}

// ---------- Panic Recovery Patterns ----------

// Add/Done with panic recovery
func GoodAddDoneWithPanicRecovery() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				wg.Done()
			}
		}()
		panic("fail")
	}()
	wg.Wait()
}

func GoodIntegerRangeFanout() {
	var wg sync.WaitGroup
	wg.Add(2000)

	start := make(chan struct{})
	for range 1000 {
		go func() {
			<-start
			wg.Done()
		}()
	}

	for i := range 1000 {
		go func(worker int) {
			<-start
			_ = worker
			wg.Done()
		}(i)
	}

	close(start)
	wg.Wait()
}

func GoodIntegerRangeWorkersWithDeferredDone() {
	var wg sync.WaitGroup
	wg.Add(50)

	start := make(chan struct{})
	for worker := range 50 {
		go func(worker int) {
			defer wg.Done()
			<-start
			_ = worker
		}(worker)
	}

	close(start)
	wg.Wait()
}

func GoodIIFEGoroutineDoneForDynamicAdd(items map[int]string) {
	var wg sync.WaitGroup
	wg.Add(len(items))

	for _, item := range items {
		func(item string) {
			go func() {
				_ = item
				wg.Done()
			}()
		}(item)
	}

	wg.Wait()
}

func GoodAddCountMatchesDynamicRangeWorkers() {
	var wg sync.WaitGroup
	base := [5]int{}
	items := base[:0]
	for i := 0; i < 5; i++ {
		items = append(items, i)
	}

	wg.Add(5)
	for _, item := range items {
		item := item
		go func() {
			defer wg.Done()
			_ = item
		}()
	}
	wg.Wait()
}

func BadUnknownDynamicRangeMayNotCoverAdd(items []int) {
	var wg sync.WaitGroup
	wg.Add(2) // want "waitgroup 'wg' has Add without corresponding Done"
	for _, item := range items {
		item := item
		go func() {
			defer wg.Done()
			_ = item
		}()
	}
	wg.Wait()
}

func GoodWorkerDoneOnContextCancellation() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				wg.Done()
				return
			case <-tick.C:
				doSomething()
			}
		}
	}()

	cancel()
	wg.Wait()
}

type customDoneSignal struct {
	ch chan struct{}
}

func (s customDoneSignal) Done() <-chan struct{} {
	return s.ch
}

// Not flagged: the worker's Done sits in one select case while the default
// returns without it. The linter cannot know which case wins at runtime, and a
// present-but-unguaranteed goroutine Done means the counter is not provably
// orphaned, so Add-without-Done stays silent (see hasUnguaranteedGoroutineDone in
// balance.go). Regression guard.
func UnflaggedWorkerDoneOnNonContextSignal(sig customDoneSignal) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for {
			select {
			case <-sig.Done():
				wg.Done()
				return
			default:
				return
			}
		}
	}()
	wg.Wait()
}

func GoodWorkerDoneOnContextCancellationInTickerRange() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	go func() {
		for range time.NewTicker(time.Millisecond).C {
			select {
			case <-ctx.Done():
				wg.Done()
				return
			default:
			}
			doSomething()
		}
	}()
	wg.Add(1)

	cancel()
	wg.Wait()
}

// Slice declared empty then reassigned from a multi-return call inside an
// if/else: its length is not statically knowable, so the range multiplier
// must not collapse to 0 and drop the per-iteration Done from the balance.
func GoodAddDonePairedInsideRangeOverAssignedSlice(items []int, err error) {
	var clusters []int
	if len(items) > 0 {
		clusters, err = loadItems(items)
	} else {
		clusters, err = loadItems(nil)
	}
	if err != nil {
		return
	}

	var wg sync.WaitGroup
	for _, cluster := range clusters {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			_ = c
		}(cluster)
	}
	wg.Wait()
}

type deferredClosureBenchmark struct {
	wg         sync.WaitGroup
	concurrent deferredClosureAtomic
	trace      []int
}

type deferredClosureAtomic struct{}

func (deferredClosureAtomic) Add(delta int) int {
	return delta
}

func (b *deferredClosureBenchmark) displayProgress(done <-chan struct{}, total int) {
	select {
	case <-done:
		return
	default:
		_ = total
		return
	}
}

func (b *deferredClosureBenchmark) GoodAddLenDoneInDeferredWorkerClosure(fallback []int) {
	trace := b.trace
	if len(trace) == 0 {
		trace = fallback
	}

	done := make(chan struct{})
	go b.displayProgress(done, len(trace))

	b.wg.Add(len(trace))

	for _, req := range trace {
		go func(req int) {
			b.concurrent.Add(1)
			defer func() {
				b.concurrent.Add(-1)
				b.wg.Done()
			}()

			_ = req
		}(req)
	}

	b.wg.Wait()
	close(done)
}

type keyspaceSetLike map[string]bool

func newKeyspaceSetLike(values ...string) keyspaceSetLike {
	set := keyspaceSetLike{}
	for _, value := range values {
		set[value] = true
	}
	return set
}

func (s keyspaceSetLike) Insert(value string) {
	s[value] = true
}

func (s keyspaceSetLike) Len() int {
	return len(s)
}

func (s keyspaceSetLike) Intersection(other keyspaceSetLike) keyspaceSetLike {
	result := keyspaceSetLike{}
	for value := range s {
		if other[value] {
			result[value] = true
		}
	}
	return result
}

type keyspaceClusterLike struct {
	missing bool
}

type keyspaceInfoLike struct {
	Shards []keyspaceShardLike
}

type keyspaceShardLike struct {
	Name string
}

func (c *keyspaceClusterLike) GetKeyspace(name string) (*keyspaceInfoLike, error) {
	if c.missing && name == "" {
		return nil, context.Canceled
	}
	return &keyspaceInfoLike{Shards: []keyspaceShardLike{{Name: name}}}, nil
}

type keyspaceErrorRecorderLike struct{}

func (keyspaceErrorRecorderLike) RecordError(error) {}

func GoodLoopAddsWorkersWithDeferredDoneAndMapLock(cluster *keyspaceClusterLike, results map[string]keyspaceSetLike) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		rec keyspaceErrorRecorderLike
	)

	mu.Lock()
	for key, values := range results {
		wg.Add(1)

		go func(key string, values keyspaceSetLike) {
			defer wg.Done()

			keyspace, err := cluster.GetKeyspace(key)
			if err != nil {
				if key == "" {
					mu.Lock()
					defer mu.Unlock()
					delete(results, key)
					return
				}
				rec.RecordError(err)
				return
			}

			fullSet := newKeyspaceSetLike()
			for _, shard := range keyspace.Shards {
				fullSet.Insert(shard.Name)
			}

			if values.Len() == 0 {
				mu.Lock()
				defer mu.Unlock()
				results[key] = fullSet
				return
			}

			overlap := values.Intersection(fullSet)
			mu.Lock()
			defer mu.Unlock()
			results[key] = overlap
		}(key, values)
	}
	mu.Unlock()

	wg.Wait()
}

func GoodLoopAddsWorkersAfterMapIndexPopulation(keys []string) {
	results := map[string]keyspaceSetLike{}
	for _, key := range keys {
		if _, ok := results[key]; !ok {
			results[key] = newKeyspaceSetLike("primary")
			continue
		}
		results[key].Insert("replica")
	}

	var wg sync.WaitGroup
	for key, values := range results {
		wg.Add(1)
		go func(key string, values keyspaceSetLike) {
			defer wg.Done()
			_ = values.Len()
			_ = key
		}(key, values)
	}
	wg.Wait()
}

// Cancellation via close() of a local channel: `case <-chClose` drains when
// the channel is closed, equivalent to `<-ctx.Done()` for context cancellation.
func GoodWorkerDoneOnCloseOfLocalChannel() {
	chClose := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(1)

	go func() {
		for {
			select {
			case <-chClose:
				wg.Done()
				return
			default:
			}
		}
	}()

	close(chClose)
	wg.Wait()
}

// Good: a condition-less for always enters its body, so a Done the body
// guarantees before any conditional exit runs at least once.
func GoodDoneGuaranteedInInfiniteLoop() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for {
			wg.Done()
			return
		}
	}()
	wg.Wait()
}

// Good: the labeled break runs after Done has already executed.
func GoodDoneBeforeLabeledBreakInInfiniteLoop() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
	loop:
		for {
			wg.Done()
			break loop
		}
	}()
	wg.Wait()
}

// Not flagged: a conditional break could leave the loop before Done runs, but the
// Done is present and the linter cannot prove the break is always taken. A
// present-but-unguaranteed goroutine Done means the counter is not provably
// orphaned, so Add-without-Done stays silent (see hasUnguaranteedGoroutineDone in
// balance.go). Regression guard.
func UnflaggedConditionalBreakBeforeDone(stop func() bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for {
			if stop() {
				break
			}
			wg.Done()
			return
		}
	}()
	wg.Wait()
}

// Not flagged: a counted loop can run zero times, so the Done is not guaranteed,
// but it is present and the linter cannot prove the loop never runs. A
// present-but-unguaranteed goroutine Done means the counter is not provably
// orphaned, so Add-without-Done stays silent (see hasUnguaranteedGoroutineDone in
// balance.go). Regression guard.
func UnflaggedDoneInConditionedLoop(n int) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		for i := 0; i < n; i++ {
			wg.Done()
			return
		}
	}()
	wg.Wait()
}

// Add takes a non-constant variable the linter can't resolve to a constant, so
// it must NOT claim a count mismatch (thanos expandedpostingscache/cache_test.go).
func GoodAddNonConstVarMatchesGoroutines() {
	concurrency := 100
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range 100 {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}
