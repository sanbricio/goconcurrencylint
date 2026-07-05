package waitgroup

import "sync"

// ========== MIXED SCENARIOS ==========

// ---------- Multiple WaitGroups ----------

// Multiple WaitGroups, each with their own Add and Done
func GoodMultipleWaitGroups() {
	var wg1, wg2 sync.WaitGroup

	wg1.Add(1)
	go func() {
		defer wg1.Done()
	}()

	wg2.Add(1)
	go func() {
		defer wg2.Done()
	}()

	wg1.Wait()
	wg2.Wait()
}

// Multiple WaitGroups, one passed to function
func GoodMixedWaitGroupUsage() {
	var wg1, wg2 sync.WaitGroup

	// wg1 is handled locally
	wg1.Add(1)
	go func() {
		defer wg1.Done()
	}()

	// wg2 is passed to another function
	wg2.Add(1)
	go handleExternalWork(&wg2)

	wg1.Wait()
	wg2.Wait()
}

func BadNestedWaitGroupWaitInsideWorker() {
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1)
	wg2.Add(1)
	go func() {
		defer wg1.Done()
		wg2.Wait() // want "waitgroup 'wg2' Wait inside worker for waitgroup 'wg1' can deadlock"
	}()
	wg1.Wait()
	wg2.Done()
}

func GoodNestedWaitGroupReleasedBeforeOuterWait() {
	var wg1, wg2 sync.WaitGroup
	wg1.Add(1)
	wg2.Add(1)
	go func() {
		defer wg1.Done()
		wg2.Wait()
	}()
	wg2.Done()
	wg1.Wait()
}

func GoodNestedWorkerSignalsReadyBeforeWaitingOnRun() {
	var ready, run, cleanup sync.WaitGroup
	ready.Add(1)
	run.Add(1)
	cleanup.Add(1)

	go func() {
		ready.Done()
		defer cleanup.Done()
		run.Wait()
	}()

	ready.Wait()
	run.Done()
	cleanup.Wait()
}

func BadNestedWorkerConditionallySignalsReadyBeforeWaitingOnRun(release bool) {
	var ready, run sync.WaitGroup
	ready.Add(1)
	run.Add(1)

	go func() {
		if release {
			ready.Done()
		} else {
			defer ready.Done()
		}
		run.Wait() // want "waitgroup 'run' Wait inside worker for waitgroup 'ready' can deadlock"
	}()

	ready.Wait()
	run.Done()
}

// ---------- Reuse Patterns ----------

func GoodReuseWaitGroup() {
	var wg sync.WaitGroup

	// First usage
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()

	// Second usage
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

type TwoPhaseBench struct {
	wg      sync.WaitGroup
	barrier sync.RWMutex
	threads int
}

func (b *TwoPhaseBench) GoodTwoPhaseWaitGroupLifecycle() {
	b.barrier.Lock()

	for i := 0; i < b.threads; i++ {
		b.wg.Add(1)
		go b.clientLoop()
	}

	b.wg.Wait()
	b.wg.Add(b.threads)
	b.barrier.Unlock()
	b.wg.Wait()
}

func (b *TwoPhaseBench) clientLoop() {
	b.wg.Done()
	b.barrier.RLock()
	b.barrier.RUnlock()
	b.wg.Done()
}

// ---------- Struct Member Patterns ----------

type MyStruct struct {
	wg sync.WaitGroup
}

// Add and Done in a method of a struct
func (m *MyStruct) DoWork() {
	m.wg.Add(1)
	go func() { defer m.wg.Done() }()
	m.wg.Wait()
}

// Add in one method, Done in a sibling method launched as a goroutine.
// The linter must not flag Add when Done lives in another method of the same struct.
type StructWithSiblingMethods struct {
	stopWg sync.WaitGroup
	stopCh chan struct{}
}

func (s *StructWithSiblingMethods) GoodAddInStartDoneInSiblingMethod() {
	s.stopCh = make(chan struct{})
	s.stopWg.Add(1)
	go s.run(s.stopCh)
}

func (s *StructWithSiblingMethods) run(stopCh chan struct{}) {
	defer s.stopWg.Done()
	<-stopCh
}

// Add in a loop, Done in the per-iteration goroutine method.
type StructWithConnHandler struct {
	connWG sync.WaitGroup
}

func (l *StructWithConnHandler) GoodAddInLoopDoneInHandlerMethod() {
	for i := 0; i < 3; i++ {
		l.connWG.Add(1)
		go l.handleConn()
	}
}

func (l *StructWithConnHandler) handleConn() {
	defer l.connWG.Done()
}

// ========== EDGE CASES ==========

// ---------- Complex Control Flow ----------

// Edge case where Add and Done are called in different goroutines
// This is valid, but can be confusing if not documented properly
func EdgeCaseComplexButValid() {
	var wg sync.WaitGroup
	ch := make(chan bool)

	wg.Add(1)

	go func() {
		<-ch
		wg.Done()
	}()

	go func() {
		ch <- true
	}()

	wg.Wait()
}

// Wait without Add on a local WaitGroup is reported.
func BadWaitNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // want "waitgroup 'wg' Wait called without any Add"
}

