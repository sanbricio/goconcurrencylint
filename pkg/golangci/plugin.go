// Package golangci exposes goconcurrencylint as a golangci-lint module plugin,
// so a project can run it from the golangci-lint it already has. A
// .custom-gcl.yml naming this import path compiles the linter in, which needs
// no approval from upstream. See docs/golangci-lint.md.
package golangci

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/golangci/plugin-module-register/register"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
	"golang.org/x/tools/go/analysis"
)

func init() {
	register.Plugin("goconcurrencylint", New)
}

// Settings is the "settings" block of the linter's golangci-lint config. It is
// decoded with DisallowUnknownFields, so a mistyped key is a config error
// instead of a setting that silently does nothing.
//
// The tags are json, not mapstructure: golangci-lint decodes its own config
// with mapstructure but hands plugin settings over as an untyped map, which
// register.DecodeSettings then round-trips through encoding/json.
type Settings struct {
	// Checks is the -checks list with one entry per element. Absent, every check
	// runs.
	Checks CheckList `json:"checks"`
}

// CheckList is the "checks" setting: the same ordered list as the -checks flag,
// written the way golangci-lint writes every other list setting.
type CheckList []string

// UnmarshalJSON exists to reject the comma-separated string with a message that
// names the fix. That string is the shape the flag takes and the shape the
// README documents, so it is the one people try first here.
func (l *CheckList) UnmarshalJSON(data []byte) error {
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		var s string
		if json.Unmarshal(data, &s) == nil {
			return fmt.Errorf("%q must be a list, one entry per check: write %s instead of %q", "checks", asList(s), s)
		}
		return err
	}
	*l = entries
	return nil
}

// asList renders a flag value as the list that replaces it. Splitting on commas
// alone is enough for a hint: any separator the flag accepts inside a single
// entry still parses the same once quoted.
func asList(value string) string {
	quoted := make([]string, 0, strings.Count(value, ",")+1)
	for entry := range strings.SplitSeq(value, ",") {
		quoted = append(quoted, fmt.Sprintf("%q", strings.TrimSpace(entry)))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

type plugin struct {
	settings Settings
}

func New(settings any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[Settings](settings)
	if err != nil {
		return nil, err
	}
	return &plugin{settings: s}, nil
}

// BuildAnalyzers writes the settings to the analyzer's own flag rather than
// parsing them here, so the CLI and the plugin share one implementation of the
// list syntax down to the error messages. It happens here and not in New
// because golangci-lint only reaches this method for a linter it will run.
func (p *plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	// An absent key and an empty list are different statements: the first asks
	// for the default, the second asks for nothing, which is never what a config
	// means to say.
	if p.settings.Checks != nil {
		if len(p.settings.Checks) == 0 {
			return nil, errors.New(`setting "checks" is an empty list; remove the key to run every check`)
		}
		if err := analyzer.Analyzer.Flags.Set("checks", strings.Join(p.settings.Checks, ",")); err != nil {
			return nil, fmt.Errorf("setting %q: %w", "checks", err)
		}
	}
	return []*analysis.Analyzer{analyzer.Analyzer}, nil
}

func (p *plugin) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
