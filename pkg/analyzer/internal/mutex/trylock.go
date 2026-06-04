package mutex

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// tryLockResult records a single TryLock/TryRLock call whose boolean return
// value decides whether the lock is actually held. The result stays pending
// until the boolean is observed (checked == true); a pending result at the end
// of the function is reported as an unchecked TryLock.
type tryLockResult struct {
	varName   string
	method    string
	pos       token.Pos
	checked   bool
	isRWMutex bool
}

// tryLockTracker owns the TryLock/TryRLock bookkeeping for a single function.
// It was extracted from Checker to give that responsibility a focused home:
// it holds only the per-function pending results plus the package-wide
// configuration it needs to classify and report calls. Keeping it self
// contained makes it unit-testable in isolation (see trylock_test.go) and
// shrinks the Checker God Object.
type tryLockTracker struct {
	// results maps a variable name (the lhs that captured the boolean) to its
	// still-pending TryLock result.
	results map[string]*tryLockResult

	mutexNames    map[string]bool
	rwMutexNames  map[string]bool
	commentFilter *commentfilter.CommentFilter
	reporter      report.Reporter
}

// newTryLockTracker builds a tracker bound to the names and reporting boundary
// of the function under analysis. The pending-results map starts empty and is
// populated as assignments are observed.
func newTryLockTracker(mutexNames, rwMutexNames map[string]bool, cf *commentfilter.CommentFilter, reporter report.Reporter) *tryLockTracker {
	return &tryLockTracker{
		results:       make(map[string]*tryLockResult),
		mutexNames:    mutexNames,
		rwMutexNames:  rwMutexNames,
		commentFilter: cf,
		reporter:      reporter,
	}
}

// recordAssignment inspects the right-hand sides of an assignment for
// TryLock/TryRLock calls. A call whose boolean is captured by a usable
// identifier becomes a pending result keyed by that name; a call whose result
// is discarded (extra rhs value or assignment to "_") is reported immediately
// as unchecked.
func (t *tryLockTracker) recordAssignment(stmt *ast.AssignStmt) {
	for i, rhs := range stmt.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		result := t.resultFromCall(call)
		if result == nil {
			continue
		}
		if i >= len(stmt.Lhs) {
			t.reportUncheckedResult(result)
			continue
		}
		ident, ok := stmt.Lhs[i].(*ast.Ident)
		if !ok || ident.Name == "_" {
			t.reportUncheckedResult(result)
			continue
		}
		t.results[ident.Name] = result
	}
}

// resultFromCall returns a pending tryLockResult when call is a
// TryLock/TryRLock invocation on a known mutex/rwmutex, or nil otherwise.
// Calls inside ignore-comment ranges are skipped.
func (t *tryLockTracker) resultFromCall(call *ast.CallExpr) *tryLockResult {
	if call == nil || t.commentFilter.ShouldSkipCall(call) {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "TryLock":
		if t.mutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryLock", pos: call.Pos()}
		}
		if t.rwMutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryLock", pos: call.Pos(), isRWMutex: true}
		}
	case "TryRLock":
		if t.rwMutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryRLock", pos: call.Pos(), isRWMutex: true}
		}
	}
	return nil
}

// markReturnedChecked marks any pending result that is handed back through a
// return statement as checked: returning the boolean delegates the decision to
// the caller, so it must not be flagged here.
func (t *tryLockTracker) markReturnedChecked(stmt *ast.ReturnStmt) {
	for _, result := range stmt.Results {
		ident, ok := result.(*ast.Ident)
		if !ok {
			continue
		}
		if tryResult := t.results[ident.Name]; tryResult != nil {
			tryResult.checked = true
		}
	}
}

// reportUnchecked emits a diagnostic for every pending result still unchecked
// at the end of the function. Call it once after the body has been analyzed.
func (t *tryLockTracker) reportUnchecked() {
	for _, result := range t.results {
		if result != nil && !result.checked {
			t.reportUncheckedResult(result)
		}
	}
}

func (t *tryLockTracker) reportUncheckedResult(result *tryLockResult) {
	if result == nil {
		return
	}
	mutexType := "mutex"
	if result.isRWMutex {
		mutexType = "rwmutex"
	}
	t.reporter.AddError(result.pos, category.UncheckedTryLock, mutexType+" '"+result.varName+"' "+result.method+" return value not checked, lock may not be held")
}

// applyToBranch handles an `if ok { ... }` whose condition is the boolean
// returned by a tracked TryLock. When cond names such a result it is marked
// checked and, inside the then-branch, the lock is treated as held. It returns
// true when cond matched a tracked result so the caller can stop looking for
// other condition shapes.
func (t *tryLockTracker) applyToBranch(cond ast.Expr, stats map[string]*Stats) bool {
	ident, ok := cond.(*ast.Ident)
	if !ok {
		return false
	}
	result := t.results[ident.Name]
	if result == nil {
		return false
	}

	result.checked = true
	st := stats[result.varName]
	if st == nil {
		return true
	}
	switch result.method {
	case "TryLock":
		st.lock++
		st.lockPos = append(st.lockPos, result.pos)
	case "TryRLock":
		st.rlock++
		st.rlockPos = append(st.rlockPos, result.pos)
	}
	return true
}
