// Package category is the single source of truth for the linter's check
// catalogue. Each check has a stable numeric code (e.g. GCL1001) that is
// emitted as analysis.Diagnostic.Category, shown in the diagnostic message,
// and documented under docs/checks. Codes are grouped by primitive:
//
//	GCL1xxx  sync.Mutex / sync.RWMutex
//	GCL2xxx  sync.WaitGroup
//	GCL3xxx  sync.Once
//	GCL4xxx  sync.Cond
//	GCL5xxx  sync.Pool
//	GCL9xxx  cross-cutting (applies to several primitives)
//
// Every check also keeps a legacy kebab-case slug (e.g. lock-without-unlock).
// The slug is no longer canonical, but it is still accepted as an alias in the
// inline `// goconcurrencylint:ignore <id>` directive so existing suppressions
// keep working. Codes and slugs must both remain stable: changing one is a
// breaking change for downstream consumers and ignore directives.
package category

// Category is the canonical identifier of a check. Its value is the numeric
// code (e.g. "GCL1001"), emitted as analysis.Diagnostic.Category.
type Category string

const (
	// Mutex / RWMutex checks (GCL1xxx).
	LockWithoutUnlock      Category = "GCL1001"
	UnlockWithoutLock      Category = "GCL1002"
	DeferUnlockWithoutLock Category = "GCL1003"
	UncheckedTryLock       Category = "GCL1004"
	DeferLock              Category = "GCL1005"
	MutexInLoop            Category = "GCL1006"
	DeferUnlockInLoop      Category = "GCL1007"
	RWMutexAPIMismatch     Category = "GCL1008"
	GoroutineLockDeadlock  Category = "GCL1009"
	PanicBeforeUnlock      Category = "GCL1010"
	DoubleLock             Category = "GCL1011"
	LockOrderCycle         Category = "GCL1012"
	RWMutexRecursiveLock   Category = "GCL1013"

	// WaitGroup checks (GCL2xxx).
	AddWithoutDone          Category = "GCL2001"
	DoneWithoutAdd          Category = "GCL2002"
	AddAfterWait            Category = "GCL2003"
	GoAfterWait             Category = "GCL2004"
	AddInsideGoroutine      Category = "GCL2005"
	DoneNotDeferred         Category = "GCL2006"
	AddLoopCountMismatch    Category = "GCL2007"
	AddZero                 Category = "GCL2008"
	AddNegative             Category = "GCL2009"
	WaitWithoutAdd          Category = "GCL2010"
	WaitDeadlock            Category = "GCL2011"
	MultipleDoneWorker      Category = "GCL2012"
	NestedWaitGroupDeadlock Category = "GCL2013"
	DoneOutsideGoroutine    Category = "GCL2014"
	GoPanic                 Category = "GCL2015"

	// sync.Once checks (GCL3xxx).
	OnceDoDeadlock     Category = "GCL3001"
	OnceDoNil          Category = "GCL3002"
	OnceConstructorNil Category = "GCL3003"

	// sync.Cond checks (GCL4xxx).
	CondNewNilLocker Category = "GCL4001"

	// sync.Pool checks (GCL5xxx).
	PoolNonPointerValue Category = "GCL5001"

	// Cross-cutting checks (GCL9xxx).
	SyncPrimitiveCopy Category = "GCL9001"
)

// Primitive labels used in documentation and the README table.
const (
	primMutex = "sync.Mutex, sync.RWMutex"
	primRW    = "sync.RWMutex"
	primWG    = "sync.WaitGroup"
	primOnce  = "sync.Once"
	primCond  = "sync.Cond"
	primPool  = "sync.Pool"
	primAll   = "sync.Mutex, sync.RWMutex, sync.WaitGroup, sync.Once, sync.Cond, sync.Pool, sync.Map"
)

