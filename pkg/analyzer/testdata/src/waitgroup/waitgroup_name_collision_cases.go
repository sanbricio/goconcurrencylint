package waitgroup

import "sync"

// wgChanLike mimics tailscale's syncs.WaitGroupChan: its method set includes
// Add(int), but completion is signaled via Decr(), not Done(). It is NOT a
// sync.WaitGroup, so its Add must never be tracked by the WaitGroup checker.
type wgChanLike struct{ c chan struct{} }

func newWGChanLike() *wgChanLike { return &wgChanLike{c: make(chan struct{})} }
func (w *wgChanLike) Add(n int)  { _ = n }
func (w *wgChanLike) Decr()      {}

// GoodSameNameWaitGroupAndLookalike reproduces tailscale net/netcheck.GetReport:
// a custom wgChanLike and a real sync.WaitGroup share the name "wg" in one
// function. waitGroupNames is keyed by bare name, so before the type guard the
// look-alike's Add(len(plan)) was blamed as add-without-done. Neither Add here
// is unbalanced; the function must stay clean.
func GoodSameNameWaitGroupAndLookalike(plan, need []int) {
	wg := newWGChanLike()
	wg.Add(len(plan))
	for range plan {
		go func() { wg.Decr() }()
	}

	{
		var wg sync.WaitGroup
		wg.Add(len(need))
		for _, n := range need {
			go func(n int) {
				defer wg.Done()
				_ = n
			}(n)
		}
		wg.Wait()
	}
}

// BadSameNameRealWaitGroupStillFlagged proves the type guard does not
// over-suppress: a real sync.WaitGroup that genuinely misses its Done is still
// flagged even when a same-named look-alike lives in the same function.
func BadSameNameRealWaitGroupStillFlagged(plan []int) {
	wg := newWGChanLike()
	wg.Add(len(plan))
	for range plan {
		go func() { wg.Decr() }()
	}

	{
		var wg sync.WaitGroup
		wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
		wg.Wait()
	}
}
