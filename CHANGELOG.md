## [0.4.1](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.4.1) - 2026-06-07

### 🐛 Bug Fixes

- Add GitHub Actions workflow for releasing binaries

### ❤️ Contributors

- @sanbricio

**Full Changelog**: https://github.com/sanbricio/goconcurrencylint/compare/v0.4.0...v0.4.1
## [0.4.0](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.4.0) - 2026-06-07

### 🚀 Features

- Add integration workflow and reporting script for real-world repos ([#30](https://github.com/sanbricio/goconcurrencylint/issues/30))
- Skip analysis on generated files and add related test cases
- Add UnwrapParenExpr function and corresponding tests for parenthesized expressions ([#36](https://github.com/sanbricio/goconcurrencylint/issues/36))
- Implement goto label snapshot capturing and merging for mutex lock states
- Enhance WaitGroup and Mutex Analyzers with New Cases and Logic
- Handle parentheses in mutex method calls and add test cases ([#47](https://github.com/sanbricio/goconcurrencylint/issues/47))
- *(mutex,waitgroup)* Detect ownership-transfer and one-way lock patterns ([#51](https://github.com/sanbricio/goconcurrencylint/issues/51))

### 🐛 Bug Fixes

- *(mutex)* Handle balanced guarded locks and deferred relocks
- Update integration workflow to use stable Go version and improve error handling
- Adjust timeout settings for integration workflow steps to improve reliability
- *(waitgroup)* Only flag Add(0) for compile-time constants
- *(waitgroup)* Fix two Add/Done balance false positives ([#40](https://github.com/sanbricio/goconcurrencylint/issues/40))
- *(waitgroup)* Don't flag Add after Wait when the Wait early-exits
- *(mutex)* Ignore go method() lifecycle effects on caller state
- *(mutex)* Stop flagging cross-goroutine unlock handoffs ([#46](https://github.com/sanbricio/goconcurrencylint/issues/46))

### 🚜 Refactor

- Simplify with modernize suggestions
- Centralize Stats copying logic and fix package name ([#28](https://github.com/sanbricio/goconcurrencylint/issues/28))
- Introduce Reporter interface and type Category ([#29](https://github.com/sanbricio/goconcurrencylint/issues/29))
- Centralize type matching ([#33](https://github.com/sanbricio/goconcurrencylint/issues/33))
- Consolidate unlock, lock, and runlock checks into a single method ([#35](https://github.com/sanbricio/goconcurrencylint/issues/35))
- Simplify varRootIsFunctionParameter by using strings.Cut for base extraction
- Optimize ReportAll and add filterAndPrepare for diagnostics
- Code structure for improved readability and maintainability ([#44](https://github.com/sanbricio/goconcurrencylint/issues/44))
- Extract funcAnalysis for per-function state management in Checker ([#45](https://github.com/sanbricio/goconcurrencylint/issues/45))
- Mutex and waitgroup analysis with tracking, detection, and lifecycle management ([#48](https://github.com/sanbricio/goconcurrencylint/issues/48))

### 📚 Documentation

- *(changelog)* Update for v0.3.0

### ⚡ Performance

- *(commentfilter)* O(log N) IsInComment, -31% on nats-server ([#31](https://github.com/sanbricio/goconcurrencylint/issues/31))
- *(mutex)* Memoize functionIsCallerManagedReleaseFor ([#32](https://github.com/sanbricio/goconcurrencylint/issues/32))

### 🌟 New Contributors

- @Rafael24595 made their first contribution in [#47](https://github.com/sanbricio/goconcurrencylint/pull/47)
- @alexandear made their first contribution

### ❤️ Contributors

- @sanbricio
- @Rafael24595
- @alexandear

**Full Changelog**: https://github.com/sanbricio/goconcurrencylint/compare/v0.3.0...v0.4.0
## [0.3.0](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.3.0) - 2026-05-10

### 🚀 Features

- Enhance WaitGroup Analyzer with additional checks and functionality ([#9](https://github.com/sanbricio/goconcurrencylint/issues/9))
- *(waitgroup)* Improve and reduce false positives in balance analysis
- Add logo image for goconcurrencylint and update README
- Reduce false positives in mutex and waitgroup analysis
- *(mutex)* Propagate lock state through method calls and detect caller-managed release
- Enhance mutex and waitgroup analysis with copy-by-value detection and validation checks
- Enhance mutex and waitgroup analysis with new checks and test cases
- Enhance mutex and waitgroup analysis with new lifecycle checks
- Enhance mutex analysis with new lifecycle checks and test cases
- Add mutex and waitgroup analysis enhancements
- Add advanced concurrency checks and ignore directive support
- Error reporting to use diagnostic categories

### 🐛 Bug Fixes

- Deleted unnecesary function
- Reduce false positives in mutex and waitgroup analyzers
- Scope done-not-deferred to explicit panic and runtime.Goexit

### 🚜 Refactor

- Waitgroup tests into multiple files for better organization ([#10](https://github.com/sanbricio/goconcurrencylint/issues/10))
- Streamline variable declarations in mutex and waitgroup analyzers
- Reorganize waitgroup checks and add new validation functions

### 📚 Documentation

- *(changelog)* Update for v0.2.1

### ❤️ Contributors

- @sanbricio

**Full Changelog**: https://github.com/sanbricio/goconcurrencylint/compare/v0.2.1...v0.3.0
## [0.2.1](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.2.1) - 2026-04-14

### 🐛 Bug Fixes

- Update build paths in CI and release workflows and add main package for goconcurrencylint

### 📚 Documentation

- *(changelog)* Update for v0.2.0

### ❤️ Contributors

- @sanbricio

**Full Changelog**: https://github.com/sanbricio/goconcurrencylint/compare/v0.2.0...v0.2.1
## [0.2.0](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.2.0) - 2026-04-13

### 🚜 Refactor

- Remove unused golangci-lint plugin files and update CI configuration

### 📚 Documentation

- *(changelog)* Update for v0.1.0

### ❤️ Contributors

- @sanbricio

**Full Changelog**: https://github.com/sanbricio/goconcurrencylint/compare/v0.1.0...v0.2.0
## [0.1.0](https://github.com/sanbricio/goconcurrencylint/releases/tag/v0.1.0) - 2026-04-13

### 🚀 Features

- Implement concurrency linter to detect misuse of sync.Mutex and sync.WaitGroup
- First steps mutex linter
- Add test cases for mutex unlock scenarios and missing locks
- Enhance mutex and rwmutex test cases for correct and incorrect usage
- Added waitgroup linter cases
- Added waitgroup for loop analysis
- Added unreachableDone waitgroup
- Added some new cases in waitgroup
- Implement CommentFilter to ignore linter warnings in commented code
- Add support for short variable declarations in sync primitives detection
- Added more edge cases to improve project coverage
- Added more unit tests
- Add test variables with typer but no typeInfo
- *(waitgroup)* Added new count handler in goroutines strategy
- Added new case ReuseWaitGroup
- *(waitgroup)* Enhance analysis of Done calls and improve error reporting (uncomplete)
- Enhance mutex/sync examples with edge cases
- Enhance synchronization primitive analysis with new functions and tests
- Add CI workflows for testing, linting, and releasing; include plugin structure
- Enhance goconcurrencylint with package-level analysis and WaitGroup.Go support; add golangci-lint plugin configuration
- Add changelog file and automate updates during release process

### 🐛 Bug Fixes

- Deleted unused parameters
- Quit toolchain of go.mod
- *(waitgroup)* Detect WaitGroup methods passed as callbacks

### 🚜 Refactor

- Extract common helpers and move testdata into checker/
- *(checker)* Move Mutex and RWMutex analysis logic to mutex.go
- *(checker)* Extract WaitGroup logic into waitgroup.go
- General refactor to improve readability
- Restructure project
- *(mutex)* General refactor to improve readability
- *(test)* Reorganize testdata with granular subcategories
- *(analyzer)* Remove debug print statements from analyzeDoneCalls and analyzeSwitchStatement
- *(waitgroup)* Remove unused functions and streamline validation logic

### ⚙️ Miscellaneous Tasks

- Create MIT LICENSE
- Added README.md
- Add .gitignore file to exclude build outputs and temporary files
- Added CI pipeline
- Fix CI pipeline
- Update README
- Update go test command
- Update Go version and dependencies to 1.25.0 and latest tools
- Update CI workflow to use latest action versions
- Update Go version in CI workflow and adjust golangci-lint configuration
- Update version in CI workflow and configuration files to v2.11.4
- Update actions/checkout version to v6 in CI and release workflows

### 🌟 New Contributors

- @sanbricio made their first contribution

### ❤️ Contributors

- @sanbricio
