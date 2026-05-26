// Package category defines stable check identifiers used as
// analysis.Diagnostic.Category values. Tools like golangci-lint and IDE
// integrations filter diagnostics by these IDs, and the inline directive
// `// goconcurrencylint:ignore <id>...` matches against them.
//
// IDs are kept lowercase-kebab-case and must remain stable: changing one is
// a breaking change for downstream consumers and ignore directives.
package category

import "slices"

// Category is the identifier of a check. Its value is emitted as
// analysis.Diagnostic.Category and matched by inline ignore directives.
type Category string

const (
	// Mutex / RWMutex checks.
	LockWithoutUnlock      Category = "lock-without-unlock"
	UnlockWithoutLock      Category = "unlock-without-lock"
	DeferUnlockWithoutLock Category = "defer-unlock-without-lock"
	UncheckedTryLock       Category = "unchecked-trylock"
	DeferLock              Category = "defer-lock"
	MutexInLoop            Category = "mutex-in-loop"
	DeferUnlockInLoop      Category = "defer-unlock-in-loop"
	RWMutexAPIMismatch     Category = "rwmutex-api-mismatch"
	GoroutineLockDeadlock  Category = "goroutine-lock-deadlock"
	PanicBeforeUnlock      Category = "panic-before-unlock"
	DoubleLock             Category = "double-lock"
	CrossGoroutineUnlock   Category = "cross-goroutine-unlock"
	LockOrderCycle         Category = "lock-order-cycle"

	// WaitGroup checks.
	AddWithoutDone          Category = "add-without-done"
	DoneWithoutAdd          Category = "done-without-add"
	AddAfterWait            Category = "add-after-wait"
	GoAfterWait             Category = "go-after-wait"
	AddInsideGoroutine      Category = "add-inside-goroutine"
	DoneNotDeferred         Category = "done-not-deferred"
	AddLoopCountMismatch    Category = "add-loop-count-mismatch"
	AddZero                 Category = "add-zero"
	AddNegative             Category = "add-negative"
	WaitWithoutAdd          Category = "wait-without-add"
	WaitDeadlock            Category = "wait-deadlock"
	MultipleDoneWorker      Category = "multiple-done-worker"
	NestedWaitGroupDeadlock Category = "nested-waitgroup-deadlock"
	DoneOutsideGoroutine    Category = "done-outside-goroutine"
	GoPanic                 Category = "go-panic"

	// Cross-primitive.
	SyncPrimitiveCopy Category = "sync-primitive-copy"
)

// All returns every known check category. Order is not significant.
func All() []Category {
	return []Category{
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
	return slices.Contains(All(), Category(id))
}