func GoodBorrowedFieldWaitGroupAliasWait(s *ServerSystemLike) {
	wg := &s.sys.wg
	wg.Wait()
}

func GoodFieldWaitGroupExternalLifecycleReuse(mirror *MirrorLike) {
	mirror.wg.Wait()
	mirror.wg.Add(1)
	go func() {
		defer mirror.wg.Done()
	}()
	mirror.wg.Wait()
}

func GoodFieldWaitGroupAddInsideGoroutineExternalLifecycle(mirror *MirrorLike) {
	go func() {
		mirror.wg.Add(1)
		defer mirror.wg.Done()
	}()
}

func GoodFieldWaitGroupAddWithoutLocalWaitExternalLifecycle(mirror *MirrorLike) {
	mirror.wg.Add(1)
}

func GoodFieldWaitGroupAddInsideGoroutineWithoutLocalWaitExternalLifecycle(mirror *MirrorLike) {
	go func() {
		mirror.wg.Add(1)
	}()
}

func BadFieldWaitGroupAddInsideGoroutineWithLocalWait(mirror *MirrorLike) {
	go func() {
		mirror.wg.Add(1) // want "waitgroup 'mirror.wg' Add called inside goroutine, may race with Wait"
		defer mirror.wg.Done()
	}()
	mirror.wg.Wait()
}

// ---------- Switch/Select Edge Cases ----------

// Not flagged: the default has Done while another case does not. The linter
// cannot prove which case is selected, and a present-but-unguaranteed goroutine
// Done means the counter is not provably orphaned, so Add-without-Done stays
// silent (see hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedSwitchDefaultOnlyDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		x := 1
		switch x {
		case 2:
			// no Done here
		default:
			wg.Done()
		}
	}()
	wg.Wait()
}

// Good: select with Done in all cases including default
func GoodSelectAllCasesDone() {
	var wg sync.WaitGroup
	ch := make(chan int, 1)
	ch <- 1
	wg.Add(1)
	go func() {
		select {
		case <-ch:
			wg.Done()
		default:
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: one select comm clause has Done, the other does not. The linter
// cannot know which clause fires, and a present-but-unguaranteed goroutine Done
// means the counter is not provably orphaned, so Add-without-Done stays silent
// (see hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedSelectMissingDoneInCase() {
	var wg sync.WaitGroup
	ch1 := make(chan int)
	ch2 := make(chan int)
	wg.Add(1)
	go func() {
		select {
		case <-ch1:
			wg.Done()
		case <-ch2:
			// no Done in this case
		}
	}()
	wg.Wait()
}

// Good: type switch with Done in all cases + default
func GoodTypeSwitchAllCasesDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		var x interface{} = 1
		switch x.(type) {
		case int:
			wg.Done()
		case string:
			wg.Done()
		default:
			wg.Done()
		}
	}()
	wg.Wait()
}

// Not flagged: a type switch with Done missing in one case. The linter cannot
// prove the uncovered case is selected, and a present-but-unguaranteed goroutine
// Done means the counter is not provably orphaned, so Add-without-Done stays
// silent (see hasUnguaranteedGoroutineDone in balance.go). Regression guard.
func UnflaggedTypeSwitchMissingDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		var x interface{} = "hello"
		switch x.(type) {
		case int:
			wg.Done()
		case string:
			// no Done for string case
		default:
			wg.Done()
		}
	}()
	wg.Wait()
}

