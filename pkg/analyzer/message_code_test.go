package analyzer

import (
	"strings"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis/analysistest"
)

// TestDiagnosticMessageCarriesCode verifies that every diagnostic the umbrella
// emits prefixes its message with the canonical check code (e.g.
// "GCL1001: ..."), so the code is visible in plain CLI output. This pins the
// behaviour against any future path that re-emits diagnostics without the
// prefix.
func TestDiagnosticMessageCarriesCode(t *testing.T) {
	results := analysistest.Run(t, analysistest.TestData(), Analyzer,
		"mutex", "waitgroup", "once", "cond", "pool", "synccopy", "packagelevel", "ignoredirective", "generated")

	total := 0
	for _, res := range results {
		require.NotNil(t, res.Pass, "analysistest must return a non-nil Pass")
		for _, diag := range res.Diagnostics {
			total++
			pos := res.Pass.Fset.Position(diag.Pos)

			_, known := category.Lookup(category.Category(diag.Category))
			assert.True(t, known,
				"diagnostic at %s carries unknown code %q (message: %q)",
				pos, diag.Category, diag.Message)

			wantPrefix := diag.Category + ": "
			assert.True(t, strings.HasPrefix(diag.Message, wantPrefix),
				"diagnostic at %s message %q must start with %q",
				pos, diag.Message, wantPrefix)
		}
	}

	assert.Greater(t, total, 0, "expected the fixtures to produce at least one diagnostic")
}
