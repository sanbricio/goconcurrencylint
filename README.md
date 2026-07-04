# goconcurrencylint

<p align="center">
  <img src="assets/goconcurrencylint.png" alt="goconcurrencylint logo with a Go-inspired concurrency inspector mascot" width="720">
</p>

<p align="center">
  <a href="https://golang.org/doc/go1.25"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License: MIT"></a>
  <a href="https://pkg.go.dev/github.com/sanbricio/goconcurrencylint"><img src="https://pkg.go.dev/badge/github.com/sanbricio/goconcurrencylint.svg" alt="Go Reference"></a>
  <a href="https://github.com/sanbricio/goconcurrencylint/actions/workflows/ci.yml"><img src="https://github.com/sanbricio/goconcurrencylint/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://goreportcard.com/report/github.com/sanbricio/goconcurrencylint"><img src="https://goreportcard.com/badge/github.com/sanbricio/goconcurrencylint" alt="Go Report Card"></a>
</p>

<p align="center">
  <b>A static analyzer for Go that catches common concurrency mistakes around <code>sync.Mutex</code>, <code>sync.RWMutex</code>, <code>sync.WaitGroup</code>, and <code>sync.Once</code> — before they reach production.</b>
</p>

---

## Table of Contents

- [Why goconcurrencylint?](#why-goconcurrencylint)
- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Checks](#checks)
- [Examples](#examples)
- [How It Works](#how-it-works)
- [Project Layout](#project-layout)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Why goconcurrencylint?

Concurrency bugs in Go are notoriously hard to debug: races, deadlocks, and leaked goroutines often surface only under production load. The standard Go toolchain ships `-race` for data races, but nothing flags **structural misuse** of synchronization primitives at compile time.

`goconcurrencylint` fills that gap with **control-flow-sensitive** static analysis. It walks the AST of every function, tracks lock/unlock and `Add`/`Done` state across `if`, `switch`, `select`, loops, and goroutines, and reports paths where synchronization primitives are used incorrectly — including across files of the same package.

It is built on the standard [`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis) framework, so it drops into any Go tooling pipeline without extra machinery.

## Installation

Install the binary with `go install`:

```bash
go install github.com/sanbricio/goconcurrencylint/cmd/goconcurrencylint@latest
```

This places `goconcurrencylint` in `$GOBIN` (or `$GOPATH/bin`). Make sure that directory is on your `PATH`.

> **Requirements:** Go 1.25 or later.

### Build from source

```bash
git clone https://github.com/sanbricio/goconcurrencylint.git
cd goconcurrencylint
go build -o goconcurrencylint ./cmd/goconcurrencylint
```

## Quick Start

Run the analyzer against your module:

```bash
goconcurrencylint ./...
```

Example diagnostics:

```text
mutex.go:12:2: GCL1001: mutex 'mu' is locked but not unlocked
waitgroup.go:23:3: GCL2001: waitgroup 'wg' has Add without corresponding Done
waitgroup.go:41:2: GCL2004: waitgroup 'wg' Go called after Wait
```

Every diagnostic is prefixed with a stable code (`GCL1001`). Run `goconcurrencylint explain GCL1001` for a full description of any check, or browse the [check catalogue](docs/checks/README.md).

Because the tool is a standard `go/analysis` single-checker, it accepts the usual package patterns (`./...`, `./pkg/...`, individual import paths) and standard analyzer flags.

## Checks

Each check has a stable code (e.g. `GCL1001`) shown in the diagnostic message and carried as the [`analysis.Diagnostic.Category`](https://pkg.go.dev/golang.org/x/tools/go/analysis#Diagnostic), so `golangci-lint` and IDE integrations can filter or label by check. The legacy kebab-case slug is still accepted in ignore directives. Per-check pages live under [`docs/checks/`](docs/checks/README.md), or run `goconcurrencylint explain <code>`.

<!-- BEGIN GENERATED CHECKS TABLE -->
| Code | Slug | Primitive | Description |
|------|------|-----------|-------------|
| [`GCL1001`](docs/checks/GCL1001.md) | `lock-without-unlock` | `sync.Mutex`, `sync.RWMutex` | A Lock()/RLock() call has no matching Unlock()/RUnlock() on some execution path. |
| [`GCL1002`](docs/checks/GCL1002.md) | `unlock-without-lock` | `sync.Mutex`, `sync.RWMutex` | An Unlock()/RUnlock() call is reached without a prior matching lock (including double-unlocks). |
| [`GCL1003`](docs/checks/GCL1003.md) | `defer-unlock-without-lock` | `sync.Mutex`, `sync.RWMutex` | A deferred Unlock()/RUnlock() can run while the mutex is unlocked. |
| [`GCL1004`](docs/checks/GCL1004.md) | `unchecked-trylock` | `sync.Mutex`, `sync.RWMutex` | TryLock()/TryRLock() is called without checking the returned boolean. |
| [`GCL1005`](docs/checks/GCL1005.md) | `defer-lock` | `sync.Mutex`, `sync.RWMutex` | defer mu.Lock()/RLock() is used where an unlock was almost certainly intended. |
| [`GCL1006`](docs/checks/GCL1006.md) | `mutex-in-loop` | `sync.Mutex`, `sync.RWMutex` | A mutex is declared inside a loop body, creating a fresh lock per iteration. |
| [`GCL1007`](docs/checks/GCL1007.md) | `defer-unlock-in-loop` | `sync.Mutex`, `sync.RWMutex` | defer mu.Unlock() lives inside a loop body, so the unlock only runs at function return. |
| [`GCL1008`](docs/checks/GCL1008.md) | `rwmutex-api-mismatch` | `sync.RWMutex` | Unlock() is used for a read lock, or RUnlock() is used for a write lock. |
| [`GCL1009`](docs/checks/GCL1009.md) | `goroutine-lock-deadlock` | `sync.Mutex`, `sync.RWMutex` | A goroutine started while a lock is held tries to take the same lock before the parent releases it. |
| [`GCL1010`](docs/checks/GCL1010.md) | `panic-before-unlock` | `sync.Mutex`, `sync.RWMutex` | A statically-known out-of-range index can panic between Lock() and a non-deferred unlock. |
| [`GCL1011`](docs/checks/GCL1011.md) | `double-lock` | `sync.Mutex`, `sync.RWMutex` | A second Lock() is taken while the first is still held. |
| [`GCL1012`](docs/checks/GCL1012.md) | `lock-order-cycle` | `sync.Mutex`, `sync.RWMutex` | Two functions acquire the same pair of mutexes in opposite orders — a classic deadlock pattern. |
| [`GCL2001`](docs/checks/GCL2001.md) | `add-without-done` | `sync.WaitGroup` | wg.Add(n) has fewer guaranteed Done()s than its count, so the counter can never reach zero. |
| [`GCL2002`](docs/checks/GCL2002.md) | `done-without-add` | `sync.WaitGroup` | wg.Done() is called more times than wg.Add() allows, which panics at runtime. |
| [`GCL2003`](docs/checks/GCL2003.md) | `add-after-wait` | `sync.WaitGroup` | wg.Add() is called after wg.Wait() returned with an empty counter — a classic reuse bug. |
| [`GCL2004`](docs/checks/GCL2004.md) | `go-after-wait` | `sync.WaitGroup` | wg.Go() is called after wg.Wait() returned empty — the Go 1.25 variant of add-after-wait. |
| [`GCL2005`](docs/checks/GCL2005.md) | `add-inside-goroutine` | `sync.WaitGroup` | wg.Add() is called from inside a worker goroutine, racing with Wait(). |
| [`GCL2006`](docs/checks/GCL2006.md) | `done-not-deferred` | `sync.WaitGroup` | A worker calls Done() after an explicit panic or runtime.Goexit path instead of deferring it. |
| [`GCL2007`](docs/checks/GCL2007.md) | `add-loop-count-mismatch` | `sync.WaitGroup` | A literal Add(n) count does not match a statically countable loop of worker goroutines. |
| [`GCL2008`](docs/checks/GCL2008.md) | `add-zero` | `sync.WaitGroup` | wg.Add(0) is a no-op and usually means the intended count was lost. |
| [`GCL2009`](docs/checks/GCL2009.md) | `add-negative` | `sync.WaitGroup` | wg.Add(n) is called with a negative literal, which panics at runtime. |
| [`GCL2010`](docs/checks/GCL2010.md) | `wait-without-add` | `sync.WaitGroup` | A local WaitGroup is waited on without any Add() in the same lifecycle. |
| [`GCL2011`](docs/checks/GCL2011.md) | `wait-deadlock` | `sync.WaitGroup` | Wait() is reached while the same goroutine still owes a Done(). |
| [`GCL2012`](docs/checks/GCL2012.md) | `multiple-done-worker` | `sync.WaitGroup` | The same worker branch can call Done() more than once. |
| [`GCL2013`](docs/checks/GCL2013.md) | `nested-waitgroup-deadlock` | `sync.WaitGroup` | A worker for one WaitGroup waits on another whose release is blocked behind the outer Wait(). |
| [`GCL2014`](docs/checks/GCL2014.md) | `done-outside-goroutine` | `sync.WaitGroup` | Done() runs on the parent goroutine instead of the worker, so a panic in the parent skips it. |
| [`GCL2015`](docs/checks/GCL2015.md) | `go-panic` | `sync.WaitGroup` | A function passed to wg.Go() may panic and bring the program down. |
| [`GCL3001`](docs/checks/GCL3001.md) | `once-do-deadlock` | `sync.Once` | once.Do(f) where f calls Do on the same Once again — Once.Do is not reentrant, so this deadlocks. |
| [`GCL3002`](docs/checks/GCL3002.md) | `once-do-nil` | `sync.Once` | once.Do(nil) panics when the function is invoked. |
| [`GCL4001`](docs/checks/GCL4001.md) | `cond-wait-not-in-loop` | `sync.Cond` | cond.Wait() is called outside a for loop, so a stale or spurious wakeup resumes without re-checking the condition. |
| [`GCL4002`](docs/checks/GCL4002.md) | `cond-new-nil-locker` | `sync.Cond` | sync.NewCond(nil) builds a Cond whose Locker is nil, so the first Wait panics at runtime. |
| [`GCL9001`](docs/checks/GCL9001.md) | `sync-primitive-copy` | `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup`, `sync.Once` | A sync primitive (or a struct embedding one) is copied by value. |
<!-- END GENERATED CHECKS TABLE -->

All checks above also fire on package-scoped primitives declared in any file of the same package — there is no separate code for that case; the diagnostic carries the same category as the in-function variant.

### Suppressing diagnostics

Place `// goconcurrencylint:ignore` on the same line as the offending call. Each id may be a canonical code (`GCL1001`) or the legacy slug (`lock-without-unlock`); the two forms are interchangeable and can be mixed:

```go
wg.Wait() // goconcurrencylint:ignore GCL2010
mu.Lock() // goconcurrencylint:ignore GCL1001, defer-lock
mu.Lock() // goconcurrencylint:ignore  legacy code, see issue #42
```

- A list of one or more check ids (separated by spaces, commas or semicolons) silences only those checks on the line.
- A bare `// goconcurrencylint:ignore`, or a directive followed only by free text, silences every check on the line.
- Tokens after the first one that does not match a known check are treated as a human-readable note, so `// goconcurrencylint:ignore GCL1001 because foo` only silences `GCL1001`.

## Examples

### Correct usage

```go
import "sync"

func GoodMutex() {
    var mu sync.Mutex
    mu.Lock()
    defer mu.Unlock()
    // critical section
}

func GoodWaitGroupGo() {
    var wg sync.WaitGroup
    wg.Go(func() {
        // work
    })
    wg.Wait()
}
```

### Incorrect usage

```go
import "sync"

// Lock without a matching Unlock.
func BadLockWithoutUnlock() {
    var mu sync.Mutex
    mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// Defer scheduled before the lock; an early return can run it while the
// mutex is still unlocked. (An adjacent `defer mu.Unlock(); mu.Lock()` is safe.)
func BadDeferUnlockBeforeLock(cond bool) {
    var mu sync.Mutex
    defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
    if cond {
        return
    }
    mu.Lock()
}

// Add without a matching Done — wg.Wait() will block forever.
func BadAddWithoutDone() {
    var wg sync.WaitGroup
    wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
    wg.Wait()
}

// Reusing a WaitGroup after Wait returned empty.
func BadWaitGroupGoAfterWait() {
    var wg sync.WaitGroup
    wg.Wait()
    wg.Go(func() {}) // want "waitgroup 'wg' Go called after Wait"
}

// Extra Done — panics at runtime.
func BadExtraDone() {
    var wg sync.WaitGroup
    wg.Add(1)
    wg.Done()
    wg.Done() // want "waitgroup 'wg' has Done without corresponding Add"
    wg.Wait()
}
```

More representative cases live under [`pkg/analyzer/testdata/src`](pkg/analyzer/testdata/src/).

## How It Works

`goconcurrencylint` is an umbrella `go/analysis` analyzer composed of four
independent sub-analyzers, wired together through the standard `Requires` graph:

- **Mutex analyzer** — tracks `lock`, `rlock`, `borrowed lock`, and `defer unlock` counters per function, visiting each control-flow node and reconciling state at join points. Final state is validated at function exit.
- **WaitGroup analyzer** — collects every `Add`, `Done`, `Wait`, and `Go` call with its position, builds a reachability map for calls inside goroutines, and validates the balance along every path. Calls that escape the function scope are intentionally excluded to minimize false positives.
- **Once analyzer** — resolves the function passed to `once.Do` (literal, named function, or method value) and reports re-entrant `Do` calls that deadlock, plus `Do(nil)` calls that panic.
- **Copy analyzer** — flags any `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup` or `sync.Once` (or a struct embedding one) copied by value.

Two foundation analyzers run once per package and share their results with the sub-analyzers: one discovers `sync` primitive declarations, the other identifies generated files and builds the comment filters behind `// goconcurrencylint:ignore`. All checks also share helpers for type detection (`IsMutex`, `IsRWMutex`, `IsWaitGroup`, `IsOnce`) and deterministic, deduplicated error reporting.

For a contributor-level map of the analyzer graph and the journey of a single diagnostic, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Project Layout

```
goconcurrencylint/
├── cmd/goconcurrencylint/       # CLI entry point (singlechecker)
├── pkg/analyzer/
│   ├── analyzer.go              # Umbrella analyzer (re-emits sub-analyzer diagnostics)
│   ├── internal/
│   │   ├── driver/              # Shared per-function run skeleton
│   │   ├── primitives/          # Discovers sync primitive names
│   │   ├── filesetup/           # Generated-file detection + comment filters
│   │   ├── mutex/               # Mutex / RWMutex analyzer
│   │   ├── waitgroup/           # WaitGroup analyzer
│   │   ├── copycheck/           # Copy-by-value analyzer
│   │   └── common/              # Shared helpers, check catalogue, reporting
│   └── testdata/src/            # analysistest fixtures
├── docs/checks/                 # Per-check reference pages + index (generated)
├── scripts/gendocs/             # Check-docs generator (go generate ./...)
├── assets/                      # Logo and branding
├── ARCHITECTURE.md              # Internal design & data flow
└── .github/workflows/           # CI, release asset build, and integration pipelines
```

## Contributing

Contributions are welcome. The most useful ones in this phase of the project are:

1. **Reduced false-positive / false-negative cases** — extra `testdata` fixtures are the fastest way to harden the analyzer.
2. **Comparisons against overlapping analyzers** — if another linter already covers part of this ground, we want to know.
3. **New checks** — proposals for additional concurrency primitives are encouraged; open an issue first to discuss scope.

To get started:

```bash
git clone https://github.com/sanbricio/goconcurrencylint.git
cd goconcurrencylint
go test -race ./...
```

Tests use [`analysistest`](https://pkg.go.dev/golang.org/x/tools/go/analysis/analysistest) with `// want "…"` markers on fixture files under `pkg/analyzer/testdata/src`.

## License

`goconcurrencylint` is released under the [MIT License](LICENSE).

---

<p align="center">
  <sub>Built by <a href="https://github.com/sanbricio">Santiago Bricio</a> · <a href="mailto:sanbriciorojas11@gmail.com">sanbriciorojas11@gmail.com</a></sub>
</p>
