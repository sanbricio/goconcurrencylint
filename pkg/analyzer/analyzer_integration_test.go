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
		{name: "once", packages: []string{"once"}},
		{name: "packagelevel", packages: []string{"packagelevel"}},
		{name: "ignoredirective", packages: []string{"ignoredirective"}},
		{name: "generated", packages: []string{"generated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysistest.Run(t, analysistest.TestData(), Analyzer, tc.packages...)
		})
	}
}
