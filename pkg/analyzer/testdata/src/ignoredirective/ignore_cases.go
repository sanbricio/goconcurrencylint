// Package ignoredirective verifies that the inline
// goconcurrencylint:ignore directive suppresses diagnostics with per-category
// granularity.
//
// The fixtures rely on analysistest.Run: a `want` marker on a line asserts
// that the matching diagnostic is reported there, and the absence of a marker
// asserts that no diagnostic is reported. Lines that would normally trigger
// a check but are silenced by the directive must therefore have no marker.
//
// When a single line needs both a directive and a `want` marker (because two
// diagnostics fire and only one is silenced), the directive is written as a
// block comment so it does not collide with analysistest's parser.
package ignoredirective

import (
	"sync"
)

// BareDirectiveSilencesEverything: a bare directive (no rules listed)
// silences every diagnostic on its line. The Lock would otherwise fire
// `lock-without-unlock`.
func BareDirectiveSilencesEverything() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore
}

// PerRuleDirectiveSilencesMatchingRule: the explicit category matches the
// fired diagnostic, so it is silenced.
func PerRuleDirectiveSilencesMatchingRule() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore lock-without-unlock
}

// CodeDirectiveSilencesMatchingCheck: the canonical code form silences the
// matching diagnostic, exactly like the legacy slug form does.
func CodeDirectiveSilencesMatchingCheck() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore GCL1001
}

// CodeAndSlugCanMix: a directive list may mix canonical codes and legacy
// slugs. The second Lock() fires both lock-without-unlock (GCL1001) and
// double-lock (GCL1011); naming one by code and the other by slug silences
// both, while the first Lock() still reports.
func CodeAndSlugCanMix() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock() // goconcurrencylint:ignore GCL1001, double-lock
}

// PerRuleDirectiveWrongRuleStillReports: the directive lists only
// wait-without-add but the diagnostic is lock-without-unlock, so the
// diagnostic must still appear.
func PerRuleDirectiveWrongRuleStillReports() {
	var mu sync.Mutex
	mu.Lock() /* goconcurrencylint:ignore wait-without-add */ // want "mutex 'mu' is locked but not unlocked"
}

// MultipleRulesDirectiveSilencesAllListed: comma-separated list silences
// each named category. Both the actual fired check and an unrelated one
// are listed, so nothing is reported.
func MultipleRulesDirectiveSilencesAllListed() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore lock-without-unlock, defer-lock
}

// MultipleDiagnosticsSameLinePerRule: two diagnostics fire on the same
// `mu.Lock()` (`lock-without-unlock` and `double-lock`); the directive only
// names the first, so the second must still be reported.
func MultipleDiagnosticsSameLinePerRule() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock() /* goconcurrencylint:ignore lock-without-unlock */ // want "mutex 'mu' is re-locked before unlock"
}

// MultipleDiagnosticsSameLineAllListed: same setup as above, but the
// directive lists every category that fires, so neither is reported.
func MultipleDiagnosticsSameLineAllListed() {
	var mu sync.Mutex
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
	mu.Lock() // goconcurrencylint:ignore lock-without-unlock, double-lock
}

// HumanNoteAfterDirectiveBehavesAsBare: no recognised category among the
// trailing tokens, so the directive silences every check on the line.
func HumanNoteAfterDirectiveBehavesAsBare() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore  legacy code, see issue #42
}

// UnknownRuleBehavesAsBare: a single unrecognised id (typo) currently
// silences every check on the line. This pins the behaviour so any future
// change is deliberate.
func UnknownRuleBehavesAsBare() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore loock-without-unlock
}

// DirectiveOnAdjacentLineDoesNotLeak: directives only affect their own
// line; a previous-line directive must not silence a diagnostic below.
func DirectiveOnAdjacentLineDoesNotLeak() {
	var mu sync.Mutex
	// goconcurrencylint:ignore lock-without-unlock
	mu.Lock() // want "mutex 'mu' is locked but not unlocked"
}

// WaitGroupBareDirective: the original public example from the README still
// works under the new semantics.
func WaitGroupBareDirective() {
	var wg sync.WaitGroup
	wg.Wait() // goconcurrencylint:ignore wait-without-add
}

// PreservedStateAfterIgnoredLock: bare directives no longer prevent state
// tracking. A subsequent Unlock() must therefore be balanced and silent;
// before this change the ignored Lock left the counter at zero and the
// Unlock would have raised unlock-without-lock.
func PreservedStateAfterIgnoredLock() {
	var mu sync.Mutex
	mu.Lock() // goconcurrencylint:ignore
	mu.Unlock()
}