// Check is the full, stable metadata for one diagnostic. The registry below
// holds exactly one Check per code and is the single source from which the
// catalogue, ignore-directive matching, README table and per-check docs are
// derived.
type Check struct {
	// Code is the canonical identifier (e.g. "GCL1001").
	Code Category
	// Slug is the legacy kebab-case alias, still accepted in ignore directives.
	Slug string
	// Primitive lists the sync types this check applies to.
	Primitive string
	// Summary is a one-line description of what the check detects.
	Summary string
	// Why explains the runtime consequence the check guards against.
	Why string
	// Bad is a minimal Go snippet illustrating the pattern the check flags.
	Bad string
	// Good is the corrected version of Bad that the check accepts.
	Good string
}

// registry is the ordered catalogue of every check. Order is by code and is
// the order used for generated documentation.
var registry = []Check{
	{LockWithoutUnlock, "lock-without-unlock", primMutex,
		"A Lock()/RLock() call has no matching Unlock()/RUnlock() on some execution path.",
		"The mutex stays locked, so every later Lock() on it blocks forever — a deadlock that usually surfaces only under load.",
		`
func update(mu *sync.Mutex, data map[string]int) {
	mu.Lock()
	data["n"]++ // returns with the mutex still locked
}`,
		`
func update(mu *sync.Mutex, data map[string]int) {
	mu.Lock()
	defer mu.Unlock()
	data["n"]++
}`},
	{UnlockWithoutLock, "unlock-without-lock", primMutex,
		"An Unlock()/RUnlock() call is reached without a prior matching lock (including double-unlocks).",
		`Unlocking a mutex that is not held panics at runtime ("sync: unlock of unlocked mutex").`,
		`
func release(mu *sync.Mutex) {
	mu.Unlock() // mu was never locked in this scope
}`,
		`
func critical(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	// ... critical section
}`},
	{DeferUnlockWithoutLock, "defer-unlock-without-lock", primMutex,
		"A deferred Unlock()/RUnlock() can run while the mutex is unlocked.",
		"If the deferred unlock runs without a matching lock — or after a return/panic between defer and lock — it panics.",
		`
func work(mu *sync.Mutex) {
	defer mu.Unlock() // deferred before the lock is taken
	mu.Lock()
}`,
		`
func work(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
}`},
	{UncheckedTryLock, "unchecked-trylock", primMutex,
		"TryLock()/TryRLock() is called without checking the returned boolean.",
		"The code enters the critical section whether or not it actually acquired the lock, defeating the mutual exclusion.",
		`
func try(mu *sync.Mutex) {
	mu.TryLock() // result ignored: the lock may not be held
	defer mu.Unlock()
	// ... critical section
}`,
		`
func try(mu *sync.Mutex) {
	if mu.TryLock() {
		defer mu.Unlock()
		// ... critical section
	}
}`},
	{DeferLock, "defer-lock", primMutex,
		"defer mu.Lock()/RLock() is used where an unlock was almost certainly intended.",
		"Deferring Lock() acquires the lock at function return instead of releasing it — typically an immediate self-deadlock.",
		`
func work(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Lock() // meant Unlock: re-locks at return
	// ...
}`,
		`
func work(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	// ...
}`},
	{MutexInLoop, "mutex-in-loop", primMutex,
		"A mutex is declared inside a loop body, creating a fresh lock per iteration.",
		"A new mutex per iteration guards nothing shared, so the intended mutual exclusion never actually happens.",
		`
for i := range items {
	var mu sync.Mutex // a fresh lock each iteration guards nothing
	mu.Lock()
	process(items[i])
	mu.Unlock()
}`,
		`
var mu sync.Mutex
for i := range items {
	mu.Lock()
	process(items[i])
	mu.Unlock()
}`},
	{DeferUnlockInLoop, "defer-unlock-in-loop", primMutex,
		"defer mu.Unlock() lives inside a loop body, so the unlock only runs at function return.",
		"The lock is held across every remaining iteration, serialising the loop or deadlocking it.",
		`
for _, it := range items {
	mu.Lock()
	defer mu.Unlock() // runs only when the function returns
	process(it)
}`,
		`
for _, it := range items {
	mu.Lock()
	process(it)
	mu.Unlock()
}`},
	{RWMutexAPIMismatch, "rwmutex-api-mismatch", primRW,
		"Unlock() is used for a read lock, or RUnlock() is used for a write lock.",
		"Mismatched lock/unlock pairs corrupt the RWMutex state and panic or deadlock at runtime.",
		`
var mu sync.RWMutex
mu.RLock()
defer mu.Unlock() // RLock paired with Unlock instead of RUnlock`,
		`
var mu sync.RWMutex
mu.RLock()
defer mu.RUnlock()`},
	{GoroutineLockDeadlock, "goroutine-lock-deadlock", primMutex,
		"A goroutine started while a lock is held tries to take the same lock before the parent releases it.",
		"The child blocks on a lock the parent still holds while the parent waits on the child — a guaranteed deadlock.",
		`
mu.Lock()
go func() {
	mu.Lock() // blocks on the parent, which is waiting on this goroutine
	defer mu.Unlock()
}()
// parent keeps holding mu`,
		`
mu.Lock()
// ... use the shared state
mu.Unlock()
go func() {
	mu.Lock()
	defer mu.Unlock()
}()`},
	{PanicBeforeUnlock, "panic-before-unlock", primMutex,
		"A statically-known out-of-range index can panic between Lock() and a non-deferred unlock.",
		"If the indexing panics before the Unlock() runs, the mutex is never released and leaks locked.",
		`
mu.Lock()
v := s[10] // can panic before the Unlock below runs
mu.Unlock()
use(v)`,
		`
mu.Lock()
defer mu.Unlock() // unlock still runs if the indexing panics
v := s[10]
use(v)`},
	{DoubleLock, "double-lock", primMutex,
		"A second Lock() is taken while the first is still held.",
		"Re-locking a non-reentrant sync.Mutex that is already held deadlocks the goroutine immediately.",
		`
mu.Lock()
mu.Lock() // second lock on a held, non-reentrant mutex deadlocks
defer mu.Unlock()`,
		`
mu.Lock()
defer mu.Unlock()
// no second Lock on the same mutex`},
	{LockOrderCycle, "lock-order-cycle", primMutex,
		"Two functions acquire the same pair of mutexes in opposite orders — a classic deadlock pattern.",
		"When the two paths interleave, each holds one lock and waits for the other — a deadlock.",
		`
func f() { a.Lock(); b.Lock(); b.Unlock(); a.Unlock() }
func g() { b.Lock(); a.Lock(); a.Unlock(); b.Unlock() } // opposite order`,
		`
// both functions take a before b
func f() { a.Lock(); b.Lock(); b.Unlock(); a.Unlock() }
func g() { a.Lock(); b.Lock(); a.Unlock(); b.Unlock() }`},
	{RWMutexRecursiveLock, "rwmutex-recursive-lock", primRW,
		"A goroutine re-acquires an RWMutex it already holds in a conflicting mode (read then write, or write then read), which self-deadlocks.",
		"Go's sync.RWMutex is neither recursive nor upgradable: Lock waits for the goroutine's own read lock to be released, and RLock waits for its own write lock — so the goroutine blocks on itself forever.",
		`
var mu sync.RWMutex
mu.RLock()
mu.Lock() // upgrading the read lock to a write lock on the same goroutine deadlocks
mu.Unlock()
mu.RUnlock()`,
		`
var mu sync.RWMutex
mu.RLock()
_ = read()
mu.RUnlock()
mu.Lock() // take the write lock only after releasing the read lock
mu.Unlock()`},

	{AddWithoutDone, "add-without-done", primWG,
		"wg.Add(n) has fewer guaranteed Done()s than its count, so the counter can never reach zero.",
		"The counter never reaches zero, so Wait() blocks forever and leaks the waiting goroutine.",
		`
var wg sync.WaitGroup
wg.Add(1)
go func() {
	work() // no wg.Done(): Wait() blocks forever
}()
wg.Wait()`,
		`
var wg sync.WaitGroup
wg.Add(1)
go func() {
	defer wg.Done()
	work()
}()
wg.Wait()`},
	{DoneWithoutAdd, "done-without-add", primWG,
		"wg.Done() is called more times than wg.Add() allows, which panics at runtime.",
		"Driving the counter below zero panics with \"sync: negative WaitGroup counter\".",
		`
var wg sync.WaitGroup
wg.Add(1)
wg.Done()
wg.Done() // drives the counter negative: panics`,
		`
var wg sync.WaitGroup
wg.Add(1)
wg.Done()`},
	{AddAfterWait, "add-after-wait", primWG,
		"wg.Add() is called after wg.Wait() returned with an empty counter — a classic reuse bug.",
		"Adding to a group whose counter already hit zero races with the returned Wait().",
		`
wg.Wait()
wg.Add(1) // reusing a drained group races with the returned Wait()
go worker(&wg)`,
		`
wg.Add(1)
go worker(&wg)
wg.Wait()`},
	{GoAfterWait, "go-after-wait", primWG,
		"wg.Go() is called after wg.Wait() returned empty — the Go 1.25 variant of add-after-wait.",
		"Reusing a drained group with Go() races with the prior Wait() the same way add-after-wait does.",
		`
wg.Wait()
wg.Go(worker) // reusing a drained group races with the prior Wait()`,
		`
wg.Go(worker)
wg.Wait()`},
	{AddInsideGoroutine, "add-inside-goroutine", primWG,
		"wg.Add() is called from inside a worker goroutine, racing with Wait().",
		"Wait() may observe a zero counter and return before the work is even registered.",
		`
for _, t := range tasks {
	go func(t Task) {
		wg.Add(1) // races with Wait(): may run after it returns
		defer wg.Done()
		t.Run()
	}(t)
}
wg.Wait()`,
		`
for _, t := range tasks {
	wg.Add(1)
	go func(t Task) {
		defer wg.Done()
		t.Run()
	}(t)
}
wg.Wait()`},
	{DoneNotDeferred, "done-not-deferred", primWG,
		"A worker calls Done() on a path a runtime.Goexit or recovered panic can skip, instead of deferring it.",
		"runtime.Goexit (or a panic the goroutine recovers) ends the worker's flow while the process keeps running, so a non-deferred Done() is skipped, the counter never reaches zero and Wait() blocks forever. An unrecovered panic is not flagged: it crashes the whole process, so the missed Done() is moot.",
		`
go func() {
	if failed {
		runtime.Goexit() // ends the goroutine, skipping the Done() below
	}
	wg.Done()
}()`,
		`
go func() {
	defer wg.Done() // runs even on runtime.Goexit or a recovered panic
	if failed {
		runtime.Goexit()
	}
}()`},
	{AddLoopCountMismatch, "add-loop-count-mismatch", primWG,
		"A literal Add(n) count does not match a statically countable loop of worker goroutines.",
		"If Add(n) and the number of workers disagree, Wait() either returns early or blocks forever.",
		`
wg.Add(3) // count disagrees with the 5 workers started below
for i := 0; i < 5; i++ {
	go func() { defer wg.Done(); work() }()
}
wg.Wait()`,
		`
const n = 5
wg.Add(n)
for i := 0; i < n; i++ {
	go func() { defer wg.Done(); work() }()
}
wg.Wait()`},
	{AddZero, "add-zero", primWG,
		"wg.Add(0) is a no-op and usually means the intended count was lost.",
		"With a zero count Wait() returns immediately and the workers run unsynchronised.",
		`
var wg sync.WaitGroup
wg.Add(0) // no-op: Wait() returns immediately
go func() { defer wg.Done(); work() }()
wg.Wait()`,
		`
var wg sync.WaitGroup
wg.Add(1)
go func() { defer wg.Done(); work() }()
wg.Wait()`},
	{AddNegative, "add-negative", primWG,
		"wg.Add(n) is called with a negative literal, which panics at runtime.",
		"A negative Add() drives the counter below zero and panics.",
		`
var wg sync.WaitGroup
wg.Add(-1) // a negative counter panics`,
		`
var wg sync.WaitGroup
wg.Add(1)
defer wg.Done()`},
	{WaitWithoutAdd, "wait-without-add", primWG,
		"A local WaitGroup is waited on without any Add() in the same lifecycle.",
		"The Wait() is either a no-op or a sign that an Add() was lost, leaving the workers unsynchronised.",
		`
var wg sync.WaitGroup
go func() { work() }() // no Add/Done in this lifecycle
wg.Wait()              // nothing to wait for`,
		`
var wg sync.WaitGroup
wg.Add(1)
go func() { defer wg.Done(); work() }()
wg.Wait()`},
	{WaitDeadlock, "wait-deadlock", primWG,
		"Wait() is reached while the same goroutine still owes a Done().",
		"The counter can never reach zero from this goroutine, so Wait() self-deadlocks.",
		`
var wg sync.WaitGroup
wg.Add(1)
wg.Wait() // this goroutine still owes the Done(): self-deadlock
wg.Done()`,
		`
var wg sync.WaitGroup
wg.Add(1)
go func() { defer wg.Done(); work() }()
wg.Wait()`},
	{MultipleDoneWorker, "multiple-done-worker", primWG,
		"The same worker branch can call Done() more than once.",
		"The extra Done() over-decrements the counter and panics.",
		`
go func() {
	defer wg.Done()
	if err != nil {
		wg.Done() // second Done() on this branch over-decrements
		return
	}
}()`,
		`
go func() {
	defer wg.Done()
	if err != nil {
		return
	}
}()`},
	{NestedWaitGroupDeadlock, "nested-waitgroup-deadlock", primWG,
		"A worker for one WaitGroup waits on another whose release is blocked behind the outer Wait().",
		"The inner Wait() can only return after the outer Wait(), which is itself waiting on this worker — a deadlock.",
		`
var outer, inner sync.WaitGroup
outer.Add(1)
inner.Add(1)
go func() {
	defer outer.Done()
	inner.Wait() // released only after outer.Wait() returns: deadlock
}()
outer.Wait()
inner.Done()`,
		`
var outer, inner sync.WaitGroup
outer.Add(1)
inner.Add(1)
go func() {
	defer outer.Done()
	inner.Done() // release inner from inside the worker
}()
inner.Wait()
outer.Wait()`},
	{DoneOutsideGoroutine, "done-outside-goroutine", primWG,
		"Done() runs on the parent goroutine instead of the worker, so a panic in the parent skips it.",
		"If the parent panics before its Done(), the counter never reaches zero and Wait() blocks forever.",
		`
wg.Add(1)
go work()  // the worker never signals completion
wg.Done()  // the parent signals instead: Wait() may return early
wg.Wait()`,
		`
wg.Add(1)
go func() {
	defer wg.Done() // the worker signals when it really finishes
	work()
}()
wg.Wait()`},
	{GoPanic, "go-panic", primWG,
		"A function passed to wg.Go() may panic and bring the program down.",
		"An unrecovered panic in a Go() worker propagates and crashes the whole program.",
		`
wg.Go(func() {
	mustParse(input) // an unrecovered panic crashes the program
})`,
		`
wg.Go(func() {
	if err := parse(input); err != nil {
		log.Print(err) // handle the error instead of panicking
	}
})`},

	{OnceDoDeadlock, "once-do-deadlock", primOnce,
		"once.Do(f) where f calls Do on the same Once again — Once.Do is not reentrant, so this deadlocks.",
		"The inner Do waits for the outer Do to finish, which is waiting on the inner one — a deadlock.",
		`
var once sync.Once
once.Do(func() {
	once.Do(func() {}) // re-entrant Do on the same Once deadlocks
})`,
		`
var once sync.Once
once.Do(func() {
	initialize() // no re-entrant Do on the same Once
})`},
	{OnceDoNil, "once-do-nil", primOnce,
		"once.Do(nil) panics when the function is invoked.",
		"The first run dereferences a nil function value and panics.",
		`
var once sync.Once
once.Do(nil) // invoking a nil function value panics`,
		`
var once sync.Once
once.Do(func() {
	initialize()
})`},
	{OnceConstructorNil, "once-constructor-nil", primOnce,
		"sync.OnceFunc/OnceValue/OnceValues is called with a nil function, which panics when the memoized function first runs.",
		"Each constructor wraps f in a Once and returns a function that invokes f; a nil f is a nil function call that panics the first time the returned function runs.",
		`
handle := sync.OnceFunc(nil) // calling handle later panics on the nil function
handle()`,
		`
handle := sync.OnceFunc(func() {
	initialize()
})
handle()`},

	{CondNewNilLocker, "cond-new-nil-locker", primCond,
		"sync.NewCond(nil) builds a Cond whose Locker is nil, so the first Wait panics at runtime.",
		"Cond.Wait unlocks and relocks the Locker; with a nil Locker that call is a nil dereference and panics the first time the Cond is used.",
		`
	c := sync.NewCond(nil) // nil Locker: c.Wait panics at runtime`,
		`
	var mu sync.Mutex
	c := sync.NewCond(&mu) // pass a real Locker`},

	{PoolNonPointerValue, "pool-non-pointer-value", primPool,
		"A non-pointer value is placed in a sync.Pool (a Put argument or a New return), so every call boxes it into an interface and heap-allocates — defeating the pool.",
		"A value that is not pointer-shaped cannot live in the pool's internal interface without a heap allocation, so pooling it allocates on every Put or New miss instead of reusing memory; storing a pointer avoids the boxing.",
		`
var pool sync.Pool
buf := make([]byte, 1024)
pool.Put(buf) // []byte is not pointer-shaped: this allocates on every Put`,
		`
var pool sync.Pool
buf := make([]byte, 1024)
pool.Put(&buf) // store a pointer so nothing is boxed`},

	{SyncPrimitiveCopy, "sync-primitive-copy", primAll,
		"A sync primitive (or a struct embedding one) is copied by value.",
		"Copying duplicates the internal state, so the copy and the original stop synchronising — causing races, lost wakeups or panics.",
		`
type Counter struct {
	mu sync.Mutex
	n  int
}

func (c Counter) Inc() { // value receiver copies the mutex
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}`,
		`
type Counter struct {
	mu sync.Mutex
	n  int
}

func (c *Counter) Inc() { // pointer receiver: no copy
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}`},
}

