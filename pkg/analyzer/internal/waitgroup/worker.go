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

	// workerCanRecover is set per worker goroutine before its body is scanned
	// for a non-deferred Done. It records whether that goroutine installs a
	// deferred recover: only then does an explicit panic strand a non-deferred
	// Done (the goroutine survives the panic and skips it). Without a recover an
	// unrecovered panic crashes the whole process, so the missed Done is moot.
	workerCanRecover bool
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
