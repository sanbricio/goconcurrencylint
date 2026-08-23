package packagelevel

import (
	"log"
	"sync"
)

func BadPackageLevelMutex() {
	packageMu.Lock() // want "mutex 'packageMu' is locked but not unlocked"
}

func BadPackageLevelWaitGroup() {
	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	packageWG.Wait()
}

func GoodPackageLevelWaitGroupDoneInHelper() {
	packageWG.Add(1)
	go packageLevelWorker()
	packageWG.Wait()
}

func packageLevelWorker() {
	defer packageWG.Done()
}

func BadShadowedPackageLevelWaitGroup() {
	var packageWG sync.WaitGroup

	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	go packageLevelWorker()
	packageWG.Wait()
}

// ========== package-level counter split across an interface ==========

type packageTaskHandler interface {
	handle(task int)
}

type packageTaskPool struct {
	handler packageTaskHandler
}

func (p *packageTaskPool) submit(task int) {
	go p.handler.handle(task)
}

type packageTaskWorker struct{}

// The worker releases a counter someone else raised; there is no Add here to
// pair it with, and none is expected.
func (packageTaskWorker) handle(task int) {
	_ = task
	poolWG.Done()
}

// Good: the Add hands both tasks to the pool, which reaches the Done through an
// interface. No call edge links the two halves, so the local totals are a
// fragment of the balance rather than the balance itself.
func GoodPackageLevelWaitGroupDoneBehindInterface(pool *packageTaskPool) {
	poolWG.Add(2)
	pool.submit(1)
	pool.submit(2)
	poolWG.Wait()
}

// Bad: the only call between the Add and the Wait is into another package,
// which cannot name this package's WaitGroup, so nothing here handed the work
// away and the Add is still unpaired.
func BadPackageLevelWaitGroupWithForeignCall() {
	packageWG.Add(1) // want "waitgroup 'packageWG' has Add without corresponding Done"
	log.Print("starting")
	packageWG.Wait()
}

// GoodPackageLevelWaitGroupResetAfterWait resets shared package state.
func GoodPackageLevelWaitGroupResetAfterWait() {
	resetWG.Wait()

	resetWG = &sync.WaitGroup{}
}
