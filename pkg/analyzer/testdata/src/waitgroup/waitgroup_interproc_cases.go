package waitgroup

import "sync"

// workerHost exercises interprocedural Done detection where the Done happens two
// levels deep: a worker goroutine defers a helper that receives the WaitGroup by
// address and calls Done. Mirrors tidb pkg/executor/parallel_apply.go.
type workerHost struct {
	workerWg sync.WaitGroup
}

// GoodDelegatedDoneViaHelperMethod must be CLEAN: the Add is balanced by the
// helper's Done, reached through `go h.worker()` → `defer h.finishWorker(&wg)`.
func (h *workerHost) GoodDelegatedDoneViaHelperMethod() {
	h.workerWg.Add(1)
	go h.worker()
	h.workerWg.Wait()
}

func (h *workerHost) worker() {
	defer h.finishWorker(&h.workerWg)
}

func (h *workerHost) finishWorker(wg *sync.WaitGroup) {
	wg.Done()
}

// BadDelegatedHelperNeverDones proves the delegation-following does not
// over-suppress: the worker delegates to a helper that receives the WaitGroup
// but never calls Done, so the Add is genuinely unbalanced and must be flagged.
func (h *workerHost) BadDelegatedHelperNeverDones() {
	h.workerWg.Add(1) // want "waitgroup 'h.workerWg' has Add without corresponding Done"
	go h.brokenWorker()
	h.workerWg.Wait()
}

func (h *workerHost) brokenWorker() {
	defer h.noopHelper(&h.workerWg)
}

func (h *workerHost) noopHelper(wg *sync.WaitGroup) {
	_ = wg // receives the WaitGroup but never calls Done
}
