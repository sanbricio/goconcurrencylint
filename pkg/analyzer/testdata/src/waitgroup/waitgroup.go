package waitgroup

import "sync"

// ========== CORRECT USAGE (Good cases) ==========

// ---------- WAITGROUP ----------

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

// Add and Done in separate functions
func GoodFuncAddDone() {
	var wg sync.WaitGroup
	wg.Add(1)
	go doWork(&wg)
	wg.Wait()
}
func doWork(wg *sync.WaitGroup) {
	defer wg.Done()
}

// Add and Wait with a gouroutine doing Done()
func GoodAddBeforeWait() {
	var wg sync.WaitGroup
	wg.Add(1) // Add ANTES de iniciar la goroutine
	go func() {
		defer wg.Done() // Solo Done dentro de la goroutine
	}()
	wg.Wait()
}

// No Add, no Done (legal, Wait returns immediately)
func GoodNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // returns immediately
}

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

// Add and Done with a channel to signal completion
func GoodWaitNoAddNoDone() {
	var wg sync.WaitGroup
	wg.Wait() // legal, returns immediately
}

type MyStruct struct {
	wg sync.WaitGroup
}

// Add and Done in a method of a struct
func (m *MyStruct) DoWork() {
	m.wg.Add(1)
	go func() { defer m.wg.Done() }()
	m.wg.Wait()
}

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

// ========== INCORRECT USAGE (Bad cases) ==========

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

// Add after Wait (illegal, Wait should be called after all Adds)
func BadAddAfterWait() {
	var wg sync.WaitGroup
	wg.Wait()
	go func() {
		wg.Add(1) // want "waitgroup 'wg' Add called after Wait"
		wg.Done()
	}()
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
