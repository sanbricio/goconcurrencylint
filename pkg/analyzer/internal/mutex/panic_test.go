package mutex

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// driveDetector mirrors how the real walker drives the detector over a function
// body: it records collection lengths on every assignment and probes each one
// for a potential panic while the supplied locks are held. Using a single,
// type-checked source keeps the test faithful to production (one AST, one
// *types.Info) instead of stitching lengths across unrelated files.
func driveDetector(d *lockedPanicDetector, body *ast.BlockStmt, stats map[string]*Stats) {
	for _, stmt := range body.List {
		if a, ok := stmt.(*ast.AssignStmt); ok {
			d.recordCollectionLengthsFromAssign(a)
			d.reportPotentialPanicWhileLocked(a, stats)
		}
	}
}

// heldLock returns a stats map with a single mutex "mu" still locked.
func heldLock() map[string]*Stats {
	return map[string]*Stats{"mu": {lock: 1}}
}

// TestLockedPanicDetector_PositiveOutOfRange: indexing a slice past its known
// length while "mu" is held must report one PanicBeforeUnlock diagnostic. This
// is the canonical pattern exercised by testdata (items := []int{1}; items[1]).
func TestLockedPanicDetector_PositiveOutOfRange(t *testing.T) {
	src := `package p
func f() {
	items := []int{1}
	_ = items[1]
}`
	file, info := parseTypedLifecycleFile(t, src)

	rep := &fakeReporter{}
	d := newLockedPanicDetector(map[string]bool{"mu": true}, nil, info, rep, false)
	driveDetector(d, funcBody(t, file, "f"), heldLock())

	if len(rep.calls) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(rep.calls))
	}
	got := rep.calls[0]
	if got.cat != category.PanicBeforeUnlock {
		t.Errorf("category = %q, want %q", got.cat, category.PanicBeforeUnlock)
	}
	if !strings.Contains(got.msg, "may remain locked") {
		t.Errorf("message %q does not contain %q", got.msg, "may remain locked")
	}
}

// TestLockedPanicDetector_NegativeInRange: an in-bounds index produces no
// diagnostic even with the lock held.
func TestLockedPanicDetector_NegativeInRange(t *testing.T) {
	src := `package p
func f() {
	items := []int{1, 2, 3}
	_ = items[1]
}`
	file, info := parseTypedLifecycleFile(t, src)

	rep := &fakeReporter{}
	d := newLockedPanicDetector(map[string]bool{"mu": true}, nil, info, rep, false)
	driveDetector(d, funcBody(t, file, "f"), heldLock())

	if len(rep.calls) != 0 {
		t.Fatalf("expected 0 diagnostics for in-range index, got %d", len(rep.calls))
	}
}

// TestLockedPanicDetector_NegativeNoLockHeld: an out-of-range index is harmless
// when no lock is held, so the detector must stay silent. Guards the
// hasUnprotectedHeldLock short-circuit.
func TestLockedPanicDetector_NegativeNoLockHeld(t *testing.T) {
	src := `package p
func f() {
	items := []int{1}
	_ = items[1]
}`
	file, info := parseTypedLifecycleFile(t, src)

	rep := &fakeReporter{}
	d := newLockedPanicDetector(map[string]bool{"mu": true}, nil, info, rep, false)
	// "mu" is present but already released: nothing remains locked.
	driveDetector(d, funcBody(t, file, "f"), map[string]*Stats{"mu": {lock: 0}})

	if len(rep.calls) != 0 {
		t.Fatalf("expected 0 diagnostics when no lock is held, got %d", len(rep.calls))
	}
}

// TestLockedPanicDetector_NegativeMapIndex: indexing a map never panics on a
// missing key, so even an out-of-literal-range key with a lock held is silent.
func TestLockedPanicDetector_NegativeMapIndex(t *testing.T) {
	src := `package p
func f() {
	m := map[int]string{1: "a", 2: "b"}
	_ = m[42]
}`
	file, info := parseTypedLifecycleFile(t, src)

	rep := &fakeReporter{}
	d := newLockedPanicDetector(map[string]bool{"mu": true}, nil, info, rep, false)
	driveDetector(d, funcBody(t, file, "f"), heldLock())

	if len(rep.calls) != 0 {
		t.Fatalf("expected 0 diagnostics for map index, got %d", len(rep.calls))
	}
}

// TestLockedPanicDetector_GuardRawBodyEffects: in simulation mode
// (rawBodyEffects=true) all panic reporting is suppressed, even for a clear
// out-of-range access with the lock held.
func TestLockedPanicDetector_GuardRawBodyEffects(t *testing.T) {
	src := `package p
func f() {
	items := []int{1}
	_ = items[1]
}`
	file, info := parseTypedLifecycleFile(t, src)

	rep := &fakeReporter{}
	d := newLockedPanicDetector(map[string]bool{"mu": true}, nil, info, rep, true)
	driveDetector(d, funcBody(t, file, "f"), heldLock())

	if len(rep.calls) != 0 {
		t.Fatalf("expected 0 diagnostics with rawBodyEffects=true, got %d", len(rep.calls))
	}
}
