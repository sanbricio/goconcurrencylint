package report

import (
	"go/token"
	"sort"

	"golang.org/x/tools/go/analysis"
)

// ErrorReport represents a single diagnostic to be reported. Category must
// match a stable identifier from the category package so downstream tools
// (golangci-lint, IDE integrations) and the inline ignore directive can
// filter by it.
type ErrorReport struct {
	Pos      token.Pos
	Category string
	Message  string
}

// IgnoreFunc decides whether a diagnostic should be suppressed based on its
// location and category. It is consulted at report time; passing a nil
// IgnoreFunc disables filtering.
type IgnoreFunc func(filename string, line int, category string) bool

// ErrorCollector accumulates diagnostics across files, deduplicates by
// (Pos, Category, Message), and emits them sorted in deterministic order.
type ErrorCollector struct {
	errors []ErrorReport
	seen   map[ErrorReport]struct{}
}

// AddError records a diagnostic. category should be a constant from the
// category package; passing an empty category is allowed for transitional
// callers but will not match any per-rule ignore directive.
func (ec *ErrorCollector) AddError(pos token.Pos, category, message string) {
	report := ErrorReport{
		Pos:      pos,
		Category: category,
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

// ReportAll emits every collected diagnostic via pass.Report. When ignore is
// non-nil, diagnostics for which it returns true are dropped.
func (ec *ErrorCollector) ReportAll(pass *analysis.Pass, ignore IgnoreFunc) {
	sort.Slice(ec.errors, func(i, j int) bool {
		posI := pass.Fset.Position(ec.errors[i].Pos)
		posJ := pass.Fset.Position(ec.errors[j].Pos)
		if posI.Filename != posJ.Filename {
			return posI.Filename < posJ.Filename
		}
		if posI.Line != posJ.Line {
			return posI.Line < posJ.Line
		}
		if posI.Column != posJ.Column {
			return posI.Column < posJ.Column
		}
		if ec.errors[i].Category != ec.errors[j].Category {
			return ec.errors[i].Category < ec.errors[j].Category
		}
		return ec.errors[i].Message < ec.errors[j].Message
	})

	for _, err := range ec.errors {
		if ignore != nil {
			pos := pass.Fset.Position(err.Pos)
			if ignore(pos.Filename, pos.Line, err.Category) {
				continue
			}
		}
		pass.Report(analysis.Diagnostic{
			Pos:      err.Pos,
			Category: err.Category,
			Message:  err.Message,
		})
	}
}
