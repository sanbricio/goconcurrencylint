package waitgroup

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// workerDoneAnalyzer answers whether a worker goroutine's Done is guaranteed:
// reachable, not pre-empted by a panic/abort, and recognised across the various
// defer/callback patterns. It is a self-contained subsystem (it does not call
// other collaborators) consumed as a set of callbacks by balanceValidator,
// goroutineInspector and escapeAnalyzer.
type workerDoneAnalyzer struct {
	function       *ast.FuncDecl
	waitGroupNames map[string]bool
	commentFilter  *commentfilter.CommentFilter
	typesInfo      *types.Info
	errorCollector report.Reporter
}

func newWorkerDoneAnalyzer(function *ast.FuncDecl, waitGroupNames map[string]bool, commentFilter *commentfilter.CommentFilter, typesInfo *types.Info, errorCollector report.Reporter) *workerDoneAnalyzer {
	return &workerDoneAnalyzer{
		function:       function,
		waitGroupNames: waitGroupNames,
		commentFilter:  commentFilter,
		typesInfo:      typesInfo,
		errorCollector: errorCollector,
	}
}
