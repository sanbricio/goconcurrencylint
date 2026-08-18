package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// findDeferDoneCalls identifies defer Done calls to avoid counting them as regular Done calls.
func (c *Checker) findDeferDoneCalls(stats map[string]*Stats) {
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		deferStmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}

		if c.commentFilter.ShouldSkipCall(deferStmt.Call) {
			return true
		}

		if call, ok := deferStmt.Call.Fun.(*ast.SelectorExpr); ok {
			if call.Sel.Name == "Done" {
				wgName := common.GetVarName(call.X)
				if c.waitGroupNames[wgName] && c.isWaitGroupReceiver(call.X) {
					stats[wgName].deferDoneCalls = append(stats[wgName].deferDoneCalls, deferStmt.Call.Pos())
				}
			}
			return true
		}

		if fnlit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok {
			c.findDoneInFunctionLiteral(fnlit.Body, stats)
		}

		return true
	})
}

// findDoneInFunctionLiteral looks for Done calls within function literals.
func (c *Checker) findDoneInFunctionLiteral(body *ast.BlockStmt, stats map[string]*Stats) {
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if c.commentFilter.ShouldSkipCall(call) {
				return true
			}

			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" {
				wgName := common.GetVarName(sel.X)
				if c.waitGroupNames[wgName] && c.isWaitGroupReceiver(sel.X) {
					stats[wgName].deferDoneCalls = append(stats[wgName].deferDoneCalls, call.Pos())
				}
			}
		}
		return true
	})
}

// collectCalls collects all Add, Done, and Wait calls in the function
func (c *Checker) collectCalls(stats map[string]*Stats) {
	alreadyReported := make(map[token.Pos]bool)
	c.traverseWithContext(c.function.Body, nil, stats, alreadyReported)
}

// traverseWithContext traverses the AST while maintaining context about for loops
func (c *Checker) traverseWithContext(n ast.Node, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	switch node := n.(type) {
	case *ast.ForStmt:
		c.handleForStatement(node, forStack, stats, alreadyReported)
	case *ast.RangeStmt:
		c.handleRangeStatement(node, forStack, stats, alreadyReported)
	case *ast.GoStmt:
		c.handleGoStatement(node, forStack, stats, alreadyReported)
	case *ast.BlockStmt:
		c.handleBlockStatement(node, forStack, stats, alreadyReported)
	case *ast.IfStmt:
		c.handleIfStatement(node, forStack, stats, alreadyReported)
	case *ast.SwitchStmt:
		c.handleSwitchStatement(node, forStack, stats, alreadyReported)
	case *ast.TypeSwitchStmt:
		c.handleTypeSwitchStatement(node, forStack, stats, alreadyReported)
	case *ast.SelectStmt:
		c.handleSelectStatement(node, forStack, stats, alreadyReported)
	case *ast.LabeledStmt:
		c.traverseWithContext(node.Stmt, forStack, stats, alreadyReported)
	case *ast.ExprStmt:
		if c.handleImmediatelyInvokedFunction(node, forStack, stats, alreadyReported) {
			return
		}
		c.handleExpressionStatement(node, stats)
	}
}

// handleForStatement processes for loop statements.
func (c *Checker) handleForStatement(stmt *ast.ForStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	for _, nestedStmt := range stmt.Body.List {
		c.traverseWithReportMap(nestedStmt, append(forStack, stmt), stats, alreadyReported)
	}
}

// handleRangeStatement processes range loop statements.
func (c *Checker) handleRangeStatement(stmt *ast.RangeStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	for _, nestedStmt := range stmt.Body.List {
		c.traverseWithReportMap(nestedStmt, forStack, stats, alreadyReported)
	}
}

// handleGoStatement processes goroutine statements.
func (c *Checker) handleGoStatement(stmt *ast.GoStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	if fnLit, ok := stmt.Call.Fun.(*ast.FuncLit); ok {
		for _, nestedStmt := range fnLit.Body.List {
			c.traverseWithReportMap(nestedStmt, forStack, stats, alreadyReported)
		}
	}
}

// handleBlockStatement processes block statements.
func (c *Checker) handleBlockStatement(stmt *ast.BlockStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	for _, nestedStmt := range stmt.List {
		c.traverseWithContext(nestedStmt, forStack, stats, alreadyReported)
	}
}

// handleIfStatement processes if statements.
func (c *Checker) handleIfStatement(stmt *ast.IfStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}

	c.traverseWithContext(stmt.Body, forStack, stats, alreadyReported)
	if stmt.Else != nil {
		c.traverseWithContext(stmt.Else, forStack, stats, alreadyReported)
	}
}

