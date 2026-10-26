# Architecture

This document is a map for **contributors** (and future-you). It explains how the
pieces fit together and how to follow a single diagnostic from the AST all the way
to the message a user sees. If you ever feel lost in the file count, start here.

For *what* the linter detects and how to *use* it, see the [README](README.md).

---

## The big picture

`goconcurrencylint` is not one monolithic analyzer. It is a small **graph of
`go/analysis` analyzers** wired through the standard `Requires` / `ResultOf`
mechanism. One umbrella analyzer is exported; everything else lives under
`internal/` and is composed behind it.

```
cmd/goconcurrencylint/main.go
        │  singlechecker.Main(analyzer.Analyzer)
        ▼
analyzer.Analyzer  ── umbrella: owns no logic, re-emits child diagnostics
        │ Requires
        ├─ mutex.SubAnalyzer      ─┐
        ├─ waitgroup.SubAnalyzer  ─┤  each returns []analysis.Diagnostic as its Result
        ├─ once.SubAnalyzer       ─┤
        └─ copycheck.Analyzer     ─┘
                                   │  and depends on shared "foundation" analyzers
        foundation (run once per package, shared via pass.ResultOf):
                                   ├─ inspect.Analyzer     AST traversal index (x/tools)
                                   ├─ primitives.Analyzer  primitive variable names
                                   └─ filesetup.Analyzer   generated-file set + comment filters
```

Why this shape:

- **Foundation analyzers run once.** Discovering `sync` primitive names
  (`primitives`) and per-file bookkeeping (`filesetup`) are expensive scans that
  every check would otherwise repeat. Declaring them in `Requires` means
  `go/analysis` runs each one *once per package* and hands the cached `Result` to
  every consumer. That is DRY enforced by the framework.
- **Each check is an independent sub-analyzer.** `mutex`, `waitgroup`, `once`
  and `copycheck` know nothing about each other. Adding the next primitive
  (`errgroup`, `atomic`…) is a new sibling, not a patch to existing code —
  `once` landed exactly this way.
- **Only the umbrella reports.** Sub-analyzers return their diagnostics as a
  `Result` ([]analysis.Diagnostic) instead of calling `pass.Report`. The umbrella
  collects those slices and re-emits them. This keeps the whole graph observable
  to `analysistest`, which targets the umbrella.

### Exact `Requires` graph

| Analyzer | Requires | Result |
|---|---|---|
| `analyzer.Analyzer` (umbrella) | `mutex`, `waitgroup`, `once`, `copycheck` | — (calls `pass.Report`) |
| `mutex.SubAnalyzer` | `inspect`, `primitives`, `filesetup` | `[]analysis.Diagnostic` |
| `waitgroup.SubAnalyzer` | `inspect`, `primitives`, `filesetup` | `[]analysis.Diagnostic` |
| `once.SubAnalyzer` | `inspect`, `primitives`, `filesetup` | `[]analysis.Diagnostic` |
| `copycheck.Analyzer` | `inspect`, `filesetup` | `[]analysis.Diagnostic` |
| `primitives.Analyzer` | — (reads `pass.Pkg.Scope()`) | `*primitives.Result` |
| `filesetup.Analyzer` | — (reads `pass.Files`) | `*filesetup.Result` |

Note `copycheck` does **not** require `primitives`: it works purely off types, so
it never needs the discovered variable names.

---

## The journey of a diagnostic

There are two shapes of flow. Most checks take the **flow-sensitive path** through
the shared driver; `copycheck` takes a simpler **direct path**.

### Flow-sensitive path — `mutex` and `waitgroup`

Tracing `lock-without-unlock` for `mu.Lock()` with no matching `Unlock()`:

1. `go/analysis` runs the foundation analyzers first (topological order):
   `primitives`, `filesetup`, `inspect`.
2. [`mutex/sub_analyzer.go`](pkg/analyzer/internal/mutex/sub_analyzer.go) → its
   `run` is a single call to `driver.Run(pass, Config{Guard, Extend, NewChecker})`.
