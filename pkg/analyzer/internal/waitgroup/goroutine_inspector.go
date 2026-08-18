package waitgroup

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

type deferDoneDetector func(*ast.DeferStmt, string) bool
type doneCallChecker func(*ast.CallExpr, string) bool
type goroutineDoneAnalyzer func(*ast.GoStmt, string) (doneCallInfo, bool)
type waitOnlyChecker func(*ast.GoStmt, string) bool
type doneBlockAnalyzer func(*ast.BlockStmt, string, map[token.Pos]bool) doneCallInfo
type inGoroutineChecker func(token.Pos) bool
type mainFlowChecker func(token.Pos) bool
type builtinPanicChecker func(*ast.Ident) bool

// wgCallSite is a Wait/Done call recorded with the WaitGroup object its
// receiver resolves to. The object lets the same-goroutine deadlock check pair
// a Wait with a Done only when both name the same WaitGroup, so a shadowing
// inner `var wg` is not conflated with an outer `wg` of the same name. obj is
// nil when the receiver cannot be resolved to a variable (e.g. a field access).
type wgCallSite struct {
	pos token.Pos
	obj types.Object
}

type waitDonePositions struct {
	waits      []wgCallSite
	dones      []wgCallSite
	deferDones []wgCallSite
}

// goroutineInspector groups diagnostics that reason about WaitGroup behavior
// inside worker goroutines.
type goroutineInspector struct {
	waitGroupNames     map[string]bool
	commentFilter      *commentfilter.CommentFilter
	reporter           report.Reporter
	deferInvokesDone   deferDoneDetector
	callInvokesDone    doneCallChecker
	goroutineDoneInfo  goroutineDoneAnalyzer
	goroutineOnlyWaits waitOnlyChecker
	analyzeDoneCalls   doneBlockAnalyzer
	isInGoroutine      inGoroutineChecker
	typesInfo          *types.Info
	isInMainFlow       mainFlowChecker
	isBuiltinPanic     builtinPanicChecker
}

func newGoroutineInspector(
	waitGroupNames map[string]bool,
	cf *commentfilter.CommentFilter,
	reporter report.Reporter,
	deferInvokesDone deferDoneDetector,
	callInvokesDone doneCallChecker,
	goroutineDoneInfo goroutineDoneAnalyzer,
	goroutineOnlyWaits waitOnlyChecker,
	analyzeDoneCalls doneBlockAnalyzer,
	isInGoroutine inGoroutineChecker,
	typesInfo *types.Info,
	isInMainFlow mainFlowChecker,
	isBuiltinPanic builtinPanicChecker,
) *goroutineInspector {
	return &goroutineInspector{
		waitGroupNames:     waitGroupNames,
		commentFilter:      cf,
		reporter:           reporter,
		deferInvokesDone:   deferInvokesDone,
		callInvokesDone:    callInvokesDone,
		goroutineDoneInfo:  goroutineDoneInfo,
		goroutineOnlyWaits: goroutineOnlyWaits,
		analyzeDoneCalls:   analyzeDoneCalls,
		isInGoroutine:      isInGoroutine,
		typesInfo:          typesInfo,
		isInMainFlow:       isInMainFlow,
		isBuiltinPanic:     isBuiltinPanic,
	}
}

func (g *goroutineInspector) shouldSkipCall(call *ast.CallExpr) bool {
	return g.commentFilter != nil && g.commentFilter.ShouldSkipCall(call)
}

func (g *goroutineInspector) shouldSkipStatement(stmt ast.Stmt) bool {
	return g.commentFilter != nil && g.commentFilter.ShouldSkipStatement(stmt)
}

func (g *goroutineInspector) isMainFlow(pos token.Pos) bool {
	return g.isInMainFlow == nil || g.isInMainFlow(pos)
}

func (g *goroutineInspector) isInsideGoroutine(pos token.Pos) bool {
	return g.isInGoroutine != nil && g.isInGoroutine(pos)
}

func (g *goroutineInspector) callIsBuiltinPanic(ident *ast.Ident) bool {
	if g.isBuiltinPanic == nil {
		return ident != nil && ident.Name == "panic"
	}
	return g.isBuiltinPanic(ident)
}
