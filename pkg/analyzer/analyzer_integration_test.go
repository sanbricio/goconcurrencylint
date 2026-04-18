package analyzer

import (
	"golang.org/x/tools/go/analysis/analysistest"
	"testing"
)

func TestAnalyzerFixtures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		packages []string
	}{
		{name: "mutex", packages: []string{"mutex"}},
		{name: "waitgroup", packages: []string{"waitgroup"}},
		{name: "packagelevel", packages: []string{"packagelevel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysistest.Run(t, analysistest.TestData(), Analyzer, tc.packages...)
		})
	}
}
