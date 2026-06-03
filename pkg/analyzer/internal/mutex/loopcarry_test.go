package mutex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

// These tests exercise loopCarryAnalyzer in isolation: no Checker, no
// analysis.Pass. We parse small Go snippets, extract AST nodes and feed them
// directly to the collaborator.

// newTestLoopCarry builds a loopCarryAnalyzer with the supplied mutex name sets
// and a fresh fakeReporter, returning both for inspection.
func newTestLoopCarry(rep *fakeReporter, cf *commentfilter.CommentFilter, mutexNames, rwMutexNames []string) *loopCarryAnalyzer {
	toSet := func(names []string) map[string]bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		return set
	}
	return newLoopCarryAnalyzer(toSet(mutexNames), toSet(rwMutexNames), cf, rep, &terminationAnalyzer{})
}

// parseFuncDecl parses src (a complete Go source file containing a single
// function) and returns its *ast.FuncDecl and a CommentFilter.
func parseFuncDecl(t *testing.T, src string) (*ast.FuncDecl, *commentfilter.CommentFilter) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok {
		t.Fatal("first declaration is not a FuncDecl")
	}
	return fn, commentfilter.NewCommentFilter(fset, file)
}

// TestReportDeferredUnlocksInLoop_DeferInsideLoop checks that a deferred
// Unlock inside a for-loop body is reported as DeferUnlockInLoop.
func TestReportDeferredUnlocksInLoop_DeferInsideLoop(t *testing.T) {
	src := `package p
import "sync"
func f() {
	var mu sync.Mutex
	for {
		mu.Lock()
		defer mu.Unlock()
	}
}`
	_, cf := parseFile(t, src)
	rep := &fakeReporter{}
	lc := newTestLoopCarry(rep, cf, []string{"mu"}, nil)

	// Build a minimal for-loop body AST by parsing it directly.
	fullSrc := `package p
func f() {
	for {
		mu.Lock()
		defer mu.Unlock()
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", fullSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	forStmt := fn.Body.List[0].(*ast.ForStmt)

	lc.reportDeferredUnlocksInLoop(forStmt.Body)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	if rep.calls[0].cat != category.DeferUnlockInLoop {
		t.Errorf("category = %q, want %q", rep.calls[0].cat, category.DeferUnlockInLoop)
	}
}

// TestLoopMayBreakHoldingMutex_DetectsBreak checks that a loop that acquires a
// lock and then hits a break is detected.
func TestLoopMayBreakHoldingMutex_DetectsBreak(t *testing.T) {
	fullSrc := `package p
func f() {
	for {
		mu.Lock()
		break
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", fullSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	forStmt := fn.Body.List[0].(*ast.ForStmt)

	_, cf := parseFile(t, `package p
func f() {}`)
	rep := &fakeReporter{}
	lc := newTestLoopCarry(rep, cf, []string{"mu"}, nil)

	_, ok := lc.loopMayBreakHoldingMutex(forStmt.Body.List, "mu", WriteLockPattern, 0, token.NoPos)
	if !ok {
		t.Error("expected loopMayBreakHoldingMutex=true for a loop that locks and then breaks")
	}
}

// TestLoopMayCarryMutexPastIteration_Balanced checks that a balanced lock/unlock
// loop is NOT detected as carrying the mutex past an iteration.
func TestLoopMayCarryMutexPastIteration_Balanced(t *testing.T) {
	fullSrc := `package p
func f() {
	for {
		mu.Lock()
		mu.Unlock()
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", fullSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	forStmt := fn.Body.List[0].(*ast.ForStmt)

	_, cf := parseFile(t, `package p
func f() {}`)
	rep := &fakeReporter{}
	lc := newTestLoopCarry(rep, cf, []string{"mu"}, nil)

	carried := lc.loopMayCarryMutexPastIteration(forStmt.Body.List, "mu", WriteLockPattern, 0)
	if carried {
		t.Error("expected loopMayCarryMutexPastIteration=false for a balanced lock/unlock loop")
	}
}

// TestIsCarriedLoopUnlock_NilFunction checks that isCarriedLoopUnlock returns
// false when function is nil.
func TestIsCarriedLoopUnlock_NilFunction(t *testing.T) {
	_, cf := parseFile(t, `package p
func f() {}`)
	rep := &fakeReporter{}
	lc := newTestLoopCarry(rep, cf, []string{"mu"}, nil)

	got := lc.isCarriedLoopUnlock("mu", token.Pos(10), nil, LockPattern{LockMethods: []string{"Lock"}, UnlockMethods: []string{"Unlock"}})
	if got {
		t.Error("expected isCarriedLoopUnlock=false when function is nil")
	}
}

// TestIsCarriedLoopUnlock_ContinueCarries checks that when a loop acquires a
// lock and uses continue without unlocking, the unlock that appears after the
// loop is recognised as a "carried" unlock (the mutex escaped the loop).
func TestIsCarriedLoopUnlock_ContinueCarries(t *testing.T) {
	// The for loop locks and continues (never unlocks inside the loop).
	// The unlock appears after the loop body, simulating the scenario where
	// a caller sees an unlock with lock-count == 0 and calls isCarriedLoopUnlock
	// to decide whether to treat it as a borrowed unlock.
	fullSrc := `package p
func f() {
	for {
		mu.Lock()
		continue
	}
	mu.Unlock()
}`
	fn, cf := parseFuncDecl(t, fullSrc)
	rep := &fakeReporter{}
	lc := newTestLoopCarry(rep, cf, []string{"mu"}, nil)

	// Locate the Unlock call after the for loop to get its position.
	var unlockPos token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		expr, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Unlock" {
			unlockPos = call.Pos()
			return false
		}
		return true
	})
	if unlockPos == token.NoPos {
		t.Fatal("could not locate Unlock call in test source")
	}

	got := lc.isCarriedLoopUnlock("mu", unlockPos, fn, WriteLockPattern)
	// The loop has Lock + continue (no unlock) → it carries the mutex past
	// the iteration boundary, so the outer Unlock is a "carried" unlock.
	if !got {
		t.Error("expected isCarriedLoopUnlock=true when loop locks without unlocking on continue path")
	}
}
