package analyzer

import (
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis/analysistest"
)

// TestDiagnosticCategoryPropagation runs the analyzer over the full set of
// fixtures and verifies every emitted diagnostic carries a Category that
// belongs to the public catalogue.
func TestDiagnosticCategoryPropagation(t *testing.T) {
	known := make(map[category.Category]struct{}, len(category.All()))
	for _, id := range category.All() {
		known[id] = struct{}{}
	}

	results := analysistest.Run(t, analysistest.TestData(), Analyzer,
		"mutex", "waitgroup", "packagelevel", "ignoredirective")

	totalDiagnostics := 0
	for _, res := range results {
		require.NotNil(t, res.Pass, "analysistest must return a non-nil Pass")
		for _, diag := range res.Diagnostics {
			totalDiagnostics++
			pos := res.Pass.Fset.Position(diag.Pos)
			assert.NotEmpty(t, diag.Category,
				"diagnostic at %s missing Category: %q", pos, diag.Message)
			if diag.Category == "" {
				continue
			}
			_, ok := known[category.Category(diag.Category)]
			assert.True(t, ok,
				"diagnostic at %s carries unknown Category %q (message: %q)",
				pos, diag.Category, diag.Message)
		}
	}

	assert.Greater(t, totalDiagnostics, 0,
		"expected the fixtures to produce at least one diagnostic")
}
