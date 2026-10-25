package mutex

import (
	"errors"
	"sync"

	"github.com/stretchr/testify/require"
)

// ===== releases through a deferred wrapper =====

type wrappedCounter struct {
	mu sync.Mutex
	n  int
}

func (w *wrappedCounter) lock() {
	w.mu.Lock()
}

func (w *wrappedCounter) unlock() {
	w.mu.Unlock()
}

func (w *wrappedCounter) touch() {
	w.n++
}

// Good: the deferred wrapper releases the lock taken directly.
func (w *wrappedCounter) IncrementDeferred() {
	w.mu.Lock()
	defer w.unlock()
	w.n++
}

// Good: taken and released through wrappers, the release deferred.
func (w *wrappedCounter) IncrementWrapped() {
	w.lock()
	defer w.unlock()
	if w.n > 10 {
		w.mu.Unlock()
		w.n = 0
		w.mu.Lock()
	}
}

// Good: the wrapper runs inside a deferred closure.
func (w *wrappedCounter) IncrementClosure() {
	w.mu.Lock()
	defer func() {
		w.unlock()
	}()
	w.n++
}

// Bad: the deferred method releases nothing.
func (w *wrappedCounter) IncrementTouched() {
	w.mu.Lock() // want "mutex 'w.mu' is locked but not unlocked"
	defer w.touch()
	w.n++
}

// Bad: the deferred closure takes the lock and drops it again, so the lock
// taken here is still held at return.
func (w *wrappedCounter) IncrementClosureRetake() {
	w.mu.Lock() // want "mutex 'w.mu' is locked but not unlocked"
	defer func() {
		w.lock()
		w.unlock()
	}()
	w.n++
}

// ===== callers that reach the lock only through acquire-or-fail helpers =====

var errSessionClosed = errors.New("session closed")

type session struct {
	mu     sync.RWMutex
	closed bool
	ready  bool
	hits   int
}

func (s *session) lockOpen() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) rlockOpen() error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) unlock() {
	s.mu.Unlock()
}

func (s *session) runlock() {
	s.mu.RUnlock()
}

// Good: these callers never name s.mu, yet their locking is checked.
func (s *session) Hit() error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

func (s *session) Hits() int {
	if err := s.rlockOpen(); err != nil {
		return 0
	}
	defer func() {
		s.runlock()
	}()
	return s.hits
}

// Good: the result is checked a few plain statements after the call.
func (s *session) HitCounted(counter *int) error {
	err := s.lockOpen()
	*counter++
	if err != nil {
		return err
	}
	s.hits++
	s.unlock()
	return nil
}

// Good: require.NoError only returns when the helper succeeded.
func (s *session) hitAsserted(t any) {
	require.NoError(t, s.lockOpen())
	s.hits++
	s.unlock()
}

// Bad: a path returns without the unlock; the lock came from the helper call.
func (s *session) HitCapped(limit int) error {
	if err := s.lockOpen(); err != nil { // want "rwmutex 's.mu' is locked but not unlocked"
		return err
	}
	s.hits++
	if s.hits > limit {
		return errSessionClosed
	}
	s.unlock()
	return nil
}

// Bad: lockOpen already released the lock when it failed. The report lands on
// the wrapper call, not inside the wrapper every caller shares.
func (s *session) HitCleanup() error {
	if err := s.lockOpen(); err != nil {
		s.unlock() // want "rwmutex 's.mu' is unlocked but not locked"
		return err
	}
	s.hits++
	s.unlock()
	return nil
}

// ===== helpers wrapping acquire-or-fail helpers =====

