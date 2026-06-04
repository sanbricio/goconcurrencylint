package waitgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
)

func newTestIterationEstimator(t *testing.T, src string) (*iterationEstimator, *ast.FuncDecl) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "iteration_test.go", src, parser.ParseComments)
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

	return newIterationEstimator(fn, nil, commentfilter.NewCommentFilter(fset, file)), fn
}

func firstForStmt(t *testing.T, fn *ast.FuncDecl) *ast.ForStmt {
	t.Helper()

	var found *ast.ForStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if stmt, ok := n.(*ast.ForStmt); ok {
			found = stmt
			return false
		}
		return true
	})
	if found == nil {
		t.Fatal("for statement not found")
	}
	return found
}

func firstRangeStmt(t *testing.T, fn *ast.FuncDecl) *ast.RangeStmt {
	t.Helper()

	var found *ast.RangeStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if stmt, ok := n.(*ast.RangeStmt); ok {
			found = stmt
			return false
		}
		return true
	})
	if found == nil {
		t.Fatal("range statement not found")
	}
	return found
}

func TestIterationEstimator_ForLoopKnownIterations(t *testing.T) {
	estimator, fn := newTestIterationEstimator(t, `package p
func f() {
	for i := 1; i <= 3; i++ {
	}
}`)

	got, ok := estimator.estimateForIterationsKnown(firstForStmt(t, fn))
	if !ok {
		t.Fatal("expected known iteration count")
	}
	if got != 3 {
		t.Fatalf("iterations = %d, want 3", got)
	}
}

func TestIterationEstimator_RangeLengthTracksAppend(t *testing.T) {
	estimator, fn := newTestIterationEstimator(t, `package p
func f() {
	items := []int{1, 2}
	items = append(items, 3, 4)
	for range items {
	}
}`)

	got, ok := estimator.estimateRangeIterationsKnown(firstRangeStmt(t, fn))
	if !ok {
		t.Fatal("expected known range iteration count")
	}
	if got != 4 {
		t.Fatalf("iterations = %d, want 4", got)
	}
}

func TestIterationEstimator_RangeLengthCountsKnownLoopAppends(t *testing.T) {
	estimator, fn := newTestIterationEstimator(t, `package p
func f() {
	items := []int{}
	for i := 0; i < 3; i++ {
		items = append(items, i)
	}
	for range items {
	}
}`)

	got, ok := estimator.estimateRangeIterationsKnown(firstRangeStmt(t, fn))
	if !ok {
		t.Fatal("expected known range iteration count")
	}
	if got != 3 {
		t.Fatalf("iterations = %d, want 3", got)
	}
}

func TestIterationEstimator_RangeLengthFromMake(t *testing.T) {
	estimator, fn := newTestIterationEstimator(t, `package p
func f() {
	items := make([]int, 5)
	for range items {
	}
}`)

	got, ok := estimator.estimateRangeIterationsKnown(firstRangeStmt(t, fn))
	if !ok {
		t.Fatal("expected known range iteration count")
	}
	if got != 5 {
		t.Fatalf("iterations = %d, want 5", got)
	}
}
