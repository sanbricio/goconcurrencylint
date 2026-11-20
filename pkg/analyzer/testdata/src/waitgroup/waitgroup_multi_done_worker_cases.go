package waitgroup

import "sync"

// A worker may pay for several units of the Add: one Done per item it
// handles. The number of launched goroutines then says nothing about the
// count, so the Add is not compared with it.

// GoodEachWorkerDonesPerItem must be CLEAN: 4 workers × 5 items = 20.
func GoodEachWorkerDonesPerItem(handle func(int)) {
	const workers, items = 4, 5
	var wg sync.WaitGroup
	wg.Add(workers * items)
	for i := 0; i < workers; i++ {
		go func() {
			for j := 0; j < items; j++ {
				handle(j)
				wg.Done()
			}
		}()
	}
	wg.Wait()
}

// BadOneDonePerWorkerShort must be flagged: each worker releases once.
func BadOneDonePerWorkerShort(handle func(int)) {
	var wg sync.WaitGroup
	wg.Add(3) // want "waitgroup 'wg' Add count 3 does not match 4 goroutines launched"
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			handle(i)
		}()
	}
	wg.Wait()
}
