package mutex

import (
	"testing"
)

// These tests exercise crossGoroutineDetector in isolation: a config-only
// collaborator that needs no Checker and no analysis.Pass.

func TestCrossGoroutineDetector_DeadlockMessage(t *testing.T) {
	d := newCrossGoroutineDetector(nil, nil, nil, nil)

	tests := []struct {
		name          string
		isRWMutex     bool
		parentRead    bool
		requestMethod string
		parentBlocks  bool
		want          string
	}{
		{
			name: "mutex, parent may release",
			want: "mutex 'mu' goroutine started while lock is held and also tries to acquire it, will deadlock if parent never releases",
		},
		{
			name:         "mutex, parent blocks",
			parentBlocks: true,
			want:         "mutex 'mu' goroutine started while lock is held and also tries to acquire it before parent unlocks",
		},
		{
			name:       "rwmutex, parent holds read lock",
			isRWMutex:  true,
			parentRead: true,
			want:       "rwmutex 'mu' goroutine started while read lock is held and also tries to acquire write lock, will deadlock if parent never runlocks",
		},
		{
			name:          "rwmutex, parent holds write lock, goroutine wants read",
			isRWMutex:     true,
			requestMethod: "RLock",
			want:          "rwmutex 'mu' goroutine started while write lock is held and also tries to acquire read lock, will deadlock if parent never releases",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := d.deadlockMessage("mu", tc.isRWMutex, tc.parentRead, tc.requestMethod, tc.parentBlocks)
			if got != tc.want {
				t.Errorf("deadlockMessage =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func TestCrossGoroutineDetector_GoroutineBodyLockCallMethod(t *testing.T) {
	t.Run("finds a direct Lock call", func(t *testing.T) {
		file, _ := parseFile(t, `package p
func f() {
	mu.Lock()
}`)
		d := newCrossGoroutineDetector(nil, nil, nil, nil)
		method, ok := d.goroutineBodyLockCallMethod(funcBody(t, file, "f"), "mu", []string{"Lock"})
		if !ok || method != "Lock" {
			t.Fatalf("got (%q, %v), want (\"Lock\", true)", method, ok)
		}
	})

	t.Run("does not recurse into nested goroutines", func(t *testing.T) {
		// The Lock lives inside a nested `go func(){...}`, a different frame, so
		// it must not be attributed to this goroutine.
		file, _ := parseFile(t, `package p
func f() {
	go func() {
		mu.Lock()
	}()
}`)
		d := newCrossGoroutineDetector(nil, nil, nil, nil)
		if _, ok := d.goroutineBodyLockCallMethod(funcBody(t, file, "f"), "mu", []string{"Lock"}); ok {
			t.Fatal("expected no match across the nested goroutine boundary")
		}
	})

	t.Run("absent method returns false", func(t *testing.T) {
		file, _ := parseFile(t, `package p
func f() {
	mu.Unlock()
}`)
		d := newCrossGoroutineDetector(nil, nil, nil, nil)
		if _, ok := d.goroutineBodyLockCallMethod(funcBody(t, file, "f"), "mu", []string{"Lock"}); ok {
			t.Fatal("Unlock must not match a search for Lock")
		}
	})
}

func TestCrossGoroutineDetector_CollectReleases(t *testing.T) {
	t.Run("records unlock when parent holds the lock", func(t *testing.T) {
		file, cf := parseFile(t, `package p
func worker() {
	mu.Unlock()
}`)
		d := newCrossGoroutineDetector(map[string]bool{"mu": true}, map[string]bool{}, cf, nil)
		parentStats := map[string]*Stats{"mu": {lock: 1}}

		releases := d.collectReleases(funcBody(t, file, "worker"), parentStats)
		if len(releases.unlocks["mu"]) != 1 {
			t.Fatalf("expected 1 cross-goroutine release for 'mu', got %d", len(releases.unlocks["mu"]))
		}
	})

	t.Run("ignores unlock balanced by a local lock", func(t *testing.T) {
		// The goroutine locks and unlocks on its own: nothing is handed back to
		// the parent.
		file, cf := parseFile(t, `package p
func worker() {
	mu.Lock()
	mu.Unlock()
}`)
		d := newCrossGoroutineDetector(map[string]bool{"mu": true}, map[string]bool{}, cf, nil)
		parentStats := map[string]*Stats{"mu": {lock: 1}}

		releases := d.collectReleases(funcBody(t, file, "worker"), parentStats)
		if len(releases.unlocks["mu"]) != 0 {
			t.Fatalf("locally-balanced unlock must not be a parent release, got %d", len(releases.unlocks["mu"]))
		}
	})

	t.Run("ignores unlock when parent does not hold the lock", func(t *testing.T) {
		file, cf := parseFile(t, `package p
func worker() {
	mu.Unlock()
}`)
		d := newCrossGoroutineDetector(map[string]bool{"mu": true}, map[string]bool{}, cf, nil)
		parentStats := map[string]*Stats{} // parent holds nothing

		releases := d.collectReleases(funcBody(t, file, "worker"), parentStats)
		if len(releases.unlocks["mu"]) != 0 {
			t.Fatalf("expected no release when parent holds no lock, got %d", len(releases.unlocks["mu"]))
		}
	})
}