// ---------- Add(n) Balance Edge Cases ----------

// Bad: Add(3) but only 2 goroutines with Done
func BadAddMoreThanGoroutines() {
	var wg sync.WaitGroup
	wg.Add(3) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() { defer wg.Done() }()
	go func() { defer wg.Done() }()
	wg.Wait()
}

// Good: Add(3) matches exactly 3 goroutines
func GoodAddMatchesGoroutines() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done() }()
	go func() { defer wg.Done() }()
	go func() { defer wg.Done() }()
	wg.Wait()
}

// ---------- If/Else Done Patterns ----------

// Good: Both branches of if/else call Done in goroutine
func GoodIfElseBothDone() {
	var wg sync.WaitGroup
	condition := true
	wg.Add(1)
	go func() {
		if condition {
			wg.Done()
		} else {
			wg.Done()
		}
	}()
	wg.Wait()
}

// ---------- Multiple Goroutine Done Patterns ----------

// Good: Multiple goroutines each with defer Done for same WaitGroup
func GoodMultipleGoroutinesSameWg() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
	}()
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// ---------- Defer Wrapped Done Patterns ----------

// Good: Done called inside defer func() { ... }()
func GoodDeferWrappedDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			wg.Done()
		}()
	}()
	wg.Wait()
}

// ========== PACKAGE-LEVEL VARIABLE TESTS ==========

var pkgWG sync.WaitGroup

// Good: package-level waitgroup with proper Add and Done
func GoodPackageLevelWaitGroup() {
	pkgWG.Add(1)
	go func() {
		defer pkgWG.Done()
	}()
	pkgWG.Wait()
}

// Bad: package-level waitgroup with Add but no Done
func BadPackageLevelWaitGroup() {
	pkgWG.Add(1) // want "waitgroup 'pkgWG' has Add without corresponding Done"
	pkgWG.Wait()
}

// ========== STRUCT FIELD ACCESS TESTS ==========

type WorkerPool struct {
	wg sync.WaitGroup
}

type GenericWorkerPool[T any] struct {
	wg    sync.WaitGroup
	value T
}

type EmbeddedWorkerPool struct {
	sync.WaitGroup
}

type HandlerLike struct {
	startWG sync.WaitGroup
}

type ServerSystemLike struct {
	sys struct {
		wg sync.WaitGroup
	}
}

type MirrorLike struct {
	wg sync.WaitGroup
}

// Good: struct field waitgroup with proper Add and Done
func GoodStructFieldWaitGroup() {
	var wp WorkerPool
	wp.wg.Add(1)
	go func() {
		defer wp.wg.Done()
	}()
	wp.wg.Wait()
}

// Bad: struct field waitgroup with Add but no Done
func BadStructFieldWaitGroup() {
	var wp WorkerPool
	wp.wg.Add(1) // want "waitgroup 'wp.wg' has Add without corresponding Done"
	wp.wg.Wait()
}

// Good: method receiver with struct field waitgroup, properly balanced
func (wp *WorkerPool) GoodMethodWaitGroup() {
	wp.wg.Add(1)
	go func() {
		defer wp.wg.Done()
	}()
	wp.wg.Wait()
}

// Bad: method receiver with struct field waitgroup, Add but no Done
func (wp *WorkerPool) BadMethodWaitGroup() {
	wp.wg.Add(1) // want "waitgroup 'wp.wg' has Add without corresponding Done"
	wp.wg.Wait()
}

func takesWorkerPoolByValue(wp WorkerPool) { // want "struct 'wp' containing waitgroup is copied by value"
	wp.wg.Add(1)
	wp.wg.Done()
	wp.wg.Wait()
}

func takesGenericWorkerPoolByValue[T any](wp GenericWorkerPool[T]) { // want "struct 'wp' containing waitgroup is copied by value"
	wp.wg.Add(1)
	wp.wg.Done()
	wp.wg.Wait()
}

func BadStructContainingWaitGroupPassedByValue() {
	var wp WorkerPool
	takesWorkerPoolByValue(wp) // want "struct 'wp' containing waitgroup is copied by value"
}

func BadStructContainingWaitGroupAssignedByValue() {
	var wp WorkerPool
	copied := wp // want "struct 'wp' containing waitgroup is copied by value"
	copied.wg.Wait()
}

