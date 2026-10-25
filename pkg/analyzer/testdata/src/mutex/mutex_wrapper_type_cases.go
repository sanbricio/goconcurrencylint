package mutex

import (
	"sync"

	"ctxsem"
)

// Projects that outgrow a bare sync.Mutex usually wrap one in a named type and
// lock through the promoted method, so `x.Lock()` is a lock on the mutex the
// type embeds. A wrapper that overrides a half — to count releases, to time the
// wait — no longer resolves to the embedded pair, but it still satisfies
// sync.Locker, and that interface is the language saying Lock acquires and
// Unlock releases. Both kinds are tracked; what decides is the method set, never
// a name.

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

// Only the release half is overridden: the pair is a promoted Lock and a
// declared Unlock, which together are still sync.Locker.
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

// An overridden release is still a release: the type satisfies sync.Locker, so
// a lock taken and never handed back is reported like any other.
func BadAuditedGuardLockWithoutRelease() {
	var a auditedGuard
	a.Lock() // want "mutex 'a' is locked but not unlocked"
}

func GoodAuditedGuardLockWithRelease() {
	var a auditedGuard
	a.Lock()
	defer a.Unlock()
	a.unlocks++
}

// timedGuard owns no promoted lock pair at all: both halves are its own, and the
// inner mutex is a plain field. The method set alone makes it a sync.Locker.
type timedGuard struct {
	mu    sync.Mutex
	waits int
}

func (t *timedGuard) Lock() {
	t.waits++
	t.mu.Lock()
}

func (t *timedGuard) Unlock() {
	t.mu.Unlock()
}

var _ sync.Locker = (*timedGuard)(nil)

func BadHandWrittenLockerWithoutUnlock(t *timedGuard) {
	t.Lock() // want "mutex 't' is locked but not unlocked"
	t.waits++
}

func GoodHandWrittenLockerWithDeferredUnlock(t *timedGuard) {
	t.Lock()
	defer t.Unlock()
	t.waits++
}

// The lock a contract method leaves held is the contract, so neither half of
// timedGuard reports its own body.

// timedReadWrite spells out the read half too, so it is tracked as an RWMutex.
type timedReadWrite struct {
	mu    sync.RWMutex
	reads int
}

func (t *timedReadWrite) Lock()    { t.mu.Lock() }
func (t *timedReadWrite) Unlock()  { t.mu.Unlock() }
func (t *timedReadWrite) RUnlock() { t.mu.RUnlock() }

func (t *timedReadWrite) RLock() {
	t.reads++
	t.mu.RLock()
}

func BadHandWrittenRLockWithoutRUnlock(t *timedReadWrite) {
	t.RLock() // want "rwmutex 't' is rlocked but not runlocked"
	_ = t.reads
}

func GoodHandWrittenRLockWithDeferredRUnlock(t *timedReadWrite) {
	t.RLock()
	defer t.RUnlock()
	_ = t.reads
}

// lonelyLocker has an acquire named like the contract and nothing that releases
// it, so the contract does not apply and its body is still reported.
type lonelyLocker struct {
	mu sync.Mutex
}

func (l *lonelyLocker) Lock() {
	l.mu.Lock() // want "mutex 'l.mu' is locked but not unlocked"
}

// crossSideGuard acquires in both halves, so nothing on the type releases the
// inner mutex: neither the same-side rule nor the requirement that the opposite
// method really release covers it, and both bodies are reported.
type crossSideGuard struct {
	mu sync.Mutex
}

func (c *crossSideGuard) Lock() {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
}

func (c *crossSideGuard) Unlock() {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
}

// keyedLocker takes an argument, so its Lock is not the sync contract and
// values of the type are not tracked as locks.
type keyedLocker struct {
	mu sync.Mutex
}

func (k *keyedLocker) Lock(key string) {
	_ = key
	k.mu.Lock()
}

func (k *keyedLocker) Unlock() {
	k.mu.Unlock()
}

func GoodKeyedLockerNotTracked(k *keyedLocker) {
	k.Lock("row")
}

// promotedAudit implements Lock itself and inherits Unlock from the embedded
// mutex, so the contract holds for the lock it takes. It says nothing about a
// different lock the method releases on the side: that release has no acquire
// and is still reported.
type promotedAudit struct {
	sync.Mutex
	other sync.Mutex
}

func (p *promotedAudit) Lock() {
	p.Mutex.Lock()
	p.other.Unlock() // want "mutex 'p.other' is unlocked but not locked"
}

// sortableGuards locks a whole set in order, the shape a house lock package
// uses to acquire several mutexes deadlock-free. The lock each half touches is
// an element of the collection, so the pair is matched by the operation the
// sibling performs rather than by a path neither method can spell.
type sortableGuards []*timedGuard

func (s sortableGuards) Lock() {
	for _, g := range s {
		g.Lock()
	}
}

func (s sortableGuards) Unlock() {
	for _, g := range s {
		g.Unlock()
	}
}

func GoodSortableGuardsRoundTrip(s sortableGuards) {
	s.Lock()
	defer s.Unlock()
}

func BadSortableGuardsWithoutUnlock(s sortableGuards) {
	s.Lock() // want "mutex 's' is locked but not unlocked"
}

// shardGuard hands its contract to a mutex it does not own, so neither half can
// spell the lock as a path below its own receiver.
var shardMu sync.Mutex

type shardGuard struct{}

func (s shardGuard) Lock() {
	shardMu.Lock()
}

func (s shardGuard) Unlock() {
	shardMu.Unlock()
}

// A house alias for the standard mutex — the shape a project uses to swap in a
// debug implementation behind a build tag — is the mutex it names.
type houseMutex = sync.Mutex

type aliasHolder struct {
	mu houseMutex
}

func BadAliasedMutexWithoutUnlock(a *aliasHolder) {
	a.mu.Lock() // want "mutex 'a.mu' is locked but not unlocked"
}

func GoodAliasedMutexWithDeferredUnlock(a *aliasHolder) {
	a.mu.Lock()
	defer a.mu.Unlock()
}

// semaphoredMutex carries the four lock operations but is built on a semaphore,
// so it can offer what sync.Mutex cannot: dropping the write lock while keeping
// the read lock. Its Lock/Unlock pair describes only part of the protocol, so
// the type is not tracked and the downgrade below is not read as a leak.
type semaphoredMutex struct {
	sem *ctxsem.Weighted
}

func (s *semaphoredMutex) Lock()          { s.sem.Acquire(maxSemReaders) }
func (s *semaphoredMutex) Unlock()        { s.sem.Release(maxSemReaders) }
func (s *semaphoredMutex) RLock()         { s.sem.Acquire(1) }
func (s *semaphoredMutex) RUnlock()       { s.sem.Release(1) }
func (s *semaphoredMutex) UnlockToRLock() { s.sem.Release(maxSemReaders - 1) }

const maxSemReaders = 1 << 30

func GoodSemaphoredDowngradeNotTracked(s *semaphoredMutex) {
	s.Lock()
	s.UnlockToRLock()
	defer s.RUnlock()
}

// contextMutex renames a semaphore rather than holding one. The struct it
// inherits does contain a mutex, but one declared in another package that this
// type's own methods could not touch, so it is not the lock behind the pair.
type contextMutex ctxsem.Weighted

func (m *contextMutex) Lock()   { (*ctxsem.Weighted)(m).Acquire(1) }
func (m *contextMutex) Unlock() { (*ctxsem.Weighted)(m).Release(1) }

func GoodForeignBackedMutexNotTracked(m *contextMutex) {
	m.Lock()
}
