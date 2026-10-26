package mutex

import (
	"errors"
	"sync"
)

// ========== lock released by a returned closure (guard/RAII pattern) ==========

type escapeGuard struct {
	release func()
}

func (g escapeGuard) Unlock() {
	if g.release != nil {
		g.release()
	}
}

type closureLocker struct {
	mu sync.Mutex
}

// Good: the unlock lives in a closure stored in the returned guard, so the
// caller owns the release. This is the sync.RWMutex-wrapper pattern (e.g. loki
// obslock) and must not be flagged as a leak.
func (c *closureLocker) LockGuard() escapeGuard {
	c.mu.Lock()
	return escapeGuard{
		release: func() {
			c.mu.Unlock()
		},
	}
}

// Good: the unlocker closure is returned directly.
func (c *closureLocker) LockFunc() func() {
	c.mu.Lock()
	return func() {
		c.mu.Unlock()
	}
}

// Good: the bound unlock method is returned directly, so the caller owns the
// release. Mirrors minio cmd/local-locker.go getMutex() (`return l.mutex.Unlock`).
func (c *closureLocker) LockReturningUnlockMethod() func() {
	c.mu.Lock()
	return c.mu.Unlock
}

// Good: the bound unlock method handed back through a local variable.
func (c *closureLocker) LockReturningUnlockViaLocal() func() {
	c.mu.Lock()
	unlock := c.mu.Unlock
	return unlock
}

// Bad: the closure that unlocks is never returned or called, so the lock still
// leaks and must be flagged.
func (c *closureLocker) BadLockClosureNotReturned() {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
	_ = func() {
		c.mu.Unlock()
	}
}

// ========== lock released by a child goroutine (ownership handoff) ==========

// Good: the parent locks, then hands the release to a goroutine. A sync.Mutex
// may be unlocked by a goroutine other than the one that locked it, so the
// deferred unlock in the child balances the parent's Lock.
func GoodGoroutineDeferUnlockReleasesParentLock() {
	var mu sync.Mutex
	mu.Lock()
	go func() {
		defer mu.Unlock()
	}()
}

// Good: same handoff, but the parent acquires with a checked TryLock guard.
func GoodGoroutineDeferUnlockReleasesParentTryLock() bool {
	var mu sync.Mutex
	if !mu.TryLock() {
		return false
	}
	go func() {
		defer mu.Unlock()
	}()
	return true
}

// ========== release delegated to a sync.Once ==========

type onceLocker struct {
	mu      sync.Mutex
	waiters []chan struct{}
}

// Good: the deferred Once releases the lock on every return path, and the
// early call inside the body only moves that same release earlier.
func (o *onceLocker) LockReleasedByDeferredOnce(ok bool) chan struct{} {
	o.mu.Lock()
	unlock := sync.Once{}
	defer unlock.Do(o.mu.Unlock)

	if !ok {
		return nil
	}

	waiter := make(chan struct{})
	o.waiters = append(o.waiters, waiter)

	unlock.Do(o.mu.Unlock)
	return waiter
}

// Good: the Once fires the release inline, without a deferred counterpart.
func (o *onceLocker) LockReleasedByInlineOnce() {
	o.mu.Lock()
	unlock := sync.Once{}
	o.waiters = nil
	unlock.Do(o.mu.Unlock)
}

// Good: the deferred callable is stored in a local before registration and
// delegates the release through Once. This mirrors Prometheus' appender cleanup.
func (o *onceLocker) LockReleasedByDeferredLocalClosure() {
	o.mu.Lock()
	unlock := sync.Once{}
	release := func() {
		unlock.Do(o.mu.Unlock)
	}
	defer release()
}

// Bad: the Once runs unrelated cleanup, so nothing releases the lock.
func (o *onceLocker) BadLockWithOnceNotReleasing() {
	o.mu.Lock() // want "mutex 'o.mu' is locked but not unlocked"
	done := sync.Once{}
	defer done.Do(func() {
		o.waiters = nil
	})
}

