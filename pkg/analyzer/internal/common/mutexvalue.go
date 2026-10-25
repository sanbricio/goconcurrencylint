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
// Lock with its own implementation — to time the wait, to record a metric, to
// sort a set of locks — resolves to its own method, so the embedding test says
// nothing about it. Such a type is still a lock: it satisfies sync.Locker, and
// that interface is the language's own statement that Lock acquires and Unlock
// releases. LockContractKind reads that contract, so `x.Lock()` on a hand
// written Locker is tracked exactly like a call on the embedded mutex it would
// otherwise be.

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
	if kind := embeddedSyncMutexKind(typ); kind != NotMutexValue {
		return kind
	}

	// No sync mutex reached through embedding: the type may still implement the
	// lock contract with methods of its own.
	return LockContractKind(typ)
}

// embeddedSyncMutexKind reports the sync mutex typ is, or exposes unchanged
// through embedding.
func embeddedSyncMutexKind(typ types.Type) MutexValueKind {
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

// LockContractKind reports the sync lock contract typ's own method set carries:
// MutexValue when it satisfies sync.Locker (a niladic Lock/Unlock pair), and
// RWMutexValue when it additionally exposes the read half (RLock/RUnlock), the
// shape of sync.RWMutex.
//
// The method set is most of the test: a Lock that takes a key or returns a
// handle is not the contract (the caller holds something the checks cannot
// follow), and a Lock without an Unlock is not a lock at all. The rest is
// hasBackingSyncMutex — the type must be built on a mutex these checks already
// model, because only then are Lock, Unlock and their read halves the whole
// protocol.
func LockContractKind(typ types.Type) MutexValueKind {
	if typ == nil {
		return NotMutexValue
	}

	// Only a named type can carry methods, and resolving a method set is the
	// expensive half of this test: ask the cheap question first.
	target := DerefOnceAndUnalias(typ)
	if _, ok := target.(*types.Named); !ok {
		return NotMutexValue
	}

	if !HasNiladicMethod(target, "Lock") || !HasNiladicMethod(target, "Unlock") {
		return NotMutexValue
	}

	if !hasBackingSyncMutex(target) {
		return NotMutexValue
	}

	if HasNiladicMethod(target, "RLock") && HasNiladicMethod(target, "RUnlock") {
		return RWMutexValue
	}

	return MutexValue
}

// HasNiladicMethod reports whether typ exposes a method named name that takes
// no arguments and returns nothing — the shape of every sync lock operation.
// Promoted methods count, so a type that inherits Unlock from an embedded mutex
// answers true just as a type declaring its own does.
func HasNiladicMethod(typ types.Type, name string) bool {
	if typ == nil {
		return false
	}

	var pkg *types.Package
	if named, ok := typ.(*types.Named); ok && named.Obj() != nil {
		pkg = named.Obj().Pkg()
	}

	obj, _, _ := types.LookupFieldOrMethod(typ, true, pkg, name)
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}

	sig, ok := fn.Type().(*types.Signature)
	return ok && sig.Recv() != nil && sig.Params().Len() == 0 && sig.Results().Len() == 0
}

// hasBackingSyncMutex reports whether typ keeps a sync mutex that its own
// methods can reach: a field holding one, either exported or declared in the
// same package as the type.
//
// This is what separates a wrapper around sync.Mutex from a lock built on
// something else — a semaphore, a channel, a file descriptor. Those offer
// transitions sync.Mutex has no way to express, such as releasing the write
// lock while keeping the read lock, or an acquire that a context can cancel;
// their Lock/Unlock pair reveals only part of the protocol, and modelling it as
// a plain mutex reports code that is correct. A mutex-backed type has no such
// hidden half: sync.Mutex offers nothing beyond the operations named here.
//
// The accessibility test is what keeps a named semaphore type out. Its struct
// does contain a mutex, but one declared in another package, which the type's
// own Lock could not touch even if it wanted to.
//
// An interface has no fields to look at, so a value typed as sync.Locker is not
// classified here. Those are picked up where the concrete lock is assigned to
// them, which is what primitives does for `var l sync.Locker = &mu`.
func hasBackingSyncMutex(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}

	// A collection type that locks its members as a set — the shape a house
	// lock package uses to take several mutexes in a deadlock-free order — is
	// backed by whatever its elements are.
	switch underlying := named.Underlying().(type) {
	case *types.Slice:
		return holdsSyncMutex(underlying.Elem())
	case *types.Array:
		return holdsSyncMutex(underlying.Elem())
	case *types.Map:
		return holdsSyncMutex(underlying.Elem())
	}

	return holdsSyncMutex(named)
}

// holdsSyncMutex reports whether values of typ are a sync mutex or keep one in
// a reachable field. It looks one level deep on purpose: a lock whose own lock
// is another hand-written lock is a protocol these checks have not been shown.
func holdsSyncMutex(typ types.Type) bool {
	target := DerefOnceAndUnalias(typ)
	if embeddedSyncMutexKind(target) != NotMutexValue {
		return true
	}

	named, ok := target.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}

	structType, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}

	for field := range structType.Fields() {
		if !field.Exported() && field.Pkg() != named.Obj().Pkg() {
			continue
		}
		if embeddedSyncMutexKind(field.Type()) != NotMutexValue {
			return true
		}
	}

	return false
}
