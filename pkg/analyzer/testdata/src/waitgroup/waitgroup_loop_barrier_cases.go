package waitgroup

import "sync"

// A Wait inside a loop waits for the Adds of earlier iterations, even when
// those Adds are written below it: reusing the WaitGroup once Wait has
// returned is allowed.

func barrierWork(int) {}

// GoodWaitAsBarrierInLoop must be CLEAN: the Wait drains what earlier
// iterations started before a barrier item is handled.
func GoodWaitAsBarrierInLoop(items []int, barrier func(int) bool) {
	var wg sync.WaitGroup
	for _, it := range items {
		if barrier(it) {
			wg.Wait()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			barrierWork(it)
		}()
	}
	wg.Wait()
}

// GoodWaitBeforeEachBatch must be CLEAN: each batch starts once the previous
// one is done.
func GoodWaitBeforeEachBatch(batches [][]int) {
	var wg sync.WaitGroup
	for _, batch := range batches {
		wg.Wait()
		for _, it := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				barrierWork(it)
			}()
		}
	}
	wg.Wait()
}

// BadWaitOnlyInEarlierLoop must be flagged: the loop holding the Wait ends
// before any Add runs, so nothing waits for the goroutines.
func BadWaitOnlyInEarlierLoop(items []int) {
	var wg sync.WaitGroup
	for range items {
		wg.Wait()
	}
	for _, it := range items {
		wg.Add(1) // want "waitgroup 'wg' Add called after Wait"
		go func() {
			defer wg.Done()
			barrierWork(it)
		}()
	}
}
