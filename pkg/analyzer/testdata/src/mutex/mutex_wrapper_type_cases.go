package mutex

import "sync"

// Projects that outgrow a bare sync.Mutex usually wrap one in a named type and
// lock through the promoted method, so `x.Lock()` is a lock on the mutex the
// type embeds. Which method the selector resolves to decides whether the type
// is tracked: a wrapper that keeps the promoted pair behaves like the mutex it
// embeds, one that overrides either half does not.

type guardedCounter struct {
	sync.Mutex
	count int
}

// Two levels of embedding: the shape a house lock package takes when a public
// wrapper hides a build-tag-selected implementation.
type innerGuard struct {
	sync.Mutex
}

type layeredGuard struct {
	innerGuard
}

type guardedReadWrite struct {
	sync.RWMutex
}

// Only the release half is overridden, so the promoted Lock and the declared
// Unlock are not the plain sync pair these checks model.
type auditedGuard struct {
	sync.Mutex
	unlocks int
}

func (a *auditedGuard) Unlock() {
	a.unlocks++
	a.Mutex.Unlock()
}

type wrapperHolder struct {
	guard  guardedCounter
	shared guardedReadWrite
}

func BadWrapperLockWithoutUnlock() {
	var g guardedCounter
	g.Lock() // want "mutex 'g' is locked but not unlocked"
	g.count++
}

func BadLayeredWrapperLockWithoutUnlock() {
	var g layeredGuard
	g.Lock() // want "mutex 'g' is locked but not unlocked"
}

func BadWrapperUnlockWithoutLock() {
	var g guardedCounter
	g.Unlock() // want "mutex 'g' is unlocked but not locked"
}

func BadWrapperParameterLockWithoutUnlock(g *guardedCounter) {
	g.Lock() // want "mutex 'g' is locked but not unlocked"
}

func BadWrapperFieldLockWithoutUnlock(w *wrapperHolder) {
	w.guard.Lock() // want "mutex 'w.guard' is locked but not unlocked"
}

// The receiver is the mutex when the method locks the type itself.
func (g *guardedCounter) BadIncrementWithoutUnlock() {
	g.Lock() // want "mutex 'g' is locked but not unlocked"
	g.count++
}

func BadReadWriteWrapperRUnlockMismatch() {
	var rw guardedReadWrite
	rw.RLock()
	rw.Unlock() // want "rwmutex 'rw' Unlock called but only read lock is held"
}

func BadReadWriteWrapperRLockWithoutRUnlock(w *wrapperHolder) {
	w.shared.RLock() // want "rwmutex 'w.shared' is rlocked but not runlocked"
}

func BadLockOnlyWrapperDeclaredInsideLoop(items []int) {
	for range items {
		var g layeredGuard // want "mutex 'g' declared inside loop, each iteration creates a new mutex that cannot protect shared state"
		g.Lock()
		g.Unlock()
	}
}

// The object embeds a lock but is not one: a fresh instance per iteration is
// how table-driven tests are written.
func GoodObjectEmbeddingLockDeclaredInsideLoop(items []int) {
	for range items {
		g := guardedCounter{}
		g.Lock()
		g.count++
		g.Unlock()
	}
}

func GoodWrapperLockWithDeferredUnlock() {
	var g guardedCounter
	g.Lock()
	defer g.Unlock()
	g.count++
}

func GoodLayeredWrapperLockWithDeferredUnlock() {
	var g layeredGuard
	g.Lock()
	defer g.Unlock()
}

func GoodWrapperFieldLockWithDeferredUnlock(w *wrapperHolder) {
	w.guard.Lock()
	defer w.guard.Unlock()
	w.guard.count++
}

func (g *guardedCounter) GoodIncrement() {
	g.Lock()
	defer g.Unlock()
	g.count++
}

func GoodReadWriteWrapperReadLock(w *wrapperHolder) {
	w.shared.RLock()
	defer w.shared.RUnlock()
}

// An overridden release means the wrapper is not tracked: its Unlock does more
// than release, and the counters these checks keep would not describe it.
func GoodOverriddenUnlockNotTracked() {
	var a auditedGuard
	a.Lock()
}
