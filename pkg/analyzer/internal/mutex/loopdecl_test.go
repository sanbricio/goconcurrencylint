package mutex

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

func TestLoopMutexDetector_VarDeclMutex(t *testing.T) {
	src := `package p
import "sync"
func f() {
	for i := 0; i < 10; i++ {
		var mu sync.Mutex
		_ = mu
	}
}`
	file, info := parseTypedLifecycleFile(t, src)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if body != nil {
			return false
		}
		if loop, ok := n.(*ast.ForStmt); ok {
			body = loop.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatal("no for loop found")
	}

	rep := &fakeReporter{}
	d := newLoopMutexDetector(rep, info)
	d.check(body)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.MutexInLoop {
		t.Errorf("category = %q, want %q", got.cat, category.MutexInLoop)
	}
	if !strings.Contains(got.msg, "mutex 'mu' declared inside loop") {
		t.Errorf("message = %q, want it to contain \"mutex 'mu' declared inside loop\"", got.msg)
	}
}

func TestLoopMutexDetector_VarDeclRWMutex(t *testing.T) {
	src := `package p
import "sync"
func f() {
	for i := 0; i < 10; i++ {
		var rw sync.RWMutex
		_ = rw
	}
}`
	file, info := parseTypedLifecycleFile(t, src)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if body != nil {
			return false
		}
		if loop, ok := n.(*ast.ForStmt); ok {
			body = loop.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatal("no for loop found")
	}

	rep := &fakeReporter{}
	d := newLoopMutexDetector(rep, info)
	d.check(body)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.MutexInLoop {
		t.Errorf("category = %q, want %q", got.cat, category.MutexInLoop)
	}
	if !strings.Contains(got.msg, "rwmutex 'rw' declared inside loop") {
		t.Errorf("message = %q, want it to contain \"rwmutex 'rw' declared inside loop\"", got.msg)
	}
}

func TestLoopMutexDetector_ShortVarDeclMutex(t *testing.T) {
	src := `package p
import "sync"
func f() {
	for i := 0; i < 10; i++ {
		mu := sync.Mutex{}
		_ = mu
	}
}`
	file, info := parseTypedLifecycleFile(t, src)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if body != nil {
			return false
		}
		if loop, ok := n.(*ast.ForStmt); ok {
			body = loop.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatal("no for loop found")
	}

	rep := &fakeReporter{}
	d := newLoopMutexDetector(rep, info)
	d.check(body)

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.MutexInLoop {
		t.Errorf("category = %q, want %q", got.cat, category.MutexInLoop)
	}
	if !strings.Contains(got.msg, "mutex 'mu' declared inside loop") {
		t.Errorf("message = %q, want it to contain \"mutex 'mu' declared inside loop\"", got.msg)
	}
}

func TestLoopMutexDetector_NonMutexDecl(t *testing.T) {
	src := `package p
func f() {
	for i := 0; i < 10; i++ {
		x := 5
		_ = x
	}
}`
	file, info := parseTypedLifecycleFile(t, src)

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if body != nil {
			return false
		}
		if loop, ok := n.(*ast.ForStmt); ok {
			body = loop.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatal("no for loop found")
	}

	rep := &fakeReporter{}
	d := newLoopMutexDetector(rep, info)
	d.check(body)

	if len(rep.calls) != 0 {
		t.Fatalf("expected 0 diagnostics for non-mutex decl, got %d", len(rep.calls))
	}
}

// TestMutexProtectsJoinedWorkers covers the per-iteration-mutex suppression: a
// mutex declared in a loop is legitimate (not flagged) only when it is captured
// by a goroutine in the same iteration AND that iteration joins (a `.Wait()`),
// so the workers cannot outlive the iteration.
func TestMutexProtectsJoinedWorkers(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "goroutine uses mutex and iteration joins",
			want: true,
			src: `package p
import "sync"
func f(items []int) {
	for range items {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); mu.Lock(); mu.Unlock() }()
		}
		wg.Wait()
	}
}`,
		},
		{
			name: "goroutine uses mutex but no join",
			want: false,
			src: `package p
import "sync"
func f(items []int) {
	for range items {
		var mu sync.Mutex
		go func() { mu.Lock(); mu.Unlock() }()
	}
}`,
		},
		{
			name: "join present but goroutine does not use the mutex",
			want: false,
			src: `package p
import "sync"
func f(items []int) {
	for range items {
		var mu sync.Mutex
		var wg sync.WaitGroup
		_ = mu
		go func() { defer wg.Done() }()
		wg.Wait()
	}
}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, _ := parseFile(t, tc.src)

			var body *ast.BlockStmt
			ast.Inspect(file, func(n ast.Node) bool {
				if body != nil {
					return false
				}
				switch loop := n.(type) {
				case *ast.RangeStmt:
					body = loop.Body
				case *ast.ForStmt:
					body = loop.Body
				}
				return body == nil
			})
			if body == nil {
				t.Fatal("no loop body found")
			}

			if got := mutexProtectsJoinedWorkers(body, "mu"); got != tc.want {
				t.Errorf("mutexProtectsJoinedWorkers() = %v, want %v", got, tc.want)
			}
		})
	}
}
