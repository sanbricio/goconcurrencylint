package waitgroup

import "sync"

// ===== where a WaitGroup comes from decides what its counter is =====

// startCountedStream hands its caller a group whose counter it already raised.
func startCountedStream() (*sync.WaitGroup, <-chan int) {
	wg := &sync.WaitGroup{}
	ch := make(chan int, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ch <- 1
	}()
	return wg, ch
}

// A group received as one result of a call carries whatever counter the callee
// left on it: waiting on it without a local Add is how such a group is used.
func GoodWaitOnGroupFromTupleResult() {
	wg, events := startCountedStream()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	<-done
	<-events
}

// A group looked up in a map belongs to whoever stored it there; its owner did
// the Add before publishing it.
func GoodWaitOnGroupFromMapLookup(latches map[uint64]*sync.WaitGroup, key uint64) {
	wg, ok := latches[key]
	if ok {
		wg.Wait()
	}
}

func GoodWaitOnGroupFromMapIndex(latches map[uint64]*sync.WaitGroup, key uint64) {
	wg := latches[key]
	wg.Wait()
}

// A group built right here starts at zero whichever assignment form builds it.
func BadWaitOnFreshGroupBesideOtherValues() {
	wg, n := &sync.WaitGroup{}, 1
	_ = n
	wg.Wait() // want "waitgroup 'wg' Wait called without any Add"
}

func BadWaitOnGroupFromNew() {
	wg := new(sync.WaitGroup)
	wg.Wait() // want "waitgroup 'wg' Wait called without any Add"
}

// ===== counting the goroutines an Add pays for =====

// One goroutine launched directly and eight from the loop: nine in all.
func GoodAddCoversDirectAndLoopWorkers() {
	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers + 1)
	go func() {
		defer wg.Done()
	}()
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

func BadAddShortOfDirectAndLoopWorkers() {
	var wg sync.WaitGroup
	wg.Add(10) // want "waitgroup 'wg' has Add without corresponding Done" "waitgroup 'wg' Add count 10 does not match 9 goroutines launched"
	go func() {
		defer wg.Done()
	}()
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

// Each worker raises the counter for the goroutines it launches itself: those
// are paid by the worker's own Add, not by the Add before the loop.
func GoodWorkersAddForTheirOwnChildren() {
	var wg sync.WaitGroup
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()
			ids := make([]int, 10)
			wg.Add(1 + len(ids)/2)
			for range ids[:len(ids)/2] {
				go func() {
					defer wg.Done()
				}()
			}
			go func() {
				defer wg.Done()
			}()
		}()
	}
	wg.Wait()
}

func BadAddShortOfWorkersThatAddForChildren() {
	var wg sync.WaitGroup
	wg.Add(50) // want "waitgroup 'wg' Add count 50 does not match 100 goroutines launched"
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()
			wg.Add(1)
			go func() {
				defer wg.Done()
			}()
		}()
	}
	wg.Wait()
}

// A launch that only some iterations make cannot be counted exactly, so no
// exact mismatch is claimed.
func GoodAddAgainstConditionalLoopLaunches(skip func(int) bool) {
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 5; i++ {
		if skip(i) {
			continue
		}
		go func() {
			defer wg.Done()
		}()
	}
	wg.Wait()
}

// A closure called on the spot launches from the loop itself: four workers.
func BadAddShortOfWorkersLaunchedByInlineClosure() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' Add count 1 does not match 4 goroutines launched"
	for i := 0; i < 4; i++ {
		func() {
			go func() {
				defer wg.Done()
			}()
		}()
	}
	wg.Wait()
}

// A closure that is only defined in the loop launches whenever its caller runs
// it; nothing says how often.
func GoodAddAgainstClosureLaunches(run func(func())) {
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 4; i++ {
		run(func() {
			go func() {
				defer wg.Done()
			}()
		})
	}
	wg.Wait()
}

// Two more workers come from the callback, which the loop count cannot see.
func GoodAddCoversLoopAndCallbackWorkers(run func(func())) {
	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
		}()
	}
	run(func() {
		for j := 0; j < 2; j++ {
			go func() {
				defer wg.Done()
			}()
		}
	})
	wg.Wait()
}
