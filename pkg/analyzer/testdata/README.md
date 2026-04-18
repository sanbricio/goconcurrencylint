# Analyzer Testdata Guide

This directory contains the `analysistest` fixtures used by the main analyzer tests in [pkg/analyzer/analyzer_integration_test.go](/Users/sanbricio/projects/goconcurrencylint/pkg/analyzer/analyzer_integration_test.go).

The goal of this layout is to keep the suite easy to grow as the linter gains new checks, regression cases, and contributor-driven bug fixes.

## Directory structure

All analyzer fixtures live under `pkg/analyzer/testdata/src`.

- `src/mutex/`
  Contains fixtures for `sync.Mutex` and `sync.RWMutex` behavior.
- `src/waitgroup/`
  Contains fixtures for `sync.WaitGroup` behavior.
- `src/packagelevel/`
  Contains cross-file regression cases for package-level primitives.

Each folder is a single Go package from the point of view of `analysistest`.
That means multiple `.go` files inside the same folder are compiled together as one fixture package.

## Why the fixtures are split across multiple files

Historically, the fixture packages were growing into very large single files.
That made a few common open source tasks harder than they needed to be:

- finding an existing scenario before adding a new one
- understanding whether a failure belongs to mutex, waitgroup, callback, or package-level behavior
- reviewing pull requests that add only one small regression
- spotting duplicate coverage

Splitting by behavior keeps the package model that `analysistest` expects, while making the cases easier to scan and maintain.

## Current file conventions

### `src/mutex/`

- `mutex_basic_cases.go`
  Core `sync.Mutex` cases such as lock/unlock balance, `defer`, and conditionals.
- `mutex_goroutine_cases.go`
  Cases where mutex state is affected from goroutines or helper flows.
- `rwmutex_cases.go`
  `sync.RWMutex`-specific coverage.
- `mutex_extended_cases.go`
  Broader regressions such as control-flow edge cases, parameters, struct fields, and package-level mutex usage.

### `src/waitgroup/`

- `waitgroup_basic_cases.go`
  Core `Add`, `Done`, `Wait`, and `WaitGroup.Go` scenarios.
- `waitgroup_goroutine_cases.go`
  Loop, goroutine, panic-recovery, and conditional `Done` coverage.
- `waitgroup_callbacks_cases.go`
  Cases where `Done` escapes through callbacks, helper functions, or indirect execution.
- `waitgroup_extended_cases.go`
  Reuse patterns, struct fields, package-level regressions, and broader control-flow cases.
- `helpers.go`
  Shared helper functions and supporting types used by multiple waitgroup fixtures.

### `src/packagelevel/`

This package intentionally uses multiple files because the behavior under test depends on declarations and usage being split across files in the same package.

## How to add a new fixture

When adding a new scenario:

1. Find the nearest existing behavioral file.
2. Add the new case there if it fits the file's current theme.
3. Create a new `*_cases.go` file only when the scenario introduces a new cluster of behavior or the existing file is getting too mixed.

As a rule of thumb, contributors should optimize for discoverability over file count.
Having a few small, well-named fixture files is better than letting one file become the dumping ground for every regression.

## Naming recommendations

Use function names that communicate intent quickly.

- Prefer `Good...` for accepted patterns.
- Prefer `Bad...` for patterns that should produce diagnostics.
- Prefer `EdgeCase...` for cases that are valid but subtle, or regressions that protect tricky control flow.

Examples:

- `GoodWaitGroupMethodPassed`
- `BadConditionalMissingUnlock`
- `EdgeCaseComplexButValid`

## Writing fixture code

Please keep fixture functions focused and readable.

- Keep each function centered on one behavior whenever possible.
- Put the `// want "..."` comment on the exact line that should report a diagnostic.
- Keep related good/bad variants near each other when that improves readability.
- Avoid reusing too much shared logic unless it represents the behavior being tested.
- Prefer realistic but compact examples over overly abstract ones.

The fixtures are not production code. Their main job is to make analyzer intent obvious to future contributors.

## When to create helpers

Helpers are useful when several fixture cases need the same supporting behavior, especially for callback-driven waitgroup scenarios.

Use `helpers.go` only for genuinely shared support code.
If a helper is only needed by one case, keeping it next to that case is usually easier to read.

## Cross-file regressions

If the bug or regression depends on symbols declared in one file and used in another, prefer a dedicated fixture package like `src/packagelevel/` or a clearly named multi-file layout inside the relevant package.

This is especially important for:

- package-level primitives
- receiver methods split from call sites
- helper flows that rely on package scope

## Review checklist for contributors

Before opening a pull request with new analyzer fixtures, check the following:

- The new case lives in the most appropriate fixture package.
- The file name matches the behavior being exercised.
- The case name clearly explains the scenario.
- Expected diagnostics use precise `// want "..."` comments.
- The fixture is small enough to understand without jumping through unrelated code.
- A new file was introduced only because it improves organization, not by accident.