func BadGenericStructContainingWaitGroupPassedByValue() {
	wp := GenericWorkerPool[int]{}
	takesGenericWorkerPoolByValue(wp) // want "struct 'wp' containing waitgroup is copied by value"
}

func BadWaitAndDoneInSameGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		wg.Wait() // want "waitgroup 'wg' Wait will deadlock: same goroutine has pending Done"
	}()
}

// GoodShadowedWaitGroupNotConflatedWithOuterDone mirrors minio cmd/erasure.go: a
// worker goroutine defers Done on an OUTER wg while an inner wg (same name,
// shadowed) is fully balanced and waited on. The inner Wait must not be blamed
// for the outer wg's pending Done — the two names resolve to different objects.
func GoodShadowedWaitGroupNotConflatedWithOuterDone(disks []int, work chan int) {
	var wg sync.WaitGroup
	wg.Add(len(disks))
	for range disks {
		go func() {
			defer wg.Done() // outer wg
			for range work {
				var wg sync.WaitGroup // shadows the outer wg
				wg.Add(1)
				go func() {
					defer wg.Done() // inner wg
				}()
				wg.Wait() // inner wg — must stay CLEAN, not a deadlock
			}
		}()
	}
	wg.Wait()
}

func BadParentDoneForWorkerGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		doSomething()
	}()
	wg.Done() // want "waitgroup 'wg' Done called outside worker goroutine"
	wg.Wait()
}

// Good: barrier-style waitgroup where goroutines wait and the main loop releases them with Done.
func GoodBarrierWaitGroupMainLoopDone() {
	const workers = 4

	var startWG sync.WaitGroup
	var endWG sync.WaitGroup

	startWG.Add(workers)
	endWG.Add(workers)

	go func() {
		startWG.Wait()
	}()

	for range workers {
		go func() {
			defer endWG.Done()
			startWG.Wait()
		}()
		startWG.Done()
	}

	endWG.Wait()
}

// Good: embedded waitgroup field managed across sibling methods.
func (wp *EmbeddedWorkerPool) GoodEmbeddedFieldSiblingMethod() {
	wp.WaitGroup.Add(1)
	go wp.run()
	wp.WaitGroup.Wait()
}

func (wp *EmbeddedWorkerPool) run() {
	defer wp.WaitGroup.Done()
}

// Good: constructor-style setup can Add in one function and Done in a lifecycle method.
func GoodConstructorLifecycleWaitGroup() {
	handler := NewHandlerLike()
	handler.Start()
}

func NewHandlerLike() *HandlerLike {
	handler := &HandlerLike{}
	handler.startWG.Add(1)
	return handler
}

func (h *HandlerLike) Start() {
	h.startWG.Done()
}

type workerSliceLifecycle struct {
	wg      sync.WaitGroup
	started chan struct{}
	workers []workerSliceLifecycleThread
}

type workerSliceLifecycleThread struct {
	parent *workerSliceLifecycle
}

func (b *workerSliceLifecycle) GoodAddInLoopDoneInIndexedWorkerMethod() {
	b.started = make(chan struct{})

	for i := range b.workers {
		b.wg.Add(1)
		go b.workers[i].run()
	}

	b.wg.Wait()
	b.wg.Add(len(b.workers))
}

func (b *workerSliceLifecycle) GoodReleaseWorkersAndWaitForSecondPhase() {
	close(b.started)
	b.wg.Wait()
}

func (w *workerSliceLifecycleThread) run() {
	parent := w.parent

	parent.wg.Done()
	<-parent.started
	parent.wg.Done()
}

// Good: a WaitGroup field whose lifecycle is balanced through returned closers.
func GoodWaitGroupLifecycleManagedByCloser() {
	var owner readerLifecycleOwner

	reader, err := owner.OpenReader()
	if err != nil {
		return
	}

	_ = reader.Close()
}

// ========== COMMENT FILTERING TESTS ==========

// Test that commented code is properly ignored by the linter.
// The following commented code should NOT trigger any linter warnings.

/*
func CommentedGoodReuseWaitGroup() {
	var wg sync.WaitGroup

	// First usage
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()

	// Second usage
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}
*/

// func CommentedBadWaitGroupUsage() {
//     var wg sync.WaitGroup
//     wg.Add(1) // This should be ignored
//     wg.Wait()
// }