// ========== lock parked in a Locker variable ==========

type quotaChecker struct {
	quotaLock sync.RWMutex
	engines   int
}

// Good: the lock is taken only once the check decides it is needed, recorded in
// a Locker variable, and released by the deferred closure that tests it. The
// alias is the guard, so acquisition and release stay paired.
func (q *quotaChecker) GoodLockParkedInLockerVariable() {
	go func() {
		var locker sync.Locker
		defer func() {
			if locker != nil {
				locker.Unlock()
			}
		}()

		for {
			if q.engines == 0 {
				return
			}
			if locker == nil {
				q.quotaLock.Lock()
				locker = &q.quotaLock
			}
			q.engines--
		}
	}()
}

// Bad: the lock is taken but never recorded in the alias, so the deferred
// release never fires for it and the lock leaks.
func (q *quotaChecker) BadLockNeverRecordedInLocker() {
	var locker sync.Locker
	defer func() { // want "rwmutex 'q.quotaLock' has defer unlock but no corresponding lock"
		if locker != nil {
			locker.Unlock()
		}
	}()

	if q.engines > 0 {
		q.quotaLock.Lock() // want "rwmutex 'q.quotaLock' is locked but not unlocked in if"
	}
	locker = &q.quotaLock
}

// Good: TiDB's disk-quota shape. Taking and dropping the lock live in named
// closures; the deferred one releases through the alias only when it is set,
// and the acquiring one records the lock right after taking it.
func (q *quotaChecker) GoodLockParkedThroughNamedClosures(steps []bool) {
	var locker sync.Locker
	take := func() {
		if locker == nil {
			q.quotaLock.Lock()
			locker = &q.quotaLock
		}
	}
	drop := func() {
		if locker != nil {
			locker.Unlock()
			locker = nil
		}
	}
	defer drop()
	for _, over := range steps {
		if !over {
			drop()
			continue
		}
		take()
		q.engines--
	}
}

// Bad: the named closure takes the lock without recording it, so the deferred
// release through the alias never covers it.
func (q *quotaChecker) BadNamedClosureNeverRecordsLock() {
	var locker sync.Locker
	take := func() {
		q.quotaLock.Lock()
	}
	drop := func() {
		if locker != nil {
			locker.Unlock()
		}
	}
	defer drop() // want "rwmutex 'q.quotaLock' has defer unlock but no corresponding lock"
	take()
	locker = &q.quotaLock
}

type serializedEntry struct {
	sync.Mutex
	value int
}

type entryCache struct {
	entries map[string]*serializedEntry
}

// The lock travels out inside the returned value, so the release belongs to
// whoever called this.
func (c *entryCache) serialize(key string) *serializedEntry {
	entry, ok := c.entries[key]
	if !ok {
		entry = &serializedEntry{}
		c.entries[key] = entry
	}

	entry.Lock()
	return entry
}

func GoodCallerReleasesHandedOverLock(c *entryCache, key string) int {
	entry := c.serialize(key)
	defer entry.Unlock()
	return entry.value
}

func BadCallerKeepsHandedOverLock(c *entryCache, key string) int {
	entry := c.serialize(key) // want "mutex 'entry' is locked but not unlocked"
	return entry.value
}

// The locked value is the first result of a multi-result call. Returning an
// error in another result slot must not hide the ownership handoff.
func (c *entryCache) serializeResult(key string) (*serializedEntry, error) {
	if key == "" {
		return nil, errors.New("empty key")
	}
	entry, ok := c.entries[key]
	if !ok {
		entry = &serializedEntry{}
		c.entries[key] = entry
	}

	entry.Lock()
	return entry, nil
}

func GoodCallerReleasesMultiResultHandoff(c *entryCache, key string) (int, error) {
	entry, err := c.serializeResult(key)
	if err != nil {
		return 0, err
	}
	defer entry.Unlock()
	return entry.value, nil
}

