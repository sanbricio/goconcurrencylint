package analyzer

import (
	"sort"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis/analysistest"
)

// setFlag sets one selection flag for the duration of a test. The flags are
// process-wide, so no test in this package may call t.Parallel().
func setFlag(t *testing.T, name, value string) {
	t.Helper()
	require.NoError(t, Analyzer.Flags.Set(name, value))
	t.Cleanup(selection.reset)
}

func TestResolveID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		want    []category.Category
		wantAll bool
		wantErr string
	}{
		{name: "canonical code", id: "GCL1001", want: []category.Category{category.LockWithoutUnlock}},
		{name: "lowercase code", id: "gcl1001", want: []category.Category{category.LockWithoutUnlock}},
		{name: "legacy slug", id: "lock-without-unlock", want: []category.Category{category.LockWithoutUnlock}},
		{name: "uppercase slug", id: "LOCK-WITHOUT-UNLOCK", want: []category.Category{category.LockWithoutUnlock}},
		{name: "mixed-case slug", id: "Lock-Without-Unlock", want: []category.Category{category.LockWithoutUnlock}},
		{name: "all keyword", id: "all", wantAll: true},
		{name: "all is case-insensitive", id: "ALL", wantAll: true},
		{name: "family prefix", id: "GCL3*", want: []category.Category{
			category.OnceDoDeadlock, category.OnceDoNil, category.OnceConstructorNil,
		}},
		{name: "single-check prefix", id: "GCL5001*", want: []category.Category{category.PoolNonPointerValue}},
		{name: "bare star", id: "*", wantAll: true},
		{name: "unknown id", id: "GCL9999", wantErr: `unknown check "GCL9999"`},
		{name: "unknown slug", id: "not-a-check", wantErr: `unknown check "not-a-check"`},
		{name: "pattern matching nothing", id: "GCL8*", wantErr: `no check matches pattern "GCL8*"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveID(tc.id)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			if tc.wantAll {
				assert.ElementsMatch(t, category.All(), got)
				return
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestSplitIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  []string
	}{
		{name: "comma", value: "GCL1001,GCL1002", want: []string{"GCL1001", "GCL1002"}},
		{name: "space", value: "all -GCL1002", want: []string{"all", "-GCL1002"}},
		{name: "semicolon", value: "GCL1001;GCL1002", want: []string{"GCL1001", "GCL1002"}},
		{name: "trailing separator", value: "GCL1001,", want: []string{"GCL1001"}},
		{name: "empty", value: "", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, splitIDs(tc.value))
		})
	}
}

// TestParseCheckList pins the ordered add/remove semantics.
func TestParseCheckList(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      string
		wantAll    bool
		want       []string
		wantErr    string
		wantAbsent []string
	}{
		{name: "all", value: "all", wantAll: true},
		{
			name:       "all minus one check",
			value:      "all,-GCL5001",
			wantAbsent: []string{"GCL5001"},
		},
		{
			name:       "all minus a family",
			value:      "all,-GCL2*",
			wantAbsent: []string{"GCL2001", "GCL2015"},
		},
		{
			name:  "only a family",
			value: "GCL3*",
			want:  []string{"GCL3001", "GCL3002", "GCL3003"},
		},
		{
			name:  "family minus one",
			value: "GCL3*,-GCL3002",
			want:  []string{"GCL3001", "GCL3003"},
		},
		{
			name:  "later entry re-adds what an earlier one removed",
			value: "GCL2*,-GCL2*,GCL2001",
			want:  []string{"GCL2001"},
		},
		{
			name:  "legacy slug excluded",
			value: "GCL1001,-lock-without-unlock",
			want:  []string{},
		},
		{name: "empty list selects nothing", value: "", want: []string{}},
		{name: "unknown entry", value: "all,-GCL9999", wantErr: `unknown check "GCL9999"`},
		{name: "lone dash", value: "all,-", wantErr: "empty check id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCheckList(tc.value)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			if tc.wantAll {
				assert.Len(t, got, len(category.All()))
				return
			}
			for _, absent := range tc.wantAbsent {
				assert.NotContains(t, sortedCodes(got), absent)
			}
			if tc.want != nil {
				if len(tc.want) == 0 {
					assert.Empty(t, got)
					return
				}
				assert.Equal(t, tc.want, sortedCodes(got))
			}
		})
	}
}

func TestCheckListDefaultsToEverything(t *testing.T) {
	s := newCheckSelection()
	assert.Equal(t, "all", s.checks.String())
	for _, code := range category.All() {
		assert.True(t, s.checks.has(code), "%s must be enabled by default", code)
	}
}

func TestCheckListSetIsLastWins(t *testing.T) {
	s := newCheckSelection()
	require.NoError(t, s.checks.Set("GCL1001"))
	require.NoError(t, s.checks.Set("GCL5001"))

	assert.False(t, s.checks.has(category.LockWithoutUnlock),
		"a second -checks replaces the first, it does not accumulate")
	assert.True(t, s.checks.has(category.PoolNonPointerValue))
	assert.Equal(t, "GCL5001", s.checks.String())
}

func TestIsTestFile(t *testing.T) {
	assert.True(t, isTestFile("worker_test.go"))
	assert.True(t, isTestFile("/abs/path/worker_test.go"))
	assert.False(t, isTestFile("worker.go"), "-tests=false must not touch non-test files")
	assert.False(t, isTestFile("not_testing.go"), "only the _test.go suffix counts")
}

func TestCheckSelectionEnabled(t *testing.T) {
	s := newCheckSelection()
	assert.True(t, s.enabled(category.LockWithoutUnlock))
	assert.True(t, s.enabled(""), "uncatalogued diagnostics must survive filtering")

	require.NoError(t, s.checks.Set("GCL5001"))
	assert.False(t, s.enabled(category.LockWithoutUnlock))
	assert.True(t, s.enabled(category.PoolNonPointerValue))
	assert.True(t, s.enabled(""), "an explicit list still cannot drop an uncatalogued diagnostic")
}

func TestCheckSelectionInvalidFlagValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{
			name:    "unknown id",
			value:   "all,-nope",
			wantErr: `unknown check "nope"`,
		},
		{
			name:    "empty value selects nothing",
			value:   "",
			wantErr: "no checks selected",
		},
		{
			name:    "everything excluded selects nothing",
			value:   "all,-all",
			wantErr: "no checks selected",
		},
		{
			name:    "family excluded from itself selects nothing",
			value:   "GCL1*,-GCL1*",
			wantErr: "no checks selected",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(selection.reset)
			err := Analyzer.Flags.Set("checks", tc.value)
			require.Error(t, err, "a list that lints nothing must fail the flag, not silently do nothing")
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, "all", selection.checks.String(),
				"a rejected value must leave the previous configuration untouched")
		})
	}
}

// The fixtures below assert the same behaviour end to end, through the real
// analyzer.

func TestSelectionDefaultReportsEveryCheck(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "selection")
}

func TestSelectionExcludesFamily(t *testing.T) {
	setFlag(t, "checks", "all,-GCL5*")
	analysistest.Run(t, analysistest.TestData(), Analyzer, "selectionfiltered")
}

func TestSelectionSkipsTestFiles(t *testing.T) {
	setFlag(t, "tests", "false")
	analysistest.Run(t, analysistest.TestData(), Analyzer, "selectiontests")
}

// sortedCodes renders a resolved set in catalogue order. Test-only.
func sortedCodes(set map[category.Category]struct{}) []string {
	codes := make([]string, 0, len(set))
	for c := range set {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	return codes
}
