package waitgroup

import "sync"

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

// Non-deferred Done after a possible panic is not guaranteed to run.
func BadDoneAfterConditionalPanic() {
	var wg sync.WaitGroup
	shouldPanic := true
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		if shouldPanic {
			panic("error")
		}
		wg.Done() // want "waitgroup 'wg' Done should be deferred so it runs on panic or runtime.Goexit"
	}()
	wg.Wait()
}

func BadDoneNotDeferredInWorker() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		doSomething()
		wg.Done() // want "waitgroup 'wg' Done should be deferred so it runs on panic or runtime.Goexit"
	}()
	wg.Wait()
}

func BadDoneNotDeferredMultipleCalls() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		doSomething()
		doSomething()
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
