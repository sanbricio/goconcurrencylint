package analyzer

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
)

// Check selection runs a subset of the catalogue without editing source, so a
// single noisy check cannot disqualify the whole linter from a pipeline.
//
// -checks takes staticcheck's syntax: an ordered list processed from the empty
// set, where a plain entry adds what it matches, a "-" prefix removes it, and
// "all" is the whole catalogue. Entries are codes (GCL1001), legacy slugs
// (lock-without-unlock) or code prefixes (GCL1*), in any case. An enable/disable
// pair was rejected because golangci-lint already gives "enable" the opposite
// meaning — additive on top of a default set, not exclusive.
//
// An entry that matches nothing, and a list that resolves to nothing, are both
// flag errors: a typo or an unset variable must not leave a green pipeline that
// lints nothing.
//
// There is deliberately no flag here for test files: the go/analysis driver
// already exposes -test=false, which keeps them from being loaded at all, and
// golangci-lint has run.tests. Either beats filtering at report time.

// selection is process-wide because analysis.Analyzer.Flags is. Consequence for
// tests: they must stay serial, since a t.Parallel() test would read whatever
// another test had set.
var selection = newCheckSelection()

func init() {
	selection.register(&Analyzer.Flags)
}

type checkSelection struct {
	checks checkList
}

func newCheckSelection() *checkSelection {
	return &checkSelection{checks: newCheckList()}
}

func (s *checkSelection) register(fs *flag.FlagSet) {
	fs.Var(&s.checks, "checks",
		`comma-separated list of checks to run: codes, legacy slugs or prefixes like "GCL1*", "-" to exclude, "all" for everything`)
}

// reset restores the defaults. Only tests need it, to undo a flag they set.
func (s *checkSelection) reset() {
	s.checks = newCheckList()
}

// enabled reports whether cat is selected. An empty category is always kept: it
// means a diagnostic outside the catalogue, and dropping it would hide a bug in
// the linter itself.
func (s *checkSelection) enabled(cat category.Category) bool {
	if cat == "" {
		return true
	}
	return s.checks.has(cat)
}

// checkList is the flag.Value behind -checks: the resolved set, plus the raw
// string so the flag package can print it.
type checkList struct {
	raw     string
	enabled map[category.Category]struct{}
}

const allChecks = "all"

func newCheckList() checkList {
	codes := category.All()
	enabled := make(map[category.Category]struct{}, len(codes))
	for _, code := range codes {
		enabled[code] = struct{}{}
	}
	return checkList{raw: allChecks, enabled: enabled}
}

func (cl *checkList) has(code category.Category) bool {
	_, ok := cl.enabled[code]
	return ok
}

func (cl *checkList) String() string {
	if cl == nil {
		return allChecks
	}
	return cl.raw
}

// Set replaces the current list — a repeated flag is last-wins, since a list
// whose meaning depends on order cannot be accumulated across occurrences.
func (cl *checkList) Set(value string) error {
	enabled, err := parseCheckList(value)
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return errors.New("no checks selected; list at least one code, slug or pattern")
	}
	cl.raw = value
	cl.enabled = enabled
	return nil
}

func parseCheckList(value string) (map[category.Category]struct{}, error) {
	enabled := map[category.Category]struct{}{}
	for _, entry := range splitIDs(value) {
		id, exclude := strings.CutPrefix(entry, "-")
		if id == "" {
			return nil, fmt.Errorf("empty check id in %q", entry)
		}
		matched, err := resolveID(id)
		if err != nil {
			return nil, err
		}
		for _, code := range matched {
			if exclude {
				delete(enabled, code)
				continue
			}
			enabled[code] = struct{}{}
		}
	}
	return enabled, nil
}

// splitIDs accepts the same separators as the inline ignore directive, so there
// is only one syntax to remember.
func splitIDs(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// resolveID turns one entry into the codes it selects. Codes are upper-case in
// the catalogue and slugs lower-case, so an id that does not match verbatim is
// retried in both cases before it is rejected.
func resolveID(id string) ([]category.Category, error) {
	if strings.EqualFold(id, allChecks) {
		return category.All(), nil
	}

	if base, ok := strings.CutSuffix(id, "*"); ok {
		prefix := strings.ToUpper(base)
		var matched []category.Category
		for _, code := range category.All() {
			if strings.HasPrefix(string(code), prefix) {
				matched = append(matched, code)
			}
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("no check matches pattern %q", id)
		}
		return matched, nil
	}

	for _, candidate := range []string{id, strings.ToUpper(id), strings.ToLower(id)} {
		if code, ok := category.Canonical(candidate); ok {
			return []category.Category{code}, nil
		}
	}
	return nil, fmt.Errorf("unknown check %q; see \"goconcurrencylint explain\" for the catalogue", id)
}
