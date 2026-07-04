# Check catalogue

Every diagnostic `goconcurrencylint` can emit, with its stable code. The code is shown in the message (e.g. `GCL1001: ...`) and carried as the [`analysis.Diagnostic.Category`](https://pkg.go.dev/golang.org/x/tools/go/analysis#Diagnostic). Run `goconcurrencylint explain <code>` for any check.

## sync.Mutex / sync.RWMutex

| Code | Slug | Description |
|------|------|-------------|
| [GCL1001](GCL1001.md) | `lock-without-unlock` | A Lock()/RLock() call has no matching Unlock()/RUnlock() on some execution path. |
| [GCL1002](GCL1002.md) | `unlock-without-lock` | An Unlock()/RUnlock() call is reached without a prior matching lock (including double-unlocks). |
| [GCL1003](GCL1003.md) | `defer-unlock-without-lock` | A deferred Unlock()/RUnlock() can run while the mutex is unlocked. |
| [GCL1004](GCL1004.md) | `unchecked-trylock` | TryLock()/TryRLock() is called without checking the returned boolean. |
| [GCL1005](GCL1005.md) | `defer-lock` | defer mu.Lock()/RLock() is used where an unlock was almost certainly intended. |
| [GCL1006](GCL1006.md) | `mutex-in-loop` | A mutex is declared inside a loop body, creating a fresh lock per iteration. |
| [GCL1007](GCL1007.md) | `defer-unlock-in-loop` | defer mu.Unlock() lives inside a loop body, so the unlock only runs at function return. |
| [GCL1008](GCL1008.md) | `rwmutex-api-mismatch` | Unlock() is used for a read lock, or RUnlock() is used for a write lock. |
| [GCL1009](GCL1009.md) | `goroutine-lock-deadlock` | A goroutine started while a lock is held tries to take the same lock before the parent releases it. |
| [GCL1010](GCL1010.md) | `panic-before-unlock` | A statically-known out-of-range index can panic between Lock() and a non-deferred unlock. |
| [GCL1011](GCL1011.md) | `double-lock` | A second Lock() is taken while the first is still held. |
| [GCL1012](GCL1012.md) | `lock-order-cycle` | Two functions acquire the same pair of mutexes in opposite orders — a classic deadlock pattern. |

## sync.WaitGroup

| Code | Slug | Description |
|------|------|-------------|
| [GCL2001](GCL2001.md) | `add-without-done` | wg.Add(n) has fewer guaranteed Done()s than its count, so the counter can never reach zero. |
| [GCL2002](GCL2002.md) | `done-without-add` | wg.Done() is called more times than wg.Add() allows, which panics at runtime. |
| [GCL2003](GCL2003.md) | `add-after-wait` | wg.Add() is called after wg.Wait() returned with an empty counter — a classic reuse bug. |
| [GCL2004](GCL2004.md) | `go-after-wait` | wg.Go() is called after wg.Wait() returned empty — the Go 1.25 variant of add-after-wait. |
| [GCL2005](GCL2005.md) | `add-inside-goroutine` | wg.Add() is called from inside a worker goroutine, racing with Wait(). |
| [GCL2006](GCL2006.md) | `done-not-deferred` | A worker calls Done() after an explicit panic or runtime.Goexit path instead of deferring it. |
| [GCL2007](GCL2007.md) | `add-loop-count-mismatch` | A literal Add(n) count does not match a statically countable loop of worker goroutines. |
| [GCL2008](GCL2008.md) | `add-zero` | wg.Add(0) is a no-op and usually means the intended count was lost. |
| [GCL2009](GCL2009.md) | `add-negative` | wg.Add(n) is called with a negative literal, which panics at runtime. |
| [GCL2010](GCL2010.md) | `wait-without-add` | A local WaitGroup is waited on without any Add() in the same lifecycle. |
| [GCL2011](GCL2011.md) | `wait-deadlock` | Wait() is reached while the same goroutine still owes a Done(). |
| [GCL2012](GCL2012.md) | `multiple-done-worker` | The same worker branch can call Done() more than once. |
| [GCL2013](GCL2013.md) | `nested-waitgroup-deadlock` | A worker for one WaitGroup waits on another whose release is blocked behind the outer Wait(). |
| [GCL2014](GCL2014.md) | `done-outside-goroutine` | Done() runs on the parent goroutine instead of the worker, so a panic in the parent skips it. |
| [GCL2015](GCL2015.md) | `go-panic` | A function passed to wg.Go() may panic and bring the program down. |

## sync.Once

| Code | Slug | Description |
|------|------|-------------|
| [GCL3001](GCL3001.md) | `once-do-deadlock` | once.Do(f) where f calls Do on the same Once again — Once.Do is not reentrant, so this deadlocks. |
| [GCL3002](GCL3002.md) | `once-do-nil` | once.Do(nil) panics when the function is invoked. |

## sync.Cond

| Code | Slug | Description |
|------|------|-------------|
| [GCL4001](GCL4001.md) | `cond-wait-not-in-loop` | cond.Wait() is called outside a for loop, so a stale or spurious wakeup resumes without re-checking the condition. |
| [GCL4002](GCL4002.md) | `cond-new-nil-locker` | sync.NewCond(nil) builds a Cond whose Locker is nil, so the first Wait panics at runtime. |

## Cross-cutting

| Code | Slug | Description |
|------|------|-------------|
| [GCL9001](GCL9001.md) | `sync-primitive-copy` | A sync primitive (or a struct embedding one) is copied by value. |

<sub>Generated from the check registry by `scripts/gendocs` — do not edit by hand.</sub>