// Good: adds a failure path of its own, which rolls back through the wrapper.
func (s *session) lockReady() error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	if !s.ready {
		s.unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) ServeReady() error {
	if err := s.lockReady(); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// Good: passes the helper's outcome on after some bookkeeping.
func (s *session) lockCounted(counter *int) error {
	err := s.lockOpen()
	*counter++
	if err != nil {
		return err
	}
	return nil
}

func (s *session) ServeCounted(counter *int) {
	if err := s.lockCounted(counter); err != nil {
		return
	}
	s.hits++
	s.unlock()
}

// Good: returns the helper's own result.
func (s *session) lockDelegated() error {
	return s.lockOpen()
}

func (s *session) ServeDelegated() error {
	err := s.lockDelegated()
	if err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// Bad: one failure path keeps the lock the wrapped helper handed over.
func (s *session) lockBroken() error {
	if err := s.lockOpen(); err != nil { // want "rwmutex 's.mu' is locked but not unlocked"
		return err
	}
	if !s.ready {
		return errSessionClosed
	}
	return nil
}

func (s *session) ServeBroken() error {
	if err := s.lockBroken(); err != nil {
		return err
	}
	s.unlock()
	return nil
}

// The result is checked by a switch, a shape the hand-over does not follow:
// the helper's lock is not counted here, and the deferred release through the
// wrapper is left alone rather than reported as an unmatched unlock.
func (s *session) HitSwitch() error {
	switch err := s.lockOpen(); {
	case err != nil:
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// Bad: the lock comes from lockReady, itself wrapping lockOpen.
func (s *session) ServeReadyCapped(limit int) error {
	if err := s.lockReady(); err != nil { // want "rwmutex 's.mu' is locked but not unlocked"
		return err
	}
	s.hits++
	if s.hits > limit {
		return errSessionClosed
	}
	s.unlock()
	return nil
}

// Good: the bool flavour of a helper wrapping a helper.
func (s *session) tryOpen() bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *session) tryReady() bool {
	if !s.tryOpen() {
		return false
	}
	if !s.ready {
		s.unlock()
		return false
	}
	return true
}

func (s *session) ServeTry() {
	if !s.tryReady() {
		return
	}
	s.hits++
	s.unlock()
}

// Bad: no caller confirms the handover, as for store. It also retries through
// itself, and checking its contract must stop at that recursion.
func (s *session) lockRecursive(depth int) error {
	if depth > 0 {
		if err := s.lockRecursive(depth - 1); err != nil {
			return err
		}
		return nil
	}
	s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

// ===== a receiver that embeds its mutex =====

type embeddedCounter struct {
	sync.Mutex
	closed bool
	n      int
}

// Good: a wrapper pair over the promoted Lock/Unlock, like the field form.
func (c *embeddedCounter) lock() {
	c.Lock()
}

func (c *embeddedCounter) unlock() {
	c.Unlock()
}

func (c *embeddedCounter) lockOpen() error {
	c.Lock()
	if c.closed {
		c.Unlock()
		return errSessionClosed
	}
	return nil
}

// Good: released through the wrapper, without a defer.
func (c *embeddedCounter) Add(v int) error {
	if err := c.lockOpen(); err != nil {
		return err
	}
	c.n += v
	c.unlock()
	return nil
}

func (c *embeddedCounter) Bump() {
	c.lock()
	c.n++
	c.unlock()
}

// A lifecycle split across methods through the wrappers: neither half can
// balance on its own, and neither is reported, as with a field mutex.
func (c *embeddedCounter) Begin() {
	c.lock()
}

func (c *embeddedCounter) End() {
	c.unlock()
}

// Bad: one path keeps the lock lockOpen handed over.
func (c *embeddedCounter) AddCapped(v, limit int) error {
	if err := c.lockOpen(); err != nil { // want "mutex 'c' is locked but not unlocked"
		return err
	}
	c.n += v
	if c.n > limit {
		return errSessionClosed
	}
	c.unlock()
	return nil
}

// ===== a helper that takes the lock through an unconditional wrapper =====

// Good: takes the lock through w.lock() and rolls back through w.unlock().
func (w *wrappedCounter) lockIfSmall(limit int) error {
	w.lock()
	if w.n > limit {
		w.unlock()
		return errSessionClosed
	}
	return nil
}

func (w *wrappedCounter) IncrementSmall(limit int) error {
	if err := w.lockIfSmall(limit); err != nil {
		return err
	}
	defer w.unlock()
	w.n++
	return nil
}

// Bad: one path keeps the lock lockIfSmall handed over.
func (w *wrappedCounter) IncrementSmallCapped(limit int) error {
	if err := w.lockIfSmall(limit); err != nil { // want "mutex 'w.mu' is locked but not unlocked"
		return err
	}
	w.n++
	if w.n == limit {
		return errSessionClosed
	}
	w.unlock()
	return nil
}

// ===== helpers that recurse =====

// Good: the retry path hands over what the nested call handed over, so the
// contract holds by induction on depth.
func (s *session) lockNested(depth int) error {
	if depth > 0 {
		if err := s.lockNested(depth - 1); err != nil {
			return err
		}
		s.hits++
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) ServeNested() error {
	if err := s.lockNested(2); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// Good: lockPrimary only probes lockSecondary on a failure path, while
// lockSecondary succeeds exactly when lockPrimary does. lockPrimary is asked
// about first (ServePrimary comes first), and lockSecondary must still be found
// to hand the lock over: the answer cannot depend on that order.
func (s *session) ServePrimary() error {
	if err := s.lockPrimary(false); err != nil {
		return err
	}
	defer s.unlock()
	return nil
}

func (s *session) lockPrimary(probe bool) error {
	if probe {
		if err := s.lockSecondary(); err != nil {
			return err
		}
		s.unlock()
		return errSessionClosed
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) lockSecondary() error {
	if err := s.lockPrimary(false); err != nil {
		return err
	}
	s.hits++
	return nil
}

func (s *session) ServeSecondary() error {
	if err := s.lockSecondary(); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// ===== a helper reached through an embedded field =====

type sessionHolder struct {
	*session
}

// The helper is promoted from the embedded session and the release names that
// field explicitly: the hand-over is not traced through promotion, so no lock
// is invented under a name the caller never releases.
func (h *sessionHolder) Serve() error {
	if err := h.lockOpen(); err != nil {
		return err
	}
	defer h.session.unlock()
	return nil
}

// ===== helpers only reached through a helper that passes the lock on =====

// Good: lockInner's only caller is lockOuter, which passes the lock on; the
// callers of lockOuter take it over, and that confirms both.
func (s *session) lockInner() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) lockOuter() error {
	if err := s.lockInner(); err != nil {
		return err
	}
	s.hits++
	return nil
}

func (s *session) ServeOuter() error {
	if err := s.lockOuter(); err != nil {
		return err
	}
	defer s.unlock()
	return nil
}

// Bad: the lock is passed on by lockDeepOuter, but its only caller never takes
// it over, so the chain confirms nothing.
func (s *session) lockDeepInner() error {
	s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) lockDeepOuter() error {
	if err := s.lockDeepInner(); err != nil {
		return err
	}
	return nil
}

func (s *session) UseDeepOuter() {
	if err := s.lockDeepOuter(); err != nil {
		return
	}
	s.hits++
}

// Bad: lockMidInner's lock only reaches callers through lockMid, and lockMid's
// callers disagree: one releases, the other locks again right after, which
// would self-deadlock if lockMid held the lock. Nothing is learned through it.
func (s *session) lockMidInner() error {
	s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) lockMid() error {
	if err := s.lockMidInner(); err != nil {
		return err
	}
	return nil
}

func (s *session) ReleaseMid() {
	if err := s.lockMid(); err != nil {
		return
	}
	s.unlock()
}

func (s *session) RelockMid() {
	if err := s.lockMid(); err != nil {
		return
	}
	s.mu.Lock()
	s.hits++
	s.mu.Unlock()
}

// Bad: lockLeft and lockRight pass the lock to each other and to nobody else.
// Each keeps the contract given the other, but no release ends the chain.
func (s *session) lockLeft(n int) error {
	if n > 0 {
		if err := s.lockRight(n - 1); err != nil {
			return err
		}
		return nil
	}
	s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) lockRight(n int) error {
	if err := s.lockLeft(n); err != nil {
		return err
	}
	return nil
}

// Asks about takeLeaky first, and does not take its lock over.
func (s *session) PeekLeaky() {
	_ = s.takeLeaky(true)
}

// Bad: takeLeaky keeps the lock on a failure path. (Its name avoids "lock": a
// lock-named method with an unlock sibling passes for a wrapper.) While it was
// being checked
// it was assumed to keep the contract, and takeOnLeaky, which rests on it,
// looked fine under that assumption; the assumption failed, so takeOnLeaky is
// not trusted either, even though a caller takes its lock over.
func (s *session) takeLeaky(probe bool) error {
	if probe {
		return s.takeOnLeaky(false)
	}
	s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
	if s.closed {
		return errSessionClosed
	}
	return nil
}

func (s *session) takeOnLeaky(direct bool) error {
	if direct {
		s.mu.Lock() // want "rwmutex 's.mu' is locked but not unlocked"
		if s.closed {
			s.mu.Unlock()
			return errSessionClosed
		}
		return nil
	}
	if err := s.takeLeaky(false); err != nil {
		return err
	}
	return nil
}

func (s *session) ServeOnLeaky() error {
	if err := s.takeOnLeaky(true); err != nil {
		return err
	}
	defer s.unlock()
	return nil
}

// ===== what follows a call, for the callers' evidence =====

// Good: every caller that keeps going takes lockLate's lock over.
func (s *session) lockLate() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

// The release sits after the branch that made the call.
func (s *session) HitLate(fast bool) error {
	if fast {
		if err := s.lockLate(); err != nil {
			return err
		}
	} else {
		if err := s.lockOpen(); err != nil {
			return err
		}
	}
	defer s.unlock()
	s.hits++
	return nil
}

// The call's result is returned: the code after the branch runs on another
// path, so its own locking says nothing about lockLate.
func (s *session) lockLateOrReset(reset bool) error {
	if !reset {
		return s.lockLate()
	}
	s.mu.Lock()
	s.hits = 0
	s.mu.Unlock()
	return errSessionClosed
}

func (s *session) HitLateOrReset(reset bool) error {
	if err := s.lockLateOrReset(reset); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

// Good: lockAsync's callers take its lock over; one runs it in a goroutine that
// hands the outcome on, and what the launching function does next is not on
// that call's path.
func (s *session) lockAsync() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (s *session) HitAsync() error {
	if err := s.lockAsync(); err != nil {
		return err
	}
	defer s.unlock()
	s.hits++
	return nil
}

func (s *session) LockInBackground(locked chan<- error) {
	go func() {
		locked <- s.lockAsync()
	}()
	s.mu.RLock()
	s.hits++
	s.mu.RUnlock()
}

// ===== a helper only ever called through an embedded field =====

type innerStore struct {
	mu     sync.Mutex
	closed bool
	items  []int
}

func (i *innerStore) lockStore() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return errSessionClosed
	}
	return nil
}

func (i *innerStore) unlockStore() {
	i.mu.Unlock()
}

type storeFront struct {
	*innerStore
}

// Good: every caller of lockStore goes through the embedded field and takes
// its lock over: deferred, directly after, or through the promoted field.
func (f *storeFront) Put(v int) error {
	if err := f.lockStore(); err != nil {
		return err
	}
	defer f.unlockStore()
	f.items = append(f.items, v)
	return nil
}

func (f *storeFront) PutNow(v int) error {
	if err := f.lockStore(); err != nil {
		return err
	}
	f.items = append(f.items, v)
	f.unlockStore()
	return nil
}

func (f *storeFront) PutDirect(v int) error {
	if err := f.lockStore(); err != nil {
		return err
	}
	f.items = append(f.items, v)
	f.mu.Unlock()
	return nil
}

// Bad: one path keeps the lock lockStore handed over.
func (f *storeFront) PutCapped(v, limit int) error {
	if err := f.lockStore(); err != nil { // want "mutex 'f.innerStore.mu' is locked but not unlocked"
		return err
	}
	f.items = append(f.items, v)
	if len(f.items) > limit {
		return errSessionClosed
	}
	f.unlockStore()
	return nil
}

// ===== acquire-or-fail that waits before taking the lock =====

// readyGate only takes its lock once the object is usable, so the failure path
// never acquired anything and has nothing to roll back. The contract — lock
// held exactly when the call reports success — holds all the same.
type readyGate struct {
	mu    sync.Mutex
	ready bool
	n     int
}

func (g *readyGate) awaitReady() error {
	if !g.ready {
		return errSessionClosed
	}
	return nil
}

func (g *readyGate) takeWhenReady() error {
	if err := g.awaitReady(); err != nil {
		return err
	}
	g.mu.Lock()
	return nil
}

func (g *readyGate) release() {
	g.mu.Unlock()
}

func (g *readyGate) Bump() error {
	if err := g.takeWhenReady(); err != nil {
		return err
	}
	defer g.release()
	g.n++
	return nil
}

func (g *readyGate) BumpLeaky() error {
	if err := g.takeWhenReady(); err != nil { // want "mutex 'g.mu' is locked but not unlocked"
		return err
	}
	g.n++
	return nil
}

// alwaysGate takes the lock on every path, so it is a plain wrapper and not an
// acquire-or-fail helper: its caller is told nothing by the error it returns.
type alwaysGate struct {
	mu sync.Mutex
	n  int
}

func (g *alwaysGate) takeAlways() error {
	g.mu.Lock() // want "mutex 'g.mu' is locked but not unlocked"
	return nil
}

func (g *alwaysGate) BumpAlways() error {
	if err := g.takeAlways(); err != nil {
		return err
	}
	g.n++
	return nil
}
