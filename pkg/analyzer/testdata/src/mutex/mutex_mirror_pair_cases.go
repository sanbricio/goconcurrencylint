package mutex

import (
	"sync"
	"sync/atomic"
)

func recordEvent(string) {}

func doSlowWork() {}

// ===== a lifecycle the type splits across two methods =====

// splitCounter names its halves after nothing in particular: what makes them a
// pair is that each performs one unconditional operation on the same lock and
// that Bump uses them as one.
type splitCounter struct {
	mu sync.Mutex
	n  int
}

func (s *splitCounter) take() {
	recordEvent("taking")
	s.mu.Lock()
}

func (s *splitCounter) give() {
	recordEvent("giving")
	s.mu.Unlock()
}

func (s *splitCounter) Bump() {
	s.take()
	defer s.give()
	s.n++
}

// strandedCounter has the same two halves and nothing in the package uses them
// together, so neither half is vouched for.
type strandedCounter struct {
	mu sync.Mutex
	n  int
}

func (s *strandedCounter) grab() {
	recordEvent("grabbing")
	s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked"
}

func (s *strandedCounter) yield() {
	recordEvent("yielding")
	s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
}

func (s *strandedCounter) BumpStranded() {
	s.grab()
	s.n++
}

// pickyCounter takes the lock only on one branch, so it has a path that holds
// the lock and a path that does not — the shape of the forgotten release, and
// no pair however it is used.
type pickyCounter struct {
	mu    sync.Mutex
	ready bool
	n     int
}

func (p *pickyCounter) admit() {
	if p.ready {
		p.mu.Lock() // want "mutex 'p.mu' is locked but not unlocked in if"
	}
}

func (p *pickyCounter) hand() {
	p.mu.Unlock() // want "mutex 'p.mu' is unlocked but not locked"
}

func (p *pickyCounter) BumpPicky() {
	p.admit()
	defer p.hand()
	p.n++
}

// busyCounter's other method balances itself, so it is nobody's counterpart and
// the lone release stands unmatched.
type busyCounter struct {
	mu sync.Mutex
	n  int
}

func (b *busyCounter) Work() {
	b.mu.Lock()
	b.n++
	b.mu.Unlock()
}

func (b *busyCounter) surrender() {
	b.mu.Unlock() // want "mutex 'b.mu' is unlocked but not locked"
}

// ===== handing a held lock to a goroutine =====

// reconnector is grpc's shape: the goroutine starts by giving the caller's lock
// back, then does its slow work and takes the lock again for its own
// bookkeeping.
type reconnector struct {
	mu    sync.Mutex
	state int
}

func (r *reconnector) resetAfterHandoff() {
	r.mu.Unlock()
	doSlowWork()
	r.mu.Lock()
	r.state = 1
	r.mu.Unlock()
}

func (r *reconnector) GoodHandsLockToReleasingGoroutine() {
	r.mu.Lock()
	go r.resetAfterHandoff()
}

// selfLocker takes the lock itself, so it was handed nothing and the parent's
// lock is still leaked.
type selfLocker struct {
	mu sync.Mutex
	n  int
}

func (s *selfLocker) refresh() {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func (s *selfLocker) BadHandsToSelfLockingGoroutine() {
	s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked"
	go s.refresh()
}

// reorderedCounter's halves are used in the wrong order — the lock is handed
// back before it is taken — so calling both is not evidence of a pair.
type reorderedCounter struct {
	mu sync.Mutex
	n  int
}

func (r *reorderedCounter) hand() {
	recordEvent("handing")
	r.mu.Unlock() // want "mutex 'r.mu' is unlocked but not locked"
}

func (r *reorderedCounter) seize() {
	recordEvent("seizing")
	r.mu.Lock() // want "mutex 'r.mu' is locked but not unlocked"
}

func (r *reorderedCounter) BumpReordered() {
	r.hand()
	r.seize()
	r.n++
}

// ===== the release written as a defer =====

// checkpointStore is cilium's allocator shape: one method takes the lock to
// hold everything back until the restore has run, and the restore gives it back
// with a defer. The lock never appears twice in the same function.
type checkpointStore struct {
	mu      sync.Mutex
	entries int
}

func (c *checkpointStore) hold() {
	c.mu.Lock()
}

func (c *checkpointStore) restore() error {
	defer c.mu.Unlock()

	c.entries++
	return nil
}

func (c *checkpointStore) Boot() error {
	c.hold()
	return c.restore()
}

// strandedCheckpoint has the same defer with nothing in the package taking the
// lock for it.
type strandedCheckpoint struct {
	mu      sync.Mutex
	entries int
}

func (c *strandedCheckpoint) revive() error {
	defer c.mu.Unlock() // want "mutex 'c.mu' has defer unlock but no corresponding lock"

	c.entries++
	return nil
}

// stuckGate closes the gate and never opens it, so the write lock is a leak and
// the read lock waits on something that will never be released. Half a gate is
// not a gate.
type stuckGate struct {
	gate sync.RWMutex
}

func (s *stuckGate) shut() {
	s.gate.Lock() // want "rwmutex 's.gate' is locked but not unlocked"
}

func (s *stuckGate) await() {
	s.gate.RLock() // want "rwmutex 's.gate' is rlocked but not runlocked"
}

// ===== a gate closed by its factory =====

// queryGate is vitess's consolidator: the registry that hands out the object
// closes the gate as it builds it, the object opens it when the work is done,
// and every waiter parks on the read lock until then. Neither half is a method
// of the same type, and nothing ever releases the read side.
type queryGate struct {
	done sync.RWMutex
	name string
}

type gateRegistry struct {
	mu    sync.Mutex
	gates map[string]*queryGate
}

func (g *gateRegistry) Create(name string) (*queryGate, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if gate, ok := g.gates[name]; ok {
		return gate, false
	}

	gate := &queryGate{name: name}
	gate.done.Lock()
	g.gates[name] = gate
	return gate, true
}

func (q *queryGate) Finish(g *gateRegistry) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.gates, q.name)
	q.done.Unlock()
}