3. [`driver.Run`](pkg/analyzer/internal/driver/driver.go) pulls the inspector,
   `primitives.Result` and `filesetup.Result`, and creates one `ErrorCollector`
   for the whole pass.
4. It `Preorder`s over every `*ast.FuncDecl`: skips bodiless and generated
   functions, computes the function's visible primitives via
   `primitives.ForFunction`, lets the optional **Extend** hook add the ones the
   sub-analyzer infers from calls (mutex: the lock an acquire-or-fail helper
   such as `e.lockAlive()` hands to its caller), and applies the **Guard**
   (`HasMutexes`). For a relevant function it calls `NewChecker` then
   `Checker.AnalyzeFunction`.
5. [`AnalyzeFunction`](pkg/analyzer/internal/mutex/analyzer.go) wires the
   per-function collaborators, runs the lock-order check, then walks the body.
   Its diagnostics are held back until the function is done: a lock-state
   problem is first re-checked by refined runs (see below) and dropped when
   every path turns out balanced.
6. The walk is the engine:
   [`analyzeStatement`](pkg/analyzer/internal/mutex/walk.go) dispatches by
   statement type; a `mu.Lock()` call updates the per-mutex `Stats` counters in
   [`lockstate.go`](pkg/analyzer/internal/mutex/lockstate.go); branches are merged
   in [`branches.go`](pkg/analyzer/internal/mutex/branches.go).
7. At function exit, `reportUnmatchedLocks`
   ([`report_unmatched.go`](pkg/analyzer/internal/mutex/report_unmatched.go))
   inspects the final `Stats`; an unbalanced lock becomes
   `ec.AddError(pos, <lock-without-unlock category>, message)`.
8. `driver.Run` returns `ec.Diagnostics(pass, files.IgnoreFunc())`
   ([`report.go`](pkg/analyzer/internal/common/report/report.go)): this
   **dedups**, drops anything silenced by an inline `// goconcurrencylint:ignore`
   directive, and **sorts deterministically**. The slice is the sub-analyzer's
   `Result`.
9. The umbrella [`run`](pkg/analyzer/analyzer.go) reads
   `pass.ResultOf[mutex.SubAnalyzer]` and re-emits each diagnostic via
   `pass.Report`.

`waitgroup` and `once` follow the exact same steps 1–4 and 8–9. Only the engine
in step 5–7 differs (see below); `once` is the smallest of the three — a single
walk that resolves each `Do` argument and scans it for re-entrant calls.

#### What counts as a mutex

Before any of this runs, something has to decide that `x.Lock()` is a lock at
all. That is [`common/mutexvalue.go`](pkg/analyzer/internal/common/mutexvalue.go),
and it answers from go/types, never from a name: the value is a `sync.Mutex` /
`sync.RWMutex`, or a type that exposes one unchanged through embedding, or a
type whose own method set carries the `sync.Locker` contract while being built
on a mutex these checks model. The last case is what tracks a house wrapper that
overrides `Lock` to time or order acquisition; the backing requirement is what
keeps out a lock built on a semaphore, whose extra transitions (dropping the
write lock but keeping the read lock) the counters cannot describe.

#### How a lone lock operation is judged

A `Lock()` with no `Unlock()` in the same function is not automatically a leak:
a type may split one lock's lifecycle across two methods on purpose. The
decision is made by [`wrapper.go`](pkg/analyzer/internal/mutex/wrapper.go) and
[`mirror_pair.go`](pkg/analyzer/internal/mutex/mirror_pair.go), and it never
reads a method name — what counts is the shape of the code and what the rest of
the package does with it: the type carries the `sync.Locker` contract, or it
holds the opposite operation in another method and something really uses the two
as a pair, or the mutex is an RWMutex driven as a one-way gate. `mirror_pair.go`
explains each condition and what it is holding back. A pair whose halves lock
and unlock only on one branch is accepted when that branch is decided by an
owner record the halves keep between them — take only if the record does not
name the caller and then record it, give back only if it does and then clear it
([`owner_record.go`](pkg/analyzer/internal/mutex/owner_record.go)).