func BadCallerKeepsMultiResultHandoff(c *entryCache, key string) (int, error) {
	entry, err := c.serializeResult(key) // want "mutex 'entry' is locked but not unlocked"
	if err != nil {
		return 0, err
	}
	return entry.value, nil
}

type promotedEntryCache struct {
	*entryCache
}

func BadCallerKeepsPromotedMultiResultHandoff(c *promotedEntryCache, key string) (int, error) {
	entry, err := c.serializeResult(key) // want "mutex 'entry' is locked but not unlocked"
	if err != nil {
		return 0, err
	}
	return entry.value, nil
}

func GoodCallerReleasesInitHandoff(c *entryCache, key string) (int, error) {
	var entry *serializedEntry
	var err error
	if entry, err = c.serializeResult(key); err != nil {
		return 0, err
	}
	entry.Unlock()
	return entry.value, nil
}

func BadCallerKeepsInitHandoff(c *entryCache, key string) (int, error) {
	var entry *serializedEntry
	var err error
	if entry, err = c.serializeResult(key); err != nil { // want "mutex 'entry' is locked but not unlocked"
		return 0, err
	}
	return entry.value, nil
}

// ===== lock ownership split across a keyed Lock/Unlock API =====

type keyedMutexRegistry struct {
	gate  sync.Mutex
	items map[string]*serializedEntry
}

func (r *keyedMutexRegistry) LockID(key string) {
	r.gate.Lock()
	entry := r.items[key]
	r.gate.Unlock()
	entry.Lock()
}

func (r *keyedMutexRegistry) UnlockID(key string) {
	r.gate.Lock()
	entry := r.items[key]
	r.gate.Unlock()
	entry.Unlock()
}

// ===== a wrapper-typed mutex handed whole to a returned iterator =====

type guardedStore struct {
	sync.RWMutex
	items []int
}

type storeIterator struct {
	store *guardedStore
	next  int
}

// Close releases the read lock the iterator was handed.
func (it *storeIterator) Close() {
	it.store.RUnlock()
}

// Iterator returns a handle holding the read lock; Close releases it.
func (s *guardedStore) Iterator() *storeIterator {
	s.RLock()
	return &storeIterator{store: s}
}

// ===== acquire-or-fail helper: the lock is handed to the caller on success =====

var errEndpointRemoved = errors.New("endpoint removed")

type aliveEndpoint struct {
	mu      sync.RWMutex
	removed bool
	value   int
}

// Good: like TryLock, lockAlive returns holding the lock exactly when it
// reports success, and its callers release it after a successful call (the
// cilium endpoint lockAlive/unlock protocol).
func (e *aliveEndpoint) lockAlive() error {
	e.mu.Lock()
	if e.removed {
		e.mu.Unlock()
		return errEndpointRemoved
	}
	return nil
}

func (e *aliveEndpoint) unlock() {
	e.mu.Unlock()
}

func (e *aliveEndpoint) unconditionalLock() {
	e.mu.Lock()
}

// Good: the read-lock flavour of the same protocol.
func (e *aliveEndpoint) rlockAlive() error {
	e.mu.RLock()
	if e.removed {
		e.mu.RUnlock()
		return errEndpointRemoved
	}
	return nil
}

func (e *aliveEndpoint) runlock() {
	e.mu.RUnlock()
}

// Good: its only caller releases through the unlock wrapper, the way every
// cilium caller of lockAlive does.
func (e *aliveEndpoint) lockReady() error {
	e.mu.Lock()
	if e.removed {
		e.mu.Unlock()
		return errEndpointRemoved
	}
	return nil
}

func (e *aliveEndpoint) SetReady(v int) error {
	if err := e.lockReady(); err != nil {
		return err
	}
	defer e.unlock()
	e.value = v
	return nil
}

// Good: the bool flavour; true means the caller now holds the lock.
func (e *aliveEndpoint) lockIfAlive() bool {
	e.mu.Lock()
	if e.removed {
		e.mu.Unlock()
		return false
	}
	return true
}

