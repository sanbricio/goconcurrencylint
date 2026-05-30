package mutex

import (
	"go/ast"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// funcBody returns the body of the top-level function named name. It lets the
// lock-order tests feed a real *ast.BlockStmt to the detector.
func funcBody(t *testing.T, file *ast.File, name string) *ast.BlockStmt {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn.Body
		}
	}
	t.Fatalf("function %q not found", name)
	return nil
}

// newTestLockOrderDetector builds a detector with no type info: the cycle tests
// use plain Lock/Unlock, which never consult typesInfo (only WaitGroup
// Wait/Done detection does).
func newTestLockOrderDetector(t *testing.T, src string, mutexNames []string) (*lockOrderDetector, *ast.File, *fakeReporter) {
	t.Helper()
	file, cf := parseFile(t, src)
	names := make(map[string]bool, len(mutexNames))
	for _, n := range mutexNames {
		names[n] = true
	}
	rep := &fakeReporter{}
	d := newLockOrderDetector(names, map[string]bool{}, cf, nil, rep)
	return d, file, rep
}

func TestLockOrderDetector_ReportsABBACycle(t *testing.T) {
	// One branch locks a-then-b, the other b-then-a: the AB-BA deadlock shape.
	src := `package p
import "sync"
func f(cond bool) {
	var a, b sync.Mutex
	if cond {
		a.Lock()
		b.Lock()
		b.Unlock()
		a.Unlock()
	} else {
		b.Lock()
		a.Lock()
		a.Unlock()
		b.Unlock()
	}
}`
	d, file, rep := newTestLockOrderDetector(t, src, []string{"a", "b"})

	d.check(funcBody(t, file, "f"))

	if len(rep.calls) != 1 {
		t.Fatalf("expected exactly 1 cycle diagnostic, got %d: %+v", len(rep.calls), rep.calls)
	}
	got := rep.calls[0]
	if got.cat != category.LockOrderCycle {
		t.Errorf("category = %q, want %q", got.cat, category.LockOrderCycle)
	}
	if want := "mutex lock order cycle between 'a' and 'b'"; got.msg != want {
		t.Errorf("message = %q, want %q", got.msg, want)
	}
}

func TestLockOrderDetector_ConsistentOrderIsClean(t *testing.T) {
	// Both branches lock a-then-b: no reverse edge, so no cycle.
	src := `package p
import "sync"
func f(cond bool) {
	var a, b sync.Mutex
	if cond {
		a.Lock()
		b.Lock()
		b.Unlock()
		a.Unlock()
	} else {
		a.Lock()
		b.Lock()
		b.Unlock()
		a.Unlock()
	}
}`
	d, file, rep := newTestLockOrderDetector(t, src, []string{"a", "b"})

	d.check(funcBody(t, file, "f"))

	if len(rep.calls) != 0 {
		t.Fatalf("expected no diagnostics for consistent lock order, got %d: %+v", len(rep.calls), rep.calls)
	}
}

func TestLockOrderDetector_ReportsCycleOnce(t *testing.T) {
	// Even with the conflicting pair repeated, the cycle is reported a single
	// time (dedup via the normalized edge key).
	src := `package p
import "sync"
func f(cond bool) {
	var a, b sync.Mutex
	if cond {
		a.Lock()
		b.Lock()
		b.Unlock()
		a.Unlock()
	} else {
		b.Lock()
		a.Lock()
		a.Unlock()
		b.Unlock()
	}
	a.Lock()
	b.Lock()
	b.Unlock()
	a.Unlock()
}`
	d, file, rep := newTestLockOrderDetector(t, src, []string{"a", "b"})

	d.check(funcBody(t, file, "f"))

	if len(rep.calls) != 1 {
		t.Fatalf("expected the cycle to be reported once, got %d: %+v", len(rep.calls), rep.calls)
	}
}