#### When the walk is not sure: refinement

The walk keeps one lock state per program point, so where two branches leave
different states it can only report the difference — even when a later branch
on the same decision balances it (`held := false; if !ok { mu.Lock(); held =
true }; ...; if held { mu.Unlock() }`). For a function that gets such a report,
[`refine.go`](pkg/analyzer/internal/mutex/refine.go) re-runs the walk knowing
what local booleans and nil checks hold at each point
([`facts.go`](pkg/analyzer/internal/mutex/facts.go)), and splits the run on a
decision the walk could not reconcile: a parameter or a local assigned once
outside any loop, or an `if` that runs at most once. Together the runs cover
every path. A run that follows the branch where a test reports its own
failure (`t.Errorf`) is not held to lock balance: that path only exists once the
test has already failed. The report stands unless every run is clean, so
refinement can only take diagnostics away, never add them. Refined runs also model a few things the
main walk leaves alone on purpose
([`refine_model.go`](pkg/analyzer/internal/mutex/refine_model.go)): a lock moving
from one variable to another, a wrapper on a value that is itself a lock, a loop
whose condition takes the lock, constant arguments of a simulated callee, and a
TryLock on a lock the goroutine already holds, which cannot succeed.

### Direct path — `copycheck`

[`copycheck`](pkg/analyzer/internal/copycheck/copycheck.go) has **no driver, no
Checker, no Stats**. Its `run` does its own `Preorder` over a handful of node
kinds (params, value specs, assignments, call args), and each `report*` helper
calls `ec.AddError` when it sees a `sync` value copied by value. Same
`ec.Diagnostics(...)` → `Result` → umbrella re-emit at the end. It is stateless
per-node detection — the deliberate contrast to the flow-sensitive engine.

---

## Inside a sub-analyzer: Checker + collaborators

> The single most useful thing to know: **`AnalyzeFunction` is your table of
> contents.** It lists, in execution order, every collaborator that participates
> in analyzing one function. Read it first; then read whichever collaborator you
> care about — each is one self-contained file named after it.

**`mutex` is flow-sensitive.** The `Checker` is a legitimate central engine: it
walks the control-flow graph, merging lock state at branch joins and tracking
"terminating tails" (a `return`/`panic` that makes a later unlock unreachable).
Its responsibilities are split across cohesive files:

| Concern | File |
|---|---|
| Walk + dispatch + guarded-lock skipping | `walk.go` |
| Lock/unlock state machine over `Stats` | `lockstate.go` |
| Branch (`if`/`switch`/`select`) merging | `branches.go` |
| `defer` / `return` handling | `defer.go` |
| Validation at function exit | `report_unmatched.go` |

**`waitgroup` is a collect-then-validate pass.** `AnalyzeFunction` calls
`collectStats` (one walk gathering every `Add`/`Done`/`Wait`/`Go` with its
position) and then `validateUsage` (runs the validators). It is *not* a lock
state machine, which is why the two engines were deliberately **not** unified

**Collaborators** come in two flavors:

- *Config-only* — built inline, only reads configuration (e.g.
  `lockOrderDetector`, `loopMutexDetector`). No per-function state.
- *Per-function* — holds mutable state for one function; wired in
  `AnalyzeFunction` **and** in `forkForSimulation` (e.g. `tryLockTracker`,
  `lifecycleResolver`). The simulation fork is how mutex models "what happens if I
  call this method": a sibling `Checker` that shares the immutable config but gets
  its own per-function state.

**Where state lives (mutex).** Two channels, on purpose:
`Stats map[string]*Stats` is threaded *by parameter* through the walk, while
per-function fields (collaborators, counters) are grouped in `funcAnalysis`
([`funcanalysis.go`](pkg/analyzer/internal/mutex/funcanalysis.go)) and embedded in
the `Checker`. Knowing this up front removes most of the "where did this value
come from?" friction.

