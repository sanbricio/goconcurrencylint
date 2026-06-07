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
