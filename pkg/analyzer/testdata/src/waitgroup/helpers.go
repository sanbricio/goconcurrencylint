package waitgroup

import "sync"

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
