// Package copycheck detects copy-by-value of sync.Mutex, sync.RWMutex
// and sync.WaitGroup. It is an independent sub-analyzer that reports
// diagnostics directly through analysis.Pass.Report.
package copycheck

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/filesetup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer reports any copy of a sync.Mutex, sync.RWMutex or
// sync.WaitGroup value (directly or via a containing struct). It does
// not call pass.Report itself: it returns the prepared diagnostic slice
// as ResultType so the umbrella analyzer can re-emit them. This is the
// pattern that makes the dependency graph visible to analysistest.
var Analyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_copy",
	Doc:        "Detects copy-by-value of sync.Mutex, sync.RWMutex and sync.WaitGroup.",
	Run:        run,
	Requires:   []*analysis.Analyzer{inspect.Analyzer, filesetup.Analyzer},
	ResultType: reflect.TypeFor[[]analysis.Diagnostic](),
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	files := pass.ResultOf[filesetup.Analyzer].(*filesetup.Result)
	ec := &report.ErrorCollector{}

	nodeFilter := []ast.Node{
		(*ast.FuncDecl)(nil),
		(*ast.FuncLit)(nil),
		(*ast.ValueSpec)(nil),
		(*ast.AssignStmt)(nil),
		(*ast.CallExpr)(nil),
	}

	insp.Preorder(nodeFilter, func(n ast.Node) {
		if files.IsGenerated(pass.Fset.File(n.Pos())) {
			return
		}
		switch node := n.(type) {
		case *ast.FuncDecl:
			reportParams(node, pass, ec)
		case *ast.FuncLit:
			reportFuncLitParams(node, pass, ec)
		case *ast.ValueSpec:
			reportValueSpec(node, pass, ec)
		case *ast.AssignStmt:
			reportAssignments(node, pass, ec)
		case *ast.CallExpr:
			reportArgs(node, pass, ec)
		}
	})

	return ec.Diagnostics(pass, files.IgnoreFunc()), nil
}

func reportParams(fn *ast.FuncDecl, pass *analysis.Pass, ec report.Reporter) {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return
	}
	if fn.Recv != nil {
		reportFieldList(fn.Recv, pass, ec)
	}
	reportFieldList(fn.Type.Params, pass, ec)
}

func reportFuncLitParams(fn *ast.FuncLit, pass *analysis.Pass, ec report.Reporter) {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return
	}
	reportFieldList(fn.Type.Params, pass, ec)
}

func reportFieldList(fields *ast.FieldList, pass *analysis.Pass, ec report.Reporter) {
	for _, field := range fields.List {
		kind := valueKind(pass.TypesInfo.TypeOf(field.Type))
		if kind == "" {
			continue
		}
		for _, name := range field.Names {
			ec.AddError(name.Pos(), category.SyncPrimitiveCopy, message(kind, name.Name))
		}
	}
}

func reportValueSpec(vs *ast.ValueSpec, pass *analysis.Pass, ec report.Reporter) {
	if vs == nil {
		return
	}
	for _, value := range vs.Values {
		if kind, name, ok := copiedPrimitive(value, pass); ok {
			ec.AddError(value.Pos(), category.SyncPrimitiveCopy, message(kind, name))
		}
	}
}

func reportAssignments(assign *ast.AssignStmt, pass *analysis.Pass, ec report.Reporter) {
	if assign == nil || (assign.Tok != token.ASSIGN && assign.Tok != token.DEFINE) {
		return
	}
	for i, rhs := range assign.Rhs {
		if i < len(assign.Lhs) && isBlankIdentifier(assign.Lhs[i]) {
			continue
		}
		if kind, name, ok := copiedPrimitive(rhs, pass); ok {
			ec.AddError(rhs.Pos(), category.SyncPrimitiveCopy, message(kind, name))
		}
	}
}

func reportArgs(call *ast.CallExpr, pass *analysis.Pass, ec report.Reporter) {
	if call == nil {
		return
	}
	for _, arg := range call.Args {
		if kind, name, ok := copiedPrimitive(arg, pass); ok {
			ec.AddError(arg.Pos(), category.SyncPrimitiveCopy, message(kind, name))
		}
	}
}

func copiedPrimitive(expr ast.Expr, pass *analysis.Pass) (string, string, bool) {
	if expr == nil || isTypeExpression(expr, pass.TypesInfo) || isFresh(expr) {
		return "", "", false
	}
	kind := valueKind(pass.TypesInfo.TypeOf(expr))
	if kind == "" {
		return "", "", false
	}
	name := common.GetVarName(expr)
	if name == "" || name == "?" {
		return "", "", false
	}
	return kind, name, true
}

func isBlankIdentifier(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "_"
}

func isTypeExpression(expr ast.Expr, info *types.Info) bool {
	if expr == nil || info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	return ok && tv.IsType()
}

func isFresh(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return true
	case *ast.UnaryExpr:
		return e.Op == token.AND || isFresh(e.X)
	}
	return false
}

func valueKind(typ types.Type) string {
	if kind := directValueKind(typ); kind != "" {
		return kind
	}
	if kind := containedValueKind(typ, make(map[types.Type]bool)); kind != "" {
		return "struct containing " + kind
	}
	return ""
}

func directValueKind(typ types.Type) string {
	typ = types.Unalias(typ)
	if match, ok := common.MatchPkgAndName(typ, "sync", "Mutex", "RWMutex", "WaitGroup"); ok {
		switch match {
		case "Mutex":
			return "mutex"
		case "RWMutex":
			return "rwmutex"
		case "WaitGroup":
			return "waitgroup"
		}
	}
	return ""
}

func containedValueKind(typ types.Type, visited map[types.Type]bool) string {
	if typ == nil {
		return ""
	}
	typ = types.Unalias(typ)
	if visited[typ] {
		return ""
	}
	visited[typ] = true

	switch t := typ.(type) {
	case *types.Named:
		if kind := directValueKind(t); kind != "" {
			return kind
		}
		return containedValueKind(t.Underlying(), visited)
	case *types.Struct:
		for field := range t.Fields() {
			fieldType := types.Unalias(field.Type())
			if _, ok := fieldType.(*types.Pointer); ok {
				continue
			}
			if kind := directValueKind(fieldType); kind != "" {
				return kind
			}
			if kind := containedValueKind(fieldType, visited); kind != "" {
				return kind
			}
		}
	}
	return ""
}

func message(kind, name string) string {
	if contained, ok := strings.CutPrefix(kind, "struct containing "); ok {
		return "struct '" + name + "' containing " + contained + " is copied by value"
	}
	return kind + " '" + name + "' is copied by value"
}
