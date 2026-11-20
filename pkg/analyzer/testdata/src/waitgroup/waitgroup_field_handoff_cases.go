package waitgroup

import "sync"

// A WaitGroup held in a struct field travels with the struct: one function
// Adds and hands the struct (or a work item pointing at it) away, another
// function of the package calls Done where the work finishes. The Add alone is
// half of a balance, so it must not be flagged.

type streamParseCtx struct {
	wg sync.WaitGroup
}

type parseWork struct {
	ctx *streamParseCtx
}

func (w *parseWork) run() {
	ctx := w.ctx
	ctx.wg.Done()
}

var parseQueue = make(chan *parseWork, 16)

// GoodFieldAddDoneInWorkItemMethod must be CLEAN. Mirrors VictoriaMetrics
// lib/protoparser/*/stream: the Done runs in the work item's method, reached
// through a different spelling (`w.ctx.wg`) of the same field.
func GoodFieldAddDoneInWorkItemMethod(ctx *streamParseCtx, n int) {
	for i := 0; i < n; i++ {
		ctx.wg.Add(1)
		parseQueue <- &parseWork{ctx: ctx}
	}
	ctx.wg.Wait()
}

// BadFieldOwnerNeverLeaves must be flagged: parseWork.run releases this field,
// but the ctx here is created locally and never handed to anything that could
// reach that Done.
func BadFieldOwnerNeverLeaves() {
	var ctx streamParseCtx
	ctx.wg.Add(1) // want "waitgroup 'ctx.wg' has Add without corresponding Done"
	ctx.wg.Wait()
}

type flightCall struct {
	wg  sync.WaitGroup
	val int
}

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

// GoodFieldAddOnStoredCallDoneInOtherMethod must be CLEAN. Mirrors go-zero
// core/syncx/singleflight.go: the call is stored in the map with its counter
// raised, and finishCall lowers it.
func (g *flightGroup) GoodFieldAddOnStoredCallDoneInOtherMethod(key string) *flightCall {
	g.mu.Lock()
	c := new(flightCall)
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()
	return c
}

func (g *flightGroup) finishCall(c *flightCall, key string) {
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	c.wg.Done()
}

type orphanFieldWork struct {
	pending sync.WaitGroup
}

// BadFieldAddNoDoneAnywhere must be flagged: no function of the package ever
// calls Done on this field, so the counter can only grow.
func (o *orphanFieldWork) BadFieldAddNoDoneAnywhere() {
	o.pending.Add(1) // want "waitgroup 'o.pending' has Add without corresponding Done"
	o.pending.Wait()
}

// ========== Same shape, other spellings ==========

type sharedCounter struct {
	wg *sync.WaitGroup
}

func (s *sharedCounter) release() { s.wg.Done() }

// GoodPointerFieldAddOnReceiverReleasedInOtherMethod must be CLEAN: a
// *sync.WaitGroup field is the same field object wherever it is used, and the
// receiver is reachable from every method.
func (s *sharedCounter) GoodPointerFieldAddOnReceiverReleasedInOtherMethod() {
	s.wg.Add(1)
}

type batchOf[T any] struct {
	pending sync.WaitGroup
	items   []T
}

func (b *batchOf[T]) finish() { b.pending.Done() }

var batchSink []*batchOf[int]

// GoodGenericFieldAddReleasedByGenericMethod must be CLEAN: the instantiated
// field batchOf[int].pending is folded onto the field declared in batchOf[T].
func GoodGenericFieldAddReleasedByGenericMethod(b *batchOf[int]) {
	b.pending.Add(1)
	batchSink = append(batchSink, b)
}

type jobOwner struct {
	wg sync.WaitGroup
}

type queuedJob struct {
	owner *jobOwner
}

func (j *queuedJob) complete() { j.owner.wg.Done() }

var jobQueue = make(chan *queuedJob, 8)

// GoodOwnerLeavesThroughLocalCopy must be CLEAN: the owner is wrapped in a
// local job first, and the job is what crosses the channel.
func GoodOwnerLeavesThroughLocalCopy(o *jobOwner) {
	o.wg.Add(1)
	job := &queuedJob{owner: o}
	jobQueue <- job
}

// BadOwnerOnlyReachesLocalChannel must be flagged: complete() releases the
// field, but the only place the owner goes is a channel created and dropped
// here.
func BadOwnerOnlyReachesLocalChannel(o *jobOwner) {
	local := make(chan *queuedJob, 1)
	o.wg.Add(1) // want "waitgroup 'o.wg' has Add without corresponding Done"
	local <- &queuedJob{owner: o}
	o.wg.Wait()
}