func (e *aliveEndpoint) SetValue(v int) error {
	if err := e.lockAlive(); err != nil {
		return err
	}
	defer e.unlock()
	e.value = v
	return nil
}

// A failure arm may take the lock unconditionally: the helper already released
// it there, so only the success path shows whether the lock was handed over.
func (e *aliveEndpoint) Reset() {
	if err := e.lockAlive(); err != nil {
		e.unconditionalLock()
		e.value = 0
		e.unlock()
		return
	}
	e.value = 0
	e.unlock()
}

func (e *aliveEndpoint) Bump() {
	if !e.lockIfAlive() {
		e.unconditionalLock()
		e.value = 0
		e.unlock()
		return
	}
	e.value++
	e.unlock()
}

func (e *aliveEndpoint) ValueOrReset() int {
	err := e.rlockAlive()
	if err != nil {
		e.unconditionalLock()
		e.value = 0
		e.unlock()
		return 0
	}
	defer e.runlock()
	return e.value
}

// Bad: the same shape, but no caller takes the lock over: the success path
// simply forgot to unlock.
func (e *aliveEndpoint) store(v int) error {
	e.mu.Lock() // want "rwmutex 'e.mu' is locked but not unlocked"
	if v < 0 {
		e.mu.Unlock()
		return errEndpointRemoved
	}
	e.value = v
	return nil
}

func (e *aliveEndpoint) Store(v int) error {
	return e.store(v)
}

// Bad: one caller releases after a successful call, but another locks again,
// which would self-deadlock if checkAlive kept the lock: the callers disagree,
// so nothing confirms the handover. checkAlive's own lock is reported, and so
// is the release its first caller makes without a lock handed over.
func (e *aliveEndpoint) checkAlive() error {
	e.mu.Lock() // want "rwmutex 'e.mu' is locked but not unlocked"
	if e.removed {
		e.mu.Unlock()
		return errEndpointRemoved
	}
	return nil
}

func (e *aliveEndpoint) ReleaseAfterCheck() {
	if err := e.checkAlive(); err != nil {
		return
	}
	e.mu.Unlock() // want "rwmutex 'e.mu' is unlocked but not locked"
}

func (e *aliveEndpoint) LockAfterCheck() {
	if err := e.checkAlive(); err != nil {
		return
	}
	e.unconditionalLock()
	e.value++
	e.unlock()
}

// Bad: a failure return keeps the lock, so a caller cannot tell from the result
// whether it owns the lock.
func (e *aliveEndpoint) lockUnlessBusy(busy bool) error {
	e.mu.Lock() // want "rwmutex 'e.mu' is locked but not unlocked"
	if busy {
		return errEndpointRemoved
	}
	if e.removed {
		e.mu.Unlock()
		return errEndpointRemoved
	}
	return nil
}

func (e *aliveEndpoint) UseUnlessBusy() {
	if err := e.lockUnlessBusy(false); err != nil {
		return
	}
	e.unlock()
}

// ===== callers that release a handed-over lock themselves =====

func (e *aliveEndpoint) SetValueDirect(v int) error {
	if err := e.lockAlive(); err != nil {
		return err
	}
	defer e.mu.Unlock()
	e.value = v
	return nil
}

func (e *aliveEndpoint) ValueDirect() (int, error) {
	err := e.rlockAlive()
	if err != nil {
		return 0, err
	}
	v := e.value
	e.mu.RUnlock()
	return v, nil
}

// The lock is only taken on the success arm, which releases it; the failure
// path never held it.
func (e *aliveEndpoint) TrySetValue(v int) bool {
	err := e.lockAlive()
	if err == nil {
		e.value = v
		e.mu.Unlock()
	}
	return err == nil
}

// The lock is held after the `if` either way: handed over on success, taken
// unconditionally when the endpoint went away.
func (e *aliveEndpoint) ForceSetValue(v int) {
	if err := e.lockAlive(); err != nil {
		e.mu.Lock()
	}
	defer e.mu.Unlock()
	e.value = v
}

