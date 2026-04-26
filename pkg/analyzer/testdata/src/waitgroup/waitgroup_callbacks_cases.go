package waitgroup

import "sync"

// ---------- Function Passing Patterns ----------

// WaitGroup passed to goroutine directly
func GoodWaitGroupPassedToGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	go handleShutdown(&wg, nil) // Passed as pointer
	wg.Wait()
}

// WaitGroup passed as value copies the counter and does not release the original.
func BadWaitGroupPassedAsValue() {
	var wg sync.WaitGroup
	wg.Add(1)
	go processWork(wg) // want "waitgroup 'wg' is copied by value"
	wg.Wait()
}

func BadWaitGroupAssignedByValue() {
	var wg sync.WaitGroup
	wg.Add(1)
	copied := wg // want "waitgroup 'wg' is copied by value"
	go func() {
		defer copied.Done()
	}()
	copied.Wait()
	wg.Done()
	wg.Wait()
}

func BadWaitGroupVarCopiedByValue() {
	var wg sync.WaitGroup
	wg.Add(1)
	var copied = wg // want "waitgroup 'wg' is copied by value"
	go func() {
		defer copied.Done()
	}()
	copied.Wait()
	wg.Done()
	wg.Wait()
}

func BadWaitGroupMultiVarCopiedByValue() {
	var wgA, wgB sync.WaitGroup
	wgA.Add(1)
	wgB.Add(1)
	var copiedA, copiedB = wgA, wgB // want "waitgroup 'wgA' is copied by value" "waitgroup 'wgB' is copied by value"
	go func() {
		defer copiedA.Done()
	}()
	go func() {
		defer copiedB.Done()
	}()
	copiedA.Wait()
	copiedB.Wait()
	wgA.Done()
	wgB.Done()
	wgA.Wait()
	wgB.Wait()
}

func BadWaitGroupFuncLiteralParamByValue() {
	_ = func(wg sync.WaitGroup) { // want "waitgroup 'wg' is copied by value"
		wg.Done()
	}
}

func GoodWaitGroupNewAllocation() {
	wg := new(sync.WaitGroup)
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
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

func GoodWaitGroupDoneInErrorCallbacks() {
	var wg sync.WaitGroup
	wg.Add(2)

	runWithErrorCallback(func(err error) {
		defer wg.Done()
		_ = err
	})

	runWithErrorCallback(func(err error) {
		defer wg.Done()
		_ = err
	})

	wg.Wait()
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

type externalCallbackRegistrar interface {
	Register(func())
}

type externalActivityRegistrar interface {
	RegisterActivity(func() error)
}

type externalTaskSubmitter interface {
	Submit(taskCarrier)
}

type taskCarrier struct {
	*sync.WaitGroup
}

func (t taskCarrier) Ack() {
	t.Done()
}

// A callback literal passed to an external registrar still means the WaitGroup lifecycle escaped.
func GoodEscapingWaitGroupDoneCallback(reg externalCallbackRegistrar) {
	var wg sync.WaitGroup
	wg.Add(1)
	reg.Register(func() {
		wg.Done()
	})
	wg.Wait()
}

// A callback variable registered externally can own the WaitGroup Done lifecycle.
func GoodEscapingWaitGroupDoneFuncVariable(reg externalActivityRegistrar) {
	var wg sync.WaitGroup
	wg.Add(2)

	activity := func() error {
		defer wg.Done()
		return nil
	}

	reg.RegisterActivity(activity)
	reg.RegisterActivity(activity)
	wg.Wait()
}

// A task object carrying a WaitGroup can escape to another component that will Ack it later.
func GoodEscapingWaitGroupInsideTaskObject(submitter externalTaskSubmitter) {
	var wg sync.WaitGroup
	wg.Add(1)
	submitter.Submit(taskCarrier{WaitGroup: &wg})
	wg.Wait()
}

// Branch-exclusive Done paths should not be counted as multiple excess Done calls.
func GoodBranchExclusiveDonePaths(startWorker, enqueue bool) {
	var wg sync.WaitGroup
	wg.Add(1)

	if !startWorker {
		wg.Done()
		return
	}

	if enqueue {
		wg.Done()
		return
	}

	go func() {
		defer wg.Done()
	}()

	wg.Wait()
}
