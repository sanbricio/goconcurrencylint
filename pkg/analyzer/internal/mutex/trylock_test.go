package mutex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

// These tests exercise tryLockTracker in isolation. That is only possible
// because the tracker was extracted out of Checker with explicit dependencies
// (names + a report.Reporter): we inject a fake reporter and feed it parsed AST
// nodes, with no analysis.Pass and no surrounding Checker.

// fakeReporter captures the diagnostics a collaborator emits so a test can
// assert on them. It satisfies report.Reporter.
type fakeReporter struct {
	calls []fakeReport
}

type fakeReport struct {
	pos token.Pos
	cat category.Category
	msg string
}

func (f *fakeReporter) AddError(pos token.Pos, cat category.Category, message string) {
	f.calls = append(f.calls, fakeReport{pos: pos, cat: cat, msg: message})
}

// parseFile parses src and returns a CommentFilter built from it, mirroring how
// the real analyzer wires the filter to the file under analysis.
func parseFile(t *testing.T, src string) (*ast.File, *commentfilter.CommentFilter) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return file, commentfilter.NewCommentFilter(fset, file)
}

// firstCallAssign returns the first `lhs := <call>(...)` assignment in file.
func firstCallAssign(t *testing.T, file *ast.File) *ast.AssignStmt {
	t.Helper()
	var found *ast.AssignStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) == 0 {
			return true
		}
		if _, ok := assign.Rhs[0].(*ast.CallExpr); ok {
			found = assign
			return false
		}
		return true
	})
	if found == nil {
		t.Fatal("no assignment with a call on the rhs was found")
	}
	return found
}

func newTestTracker(reporter *fakeReporter, cf *commentfilter.CommentFilter, mutexNames, rwMutexNames []string) *tryLockTracker {
	toSet := func(names []string) map[string]bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		return set
	}
	return newTryLockTracker(toSet(mutexNames), toSet(rwMutexNames), cf, reporter)
}

func TestTryLockTracker_CapturedButNeverChecked(t *testing.T) {
	file, cf := parseFile(t, `package p
import "sync"
func f() {
	var mu sync.Mutex
	ok := mu.TryLock()
	_ = ok
}`)
	rep := &fakeReporter{}
	tracker := newTestTracker(rep, cf, []string{"mu"}, nil)

	tracker.recordAssignment(firstCallAssign(t, file))
	// Capturing the boolean defers the verdict; nothing is reported yet.
	if len(rep.calls) != 0 {
		t.Fatalf("expected no diagnostics after recording, got %d", len(rep.calls))
	}

	// At end of function an unchecked, captured result must be flagged.
	tracker.reportUnchecked()
	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.UncheckedTryLock {
		t.Errorf("category = %q, want %q", got.cat, category.UncheckedTryLock)
	}
	if want := "mutex 'mu' TryLock return value not checked, lock may not be held"; got.msg != want {
		t.Errorf("message = %q, want %q", got.msg, want)
	}
}

func TestTryLockTracker_DiscardedResultReportedImmediately(t *testing.T) {
	file, cf := parseFile(t, `package p
import "sync"
func f() {
	var mu sync.Mutex
	_ = mu.TryLock()
}`)
	rep := &fakeReporter{}
	tracker := newTestTracker(rep, cf, []string{"mu"}, nil)

	// Assigning to "_" throws the boolean away: report on the spot.
	tracker.recordAssignment(firstCallAssign(t, file))
	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 immediate diagnostic, got %d", len(rep.calls))
	}
	// reportUnchecked must not double-report (nothing pending was stored).
	tracker.reportUnchecked()
	if len(rep.calls) != 1 {
		t.Fatalf("expected no further diagnostics, got %d", len(rep.calls))
	}
}

func TestTryLockTracker_CheckedInBranchClearsAndHoldsLock(t *testing.T) {
	file, cf := parseFile(t, `package p
import "sync"
func f() {
	var mu sync.Mutex
	ok := mu.TryLock()
	_ = ok
}`)
	rep := &fakeReporter{}
	tracker := newTestTracker(rep, cf, []string{"mu"}, nil)
	tracker.recordAssignment(firstCallAssign(t, file))

	stats := map[string]*Stats{"mu": {}}
	// `if ok { ... }` consumes the result: it is marked checked and the lock is
	// treated as held inside the branch.
	if !tracker.applyToBranch(&ast.Ident{Name: "ok"}, stats) {
		t.Fatal("applyToBranch returned false for a tracked result")
	}
	if stats["mu"].lock != 1 {
		t.Errorf("lock count = %d, want 1", stats["mu"].lock)
	}

	tracker.reportUnchecked()
	if len(rep.calls) != 0 {
		t.Fatalf("checked result must not be reported, got %d diagnostics", len(rep.calls))
	}
}

func TestTryLockTracker_ResultFromCallClassification(t *testing.T) {
	src := `package p
import "sync"
func f() {
	var mu sync.Mutex
	var rw sync.RWMutex
	a := mu.TryLock()
	b := rw.TryRLock()
	c := mu.TryRLock()
	d := other.TryLock()
	_, _, _, _ = a, b, c, d
}`
	tests := []struct {
		name       string
		wantNil    bool
		wantMethod string
		wantRW     bool
	}{
		{name: "mutex TryLock", wantMethod: "TryLock", wantRW: false},
		{name: "rwmutex TryRLock", wantMethod: "TryRLock", wantRW: true},
		{name: "mutex TryRLock is not valid", wantNil: true},
		{name: "unknown receiver", wantNil: true},
	}

	file, cf := parseFile(t, src)
	tracker := newTestTracker(&fakeReporter{}, cf, []string{"mu"}, []string{"rw"})

	// Collect the four call expressions in source order.
	var calls []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			calls = append(calls, call)
		}
		return true
	})
	if len(calls) != len(tests) {
		t.Fatalf("expected %d calls, found %d", len(tests), len(calls))
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tracker.resultFromCall(calls[i])
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil result, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected a result, got nil")
			}
			if got.method != tc.wantMethod {
				t.Errorf("method = %q, want %q", got.method, tc.wantMethod)
			}
			if got.isRWMutex != tc.wantRW {
				t.Errorf("isRWMutex = %v, want %v", got.isRWMutex, tc.wantRW)
			}
		})
	}
}
