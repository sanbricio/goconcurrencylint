// Package category defines stable check identifiers used as
// analysis.Diagnostic.Category values. Tools like golangci-lint and IDE
// integrations filter diagnostics by these IDs, and the inline directive
// `// goconcurrencylint:ignore <id>...` matches against them.
//
// IDs are kept lowercase-kebab-case and must remain stable: changing one is
// a breaking change for downstream consumers and ignore directives.
package category

import "slices"

const (
	// Mutex / RWMutex checks.
	LockWithoutUnlock      = "lock-without-unlock"
	UnlockWithoutLock      = "unlock-without-lock"
	DeferUnlockWithoutLock = "defer-unlock-without-lock"
	UncheckedTryLock       = "unchecked-trylock"
	DeferLock              = "defer-lock"
	MutexInLoop            = "mutex-in-loop"
	DeferUnlockInLoop      = "defer-unlock-in-loop"
	RWMutexAPIMismatch     = "rwmutex-api-mismatch"
	GoroutineLockDeadlock  = "goroutine-lock-deadlock"
	PanicBeforeUnlock      = "panic-before-unlock"
	DoubleLock             = "double-lock"
	CrossGoroutineUnlock   = "cross-goroutine-unlock"
	LockOrderCycle         = "lock-order-cycle"

	// WaitGroup checks.
	AddWithoutDone          = "add-without-done"
	DoneWithoutAdd          = "done-without-add"
	AddAfterWait            = "add-after-wait"
	GoAfterWait             = "go-after-wait"
	AddInsideGoroutine      = "add-inside-goroutine"
	DoneNotDeferred         = "done-not-deferred"
	AddLoopCountMismatch    = "add-loop-count-mismatch"
	AddZero                 = "add-zero"
	AddNegative             = "add-negative"
	WaitWithoutAdd          = "wait-without-add"
	WaitDeadlock            = "wait-deadlock"
	MultipleDoneWorker      = "multiple-done-worker"
	NestedWaitGroupDeadlock = "nested-waitgroup-deadlock"
	DoneOutsideGoroutine    = "done-outside-goroutine"
	GoPanic                 = "go-panic"

	// Cross-primitive.
	SyncPrimitiveCopy = "sync-primitive-copy"
)

// All returns every known check category. Order is not significant.
func All() []string {
	return []string{
		LockWithoutUnlock,
		UnlockWithoutLock,
		DeferUnlockWithoutLock,
		UncheckedTryLock,
		DeferLock,
		MutexInLoop,
		DeferUnlockInLoop,
		RWMutexAPIMismatch,
		GoroutineLockDeadlock,
		PanicBeforeUnlock,
		DoubleLock,
		CrossGoroutineUnlock,
		LockOrderCycle,
		AddWithoutDone,
		DoneWithoutAdd,
		AddAfterWait,
		GoAfterWait,
		AddInsideGoroutine,
		DoneNotDeferred,
		AddLoopCountMismatch,
		AddZero,
		AddNegative,
		WaitWithoutAdd,
		WaitDeadlock,
		MultipleDoneWorker,
		NestedWaitGroupDeadlock,
		DoneOutsideGoroutine,
		GoPanic,
		SyncPrimitiveCopy,
	}
}

// IsKnown reports whether id is a recognised check category.
func IsKnown(id string) bool {
	return slices.Contains(All(), id)
}