func (c *Checker) handleSwitchStatement(stmt *ast.SwitchStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}
	if stmt.Init != nil {
		c.traverseWithContext(stmt.Init, forStack, stats, alreadyReported)
	}
	for _, nestedStmt := range stmt.Body.List {
		if cc, ok := nestedStmt.(*ast.CaseClause); ok {
			for _, caseStmt := range cc.Body {
				c.traverseWithContext(caseStmt, forStack, stats, alreadyReported)
			}
		}
	}
}

func (c *Checker) handleTypeSwitchStatement(stmt *ast.TypeSwitchStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}
	if stmt.Init != nil {
		c.traverseWithContext(stmt.Init, forStack, stats, alreadyReported)
	}
	if stmt.Assign != nil {
		c.traverseWithContext(stmt.Assign, forStack, stats, alreadyReported)
	}
	for _, nestedStmt := range stmt.Body.List {
		if cc, ok := nestedStmt.(*ast.CaseClause); ok {
			for _, caseStmt := range cc.Body {
				c.traverseWithContext(caseStmt, forStack, stats, alreadyReported)
			}
		}
	}
}

func (c *Checker) handleSelectStatement(stmt *ast.SelectStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
		return
	}
	for _, nestedStmt := range stmt.Body.List {
		if cc, ok := nestedStmt.(*ast.CommClause); ok {
			if cc.Comm != nil {
				c.traverseWithContext(cc.Comm, forStack, stats, alreadyReported)
			}
			for _, caseStmt := range cc.Body {
				c.traverseWithContext(caseStmt, forStack, stats, alreadyReported)
			}
		}
	}
}

// handleExpressionStatement processes expression statements (Add, Done, Wait calls).
func (c *Checker) handleExpressionStatement(stmt *ast.ExprStmt, stats map[string]*Stats) {
	if c.commentFilter.ShouldSkipStatement(stmt) {
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
	if !c.waitGroupNames[wgName] {
		return
	}
	if !c.isWaitGroupReceiver(sel.X) {
		return
	}

	switch sel.Sel.Name {
	case "Add":
		c.handleAddCall(call, wgName, stats)
	case "Done":
		c.handleDoneCall(call, wgName, stats)
	case "Go":
		c.handleGoCall(call, wgName, stats)
	case "Wait":
		c.handleWaitCall(call, wgName, stats)
	}
}

// isWaitGroupReceiver confirms that selector receiver x statically has type
// sync.WaitGroup (or *sync.WaitGroup). waitGroupNames is keyed by bare variable
// name, so two identically-named variables of different types in the same
// function — e.g. a real sync.WaitGroup and a custom type that also exposes
// Add/Done/Wait — would otherwise be conflated and the custom type's calls
// miscounted. When the type cannot be resolved we fall back to the name-based
// decision to avoid regressing inputs without full type information.
func (c *Checker) isWaitGroupReceiver(x ast.Expr) bool {
	if c.typesInfo == nil {
		return true
	}
	typ := c.typesInfo.TypeOf(x)
	if typ == nil {
		return true
	}
	return common.IsWaitGroup(typ)
}

func (c *Checker) handleImmediatelyInvokedFunction(stmt *ast.ExprStmt, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) bool {
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}

	fnLit, ok := call.Fun.(*ast.FuncLit)
	if !ok || fnLit.Body == nil {
		return false
	}

	for _, nestedStmt := range fnLit.Body.List {
		c.traverseWithReportMap(nestedStmt, forStack, stats, alreadyReported)
	}
	return true
}

// traverseWithReportMap is a helper for avoiding multiple diagnostics per loop
func (c *Checker) traverseWithReportMap(n ast.Node, forStack []*ast.ForStmt, stats map[string]*Stats, alreadyReported map[token.Pos]bool) {
	switch node := n.(type) {
	case *ast.ForStmt:
		c.handleForStatement(node, forStack, stats, alreadyReported)
	case *ast.RangeStmt:
		c.handleRangeStatement(node, forStack, stats, alreadyReported)
	case *ast.BlockStmt:
		c.handleBlockStatement(node, forStack, stats, alreadyReported)
	case *ast.IfStmt:
		c.handleIfStatement(node, forStack, stats, alreadyReported)
	case *ast.SwitchStmt:
		c.handleSwitchStatement(node, forStack, stats, alreadyReported)
	case *ast.TypeSwitchStmt:
		c.handleTypeSwitchStatement(node, forStack, stats, alreadyReported)
	case *ast.SelectStmt:
		c.handleSelectStatement(node, forStack, stats, alreadyReported)
	case *ast.LabeledStmt:
		c.traverseWithReportMap(node.Stmt, forStack, stats, alreadyReported)
	case *ast.ExprStmt:
		if c.handleImmediatelyInvokedFunction(node, forStack, stats, alreadyReported) {
			return
		}
		c.handleExpressionStatement(node, stats)
	}
}
