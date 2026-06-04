package mutex

import (
	"go/ast"
	"testing"
)

// buildResolver parses src, indexes its receiver methods the same way the real
// analyzer does (buildReceiverMethodMap), and returns a resolver bound to the
// method named methodName on type receiverType.
func buildResolver(t *testing.T, src, receiverType, methodName string, rawBodyEffects bool) *wrapperResolver {
	t.Helper()
	file, _ := parseFile(t, src)
	rm := buildReceiverMethodMap([]*ast.File{file})
	fn := rm[receiverType][methodName]
	if fn == nil {
		t.Fatalf("method %s.%s not found in receiver map", receiverType, methodName)
	}
	return newWrapperResolver(rm, fn, rawBodyEffects)
}

func TestWrapperResolver_RecognizesWrapperPair(t *testing.T) {
	// s.mu.Lock() inside Lock() is balanced by s.mu.Unlock() in the sibling
	// Unlock(): it is a borrowed wrapper, not an unmatched lock.
	src := `package p
import "sync"
type S struct{ mu sync.Mutex }
func (s *S) Lock()   { s.mu.Lock() }
func (s *S) Unlock() { s.mu.Unlock() }`

	w := buildResolver(t, src, "S", "Lock", false)
	if !w.resolve("s.mu", "Lock") {
		t.Error("expected s.mu.Lock() to be recognized as a borrowed wrapper")
	}
}

func TestWrapperResolver_NoSiblingIsNotAWrapper(t *testing.T) {
	// Lock() with no balancing sibling: the lock really is unmatched.
	src := `package p
import "sync"
type S struct{ mu sync.Mutex }
func (s *S) Lock() { s.mu.Lock() }`

	w := buildResolver(t, src, "S", "Lock", false)
	if w.resolve("s.mu", "Lock") {
		t.Error("a Lock without a balancing sibling must not be treated as a wrapper")
	}
}

func TestWrapperResolver_RawBodyEffectsShortCircuits(t *testing.T) {
	// During a simulated run (rawBodyEffects=true) the resolver is inert so it
	// cannot mutate the caller's verdict.
	src := `package p
import "sync"
type S struct{ mu sync.Mutex }
func (s *S) Lock()   { s.mu.Lock() }
func (s *S) Unlock() { s.mu.Unlock() }`

	w := buildResolver(t, src, "S", "Lock", true)
	if w.resolve("s.mu", "Lock") {
		t.Error("resolve must short-circuit to false when rawBodyEffects is set")
	}
}

func TestWrapperResolver_RUnlockWrapperPair(t *testing.T) {
	// The read-lock variant: RUnlock() balanced by a sibling RLock().
	src := `package p
import "sync"
type S struct{ mu sync.RWMutex }
func (s *S) RLock()   { s.mu.RLock() }
func (s *S) RUnlock() { s.mu.RUnlock() }`

	w := buildResolver(t, src, "S", "RUnlock", false)
	if !w.resolve("s.mu", "RUnlock") {
		t.Error("expected s.mu.RUnlock() to be recognized as a borrowed wrapper")
	}
}
