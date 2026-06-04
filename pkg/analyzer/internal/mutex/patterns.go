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