---

## Package map

| Path | Role |
|---|---|
| [`cmd/goconcurrencylint`](cmd/goconcurrencylint) | CLI entry point (`singlechecker.Main`) |
| [`pkg/analyzer`](pkg/analyzer) | Umbrella analyzer; the only exported one |
| [`internal/driver`](pkg/analyzer/internal/driver) | Shared per-function run skeleton for the two flow-sensitive sub-analyzers |
| [`internal/primitives`](pkg/analyzer/internal/primitives) | Discovers `sync` primitive names (package scope + per function) |
| [`internal/filesetup`](pkg/analyzer/internal/filesetup) | Generated-file detection + per-file comment filters |
| [`internal/mutex`](pkg/analyzer/internal/mutex) | Mutex / RWMutex engine + collaborators |
| [`internal/waitgroup`](pkg/analyzer/internal/waitgroup) | WaitGroup engine + collaborators |
| [`internal/once`](pkg/analyzer/internal/once) | sync.Once checks (re-entrant Do, Do(nil)) |
| [`internal/copycheck`](pkg/analyzer/internal/copycheck) | Copy-by-value detection |
| [`internal/common`](pkg/analyzer/internal/common) | Shared AST/type helpers (`IsMutex`, `GetVarName`…) |
| [`internal/common/category`](pkg/analyzer/internal/common/category) | Check catalogue — single source of truth: code (`GCL1001`), legacy slug, primitive, summary, rationale, bad/good examples |
| [`internal/common/commentfilter`](pkg/analyzer/internal/common/commentfilter) | Inline `// goconcurrencylint:ignore` directives |
| [`internal/common/report`](pkg/analyzer/internal/common/report) | `Reporter` interface + `ErrorCollector` (dedup, sort, filter) |
| [`pkg/analyzer/testdata/src`](pkg/analyzer/testdata/src) | `analysistest` golden fixtures |

> Why one flat package per domain instead of sub-packages: the unexported `Stats`
> type is shared and *mutated* by ~10 files within `mutex`. Splitting into
> sub-packages would force exporting those internals. In Go the unit of
> encapsulation is the package, and a flat package with many cohesive files is
> idiomatic (`go/types`, the compiler do the same).

---

## How to navigate (the "I'm lost" guide)

1. **Start at [`pkg/analyzer/analyzer.go`](pkg/analyzer/analyzer.go)** — the whole
   graph and the re-emit loop in ~40 lines.
2. **Pick a domain**, open its `sub_analyzer.go` — the `Guard` and `NewChecker`
   tell you what it cares about, in ~3 lines.
3. **Open that domain's `AnalyzeFunction`** — your ordered index of collaborators.
4. **Each collaborator is one file** named after it; read it in isolation.
5. **To find where a check is emitted:** grep for `AddError`. The `category`
   passed is a constant from `internal/common/category` whose name mirrors the
   slug (`LockWithoutUnlock`) but whose value is the canonical code (`GCL1001`).
   The umbrella prefixes each message with that code (`GCL1001: …`) on re-emit,
   so CLI output, the catalogue and the call site all line up.
6. **To add a new check:** add a constant and a `registry` entry (code, slug,
   primitive, summary, why, bad/good examples) in `internal/common/category`, emit it via
   `ec.AddError` from the relevant engine/collaborator, add a fixture under
   `pkg/analyzer/testdata/src/...` with a `// want "…"` marker, and run
   `go generate ./...` to refresh `docs/checks` and the README table.

## What pins behavior

The safety net is [`analysistest`](https://pkg.go.dev/golang.org/x/tools/go/analysis/analysistest):
fixtures under `pkg/analyzer/testdata/src` carry `// want "…"` markers that assert
the exact diagnostics. The set is **golden** — a diff there means behavior
changed, not that the expectation should be updated. Run it with:

```bash
go test ./... -count=1
```