func (e *aliveEndpoint) BumpDirect() {
	if !e.lockIfAlive() {
		return
	}
	e.value++
	e.mu.Unlock()
}

func (e *aliveEndpoint) BumpIfAlive() {
	if e.lockIfAlive() {
		e.value++
		e.mu.Unlock()
	}
}

func (e *aliveEndpoint) BumpChecked() {
	ok := e.lockIfAlive()
	if !ok {
		return
	}
	defer e.mu.Unlock()
	e.value++
}

// Bad: the success path returns early without releasing the handed-over lock.
func (e *aliveEndpoint) SetValueDirectLeak(v int) error {
	if err := e.lockAlive(); err != nil { // want "rwmutex 'e.mu' is locked but not unlocked"
		return err
	}
	e.value = v
	if v > 10 {
		return nil
	}
	e.mu.Unlock()
	return nil
}

// Bad: lockAlive already released the lock when it failed.
func (e *aliveEndpoint) SetValueCleansUpOnFailure(v int) error {
	if err := e.lockAlive(); err != nil {
		e.mu.Unlock() // want "rwmutex 'e.mu' is unlocked but not locked"
		return err
	}
	e.value = v
	e.mu.Unlock()
	return nil
}

// store does not hand its lock over (this caller locks right after a successful
// call), so the lock is not counted here: only store's own leak is reported.
func (e *aliveEndpoint) StoreThenBump(v int) error {
	if err := e.store(v); err != nil {
		return err
	}
	e.mu.Lock()
	e.value++
	e.mu.Unlock()
	return nil
}

// Called with e.mu held: it drops the lock around slow work and takes it back
// through the helper, or unconditionally when the endpoint went away.
func (e *aliveEndpoint) refreshWhileHeld() {
	e.mu.Unlock()
	e.value++
	if !e.lockIfAlive() {
		e.unconditionalLock()
	}
}

// ===== acquire-or-fail helper on a receiver that embeds its mutex =====

type aliveStore struct {
	sync.Mutex
	closed bool
	items  []int
}

// Good: the receiver itself is the lock the helper hands over.
func (s *aliveStore) lockOpen() error {
	s.Lock()
	if s.closed {
		s.Unlock()
		return errEndpointRemoved
	}
	return nil
}

func (s *aliveStore) Append(v int) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.Unlock()
	s.items = append(s.items, v)
	return nil
}

// ========== handle built inside the return expression ==========

type txStore struct {
	mu  sync.RWMutex
	rev int
}

type txCommon struct {
	s   *txStore
	rev int
}

type txRead struct {
	txCommon
	limit int
}

func (tr *txRead) End() { tr.s.mu.RUnlock() }

type txWrite struct {
	txCommon
	changes int
}

func (tw *txWrite) End() { tw.s.mu.RUnlock() }

type txHandle interface{ End() }

type metricsTx struct {
	inner txHandle
	ops   int
}

func (m *metricsTx) End() { m.inner.End() }

func withMetrics(inner txHandle) txHandle { return &metricsTx{inner: inner} }

// Good: the read lock travels to the transaction stored through the embedded
// common part; wrapping the transaction for metrics does not change who
// releases it.
func (s *txStore) GoodReadHandsLockToWrappedTxn() txHandle {
	s.mu.RLock()
	return withMetrics(&txRead{txCommon{s, s.rev}, 10})
}

// Good: the same with the transaction built into a local first.
func (s *txStore) GoodWriteHandsLockToWrappedTxn() txHandle {
	s.mu.RLock()
	tw := &txWrite{txCommon: txCommon{s: s, rev: s.rev}}
	return withMetrics(tw)
}

// opaqueTx stores the store but nothing of it ever releases the lock.
type opaqueTx struct {
	txCommon
}

func (o *opaqueTx) End() {}

func (s *txStore) BadReadHandleNeverReleases() txHandle {
	s.mu.RLock() // want "rwmutex 's.mu' is rlocked but not runlocked"
	return withMetrics(&opaqueTx{txCommon{s, s.rev}})
}
