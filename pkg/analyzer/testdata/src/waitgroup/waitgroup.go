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
			// some work
		default:
			wg.Done() // This always executes
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

func handleExternalWork(wg *sync.WaitGroup) {
	defer wg.Done()
	// external work
}

func doSomething() any {
	return nil
}
