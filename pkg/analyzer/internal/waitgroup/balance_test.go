package waitgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

func newTestBalanceValidator(t *testing.T, src string) (*balanceValidator, *ast.FuncDecl) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "balance_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == "f" {
			fn = candidate
			break
		}
	}
	if fn == nil {
		t.Fatal("function f not found")
	}

	reporter := &fakeWaitGroupReporter{}
	balance := newBalanceValidator(balanceValidatorConfig{
		function:              fn,
		waitGroupNames:        map[string]bool{"wg": true},
		localWaitGroupNames:   map[string]bool{"wg": true},
		commentFilter:         commentfilter.NewCommentFilter(fset, file),
		reporter:              reporter,
		isInGoroutine:         testInGoroutine(fn),
		isNodeInGoroutine:     testNodeInGoroutine(fn),
		callInvokesDone:       testCallInvokesDone,
		goroutineDoneInfo:     testGoroutineDoneInfo,
		isSimpleDeferDone:     testDeferInvokesDone,
		findRelatedAddCall:    testFindRelatedAddCall(fn),
		hasUnreachableDone:    func(*ast.BlockStmt, string) bool { return false },
		waitInEarlyExitBranch: func(token.Pos) bool { return false },
		estimateForIterations: func(*ast.ForStmt) int { return 1 },
		estimateForIterationsKnown: func(*ast.ForStmt) (int, bool) {
			return 0, false
		},
		estimateRangeIterations: func(*ast.RangeStmt) int { return 1 },
		estimateRangeIterationsKnown: func(*ast.RangeStmt) (int, bool) {
			return 0, false
		},
	})
	return balance, fn
}

func balanceReporter(b *balanceValidator) *fakeWaitGroupReporter {
	reporter, ok := b.reporter.(*fakeWaitGroupReporter)
	if !ok {
		panic("test balance reporter has unexpected type")
	}
	return reporter
}

func testInGoroutine(fn *ast.FuncDecl) inGoroutineChecker {
	return func(pos token.Pos) bool {
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			fnLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
			if ok && nodeContainsPos(fnLit.Body, pos) {
				found = true
				return false
			}
			return true
		})
		return found
	}
}

func testNodeInGoroutine(fn *ast.FuncDecl) func(ast.Node) bool {
	return func(target ast.Node) bool {
		if target == nil {
			return false
		}
		return testInGoroutine(fn)(target.Pos())
	}
}

func testFindRelatedAddCall(fn *ast.FuncDecl) func(*ast.GoStmt, string) token.Pos {
	return func(goStmt *ast.GoStmt, wgName string) token.Pos {
		var addPos token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if addPos != token.NoPos {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || call.Pos() > goStmt.Pos() {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
				addPos = call.Pos()
			}
			return true
		})
		return addPos
	}
}

func methodCallPos(fn *ast.FuncDecl, wgName, method string, index int) token.Pos {
	seen := 0
	var pos token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if pos != token.NoPos {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method || common.GetVarName(sel.X) != wgName {
			return true
		}
		if seen == index {
			pos = call.Pos()
			return false
		}
		seen++
		return true
	})
	return pos
}

func TestBalanceValidator_UnmatchedAddReports(t *testing.T) {
	balance, fn := newTestBalanceValidator(t, `package p
func f() {
	wg.Add(1)
}`)
	stats := map[string]*Stats{
		"wg": {
			addCalls: []addCall{{pos: methodCallPos(fn, "wg", "Add", 0), value: 1, known: true}},
			totalAdd: 1,
		},
	}

	balance.checkWaitGroupBalance(stats)

	reporter := balanceReporter(balance)
	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(reporter.calls))
	}
	if reporter.calls[0].cat != category.AddWithoutDone {
		t.Fatalf("category = %q, want %q", reporter.calls[0].cat, category.AddWithoutDone)
	}
}

func TestBalanceValidator_GuaranteedGoroutineDoneBalancesAdd(t *testing.T) {
	balance, fn := newTestBalanceValidator(t, `package p
func f() {
	wg.Add(1)
	go func() {
		wg.Done()
	}()
}`)
	stats := map[string]*Stats{
		"wg": {
			addCalls: []addCall{{pos: methodCallPos(fn, "wg", "Add", 0), value: 1, known: true}},
			totalAdd: 1,
		},
	}

	balance.checkWaitGroupBalance(stats)

	if got := len(balanceReporter(balance).calls); got != 0 {
		t.Fatalf("expected guaranteed goroutine Done to balance Add, got %d diagnostics", got)
	}
}

func TestBalanceValidator_LiteralAddLoopMismatchReports(t *testing.T) {
	balance, fn := newTestBalanceValidator(t, `package p
func f() {
	wg.Add(1)
	for i := 0; i < 3; i++ {
		go func() {
			wg.Done()
		}()
	}
}`)
	balance.estimateForIterations = func(*ast.ForStmt) int { return 3 }
	stats := map[string]*Stats{
		"wg": {
			addCalls: []addCall{{pos: methodCallPos(fn, "wg", "Add", 0), value: 1, known: true}},
			totalAdd: 1,
		},
	}

	balance.checkLiteralAddLoopGoroutineMismatch(stats)

	reporter := balanceReporter(balance)
	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(reporter.calls))
	}
	if reporter.calls[0].cat != category.AddLoopCountMismatch {
		t.Fatalf("category = %q, want %q", reporter.calls[0].cat, category.AddLoopCountMismatch)
	}
}

func TestBalanceValidator_AddAfterEmptyWaitReports(t *testing.T) {
	balance, fn := newTestBalanceValidator(t, `package p
func f() {
	wg.Wait()
	wg.Add(1)
}`)
	stats := map[string]*Stats{
		"wg": {
			addCalls:  []addCall{{pos: methodCallPos(fn, "wg", "Add", 0), value: 1, known: true}},
			waitCalls: []token.Pos{methodCallPos(fn, "wg", "Wait", 0)},
			totalAdd:  1,
		},
	}

	balance.checkAddAfterWait(stats)

	reporter := balanceReporter(balance)
	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(reporter.calls))
	}
	if reporter.calls[0].cat != category.AddAfterWait {
		t.Fatalf("category = %q, want %q", reporter.calls[0].cat, category.AddAfterWait)
	}
}
