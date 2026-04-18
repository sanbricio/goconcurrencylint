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
  <b>A static analyzer for Go that catches common concurrency mistakes around <code>sync.Mutex</code>, <code>sync.RWMutex</code>, and <code>sync.WaitGroup</code> — before they reach production.</b>
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
mutex.go:12:2: mutex 'mu' is locked but not unlocked
waitgroup.go:23:3: waitgroup 'wg' has Add without corresponding Done
waitgroup.go:41:2: waitgroup 'wg' Go called after Wait
```

Because the tool is a standard `go/analysis` single-checker, it accepts the usual package patterns (`./...`, `./pkg/...`, individual import paths) and standard analyzer flags.

## Checks

| ID | Primitive | Description |
|---|---|---|
| `lock-without-unlock` | `sync.Mutex`, `sync.RWMutex` | A `Lock()` / `RLock()` call has no matching `Unlock()` / `RUnlock()` on some execution path. |
| `unlock-without-lock` | `sync.Mutex`, `sync.RWMutex` | An `Unlock()` / `RUnlock()` call is reached without a prior matching lock (including double-unlocks). |
| `defer-unlock-without-lock` | `sync.Mutex`, `sync.RWMutex` | `defer mu.Unlock()` / `defer mu.RUnlock()` is scheduled before the corresponding lock is acquired. |
| `add-without-done` | `sync.WaitGroup` | `wg.Add(n)` has no matching number of `Done()` calls on all paths — the counter may never reach zero. |
| `done-without-add` | `sync.WaitGroup` | `wg.Done()` is called more times than `wg.Add()` allows, which panics at runtime. |
| `add-after-wait` | `sync.WaitGroup` | `wg.Add()` is called after `wg.Wait()` has returned with an empty counter — a classic reuse bug. |
| `go-after-wait` | `sync.WaitGroup` | `wg.Go()` is called after `wg.Wait()` returned empty — same family as `add-after-wait`, specific to Go 1.25's `Go` method. |
| `package-level-primitive` | all | Any of the above, applied to package-scoped primitives declared in a different file of the same package. |

All checks are enabled by default and emitted as standard `go/analysis` diagnostics.

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

// Defer scheduled before the lock is acquired.
func BadDeferUnlockBeforeLock() {
    var mu sync.Mutex
    defer mu.Unlock() // want "mutex 'mu' has defer unlock but no corresponding lock"
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

`goconcurrencylint` is a thin orchestrator over two focused analyzers:

- **Mutex analyzer** (`pkg/analyzer/mutex`) — tracks `lock`, `rlock`, `borrowed lock`, and `defer unlock` counters per function, visiting each control-flow node and reconciling state at join points. Final state is validated at function exit.
- **WaitGroup analyzer** (`pkg/analyzer/waitgroup`) — collects every `Add`, `Done`, `Wait`, and `Go` call with its position, builds a reachability map for calls inside goroutines, and validates the balance along every path. Calls that escape the function scope are intentionally excluded to minimize false positives.

Both analyzers share helpers for type detection (`IsMutex`, `IsRWMutex`, `IsWaitGroup`), comment-aware filtering, and consistent error reporting.

## Project Layout

```
goconcurrencylint/
├── cmd/goconcurrencylint/   # CLI entry point (singlechecker)
├── pkg/analyzer/
│   ├── analyzer.go          # Top-level orchestrator
│   ├── mutex/               # Mutex / RWMutex analyzer
│   ├── waitgroup/           # WaitGroup analyzer
│   ├── common/              # Shared type detection and reporting
│   └── testdata/src/        # analysistest fixtures
├── assets/                  # Logo and branding
└── .github/workflows/       # CI and release pipelines
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
