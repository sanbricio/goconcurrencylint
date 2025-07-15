package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

// findDeferDoneCalls identifies defer Done calls to avoid counting them as regular Done calls
func (wga *Analyzer) findDeferDoneCalls(stats map[string]*Stats) {
	ast.Inspect(wga.function.Body, func(n ast.Node) bool {
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}

		if wga.commentFilter.ShouldSkipCall(deferStmt.Call) {
			return true
		}

		if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := call.X.(*ast.Ident); ok && call.Sel.Name == "Done" {
				if wga.waitGroupNames[ident.Name] {
					stats[ident.Name].hasDeferDone = true
				}
			}
			return true
		}

		if fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
			wga.findDoneInFunctionLiteral(fnlit.Body, stats)
		}

		return true
	})
}

// findDoneInFunctionLiteral looks for Done calls within function literals
func (wga *Analyzer) findDoneInFunctionLiteral(body *ast.BlockStmt, stats map[string]*Stats) {
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if wga.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" {
				wgName := common.GetVarName(sel.X)
				if wga.waitGroupNames[wgName] {
					stats[wgName].hasDeferDone = true
				}
			}
		}
		return true
	})
}

// collectCalls collects all Add, Done, and Wait calls in the function
func (wga *Analyzer) collectCalls(stats map[string]*Stats) {
	alreadyReported := make(map[token.Pos]bool)
	wga.traverseWithContext(wga.function.Body, nil, stats, alreadyReported)
}

// traverseWithContext traverses the AST while maintaining context about for loops
func (wga *Analyzer) traverseWithContext(n ast.Node, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	switch node := n.(type) {
	case *ast.ForStmt:
		wga.handleForStatement(node, forStack, stats, alreadyReported)
	case *ast.GoStmt:
		wga.handleGoStatement(node, forStack, stats, alreadyReported)
	case *ast.BlockStmt:
		wga.handleBlockStatement(node, forStack, stats, alreadyReported)
	case *ast.IfStmt:
		wga.handleIfStatement(node, forStack, stats, alreadyReported)
	case *ast.ExprStmt:
		wga.handleExpressionStatement(node, stats)
	}
}

// handleForStatement processes for loop statements
func (wga *Analyzer) handleForStatement(stmt *ast.ForStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if wga.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	for _, nestedStmt := range stmt.Body.List {
		wga.traverseWithReportMap(nestedStmt, append(forStack, stmt), stats, alreadyReported)
	}
}

// handleGoStatement processes goroutine statements
func (wga *Analyzer) handleGoStatement(stmt *ast.GoStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if wga.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		for _, nestedStmt := range fnLit.Body.List {
			wga.traverseWithReportMap(nestedStmt, forStack, stats, alreadyReported)
		}
	}
}

// handleBlockStatement processes block statements
func (wga *Analyzer) handleBlockStatement(stmt *ast.BlockStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	for _, nestedStmt := range stmt.List {
		wga.traverseWithContext(nestedStmt, forStack, stats, alreadyReported)
	}
}

// handleIfStatement processes if statements
func (wga *Analyzer) handleIfStatement(stmt *ast.IfStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if wga.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	wga.traverseWithContext(stmt.Body, forStack, stats, alreadyReported)
	if stmt.Else != nil {
		wga.traverseWithContext(stmt.Else, forStack, stats, alreadyReported)
	}
}

// handleExpressionStatement processes expression statements (Add, Done, Wait calls)
func (wga *Analyzer) handleExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	if wga.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	wgName := common.GetVarName(sel.X)
	if !wga.waitGroupNames[wgName] {
		return
	}

	switch sel.Sel.Name {
	case "Add":
		wga.handleAddCall(call, wgName, stats)
	case "Done":
		wga.handleDoneCall(call, wgName, stats)
	case "Wait":
		wga.handleWaitCall(call, wgName, stats)
	}
}

// traverseWithReportMap is a helper for avoiding multiple diagnostics per loop
func (wga *Analyzer) traverseWithReportMap(n ast.Node, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	switch node := n.(type) {
	case *ast.ForStmt:
		wga.handleForStatement(node, forStack, stats, alreadyReported)
	case *ast.BlockStmt:
		wga.handleBlockStatement(node, forStack, stats, alreadyReported)
	case *ast.IfStmt:
		wga.handleIfStatement(node, forStack, stats, alreadyReported)
	case *ast.ExprStmt:
		wga.handleExpressionStatement(node, stats)
	}
}
