package mutex

import (
	"go/ast"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// buildResolver parses src, indexes its receiver methods the same way the real
// analyzer does (common.BuildReceiverMethodMap), and returns a resolver bound
// to the method named methodName on type receiverType.
func buildResolver(t *testing.T, src, receiverType, methodName string, rawBodyEffects bool) *wrapperResolver {
	t.Helper()
	file, _ := parseFile(t, src)
	rm := common.BuildReceiverMethodMap([]*ast.File{file})
	fn := rm[receiverType][methodName]
	if fn == nil {
		t.Fatalf("method %s.%s not found in receiver map", receiverType, methodName)
	}
	return newWrapperResolver(rm, fn, rawBodyEffects, nil)
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

func TestWrapperResolver_CrossMethodBarrierByRole(t *testing.T) {
	// Methods named by role rather than "Lock"/"Unlock", each a single statement:
	// a write Lock balanced by a sibling write Unlock on the same field is an
	// intentional barrier, recognized structurally without a name hint.
	src := `package p
import "sync"
type B struct{ lock sync.RWMutex }
func (b *B) Hold()    { b.lock.Lock() }
func (b *B) Release() { b.lock.Unlock() }`

	w := buildResolver(t, src, "B", "Hold", false)
	if !w.resolve("b.lock", "Lock") {
		t.Error("a single-statement Lock balanced by a single-statement Unlock sibling must be recognized as a barrier pair")
	}
}

func TestWrapperResolver_BarrierRequiresSingleStatement(t *testing.T) {
	// The Lock method does work besides locking, so it is not a pure barrier
	// half; with role names and no opposite call inside, it stays unmatched.
	src := `package p
import "sync"
type B struct{ lock sync.RWMutex }
func (b *B) Hold()    { b.lock.Lock(); b.work() }
func (b *B) Release() { b.lock.Unlock() }
func (b *B) work()    {}`

	w := buildResolver(t, src, "B", "Hold", false)
	if w.resolve("b.lock", "Lock") {
		t.Error("a multi-statement method must not be treated as a barrier half")
	}
}

func TestWrapperResolver_BarrierNeedsOppositeSibling(t *testing.T) {
	// RLock with no single-statement RUnlock sibling (the read side is never
	// released): the lock is genuinely unmatched and must be reported.
	src := `package p
import "sync"
type B struct{ lock sync.RWMutex }
func (b *B) Hold()     { b.lock.Lock() }
func (b *B) Release()  { b.lock.Unlock() }
func (b *B) ReadHold() { b.lock.RLock() }`

	w := buildResolver(t, src, "B", "ReadHold", false)
	if w.resolve("b.lock", "RLock") {
		t.Error("RLock without a matching RUnlock sibling must stay unmatched")
	}
}
