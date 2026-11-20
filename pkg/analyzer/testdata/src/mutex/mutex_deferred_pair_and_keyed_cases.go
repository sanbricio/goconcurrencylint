package mutex

import "sync"

// A caller can release a pair's acquiring half in a deferred closure written
// before it: the defer runs last.

type deferredPairSession struct{ n int }

type deferredPairConsumer struct {
	mu sync.Mutex
	s  *deferredPairSession
}

func (c *deferredPairConsumer) guardDeferredPair() *deferredPairSession {
	c.mu.Lock()
	if c.s == nil {
		c.s = &deferredPairSession{}
	}
	return c.s
}

func (c *deferredPairConsumer) unguardDeferredPair(s *deferredPairSession) {
	s.n++
	c.mu.Unlock()
}

// GoodDeferredUnguard must be CLEAN, and so must both halves.
func (c *deferredPairConsumer) GoodDeferredUnguard() {
	var s *deferredPairSession
	defer func() {
		c.unguardDeferredPair(s)
	}()
	s = c.guardDeferredPair()
	s.n++
}

type undeferredPairConsumer struct {
	mu sync.Mutex
	n  int
}

// takeUndeferredPair must be flagged: its caller runs the release before it.
func (c *undeferredPairConsumer) takeUndeferredPair() {
	c.mu.Lock() // want "mutex 'c.mu' is locked but not unlocked"
	c.n++
}

// giveUndeferredPair must be flagged.
func (c *undeferredPairConsumer) giveUndeferredPair() {
	c.n--
	c.mu.Unlock() // want "mutex 'c.mu' is unlocked but not locked"
}

// BadReleaseBeforeTake uses the halves in the wrong order.
func (c *undeferredPairConsumer) BadReleaseBeforeTake() {
	c.giveUndeferredPair()
	c.takeUndeferredPair()
}

// Keyed lockers reaching their locks through a lookup on a registry field.

type registryLocker struct {
	m sync.Map
}

func (s *registryLocker) mutexFor(id string) *sync.Mutex {
	l, ok := s.m.Load(id)
	if !ok {
		l, _ = s.m.LoadOrStore(id, &sync.Mutex{})
	}
	return l.(*sync.Mutex)
}

// LockID must be CLEAN.
func (s *registryLocker) LockID(id string) {
	s.mutexFor(id).Lock()
}

// UnlockID must be CLEAN: it releases a lock of the registry LockID takes
// from.
func (s *registryLocker) UnlockID(id string) {
	l, _ := s.m.Load(id)
	lock := l.(*sync.Mutex)
	lock.Unlock()
}

type releaseOnlyRegistry struct {
	m sync.Map
}

// UnlockID must be flagged: nothing on the type takes a lock from the
// registry.
func (s *releaseOnlyRegistry) UnlockID(id string) {
	l, _ := s.m.Load(id)
	lock := l.(*sync.Mutex)
	lock.Unlock() // want "mutex 'lock' is unlocked but not locked"
}

// A lock the package takes only through its bound methods.

type opaqueFlock struct {
	local sync.RWMutex
	inode int
}

func (j *opaqueFlock) acquire(write bool) {
	try := j.local.TryRLock
	if write {
		try = j.local.TryLock
	}
	for !try() {
	}
}

// GetLock takes the lock through acquire.
func (j *opaqueFlock) GetLock() { j.acquire(true) }

// Unlock must be CLEAN.
func (j *opaqueFlock) Unlock() {
	if j.inode == 0 {
		return
	}
	j.local.Unlock()
}

// RUnlock must be CLEAN.
func (j *opaqueFlock) RUnlock() {
	if j.inode == 0 {
		return
	}
	j.local.RUnlock()
}

type directFlock struct {
	local sync.RWMutex
}

func (j *directFlock) take() bool {
	try := j.local.TryLock
	if try() {
		return true
	}
	j.local.Lock() // want "rwmutex 'j.local' is locked but not unlocked"
	return true
}

// Release must be flagged: the package also takes the lock with direct
// calls, so the release is paired by name and has no match here.
func (j *directFlock) Release() {
	j.local.Unlock() // want "rwmutex 'j.local' is unlocked but not locked"
}
