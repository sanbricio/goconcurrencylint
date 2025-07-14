# goconcurrencylint

[![Go Version](https://img.shields.io/badge/Go-1.23+-blue)](https://golang.org/doc/go1.23)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)


## Overview

**goconcurrencylint** is a static analysis linter for Go that detects common mistakes and dangerous patterns in the use of `sync.Mutex`, `sync.RWMutex`, and `sync.WaitGroup`. It helps you prevent concurrency bugs such as forgotten unlocks or missing Done calls in WaitGroups.

- 🚦 **Detects**: locks without unlocks, unlocks without locks, WaitGroup adds without corresponding Done, and more.
- 🛠️ **Based on `go/analysis`**: fully compatible with `golangci-lint` and modern Go tooling.
- ✔️ **Fully tested**: includes comprehensive tests and real-world usage examples.
- ⚡ **Easy to use, fast and reliable**.

## Features

- Detects incorrect usage of `sync.Mutex`, `sync.RWMutex` (lock/unlock, defer, conditional, etc.).
- Detects incorrect usage of `sync.WaitGroup` (Add/Done patterns, goroutines, etc.).
- Analyzes complex patterns: conditionals, loops, and goroutine usage.
- Produces clear, actionable diagnostics with file and line number.


## Features

- Detects incorrect usage of `sync.Mutex`, `sync.RWMutex` (lock/unlock, defer, conditional, etc.).
- Detects incorrect usage of `sync.WaitGroup` (Add/Done patterns, goroutines, etc.).
- Analyzes complex patterns: conditionals, loops, and goroutine usage.
- Produces clear, actionable diagnostics with file and line number.

## Installation

```bash
go install github.com/YOUR_USER/goconcurrencylint/cmd/goconcurrencylint@latest
```

Or use it as part of [golangci-lint](https://golangci-lint.run/) (once officially integrated).

## Quick Start

Analyze your project:

```bash
goconcurrencylint ./...
```

**Example output:**

```
mutex.go:12:2: mutex 'mu' is locked but not unlocked
waitgroup.go:23:3: waitgroup 'wg' has Add without corresponding Done
```

## Usage Examples

### Mutex and RWMutex

#### ✅ Correct Usage

```go
import "sync"

func GoodBasicLockUnlock() {
    var mu sync.Mutex
    mu.Lock()
    mu.Unlock()
}

func GoodDeferUnlock() {
    var mu sync.Mutex
    mu.Lock()
    defer mu.Unlock()
}

func GoodRWDeferRUnlock() {
    var mu sync.RWMutex
    mu.RLock()
    defer mu.RUnlock()
}
```

#### ❌ Incorrect Usage & Diagnostics

```go
import "sync"

func BadLockWithoutUnlock() {
    var mu sync.Mutex
    mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

func BadImbalancedLockUnlock() {
    var mu sync.Mutex
    mu.Lock()
    mu.Lock() // want "mutex 'mu' is locked but not unlocked"
    mu.Unlock()
}

func BadRWImbalancedRLockRUnlock() {
    var mu sync.RWMutex
    mu.RLock()
    mu.RLock() // want "rwmutex 'mu' is rlocked but not runlocked"
    mu.RUnlock()
}
```

**Sample output:**
```
mutex.go:5:6: mutex 'mu' is locked but not unlocked
mutex.go:12:6: mutex 'mu' is locked but not unlocked
mutex.go:25:6: rwmutex 'mu' is rlocked but not runlocked
```

---

### WaitGroup

#### ✅ Correct Usage

```go
import "sync"

func GoodBasicAddDone() {
    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
    }()
    wg.Wait()
}

func GoodFuncAddDone() {
    var wg sync.WaitGroup
    wg.Add(1)
    go doWork(&wg)
    wg.Wait()
}
func doWork(wg *sync.WaitGroup) {
    defer wg.Done()
}
```

#### ❌ Incorrect Usage & Diagnostics

```go
import "sync"

func BadAddWithoutDone() {
    var wg sync.WaitGroup
    wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
    wg.Wait()
}

func BadMultipleAddOneDone() {
    var wg sync.WaitGroup
    wg.Add(2) // want "waitgroup 'wg' has Add without corresponding Done"
    wg.Add(1)
    wg.Done()
    wg.Wait()
}
```

**Sample output:**
```
waitgroup.go:5:6: waitgroup 'wg' has Add without corresponding Done
waitgroup.go:13:6: waitgroup 'wg' has Add without corresponding Done
```

---

## Integration with golangci-lint

Enable `goconcurrencylint` in your `.golangci.yml`:

```yaml
linters:
  enable:
    - goconcurrencylint
```

## Contributing

Contributions are welcome! Please open issues or pull requests for bug reports, feature requests, or improvements.

## License

MIT. See the [LICENSE](LICENSE) file for details.

---

**Author:** Santiago Bricio (sanbriciorojas11@gmail.com)