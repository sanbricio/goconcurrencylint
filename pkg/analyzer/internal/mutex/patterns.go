package mutex

import "slices"

type LockPattern struct {
	LockMethods   []string
	UnlockMethods []string
}

var patterns = []LockPattern{
	WriteLockPattern,
	ReadLockPattern,
}

var (
	WriteLockPattern = LockPattern{
		LockMethods:   []string{"Lock", "TryLock"},
		UnlockMethods: []string{"Unlock"},
	}
	ReadLockPattern = LockPattern{
		LockMethods:   []string{"RLock", "TryRLock"},
		UnlockMethods: []string{"RUnlock"},
	}
)

// oppositeMutexMethods returns the method names that balance method: the
// Unlock side for a Lock call and vice versa.
func oppositeMutexMethods(method string) []string {
	for _, p := range patterns {
		if slices.Contains(p.LockMethods, method) {
			return p.UnlockMethods
		}
		if slices.Contains(p.UnlockMethods, method) {
			return p.LockMethods
		}
	}
	return nil
}

// mutexMethodGroup returns the method names in the same mutex-method group as
// method: Lock and TryLock share a group, as do RLock and TryRLock.
func mutexMethodGroup(method string) []string {
	for _, p := range patterns {
		if slices.Contains(p.LockMethods, method) {
			return p.LockMethods
		}
		if slices.Contains(p.UnlockMethods, method) {
			return p.UnlockMethods
		}
	}
	return nil
}

// lockSide separates the acquiring half of a lock protocol from the releasing
// half, regardless of which pattern (write or read) the method belongs to.
type lockSide uint8

const (
	unknownLockSide lockSide = iota
	acquireLockSide
	releaseLockSide
)

// lockMethodSide classifies a sync lock method as an acquire or a release.
func lockMethodSide(method string) lockSide {
	for _, p := range patterns {
		if slices.Contains(p.LockMethods, method) {
			return acquireLockSide
		}
		if slices.Contains(p.UnlockMethods, method) {
			return releaseLockSide
		}
	}
	return unknownLockSide
}

// sameLockSide reports whether two sync lock methods both acquire or both
// release. The read and write halves share a side on purpose: a `Lock`
// implemented over an inner `RLock` is the sync.RWMutex.RLocker adapter, and
// both operations acquire.
func sameLockSide(a, b string) bool {
	side := lockMethodSide(a)
	return side != unknownLockSide && side == lockMethodSide(b)
}
