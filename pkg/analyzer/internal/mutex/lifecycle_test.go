package mutex

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func buildLifecycleResolver(t *testing.T, src, receiverType, methodName string) *lifecycleResolver {
	t.Helper()
	file, info := parseTypedLifecycleFile(t, src)
	rm := buildReceiverMethodMap([]*ast.File{file})
	fn := rm[receiverType][methodName]
	if fn == nil {
		t.Fatalf("method %s.%s not found in receiver map", receiverType, methodName)
	}
	return newLifecycleResolver(rm, collectFunctionDecls([]*ast.File{file}), info, nil, nil, fn)
}

func parseTypedLifecycleFile(t *testing.T, src string) (*ast.File, *types.Info) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "lifecycle_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
		Defs:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("p", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	return file, info
}

func TestLifecycleResolver_ReturnsKeyedHandle(t *testing.T) {
	src := `package p
import "sync"
type store struct{ mu sync.Mutex }
type token struct{ owner *store }
func (s *store) Acquire() *token {
	s.mu.Lock()
	token := &token{owner: s}
	return token
}
func (t *token) Close() {
	t.owner.mu.Unlock()
}`

	l := buildLifecycleResolver(t, src, "store", "Acquire")
	if !l.returnsHandleFor("s.mu", []string{"Unlock"}) {
		t.Fatal("expected returned token Close method to own the eventual Unlock")
	}
}

func TestLifecycleResolver_ReturnsUnkeyedEmbeddedHandle(t *testing.T) {
	src := `package p
import "sync"
type store struct{ mu sync.Mutex }
type token struct{ *store }
func (s *store) Acquire() *token {
	s.mu.Lock()
	return &token{s}
}
func (t *token) Release() {
	t.mu.Unlock()
}`

	l := buildLifecycleResolver(t, src, "store", "Acquire")
	if !l.returnsHandleFor("s.mu", []string{"Unlock"}) {
		t.Fatal("expected embedded positional token Release method to own the eventual Unlock")
	}
}

func TestLifecycleResolver_ReleaseMethodMatchesReturnedHandle(t *testing.T) {
	src := `package p
import "sync"
type store struct{ mu sync.Mutex }
type token struct{ owner *store }
func (s *store) Acquire() *token {
	s.mu.Lock()
	return &token{owner: s}
}
func (t *token) Close() {
	t.owner.mu.Unlock()
}`

	l := buildLifecycleResolver(t, src, "token", "Close")
	if !l.isReleaseFor("t.owner.mu", []string{"Lock", "TryLock"}) {
		t.Fatal("expected Close to be recognized as the lifecycle release for the returned handle")
	}
}

func TestLifecycleResolver_CallerManagedRelease(t *testing.T) {
	src := `package p
import "sync"
type managed struct{ mu sync.Mutex }
func (m *managed) releaseHelper() {
	m.mu.Unlock()
}
func (m *managed) Good() {
	m.mu.Lock()
	m.releaseHelper()
}`

	l := buildLifecycleResolver(t, src, "managed", "releaseHelper")
	if !l.isCallerManagedReleaseFor("m.mu", []string{"Lock", "TryLock"}) {
		t.Fatal("expected releaseHelper to be caller-managed when every call site locks first")
	}
}

func TestLifecycleResolver_CallerManagedReleaseRequiresEveryCallSite(t *testing.T) {
	src := `package p
import "sync"
type managed struct{ mu sync.Mutex }
func (m *managed) releaseHelper() {
	m.mu.Unlock()
}
func (m *managed) Good() {
	m.mu.Lock()
	m.releaseHelper()
}
func (m *managed) Bad() {
	m.releaseHelper()
}`

	l := buildLifecycleResolver(t, src, "managed", "releaseHelper")
	if l.isCallerManagedReleaseFor("m.mu", []string{"Lock", "TryLock"}) {
		t.Fatal("caller-managed release must be false when any call site skips the matching lock")
	}
}
