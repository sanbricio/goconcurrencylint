package waitgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

type fakeWaitGroupReporter struct {
	calls []fakeWaitGroupReport
}

type fakeWaitGroupReport struct {
	pos token.Pos
	cat category.Category
	msg string
}

func (f *fakeWaitGroupReporter) AddError(pos token.Pos, cat category.Category, message string) {
	f.calls = append(f.calls, fakeWaitGroupReport{pos: pos, cat: cat, msg: message})
}

func parseWaitGroupFunc(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "goroutine_inspector_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "f" {
			return fn
		}
	}
	t.Fatal("function f not found")
	return nil
}

func testDeferInvokesDone(deferStmt *ast.DeferStmt, wgName string) bool {
	if deferStmt == nil || deferStmt.Call == nil {
		return false
	}
	sel, ok := deferStmt.Call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Done" && common.GetVarName(sel.X) == wgName
}

func newTestGoroutineInspector(rep *fakeWaitGroupReporter) *goroutineInspector {
	return newGoroutineInspector(map[string]bool{"wg": true}, nil, rep, testDeferInvokesDone, nil, nil, testIsBuiltinPanic)
}

func testIsBuiltinPanic(ident *ast.Ident) bool {
	return ident != nil && ident.Name == "panic"
}

func TestGoroutineInspector_WaitBeforeDoneReportsDeadlock(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	go func() {
		wg.Wait()
		wg.Done()
	}()
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkWaitAndDoneInSameGoroutine(fn)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.WaitDeadlock {
		t.Fatalf("category = %q, want %q", got.cat, category.WaitDeadlock)
	}
	if want := "waitgroup 'wg' Wait will deadlock: same goroutine has pending Done"; got.msg != want {
		t.Fatalf("message = %q, want %q", got.msg, want)
	}
}

func TestGoroutineInspector_AddInsideGoroutineWithMainWaitReportsRace(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	go func() {
		wg.Add(1)
		wg.Done()
	}()
	wg.Wait()
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkAddInsideGoroutine(fn)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.AddInsideGoroutine {
		t.Fatalf("category = %q, want %q", got.cat, category.AddInsideGoroutine)
	}
	if want := "waitgroup 'wg' Add called inside goroutine, may race with Wait"; got.msg != want {
		t.Fatalf("message = %q, want %q", got.msg, want)
	}
}

func TestGoroutineInspector_AddInsideGoroutineWithoutMainWaitIsClean(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	go func() {
		wg.Add(1)
		wg.Done()
	}()
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkAddInsideGoroutine(fn)

	if len(rep.calls) != 0 {
		t.Fatalf("expected no diagnostics without a main-flow Wait, got %d", len(rep.calls))
	}
}

func TestGoroutineInspector_WaitGroupGoPanicReports(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	wg.Go(func() {
		panic("boom")
	})
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkWaitGroupGoPanic(fn)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.GoPanic {
		t.Fatalf("category = %q, want %q", got.cat, category.GoPanic)
	}
	if want := "waitgroup 'wg' Go function may panic"; got.msg != want {
		t.Fatalf("message = %q, want %q", got.msg, want)
	}
}

func TestGoroutineInspector_WaitGroupGoRecoveredPanicIsClean(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	wg.Go(func() {
		defer func() {
			_ = recover()
		}()
		panic("boom")
	})
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkWaitGroupGoPanic(fn)

	if len(rep.calls) != 0 {
		t.Fatalf("expected recovered panic to be clean, got %d diagnostics", len(rep.calls))
	}
}

func TestGoroutineInspector_DeferredDoneReportsDeadlock(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	go func() {
		defer wg.Done()
		wg.Wait()
	}()
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkWaitAndDoneInSameGoroutine(fn)

	if len(rep.calls) != 1 {
		t.Fatalf("expected deferred Done to make Wait depend on same goroutine, got %d diagnostics", len(rep.calls))
	}
}

func TestGoroutineInspector_DoneBeforeWaitIsClean(t *testing.T) {
	fn := parseWaitGroupFunc(t, `package p
func f() {
	go func() {
		wg.Done()
		wg.Wait()
	}()
}`)
	rep := &fakeWaitGroupReporter{}
	newTestGoroutineInspector(rep).checkWaitAndDoneInSameGoroutine(fn)

	if len(rep.calls) != 0 {
		t.Fatalf("expected no diagnostics when Done precedes Wait, got %d", len(rep.calls))
	}
}
