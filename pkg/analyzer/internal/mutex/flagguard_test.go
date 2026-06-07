package mutex

import (
	"go/ast"
	"testing"
)

// flagGuardFuncDecl parses src with the typed lifecycle helper and returns the
// top-level function named name, ready to feed to detectFlagGuardedReleaseFlags.
func flagGuardFuncDecl(t *testing.T, src, name string) *ast.FuncDecl {
	t.Helper()
	file, _ := parseTypedLifecycleFile(t, src)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("function %q not found", name)
	return nil
}

// firstStmt parses src and returns the first statement in f's body. It lets the
// single-statement helper tests feed a real ast.Stmt to the predicate.
func firstStmt(t *testing.T, src string) ast.Stmt {
	t.Helper()
	file, _ := parseTypedLifecycleFile(t, src)
	body := funcBody(t, file, "f")
	if len(body.List) == 0 {
		t.Fatal("function body is empty")
	}
	return body.List[0]
}

// TestDetectFlagGuardedReleases drives the whole detector end-to-end: it wires a
// minimal Checker (only the name sets are consulted) and asserts which mutexes
// are recognised as flag-guarded releases.
func TestDetectFlagGuardedReleases(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		mutex   map[string]bool
		rwMutex map[string]bool
		varName string
		want    bool
	}{
		{
			// held := false; defer func(){ if held { mu.Unlock() } }();
			// if c { mu.Lock(); held = true } — the canonical pattern.
			name:    "flag-guarded release qualifies",
			varName: "mu",
			mutex:   map[string]bool{"mu": true},
			want:    true,
			src: `package p
import "sync"
func f(c bool) {
	var mu sync.Mutex
	held := false
	defer func() {
		if held {
			mu.Unlock()
		}
	}()
	if c {
		mu.Lock()
		held = true
	}
}`,
		},
		{
			// The Lock is not followed by held = true, so the deferred release does
			// not cover it: reporting must stay on. This is the false-negative guard.
			name:    "lock without set-flag does not qualify",
			varName: "mu",
			mutex:   map[string]bool{"mu": true},
			want:    false,
			src: `package p
import "sync"
func f(c bool) {
	var mu sync.Mutex
	held := false
	defer func() {
		if held {
			mu.Unlock()
		}
	}()
	if c {
		mu.Lock()
	}
}`,
		},
		{
			// The deferred closure unlocks unconditionally — there is no flag guard,
			// so the acquire cannot borrow a guarded release.
			name:    "unguarded unlock in defer does not qualify",
			varName: "mu",
			mutex:   map[string]bool{"mu": true},
			want:    false,
			src: `package p
import "sync"
func f(c bool) {
	var mu sync.Mutex
	defer func() {
		mu.Unlock()
	}()
	if c {
		mu.Lock()
	}
}`,
		},
		{
			// No deferred closure at all: nothing to pair the acquisition with.
			name:    "no deferred closure does not qualify",
			varName: "mu",
			mutex:   map[string]bool{"mu": true},
			want:    false,
			src: `package p
import "sync"
func f(c bool) {
	var mu sync.Mutex
	if c {
		mu.Lock()
		mu.Unlock()
	}
}`,
		},
		{
			// The rwMutexNames set is consulted as well: an RWMutex driven through
			// the write Lock/Unlock pair qualifies via the same shape.
			name:    "rwmutex write lock qualifies via rwMutexNames",
			varName: "rw",
			rwMutex: map[string]bool{"rw": true},
			want:    true,
			src: `package p
import "sync"
func f(c bool) {
	var rw sync.RWMutex
	held := false
	defer func() {
		if held {
			rw.Unlock()
		}
	}()
	if c {
		rw.Lock()
		held = true
	}
}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fn := flagGuardFuncDecl(t, tc.src, "f")
			c := &Checker{mutexNames: tc.mutex, rwMutexNames: tc.rwMutex}

			got := c.detectFlagGuardedReleaseFlags(fn)
			if (got[tc.varName] != "") != tc.want {
				t.Errorf("detectFlagGuardedReleaseFlags()[%q] present = %v, want %v", tc.varName, got[tc.varName] != "", tc.want)
			}
		})
	}
}

func TestDetectFlagGuardedReleases_NilFunc(t *testing.T) {
	c := &Checker{mutexNames: map[string]bool{"mu": true}}
	if got := c.detectFlagGuardedReleaseFlags(nil); got != nil {
		t.Errorf("detectFlagGuardedReleaseFlags(nil) = %v, want nil", got)
	}
}

// TestDeferredFlagGuardedUnlock isolates the closure scan: it must report the
// guard flag only when every unlock in a deferred closure sits under one
// `if <flag>` guard.
func TestDeferredFlagGuardedUnlock(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantFlag string
		wantOK   bool
	}{
		{
			name:     "guarded unlock returns flag",
			wantFlag: "held",
			wantOK:   true,
			src: `package p
import "sync"
func f() {
	var mu sync.Mutex
	held := false
	defer func() {
		if held {
			mu.Unlock()
		}
	}()
}`,
		},
		{
			name:     "unconditional unlock is not guarded",
			wantFlag: "",
			wantOK:   false,
			src: `package p
import "sync"
func f() {
	var mu sync.Mutex
	defer func() {
		mu.Unlock()
	}()
}`,
		},
		{
			// Two unlocks but only one under the guard: the closure is not fully
			// covered, so it must not qualify.
			name:     "partially guarded unlocks do not qualify",
			wantFlag: "",
			wantOK:   false,
			src: `package p
import "sync"
func f() {
	var mu sync.Mutex
	held := false
	defer func() {
		mu.Unlock()
		if held {
			mu.Unlock()
		}
	}()
}`,
		},
		{
			name:     "no deferred closure",
			wantFlag: "",
			wantOK:   false,
			src: `package p
import "sync"
func f() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, _ := parseTypedLifecycleFile(t, tc.src)
			body := funcBody(t, file, "f")

			flag, ok := deferredFlagGuardedUnlock(body, "mu", "Unlock")
			if flag != tc.wantFlag || ok != tc.wantOK {
				t.Errorf("deferredFlagGuardedUnlock() = (%q, %v), want (%q, %v)", flag, ok, tc.wantFlag, tc.wantOK)
			}
		})
	}
}

