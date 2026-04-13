# goconcurrencylint

[![Go Version](https://img.shields.io/badge/Go-1.25+-blue)](https://golang.org/doc/go1.25)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

`goconcurrencylint` is a `go/analysis` linter for incorrect use of `sync.Mutex`, `sync.RWMutex`, and `sync.WaitGroup`.

## Scope

It currently detects:

- locks without matching unlocks
- unlocks without prior locks
- invalid `defer Unlock` / `defer Done` usage
- `WaitGroup.Add` without a corresponding `Done`
- `WaitGroup.Done` without a corresponding `Add`
- `Add` and `WaitGroup.Go` after an "empty" `Wait`
- package-level mutexes and waitgroups, even when the declaration lives in a different file of the same package

The goal for an eventual `golangci-lint` contribution is to complement existing analyzers with concurrency-focused control-flow checks across conditionals, goroutines, loops, and package-level state.

## Installation

```bash
go install github.com/sanbricio/goconcurrencylint/cmd/goconcurrencylint@latest
```

## CLI Usage

```bash
goconcurrencylint ./...
```

Example diagnostics:

```text
mutex.go:12:2: mutex 'mu' is locked but not unlocked
waitgroup.go:23:3: waitgroup 'wg' has Add without corresponding Done
waitgroup.go:41:2: waitgroup 'wg' Go called after Wait
```

## golangci-lint Plugin

This repository includes the files needed to exercise the module-plugin flow locally and in CI:

- [`.custom-gcl.yml`](/Users/sanbricio/projects/goconcurrencylint/.custom-gcl.yml)
- [`.golangci.plugin.yml`](/Users/sanbricio/projects/goconcurrencylint/.golangci.plugin.yml)

Build and run the custom binary locally with:

```bash
golangci-lint custom
./custom-gcl run --config=.golangci.plugin.yml ./...
```

The plugin is exposed under the linter name `goconcurrencylint`.

## Example Cases

Correct usage:

```go
import "sync"

func GoodWaitGroupGo() {
	var wg sync.WaitGroup
	wg.Go(func() {
		// work
	})
	wg.Wait()
}
```

Incorrect usage:

```go
import "sync"

func BadLockWithoutUnlock() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

func BadAddWithoutDone() {
	var wg sync.WaitGroup
	wg.Add(1) // want "waitgroup 'wg' has Add without corresponding Done"
	wg.Wait()
}

func BadWaitGroupGoAfterWait() {
	var wg sync.WaitGroup
	wg.Wait()
	wg.Go(func() {}) // want "waitgroup 'wg' Go called after Wait"
}
```

## Quality Gates

- functional analyzer tests via `analysistest`
- regression coverage for mutex, waitgroup, and package-level primitives
- CI coverage for tests, custom `golangci-lint` plugin build, and plugin execution

## Contributing

Issues and pull requests are welcome. For upstreaming into `golangci-lint`, the most useful additions are reduced false-positive / false-negative cases and comparisons against overlapping analyzers.

## License

MIT. See [LICENSE](LICENSE).

**Author:** Santiago Bricio (sanbriciorojas11@gmail.com)