package mutex

import "sync"

// A field of a parameter or of the receiver that the function never writes
// has one value for the whole call, like an unassigned parameter: deciding
// twice on it pairs the lock taken under the first decision with the release
// under the second.

type slowReadOption struct {
	Slow     bool
	Fast     bool
	Revision int
}

type fieldFactVolume struct {
	access sync.RWMutex
	data   []byte
	raft   *int
	peer   *int
}

// GoodParamFieldDecidesTwice must be CLEAN: every pass decides on the same
// unwritten field, before taking the lock and on each way out.
func (v *fieldFactVolume) GoodParamFieldDecidesTwice(opt *slowReadOption, chunks int) int {
	n := 0
	for i := 0; i < chunks; i++ {
		if opt.Slow {
			v.access.RLock()
			if v.data == nil {
				v.access.RUnlock()
				return n
			}
		}
		if len(v.data) < i {
			if opt.Slow {
				v.access.RUnlock()
			}
			return n
		}
		n += len(v.data)
		opt.Revision++
		if opt.Slow {
			v.access.RUnlock()
		}
	}
	return n
}

// GoodReceiverFieldsExhaustive must be CLEAN: past the first guard one of the
// two fields is set.
func (v *fieldFactVolume) GoodReceiverFieldsExhaustive() int {
	n := 0
	v.access.RLock()
	if v.raft == nil && v.peer == nil {
		v.access.RUnlock()
		return 0
	}
	if v.raft != nil {
		n = *v.raft
		v.access.RUnlock()
	} else if v.peer != nil {
		n = *v.peer
		v.access.RUnlock()
	}
	return n
}

// BadParamFieldsDiffer must be flagged: the two decisions read different
// fields.
func (v *fieldFactVolume) BadParamFieldsDiffer(opt *slowReadOption) int {
	if opt.Slow {
		v.access.RLock() // want "rwmutex 'v.access' is rlocked but not runlocked"
	}
	n := len(v.data)
	if opt.Fast {
		v.access.RUnlock() // want "rwmutex 'v.access' is runlocked but not rlocked"
	}
	return n
}

// A Lock that implements sync.Locker by retrying TryLock returns holding the
// lock, which is what Lock means.

type retryingLocker struct {
	main *sync.RWMutex
	gate chan struct{}
}

// Lock must be CLEAN.
func (m *retryingLocker) Lock() {
	for {
		<-m.gate
		if m.main.TryLock() {
			return
		}
	}
}

// Unlock must be CLEAN.
func (m *retryingLocker) Unlock() {
	m.main.Unlock()
	select {
	case m.gate <- struct{}{}:
	default:
	}
}

type lonelyTryLocker struct {
	main sync.Mutex
}

// Acquire must be flagged: nothing on the type releases what it takes.
func (m *lonelyTryLocker) Acquire() {
	for {
		if m.main.TryLock() { // want "mutex 'm.main' is locked but not unlocked"
			return
		}
	}
}
