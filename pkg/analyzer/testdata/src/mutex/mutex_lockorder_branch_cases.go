package mutex

import "sync"

// Lock order across branches: what is held after an if/switch/select is what
// every arm that falls through still holds. A release made in both arms must
// not leave the lock "held" for the order edges drawn after the statement.

type branchOrder struct {
	a, b sync.Mutex
	n    int
}

// GoodReleasedInBothArms must be CLEAN: a is released on both paths before b
// is taken, so b-then-a below is the only order in the function.
func (x *branchOrder) GoodReleasedInBothArms() {
	x.a.Lock()
	if x.n > 0 {
		x.a.Unlock()
	} else {
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// GoodReleasedInEveryElseIfArm must be CLEAN: the else-if chain is exhaustive
// and every arm releases a.
func (x *branchOrder) GoodReleasedInEveryElseIfArm() {
	x.a.Lock()
	if x.n > 0 {
		x.a.Unlock()
	} else if x.n < 0 {
		x.a.Unlock()
	} else {
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// GoodReleasedInEverySwitchCase must be CLEAN: the default clause makes the
// switch exhaustive and every clause releases a.
func (x *branchOrder) GoodReleasedInEverySwitchCase() {
	x.a.Lock()
	switch x.n {
	case 1:
		x.a.Unlock()
	default:
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// GoodReleasedInEverySelectClause must be CLEAN: a select runs exactly one
// clause, and each of them releases a.
func (x *branchOrder) GoodReleasedInEverySelectClause(ch chan int) {
	x.a.Lock()
	select {
	case <-ch:
		x.a.Unlock()
	default:
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

type perKeyLocks struct {
	mu    sync.RWMutex
	locks map[string]*sync.Mutex
	vals  map[string]int
}

// GoodPerKeyLockAfterReadUnlockInBothArms must be CLEAN. Mirrors seaweedfs
// broker_client_publish.go: the map lock is dropped in both arms before the
// per-key lock is taken, and only then is the map lock taken again.
func (p *perKeyLocks) GoodPerKeyLockAfterReadUnlockInBothArms(key string) int {
	p.mu.RLock()
	keyLock, ok := p.locks[key]
	if !ok {
		p.mu.RUnlock()
		keyLock = &sync.Mutex{}
	} else {
		p.mu.RUnlock()
	}
	keyLock.Lock()
	defer keyLock.Unlock()
	p.mu.RLock()
	v := p.vals[key]
	p.mu.RUnlock()
	return v
}

// BadOnlyEarlyExitArmReleases must be flagged: the arm that releases a returns,
// so the path that reaches b.Lock still holds a, and b-then-a follows.
func (x *branchOrder) BadOnlyEarlyExitArmReleases() {
	x.a.Lock()
	if x.n > 0 {
		x.a.Unlock()
		return
	}
	x.b.Lock()
	x.b.Unlock()
	x.a.Unlock()

	x.b.Lock()
	x.a.Lock() // want "mutex lock order cycle between 'x.a' and 'x.b'"
	x.a.Unlock()
	x.b.Unlock()
}

// BadAcquiredInBothArms must be flagged: both arms take a, so it is held when
// b is taken after the if.
func (x *branchOrder) BadAcquiredInBothArms() {
	if x.n > 0 {
		x.a.Lock()
	} else {
		x.a.Lock()
	}
	x.b.Lock()
	x.b.Unlock()
	x.a.Unlock()

	x.b.Lock()
	x.a.Lock() // want "mutex lock order cycle between 'x.a' and 'x.b'"
	x.a.Unlock()
	x.b.Unlock()
}

// GoodCaseBreaksAfterRelease must be CLEAN: a bare break in a switch clause
// leaves the switch and lands right after it, so that clause's release counts.
func (x *branchOrder) GoodCaseBreaksAfterRelease() {
	x.a.Lock()
	switch {
	case x.n > 0:
		x.a.Unlock()
		break
	default:
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// GoodTypeSwitchReleasesInEveryClause must be CLEAN.
func (x *branchOrder) GoodTypeSwitchReleasesInEveryClause(v any) {
	x.a.Lock()
	switch v.(type) {
	case int:
		x.a.Unlock()
	default:
		x.a.Unlock()
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// GoodNestedArmsAllRelease must be CLEAN: the inner if/else is itself
// exhaustive, so the outer else arm ends without the lock too.
func (x *branchOrder) GoodNestedArmsAllRelease() {
	x.a.Lock()
	if x.n > 0 {
		x.a.Unlock()
	} else {
		if x.n < -10 {
			x.a.Unlock()
		} else {
			x.a.Unlock()
		}
	}
	x.b.Lock()
	x.a.Lock()
	x.a.Unlock()
	x.b.Unlock()
}

// BadSwitchWithoutDefaultKeepsLock must be flagged: with no default, the path
// that matches no case keeps a, and a was taken in every case too, so a is held
// when b is taken.
func (x *branchOrder) BadSwitchWithoutDefaultKeepsLock() {
	x.a.Lock()
	switch x.n {
	case 1:
		x.n++
	}
	x.b.Lock()
	x.b.Unlock()
	x.a.Unlock()

	x.b.Lock()
	x.a.Lock() // want "mutex lock order cycle between 'x.a' and 'x.b'"
	x.a.Unlock()
	x.b.Unlock()
}
