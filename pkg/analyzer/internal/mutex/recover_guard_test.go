package mutex

import (
	"testing"
)

// TestRecoverGuardInspector_ContainsPresence verifies that containsUnlock and
// containsLock correctly detect the presence/absence of calls.
func TestRecoverGuardInspector_ContainsPresence(t *testing.T) {
	src := `package p
func f() {
	mu.Unlock()
}
`
	file, cf := parseFile(t, src)
	body := funcBody(t, file, "f")
	g := newRecoverGuardInspector(cf)

	if !g.containsUnlock(body, "mu") {
		t.Error("expected containsUnlock(body, \"mu\") == true")
	}
	if g.containsLock(body, "mu") {
		t.Error("expected containsLock(body, \"mu\") == false")
	}
}

// TestRecoverGuardInspector_UnlockGuardedByRecover verifies the positive case:
// the Unlock sits inside a recover()-guarded block.
func TestRecoverGuardInspector_UnlockGuardedByRecover(t *testing.T) {
	src := `package p
func f() {
	if r := recover(); r != nil {
		mu.Unlock()
	}
}
`
	file, cf := parseFile(t, src)
	body := funcBody(t, file, "f")
	g := newRecoverGuardInspector(cf)

	if !g.unlocksOnlyInRecoverGuard(body, "mu", "Unlock") {
		t.Error("expected unlocksOnlyInRecoverGuard == true when Unlock is inside recover guard")
	}
}

// TestRecoverGuardInspector_UnlockNotGuarded verifies the negative case:
// the Unlock is at top level with no recover().
func TestRecoverGuardInspector_UnlockNotGuarded(t *testing.T) {
	src := `package p
func f() {
	mu.Unlock()
}
`
	file, cf := parseFile(t, src)
	body := funcBody(t, file, "f")
	g := newRecoverGuardInspector(cf)

	if g.unlocksOnlyInRecoverGuard(body, "mu", "Unlock") {
		t.Error("expected unlocksOnlyInRecoverGuard == false when Unlock is not guarded")
	}
}

// TestRecoverGuardInspector_RWMutexVariant verifies containsRUnlock and
// containsRLock for an RWMutex.
func TestRecoverGuardInspector_RWMutexVariant(t *testing.T) {
	src := `package p
func f() {
	rw.RUnlock()
}
`
	file, cf := parseFile(t, src)
	body := funcBody(t, file, "f")
	g := newRecoverGuardInspector(cf)

	if !g.containsRUnlock(body, "rw") {
		t.Error("expected containsRUnlock(body, \"rw\") == true")
	}
	if g.containsRLock(body, "rw") {
		t.Error("expected containsRLock(body, \"rw\") == false")
	}
}