// TestEveryLockPairsWithSetFlag checks the acquire-site rule: there must be at
// least one Lock and every Lock must be immediately followed by `flag = true`.
func TestEveryLockPairsWithSetFlag(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "lock immediately sets flag",
			want: true,
			src: `package p
import "sync"
func f(held bool) {
	var mu sync.Mutex
	mu.Lock()
	held = true
}`,
		},
		{
			name: "lock without following flag",
			want: false,
			src: `package p
import "sync"
func f() {
	var mu sync.Mutex
	mu.Lock()
}`,
		},
		{
			// The Lock sits inside an if; visit recurses into nested blocks and
			// still finds the paired flag assignment.
			name: "lock nested in if still pairs",
			want: true,
			src: `package p
import "sync"
func f(c bool, held bool) {
	var mu sync.Mutex
	if c {
		mu.Lock()
		held = true
	}
}`,
		},
		{
			// No Lock at all: foundLock stays false, so the body never qualifies
			// even though a flag is set.
			name: "no lock returns false",
			want: false,
			src: `package p
import "sync"
func f(held bool) {
	var mu sync.Mutex
	mu.Unlock()
	held = true
}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, _ := parseTypedLifecycleFile(t, tc.src)
			body := funcBody(t, file, "f")

			if got := everyLockPairsWithSetFlag(body, "mu", "Lock", "held"); got != tc.want {
				t.Errorf("everyLockPairsWithSetFlag() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAssignTrue(t *testing.T) {
	tests := []struct {
		name string
		src  string
		flag string
		want bool
	}{
		{
			name: "assigns true to flag",
			flag: "held",
			want: true,
			src:  "package p\nfunc f(held bool, other bool) { held = true }",
		},
		{
			name: "assigns false to flag",
			flag: "held",
			want: false,
			src:  "package p\nfunc f(held bool, other bool) { held = false }",
		},
		{
			name: "assigns true to a different name",
			flag: "held",
			want: false,
			src:  "package p\nfunc f(held bool, other bool) { other = true }",
		},
		{
			name: "multi-assignment is rejected",
			flag: "held",
			want: false,
			src:  "package p\nfunc f(held bool, other bool) { held, other = true, false }",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAssignTrue(firstStmt(t, tc.src), tc.flag); got != tc.want {
				t.Errorf("isAssignTrue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsMutexMethodCallStmt(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		mutex  string
		method string
		want   bool
	}{
		{
			name:   "matching receiver and method",
			mutex:  "mu",
			method: "Lock",
			want:   true,
			src:    "package p\nimport \"sync\"\nfunc f(mu *sync.Mutex) { mu.Lock() }",
		},
		{
			name:   "wrong method",
			mutex:  "mu",
			method: "Lock",
			want:   false,
			src:    "package p\nimport \"sync\"\nfunc f(mu *sync.Mutex) { mu.Unlock() }",
		},
		{
			name:   "wrong receiver",
			mutex:  "mu",
			method: "Lock",
			want:   false,
			src:    "package p\nimport \"sync\"\nfunc f(other *sync.Mutex) { other.Lock() }",
		},
		{
			name:   "not a call statement",
			mutex:  "mu",
			method: "Lock",
			want:   false,
			src:    "package p\nfunc f() { x := 1; _ = x }",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMutexMethodCallStmt(firstStmt(t, tc.src), tc.mutex, tc.method); got != tc.want {
				t.Errorf("isMutexMethodCallStmt() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCountMutexMethodCalls confirms the count walks nested blocks (via
// ast.Inspect) and only matches the requested receiver and method.
func TestCountMutexMethodCalls(t *testing.T) {
	src := `package p
import "sync"
func f(c bool) {
	var mu sync.Mutex
	mu.Unlock()
	mu.Unlock()
	if c {
		mu.Unlock()
	}
}`

	tests := []struct {
		name   string
		mutex  string
		method string
		want   int
	}{
		{name: "counts all calls including nested", mutex: "mu", method: "Unlock", want: 3},
		{name: "wrong method counts zero", mutex: "mu", method: "Lock", want: 0},
		{name: "unknown receiver counts zero", mutex: "other", method: "Unlock", want: 0},
	}

	file, _ := parseTypedLifecycleFile(t, src)
	body := funcBody(t, file, "f")

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countMutexMethodCalls(body, tc.mutex, tc.method); got != tc.want {
				t.Errorf("countMutexMethodCalls() = %d, want %d", got, tc.want)
			}
		})
	}
}
