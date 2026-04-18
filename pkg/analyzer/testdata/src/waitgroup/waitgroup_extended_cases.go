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
