package common

import "go/types"

// MutexValueKind classifies the sync lock behaviour exposed by a value.
// Keeping this as one classification avoids resolving the promoted method set
// once for Mutex and again for RWMutex at every use site.
type MutexValueKind uint8

const (
	NotMutexValue MutexValueKind = iota
	MutexValue
	RWMutexValue
)

// Go code rarely stores a bare sync.Mutex once a project grows: it wraps one in
// a named type (`type Mutex struct { sync.Mutex }`) to add deadlock detection,
// metrics or a house API, and then locks through the promoted method. The
// helpers here answer "does a value of this type behave as a sync.Mutex" so a
// wrapper is tracked like the mutex it embeds.
//
// The question is answered by asking go/types which method `x.Lock()` actually
// resolves to, never by matching type or field names. A wrapper that overrides
// Lock with its own implementation resolves to its own method, and is
// deliberately left untracked: its body may hold the lock past the call, so the
// pairing the mutex checks assume would not hold.

// IsMutexValue reports whether typ is a sync.Mutex or a named type that
// exposes sync.Mutex's Lock/Unlock pair through embedding.
func IsMutexValue(typ types.Type) bool {
	return ClassifyMutexValue(typ) == MutexValue
}

// IsRWMutexValue reports whether typ is a sync.RWMutex or a named type that
// exposes sync.RWMutex's lock methods through embedding.
func IsRWMutexValue(typ types.Type) bool {
	return ClassifyMutexValue(typ) == RWMutexValue
}

// ClassifyMutexValue reports which sync mutex, if any, typ exposes directly or
// through promoted methods from an embedded value.
func ClassifyMutexValue(typ types.Type) MutexValueKind {
	if IsMutex(typ) {
		return MutexValue
	}
	if IsRWMutex(typ) {
		return RWMutexValue
	}

	name, ok := promotedSyncMutexName(typ)
	if !ok {
		return NotMutexValue
	}
	if name == "RWMutex" {
		return RWMutexValue
	}
	return MutexValue
}

// IsLockOnlyValue reports whether typ is a sync mutex or a named type that
// exists only to wrap one: a struct whose single field is the embedded mutex,
// however many wrappers deep.
//
// Checks that reason about the mutex as a standalone value rather than about
// the operations performed on it use this narrower question. Creating a lock
// per loop iteration is a bug; creating a server, a connection or a test
// fixture per iteration is ordinary code, and those objects embed mutexes all
// the time.
func IsLockOnlyValue(typ types.Type) bool {
	if ClassifyMutexValue(typ) == NotMutexValue {
		return false
	}
	if IsMutex(typ) || IsRWMutex(typ) {
		return true
	}

	named, ok := DerefOnceAndUnalias(typ).(*types.Named)
	if !ok {
		return false
	}

	structType, ok := named.Underlying().(*types.Struct)
	if !ok || structType.NumFields() != 1 || !structType.Field(0).Embedded() {
		return false
	}

	return IsLockOnlyValue(structType.Field(0).Type())
}

// promotedSyncMutexName returns the sync mutex type ("Mutex" or "RWMutex") that
// typ inherits its lock methods from, or false when typ does not wrap one.
//
// Both halves of the pair must resolve to the same sync type. A wrapper that
// overrides one half only — an Unlock that also records how long the lock was
// held, say — is not a plain mutex, and the lock/unlock bookkeeping the checks
// perform would not describe it.
func promotedSyncMutexName(typ types.Type) (string, bool) {
	if typ == nil {
		return "", false
	}

	named, ok := DerefOnceAndUnalias(typ).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}

	// sync.Mutex and sync.RWMutex themselves are the business of IsMutex and
	// IsRWMutex; this helper only answers for the types that wrap them.
	if MatchesPkgAndName(named, "sync", "Mutex", "RWMutex") {
		return "", false
	}

	if _, undOk := named.Underlying().(*types.Struct); !undOk {
		return "", false
	}

	owner, ok := promotedSyncMutexMethod(named, "Lock")
	if !ok {
		return "", false
	}

	if unlockOwner, ok := promotedSyncMutexMethod(named, "Unlock"); !ok || unlockOwner != owner {
		return "", false
	}

	if owner == "RWMutex" {
		if rLockOwner, ok := promotedSyncMutexMethod(named, "RLock"); !ok || rLockOwner != owner {
			return "", false
		}
		if rUnlockOwner, ok := promotedSyncMutexMethod(named, "RUnlock"); !ok || rUnlockOwner != owner {
			return "", false
		}
	}

	return owner, true
}

// promotedSyncMutexMethod returns the sync type that owns the method named
// method on named, when the method resolves to one declared on an embedded
// sync mutex. Embedding depth does not matter: a type embedding a type that
// embeds sync.Mutex resolves the same way a direct embed does.
func promotedSyncMutexMethod(named *types.Named, method string) (string, bool) {
	obj, _, _ := types.LookupFieldOrMethod(named, true, named.Obj().Pkg(), method)

	fn, ok := obj.(*types.Func)
	if !ok {
		return "", false
	}

	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", false
	}

	return MatchPkgAndName(DerefOnceAndUnalias(sig.Recv().Type()), "sync", "Mutex", "RWMutex")
}
