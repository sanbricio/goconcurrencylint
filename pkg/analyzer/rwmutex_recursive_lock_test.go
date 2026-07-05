package analyzer

import (
	"strings"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"golang.org/x/tools/go/analysis/analysistest"
)

// TestRWMutexRecursiveLockCategory pins the read/write self-deadlock
// diagnostics to GCL1013 specifically. The `// want` markers on those fixtures
// only match message text, which is also a substring of the GCL1011
// double-lock messages; without this test, silently reverting the
// reclassification back to GCL1011 (see lockstate.go) would not fail any
// fixture.
func TestRWMutexRecursiveLockCategory(t *testing.T) {
	results := analysistest.Run(t, analysistest.TestData(), Analyzer, "mutex")

	const (
		readThenWrite = "attempts write Lock while read lock is held"
		writeThenRead = "attempts read RLock while write lock is held"
	)

	found := 0
	for _, res := range results {
		for _, diag := range res.Diagnostics {
			if !strings.Contains(diag.Message, readThenWrite) && !strings.Contains(diag.Message, writeThenRead) {
				continue
			}
			found++
			if diag.Category != string(category.RWMutexRecursiveLock) {
				t.Errorf("diagnostic %q carries category %q, want %q",
					diag.Message, diag.Category, category.RWMutexRecursiveLock)
			}
		}
	}

	if found == 0 {
		t.Fatal("expected at least one rwmutex recursive-lock diagnostic in the mutex fixtures")
	}
}