func (q *queryGate) AwaitFinish() {
	q.done.RLock()
}

// otherHolder keeps a field with the same name as the gate above and no gate
// behind it, so the read lock is matched against its own type, not the name.
type otherHolder struct {
	done sync.RWMutex
}

func (o *otherHolder) read() {
	o.done.RLock() // want "rwmutex 'o.done' is rlocked but not runlocked"
}

// mixedGate has the split write halves a gate has, but something in the package
// releases the read side too, so that side has a lifecycle of its own and a
// read lock nobody released is the ordinary leak.
type mixedGate struct {
	rw sync.RWMutex
	n  int
}

func (m *mixedGate) shutIt() {
	m.rw.Lock()
}

func (m *mixedGate) openIt() {
	m.rw.Unlock()
}

func (m *mixedGate) peek() {
	m.rw.RLock() // want "rwmutex 'm.rw' is rlocked but not runlocked"
}

func dropMixedRead(m *mixedGate) {
	m.rw.RUnlock()
}

// flakyGate closes on one branch only, so it is not the all-or-nothing gate the
// waiters would need.
type flakyGate struct {
	gate sync.RWMutex
	on   bool
}

func (f *flakyGate) shutIf() {
	if f.on {
		f.gate.Lock() // want "rwmutex 'f.gate' is locked but not unlocked in if"
	}
}

func (f *flakyGate) openUp() {
	f.gate.Unlock() // want "rwmutex 'f.gate' is unlocked but not locked"
}

func (f *flakyGate) awaitIt() {
	f.gate.RLock() // want "rwmutex 'f.gate' is rlocked but not runlocked"
}

// ===== a lock made reentrant by an owner record =====

// ownedSchema takes the lock once per owner: a second take by the same owner
// finds itself recorded and leaves the lock alone, and only the recorded owner
// gives it back. The lock is held exactly while the record is set.
type ownedSchema struct {
	mu    sync.Mutex
	owner atomic.Int64
	ver   int
}

func (o *ownedSchema) lockFor(id int64) error {
	current := o.owner.Load()
	if current != id {
		o.mu.Lock()
		o.owner.Store(id)
	}
	return nil
}

func (o *ownedSchema) unlockFor(id int64) {
	if o.owner.Load() == id {
		o.owner.Store(0)
		o.mu.Unlock()
	}
}

func (o *ownedSchema) Step(id int64) {
	if err := o.lockFor(id); err != nil {
		return
	}
	defer o.unlockFor(id)
	o.ver++
}

// forgetfulOwner never clears its record, so its release is no counterpart:
// a second release by the same owner would give back a lock nobody holds.
type forgetfulOwner struct {
	mu    sync.Mutex
	owner atomic.Int64
}

func (f *forgetfulOwner) lockFor(id int64) {
	if f.owner.Load() != id {
		f.mu.Lock() // want "mutex 'f.mu' is locked but not unlocked in if"
		f.owner.Store(id)
	}
}

func (f *forgetfulOwner) unlockFor(id int64) {
	if f.owner.Load() == id {
		f.mu.Unlock() // want "mutex 'f.mu' is unlocked but not locked"
	}
}

// silentOwner takes the lock without recording itself, so its guard decides
// nothing about who holds the lock.
type silentOwner struct {
	mu    sync.Mutex
	owner atomic.Int64
}

func (s *silentOwner) lockFor(id int64) {
	if s.owner.Load() != id {
		s.mu.Lock() // want "mutex 's.mu' is locked but not unlocked in if"
	}
}

func (s *silentOwner) unlockFor(id int64) {
	if s.owner.Load() == id {
		s.owner.Store(0)
		s.mu.Unlock() // want "mutex 's.mu' is unlocked but not locked"
	}
}
