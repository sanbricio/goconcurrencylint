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

// ========== Add delegated to a callee that received the WaitGroup ==========

type jobItem struct{ id int }

// generateJobs owns the Add for every job it pushes; the caller drains the
// channel and calls the matching Done.
func generateJobs(ch chan *jobItem, wg *sync.WaitGroup, count int) {
	for i := range count {
		wg.Add(1)
		ch <- &jobItem{id: i}
	}
}

// GoodAddDelegatedToCalleeDrainedLocally is the mirror of the delegated Done:
// the Add lives in the callee that received &jobWg, so the local Done calls are
// not excess (tidb's generateAndSendJob tests drain jobs exactly this way).
func GoodAddDelegatedToCalleeDrainedLocally() {
	ch := make(chan *jobItem, 4)
	jobWg := sync.WaitGroup{}
	generateJobs(ch, &jobWg, 4)

	for range 4 {
		<-ch
		jobWg.Done()
	}
	jobWg.Wait()
}

// BadDoneWithoutAddNotShared proves the mirror does not over-suppress: nothing
// receives this WaitGroup, so the unpaired Done is still reported.
func BadDoneWithoutAddNotShared() {
	var localWg sync.WaitGroup
	localWg.Done() // want "waitgroup 'localWg' has Done without corresponding Add"
}

// ========== WaitGroup handed to a method through a receiver literal ==========

type debugHandler struct {
	wg     *sync.WaitGroup
	rounds int
}

func (h *debugHandler) run() {
	defer func() {
		h.wg.Done()
	}()
	h.rounds++
}

// GoodWaitGroupPassedInReceiverLiteral: the goroutine's receiver is built at
// the call site and carries the WaitGroup as a field, so the Done lives in the
// method under a different name.
func GoodWaitGroupPassedInReceiverLiteral() {
	waitGroup := sync.WaitGroup{}
	waitGroup.Add(1)
	defer func() {
		waitGroup.Wait()
	}()
	go (&debugHandler{wg: &waitGroup}).run()
}

type idleHandler struct {
	wg *sync.WaitGroup
}

func (h *idleHandler) run() {
	_ = h.wg // holds the WaitGroup but never releases it
}

// BadWaitGroupPassedInReceiverLiteralNeverDone proves the receiver literal is
// followed rather than trusted: this method never calls Done, so the Add is
// still unbalanced.
func BadWaitGroupPassedInReceiverLiteralNeverDone() {
	waitGroup := sync.WaitGroup{}
	waitGroup.Add(1) // want "waitgroup 'waitGroup' has Add without corresponding Done"
	go (&idleHandler{wg: &waitGroup}).run()
	waitGroup.Wait()
}