// Lookup tables built once from the registry. byCode and bySlug map either
// identifier form to its full Check.
var (
	byCode = func() map[Category]Check {
		m := make(map[Category]Check, len(registry))
		for _, c := range registry {
			m[c.Code] = c
		}
		return m
	}()
	bySlug = func() map[string]Check {
		m := make(map[string]Check, len(registry))
		for _, c := range registry {
			m[c.Slug] = c
		}
		return m
	}()
)

// All returns every known check code, ordered by code.
func All() []Category {
	out := make([]Category, len(registry))
	for i, c := range registry {
		out[i] = c.Code
	}
	return out
}

// Checks returns the full catalogue, ordered by code. It is the source for
// generated documentation and the explain command.
func Checks() []Check {
	out := make([]Check, len(registry))
	copy(out, registry)
	return out
}

// IsKnown reports whether id is a recognised check, matching either the
// canonical code (GCL1001) or the legacy slug (lock-without-unlock).
func IsKnown(id string) bool {
	if _, ok := byCode[Category(id)]; ok {
		return true
	}
	_, ok := bySlug[id]
	return ok
}

// Canonical resolves id — a code or a legacy slug — to its canonical code.
// The second result is false when id matches no known check.
func Canonical(id string) (Category, bool) {
	if c, ok := byCode[Category(id)]; ok {
		return c.Code, true
	}
	if c, ok := bySlug[id]; ok {
		return c.Code, true
	}
	return "", false
}

// Lookup returns the full Check for a canonical code. The second result is
// false when code is unknown.
func Lookup(code Category) (Check, bool) {
	c, ok := byCode[code]
	return c, ok
}
