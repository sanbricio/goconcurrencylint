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
