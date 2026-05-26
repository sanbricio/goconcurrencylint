package report

import (
	"go/token"
	"sort"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"golang.org/x/tools/go/analysis"
)

// Reporter receives diagnostics from checkers as (position, category,
// message) triplets. ErrorCollector is the standard implementation.
type Reporter interface {
	AddError(pos token.Pos, cat category.Category, message string)
}

// ErrorReport represents a single diagnostic to be reported. Category must
// match a stable identifier from the category package so downstream tools
// (golangci-lint, IDE integrations) and the inline ignore directive can
// filter by it.
type ErrorReport struct {
	Pos      token.Pos
	Category category.Category
	Message  string
}

// IgnoreFunc decides whether a diagnostic should be suppressed based on its
// location and category. It is consulted at report time; passing a nil
// IgnoreFunc disables filtering.
type IgnoreFunc func(filename string, line int, cat category.Category) bool

// ErrorCollector accumulates diagnostics across files, deduplicates by
// (Pos, Category, Message), and emits them sorted in deterministic order.
type ErrorCollector struct {
	errors []ErrorReport
	seen   map[ErrorReport]struct{}
}

// AddError records a diagnostic. cat should be a constant from the category
// package; an empty category will not match any per-rule ignore directive.
func (ec *ErrorCollector) AddError(pos token.Pos, cat category.Category, message string) {
	report := ErrorReport{
		Pos:      pos,
		Category: cat,
		Message:  message,
	}

	if ec.seen == nil {
		ec.seen = make(map[ErrorReport]struct{})
	}
	if _, exists := ec.seen[report]; exists {
		return
	}
	ec.seen[report] = struct{}{}
	ec.errors = append(ec.errors, report)
}

type preparedError struct {
	err ErrorReport
	pos token.Position
}

// ReportAll emits every collected diagnostic via pass.Report. When ignore is
// non-nil, diagnostics for which it returns true are dropped.
func (ec *ErrorCollector) ReportAll(pass *analysis.Pass, ignore IgnoreFunc) {
	for _, d := range ec.Diagnostics(pass, ignore) {
		pass.Report(d)
	}
}

// Diagnostics returns the filtered, sorted set of diagnostics without
// emitting them. Sub-analyzers that ship their diagnostics through a
// Result (so an umbrella analyzer can re-emit them) call this instead of
// ReportAll. When ignore is non-nil, entries for which it returns true
// are dropped.
func (ec *ErrorCollector) Diagnostics(pass *analysis.Pass, ignore IgnoreFunc) []analysis.Diagnostic {
	prepared := ec.filterAndPrepare(pass, ignore)
	if len(prepared) == 0 {
		return nil
	}

	sort.Slice(prepared, func(i, j int) bool {
		posI := prepared[i].pos
		posJ := prepared[j].pos
		if posI.Filename != posJ.Filename {
			return posI.Filename < posJ.Filename
		}
		if posI.Line != posJ.Line {
			return posI.Line < posJ.Line
		}
		if posI.Column != posJ.Column {
			return posI.Column < posJ.Column
		}

		errI := prepared[i].err
		errJ := prepared[j].err
		if errI.Category != errJ.Category {
			return errI.Category < errJ.Category
		}

		return errI.Message < errJ.Message
	})

	out := make([]analysis.Diagnostic, len(prepared))
	for i, item := range prepared {
		out[i] = analysis.Diagnostic{
			Pos:      item.err.Pos,
			Category: string(item.err.Category),
			Message:  item.err.Message,
		}
	}
	return out
}

// filterAndPrepare resolves token positions once per diagnostic,
// filters ignored entries, and prepares the remaining data for sorting.
func (ec *ErrorCollector) filterAndPrepare(pass *analysis.Pass, ignore IgnoreFunc) []preparedError {
	cache := make([]preparedError, 0, len(ec.errors))

	for _, err := range ec.errors {
		pos := pass.Fset.Position(err.Pos)
		if ignore != nil && ignore(pos.Filename, pos.Line, err.Category) {
			continue
		}

		cache = append(cache, preparedError{
			err: err,
			pos: pos,
		})
	}

	return cache
}
