package waitgroup

import "sync"

// ========== WAITGROUP TESTS ==========

// ---------- Basic Add/Done Patterns ----------

// Add and Done inside a goroutine
func GoodBasicAddDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// Correct WaitGroup usage with short declaration
func GoodWaitGroupShortDecl() {
	wg := sync.WaitGroup{} // short declaration
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// Add and Done in separate functions
func GoodFuncAddDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go handleExternalWork(&wg)
	wg.Wait()
}

// Add and Wait with a gouroutine doing Done()
func GoodAddBeforeWait() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// No Add, no Done (legal, Wait returns immediately)
func GoodNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // returns immediately
}

// Add without Done (counter never decremented)
func BadAddWithoutDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Wait()
}

// Bad Waitgroup with short declaration
func BadWaitGroupShortDecl() {
	wg := sync.WaitGroup{} // short declaration
	wg.Add(1)              // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Wait()
}

// Multiple Add, but only one Done (the rest remain pending)
func BadMultipleAddOneDone() {
	var wg sync.WaitGroup
	wg.Add(2) // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Add(1)
	wg.Done()
	wg.Wait()
}

// Add without Done in a goroutine that never runs
func BadExtraDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Done()
	wg.Done() // want "waitgroup 'wg' has Done without corresponding Add"
	wg.Wait()
}

// ---------- Wait Ordering Patterns ----------

// Add after Wait (illegal, Wait should be called after all Adds)
func BadAddAfterWait() {
	var wg sync.WaitGroup
	wg.Wait()
	go func() {
		wg.Add(1) // want "waitgroup 'wg' Add called after Wait"
		wg.Done()
	}()
}

// Edge case where Add is called after Wait, but in a different flow
func EdgeCaseAddAfterWaitMainFlow() {
	var wg sync.WaitGroup

	wg.Wait()
	wg.Add(1) // want "waitgroup 'wg' Add called after Wait"
	wg.Done()
}

// Edge case where Wait is called without any Adds
func EdgeCaseNoAddNoDoneNoGoroutine() {
	var wg sync.WaitGroup
	wg.Wait() // legal, returns immediately
}

// Edge case where Wait is called multiple times
func EdgeCaseMultipleWaits() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
	wg.Wait()
}

// WaitGroup.Go handles Add/Done internally and should be accepted.
func GoodWaitGroupGo() {
	var wg sync.WaitGroup
	wg.Go(func() {
		doSomething()
	})
	wg.Wait()
}

// WaitGroup.Go after an empty Wait should be rejected, same as Add after Wait.
func BadWaitGroupGoAfterWait() {
	var wg sync.WaitGroup
	wg.Wait()
	wg.Go(func() { // want "waitgroup 'wg' Go called after Wait"
		doSomething()
	})
}

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

// Add in a goroutine that never calls Done (e.g., due to deadlock or channel never sent)
func BadAddNeverDone() {
	var wg sync.WaitGroup
	ch := make(chan struct{})
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		<-ch // never sends, so Done is never called
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

// Add without Done in a goroutine with a conditional return
func BadDeferWithConditionalReturn() {
	var wg sync.WaitGroup
	wg.Add(2) // want "waitgroup 'wg' has Add without corresponding Done"

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

// Add without Done in a goroutine with conditional Done
func BadConditionalDone() {
	var wg sync.WaitGroup
	condition := false
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		if condition { // condition is false, so Done is never called
			wg.Done()
		}
	}()
	wg.Wait()
}

// Also test with a more complex conditional
func BadConditionalDoneComplex() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		x := 1
		if x > 5 { // This is always false
			wg.Done()
		}
	}()
	wg.Wait()
}

// Test with switch statement
func BadConditionalDoneSwitch() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		x := 1
		switch x {
		case 2: // x is 1, so this case won't match
			wg.Done()
		}
	}()
	wg.Wait()
}

func BadSwitchNoDefault() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		x := 1
		switch x {
		case 2:
			wg.Done()
		case 3:
			wg.Done()
			// No default, and x=1 doesn't match any case
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

// ---------- Function Passing Patterns ----------

// WaitGroup passed to goroutine directly
func GoodWaitGroupPassedToGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	go handleShutdown(&wg, nil) // Passed as pointer
	wg.Wait()
}

// WaitGroup passed as value
func GoodWaitGroupPassedAsValue() {
	var wg sync.WaitGroup
	wg.Add(1)
	go processWork(wg) // Passed as value, not pointer
	wg.Wait()
}

// WaitGroup method passed as function
func GoodWaitGroupMethodPassed() {
	var wg sync.WaitGroup
	wg.Add(1)
	go runWithCallback(wg.Done) // Done method passed as callback
	wg.Wait()
}

type callbackOwner struct {
	wg sync.WaitGroup
}

func (o *callbackOwner) GoodWaitGroupFieldMethodPassed() {
	o.wg.Add(1)
	go runWithCallback(o.wg.Done)
	o.wg.Wait()
}

func (o *callbackOwner) GoodWaitGroupFieldMethodPassedThroughOnce() {
	o.wg.Add(1)
	go runWithOnceCallback(o.wg.Done)
	o.wg.Wait()
}

