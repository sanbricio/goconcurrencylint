package report

import (
	"go/token"
	"sort"

	"golang.org/x/tools/go/analysis"
)

// ErrorReport represents a single error to be reported
type ErrorReport struct {
	Pos     token.Pos
	Message string
}

// ErrorCollector collects all errors and allows sorting them by position
type ErrorCollector struct {
	errors []ErrorReport
}

func (ec *ErrorCollector) AddError(pos token.Pos, message string) {
	ec.errors = append(ec.errors, ErrorReport{
		Pos:     pos,
		Message: message,
	})
}

func (ec *ErrorCollector) ReportAll(pass *analysis.Pass) {
	// Sort errors by position using the file set
	sort.Slice(ec.errors, func(i, j int) bool {
		posI := pass.Fset.Position(ec.errors[i].Pos)
		posJ := pass.Fset.Position(ec.errors[j].Pos)
		if posI.Filename != posJ.Filename {
			return posI.Filename < posJ.Filename
		}
		if posI.Line != posJ.Line {
			return posI.Line < posJ.Line
		}
		return posI.Column < posJ.Column
	})
	for _, err := range ec.errors {
		pass.Reportf(err.Pos, err.Message)
	}
}
