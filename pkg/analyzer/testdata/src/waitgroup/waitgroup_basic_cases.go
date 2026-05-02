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

// Wait without Add is reported even though Wait returns immediately.
func BadNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // want "waitgroup 'wg' Wait called without any Add"
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

// Negative Add is a fragile Done substitute and should be called out directly.
func BadNegativeAddAsDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Add(-1) // want "waitgroup 'wg' has negative Add"
	wg.Wait()
}

// Negative Add without prior work can drive the counter below zero.
func BadNegativeAddWithoutPositiveAdd() {
	var wg sync.WaitGroup
	wg.Add(-1) // want "waitgroup 'wg' has negative Add"
}

func BadNegativeConstAdd() {
	const negativeAdd = -1
	var wg sync.WaitGroup
	wg.Add(negativeAdd) // want "waitgroup 'wg' has negative Add"
}

func BadAddZero() {
	var wg sync.WaitGroup
	wg.Add(0) // want "waitgroup 'wg' Add\\(0\\) is a no-op"
	wg.Wait()
}

func BadAddZeroAfterValidAdd() {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Add(0) // want "waitgroup 'wg' Add\\(0\\) is a no-op"
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

func GoodAddUnknownVariableNoAddZero(n int) {
	var wg sync.WaitGroup
	wg.Add(n)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

func GoodAddPositive() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// ---------- Wait Ordering Patterns ----------

// Add after Wait (illegal, Wait should be called after all Adds)
func BadAddAfterWait() {
	var wg sync.WaitGroup
	wg.Wait()
	go func() {
		wg.Add(1) // want "waitgroup 'wg' Add called after Wait" "waitgroup 'wg' Add called inside goroutine, may race with Wait"
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
	wg.Wait() // want "waitgroup 'wg' Wait called without any Add"
}

func GoodIgnoredWaitWithoutAdd() {
	var wg sync.WaitGroup
	wg.Wait() // goconcurrencylint:ignore wait-without-add
}

// Waiting in the same goroutine before any separate Done can run deadlocks.
func BadWaitBeforeDoneSameGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Wait() // want "waitgroup 'wg' waits with pending Add in the same goroutine"
	wg.Done()
}

// Main-flow Done before Wait drains the counter and is legal.
func GoodDoneBeforeWaitSameGoroutine() {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Done()
	wg.Wait()
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

func GoodWaitGroupGoRecoveredPanic() {
	var wg sync.WaitGroup
	wg.Go(func() {
		defer func() {
			_ = recover()
		}()
		panic("handled")
	})
	wg.Wait()
}

func BadWaitGroupGoPanic() {
	var wg sync.WaitGroup
	wg.Go(func() { // want "waitgroup 'wg' Go function may panic"
		panic("boom")
	})
	wg.Wait()
}

func BadWaitGroupGoRecoverOutsideDefer() {
	var wg sync.WaitGroup
	wg.Go(func() { // want "waitgroup 'wg' Go function may panic"
		_ = recover()
		panic("boom")
	})
	wg.Wait()
}

func BadWaitGroupGoRecoverInNestedFunction() {
	var wg sync.WaitGroup
	wg.Go(func() { // want "waitgroup 'wg' Go function may panic"
		go func() {
			_ = recover()
		}()
		panic("boom")
	})
	wg.Wait()
}