// WaitGroup Done callback passed through a runner helper that starts goroutines internally.
func GoodWaitGroupDoneCallbackPassedIntoRunner() {
	var wg sync.WaitGroup
	wg.Add(2)

	runner := newCallbackRunner(
		func(stopCh chan struct{}) { runUntilFirstSuccess(wg.Done, stopCh) },
		func(stopCh chan struct{}) { runUntilFirstSuccess(wg.Done, stopCh) },
	)
	runner.Start()
	wg.Wait()
}

func GoodWaitGroupDoneInRegisteredCallback() {
	var wg sync.WaitGroup
	wg.Add(1)
	registerCallback(func() {
		wg.Done()
	})
	wg.Wait()
}

func GoodWaitGroupDoneInCallbackVariable() {
	var wg sync.WaitGroup
	wg.Add(3)

	test := func() {
		wg.Done()
		wg.Wait()
	}

	runNamedCallbacks(test, test, test)
}

type syncHooks struct {
	run func()
}

func (o *callbackOwner) GoodWaitGroupMethodValueAssignedToField() {
	hooks := &syncHooks{}
	o.wg.Add(1)
	hooks.run = o.markDone
	go hooks.run()
	o.wg.Wait()
}

// Reusing a WaitGroup as a generation barrier is valid when Add happens
// after the previous Wait has returned and the same goroutine later calls Done.
func GoodWaitGroupReuseAfterWait() {
	var wg sync.WaitGroup

	startCycle(&wg)
	startCycle(&wg)
}

func GoodWaitGroupAddBeforeFixedLoopGoroutines() {
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func GoodWaitGroupInsideCoordinatorGoroutine() {
	done := make(chan struct{})

	go func() {
		defer close(done)

		var wg sync.WaitGroup
		wg.Add(3)
		for i := 0; i < 3; i++ {
			go func() {
				defer wg.Done()
			}()
		}
		wg.Wait()
	}()

	<-done
}

type RestartableReflector struct {
	wg sync.WaitGroup
}

func (r *RestartableReflector) GoodWaitGroupReuseAfterWaitOnField() {
	r.wg.Wait()
	r.wg.Add(1)
	defer r.wg.Done()
}

// A goroutine that only waits on the WaitGroup is a coordinator, not work that
// needs to call Done itself.
func GoodWaitGroupWaitOnlyGoroutine() {
	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
	}()

	go func() {
		defer close(done)
		wg.Wait()
	}()

	<-done
}

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

// Add and Wait with a channel to signal completion
func GoodWaitNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // legal, returns immediately
}

// ---------- Switch/Select Edge Cases ----------

// Bad: switch with default that has Done, but another case does NOT
func BadSwitchDefaultOnlyDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		x := 1
		switch x {
		case 2:
			// no Done here - if x==2, Done is never called
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

// Bad: select with Done missing in one comm clause
func BadSelectMissingDoneInCase() {
	var wg sync.WaitGroup
	ch1 := make(chan int)
	ch2 := make(chan int)
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		select {
		case <-ch1:
			wg.Done()
		case <-ch2:
			// forgot Done in this case
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

// Bad: type switch with Done missing in one case
func BadTypeSwitchMissingDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		var x interface{} = "hello"
		switch x.(type) {
		case int:
			wg.Done()
		case string:
			// forgot Done for string case
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

// ========== HELPER FUNCTIONS ==========

// Helper functions for the test cases above
func handleWork(wg *sync.WaitGroup) {
	defer wg.Done()
	// do work
}

func handleShutdown(wg *sync.WaitGroup, servers interface{}) {
	defer wg.Done()
	// shutdown logic
}

func processWork(wg sync.WaitGroup) {
	defer wg.Done()
	// process work
}

func runWithCallback(done func()) {
	defer done()
	// run with callback
}

func runWithOnceCallback(done func()) {
	var once sync.Once
	once.Do(done)
}

func registerCallback(callback func()) {
	go callback()
}

func runNamedCallbacks(callbacks ...func()) {
	for _, callback := range callbacks {
		go callback()
	}
}

type callbackRunner struct {
	loopFuncs []func(stopCh chan struct{})
}

func newCallbackRunner(loopFuncs ...func(stopCh chan struct{})) *callbackRunner {
	return &callbackRunner{loopFuncs: loopFuncs}
}

func (r *callbackRunner) Start() {
	stopCh := make(chan struct{})
	for _, loopFn := range r.loopFuncs {
		go loopFn(stopCh)
	}
}

func runUntilFirstSuccess(onFirstSuccess func(), stopCh chan struct{}) {
	_ = stopCh
	onFirstSuccess()
}

func startCycle(wg *sync.WaitGroup) {
	wg.Wait()
	wg.Add(1)
	defer wg.Done()
}

func handleExternalWork(wg *sync.WaitGroup) {
	defer wg.Done()
	// external work
}

func (o *callbackOwner) markDone() {
	o.wg.Done()
}

func doSomething() any {
	return nil
}
