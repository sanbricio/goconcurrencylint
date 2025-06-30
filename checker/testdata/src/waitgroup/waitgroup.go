package waitgroup

import "sync"

// Incorrect: Add without Done
func badWaitGroup1() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Wait()
}

// Correct: Add with Done inside goroutine
func goodWaitGroup1() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
	}()
	wg.Wait()
}

// Incorrect: Multiple Add, only one Done
func badWaitGroup2() {
	var wg sync.WaitGroup
	wg.Add(2) // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Add(1)
	wg.Done()
	wg.Wait()
}

// Correct: Add and Done inside loop
func goodWaitGroup2() {
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

// Incorrect: Add inside loop, missing Done in one path
func badWaitGroup3() {
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

// Correct: Add/Done in separate functions
func goodWaitGroup3() {
	var wg sync.WaitGroup
	wg.Add(1)
	go doWork(&wg)
	wg.Wait()
}

func doWork(wg *sync.WaitGroup) {
	defer wg.Done()
}

// Correct: No Add, no Done (legal)
func goodWaitGroup4() {
	var wg sync.WaitGroup
	wg.Wait() // returns immediately
}

// Incorrect: Add/Done in different goroutines, but one Add never matched
func badWaitGroupWeird() {
	var wg sync.WaitGroup
	ch := make(chan struct{})
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	go func() {
		<-ch // never sends, so Done never called
		wg.Done()
	}()
	// No send to ch, so Done is never reached
	wg.Wait()
}

// Correct: Add/Done with panic recovery
func goodWaitGroupPanic() {
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
