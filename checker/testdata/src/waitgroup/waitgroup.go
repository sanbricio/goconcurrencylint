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

// ========== INCORRECT USAGE (Bad cases) ==========

// ---------- WAITGROUP ----------

// Add without Done (counter never decremented)
func BadAddWithoutDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
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

// Add without Done in a goroutine that never runs
func EdgeCaseNoAddNoDoneNoGoroutine() {
	var wg sync.WaitGroup
	wg.Wait() // legal, returns immediately
}