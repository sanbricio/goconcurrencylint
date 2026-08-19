package golangci

import (
	"testing"

	"github.com/golangci/plugin-module-register/register"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checksFlag reads the flag the plugin writes to. It is process-wide, so no
// test in this package may call t.Parallel().
func checksFlag(t *testing.T) string {
	t.Helper()
	f := analyzer.Analyzer.Flags.Lookup("checks")
	require.NotNil(t, f, "the analyzer no longer registers -checks")
	return f.Value.String()
}

func restoreChecks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, analyzer.Analyzer.Flags.Set("checks", "all"))
	})
}

// TestPluginIsRegistered pins the name users put in their YAML: renaming it
// would break every config with an error that points at the config, not here.
func TestPluginIsRegistered(t *testing.T) {
	got, err := register.GetPlugin("goconcurrencylint")
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings any
		want     Settings
		wantErr  string
	}{
		{name: "no settings block", settings: nil},
		{name: "empty settings", settings: map[string]any{}},
		{name: "null checks", settings: map[string]any{"checks": nil}},
		{
			name:     "list",
			settings: map[string]any{"checks": []any{"all", "-GCL5001"}},
			want:     Settings{Checks: CheckList{"all", "-GCL5001"}},
		},
		{
			name:     "empty list decodes, and is rejected later",
			settings: map[string]any{"checks": []any{}},
			want:     Settings{Checks: CheckList{}},
		},
		{
			name:     "flag-style string",
			settings: map[string]any{"checks": "all,-GCL5001"},
			wantErr:  `"checks" must be a list, one entry per check: write ["all", "-GCL5001"] instead of "all,-GCL5001"`,
		},
		{
			name:     "unknown key",
			settings: map[string]any{"check": []any{"GCL1001"}},
			wantErr:  `unknown field "check"`,
		},
		{
			name:     "non-string entry",
			settings: map[string]any{"checks": []any{"all", 42}},
			wantErr:  "cannot unmarshal number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.settings)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.(*plugin).settings)
		})
	}
}

// TestAsList checks the hint is valid to paste back, including the separators
// the flag accepts inside one entry.
func TestAsList(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{value: "all", want: `["all"]`},
		{value: "all,-GCL5001", want: `["all", "-GCL5001"]`},
		{value: "all, -GCL5*", want: `["all", "-GCL5*"]`},
		{value: "GCL1001;GCL2001", want: `["GCL1001;GCL2001"]`},
	} {
		t.Run(tc.value, func(t *testing.T) {
			assert.Equal(t, tc.want, asList(tc.value))
		})
	}
}

func TestBuildAnalyzersReturnsTheUmbrella(t *testing.T) {
	p, err := New(nil)
	require.NoError(t, err)

	analyzers, err := p.BuildAnalyzers()
	require.NoError(t, err)
	require.Len(t, analyzers, 1)
	assert.Same(t, analyzer.Analyzer, analyzers[0])
}

func TestBuildAnalyzersAppliesChecks(t *testing.T) {
	restoreChecks(t)

	p, err := New(map[string]any{"checks": []any{"all", "-GCL5*"}})
	require.NoError(t, err)

	_, err = p.BuildAnalyzers()
	require.NoError(t, err)
	assert.Equal(t, "all,-GCL5*", checksFlag(t))
}

func TestBuildAnalyzersWithoutChecksKeepsEverything(t *testing.T) {
	restoreChecks(t)

	p, err := New(map[string]any{})
	require.NoError(t, err)

	_, err = p.BuildAnalyzers()
	require.NoError(t, err)
	assert.Equal(t, "all", checksFlag(t))
}

// TestBuildAnalyzersRejectsBadChecks covers the reason the plugin defers to the
// flag: a typo has to fail the run, not leave a check quietly enabled. The
// empty list is ours to reject — an absent key already means "everything", so a
// config that writes one out is not asking for that.
func TestBuildAnalyzersRejectsBadChecks(t *testing.T) {
	restoreChecks(t)

	for _, tc := range []struct {
		name    string
		checks  []any
		wantErr string
	}{
		{name: "unknown code", checks: []any{"GCL9999"}, wantErr: `unknown check "GCL9999"`},
		{name: "pattern matching nothing", checks: []any{"GCL8*"}, wantErr: `no check matches pattern "GCL8*"`},
		{name: "selects nothing", checks: []any{"all", "-all"}, wantErr: "no checks selected"},
		{name: "lone dash", checks: []any{"-"}, wantErr: `empty check id in "-"`},
		{name: "empty list", checks: []any{}, wantErr: `setting "checks" is an empty list`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(map[string]any{"checks": tc.checks})
			require.NoError(t, err)

			_, err = p.BuildAnalyzers()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Equal(t, "all", checksFlag(t), "a rejected value must leave the selection alone")
		})
	}
}

func TestGetLoadMode(t *testing.T) {
	p, err := New(nil)
	require.NoError(t, err)
	assert.Equal(t, register.LoadModeTypesInfo, p.GetLoadMode())
}
