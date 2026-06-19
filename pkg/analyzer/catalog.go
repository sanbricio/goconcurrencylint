package analyzer

//go:generate go run github.com/sanbricio/goconcurrencylint/scripts/gendocs

import "github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"

// Check is the public, stable metadata for one diagnostic the linter can
// emit. It is the catalogue entry behind a diagnostic code such as "GCL1001":
// downstream tooling, the docs generator and the `explain` subcommand all read
// the catalogue through Checks and Lookup rather than the internal registry.
type Check struct {
	// Code is the canonical identifier shown in diagnostics, e.g. "GCL1001".
	Code string
	// Slug is the legacy kebab-case alias still accepted in ignore directives.
	Slug string
	// Primitive lists the sync types the check applies to.
	Primitive string
	// Summary is a one-line description of what the check detects.
	Summary string
	// Why explains the runtime consequence the check guards against.
	Why string
	// Bad is a minimal Go snippet illustrating the pattern the check flags.
	Bad string
	// Good is the corrected version of Bad that the check accepts.
	Good string
}

// Checks returns the full catalogue of checks the linter can report, ordered
// by code.
func Checks() []Check {
	internal := category.Checks()
	out := make([]Check, len(internal))
	for i, c := range internal {
		out[i] = toCheck(c)
	}
	return out
}

// Lookup returns the catalogue entry for id, which may be a canonical code
// (GCL1001) or a legacy slug (lock-without-unlock). The second result is false
// when id matches no known check.
func Lookup(id string) (Check, bool) {
	code, ok := category.Canonical(id)
	if !ok {
		return Check{}, false
	}
	c, ok := category.Lookup(code)
	if !ok {
		return Check{}, false
	}
	return toCheck(c), true
}

func toCheck(c category.Check) Check {
	return Check{
		Code:      string(c.Code),
		Slug:      c.Slug,
		Primitive: c.Primitive,
		Summary:   c.Summary,
		Why:       c.Why,
		Bad:       c.Bad,
		Good:      c.Good,
	}
}
