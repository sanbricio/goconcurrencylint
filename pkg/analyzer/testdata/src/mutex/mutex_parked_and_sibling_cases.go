package mutex

import "sync"

type parkedCandidate struct {
	sync.Mutex
	ready bool
	next  *parkedCandidate
}

// GoodParkWhileHeld must be CLEAN: the search parks the candidate it holds,
// and the release goes through the parking variable.
func GoodParkWhileHeld(candidates []*parkedCandidate) bool {
	var locked *parkedCandidate
	for _, c := range candidates {
		n := c
		if n.TryLock() {
			if n.ready {
				locked = n
				break
			}
			n.Unlock()
		}
	}
	if locked == nil {
		return false
	}
	locked.Unlock()
	return true
}

// BadParkWithoutHolding must be flagged: the candidate is parked after its
// lock was given back.
func BadParkWithoutHolding(candidates []*parkedCandidate) bool {
	var locked *parkedCandidate
	for _, c := range candidates {
		n := c
		if n.TryLock() {
			n.Unlock()
			if n.ready {
				locked = n
				break
			}
		}
	}
	if locked == nil {
		return false
	}
	locked.Unlock() // want "mutex 'n' is unlocked but not locked"
	return true
}

type siblingDisk struct {
	mu   sync.RWMutex
	data int
}

type siblingStore struct {
	disks []*siblingDisk
	extra *otherDisk
}

// rLockAll takes every disk's lock and returns the release.
func (s *siblingStore) rLockAll() func() {
	for _, d := range s.disks {
		d.mu.RLock()
	}
	return func() {
		for _, d := range s.disks {
			d.mu.RUnlock()
		}
	}
}

func (s *siblingStore) pick() *siblingDisk {
	for _, d := range s.disks {
		if d.data > 0 {
			return d
		}
	}
	return nil
}

// GoodReleaseOthersThenPicked must be CLEAN: every disk was locked by
// rLockAll; the loop gives back all but the picked one, which goes last.
func (s *siblingStore) GoodReleaseOthersThenPicked() int {
	unlock := s.rLockAll()
	picked := s.pick()
	if picked == nil {
		unlock()
		return 0
	}
	for _, d := range s.disks {
		if d != picked {
			d.mu.RUnlock()
		}
	}
	n := picked.data
	picked.mu.RUnlock()
	return n
}

type otherDisk struct {
	mu   sync.RWMutex
	data int
}

// BadReleaseUnrelatedDisk must be flagged: the released lock belongs to a
// value of another type than the collection's elements.
func (s *siblingStore) BadReleaseUnrelatedDisk() {
	for _, d := range s.disks {
		d.mu.RUnlock()
	}
	s.extra.mu.RUnlock() // want "rwmutex 's.extra.mu' is runlocked but not rlocked"
}
