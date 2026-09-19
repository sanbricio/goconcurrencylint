package mutex

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// lockedReturnSummary records result positions that are definitely returned
// with a lock held. It deliberately models only explicit, dominating
// Lock/RLock calls; uncertain control flow produces no summary rather than a
// false credit in the caller.
type lockedReturnSummary struct {
	byResult            map[int]string
	errorResultByLocked map[int]int
}

func (c *Checker) lockedReturns(fn *ast.FuncDecl) lockedReturnSummary {
	if c.lockedReturnCache == nil {
		c.lockedReturnCache = make(map[*ast.FuncDecl]lockedReturnSummary)
	}
	if summary, ok := c.lockedReturnCache[fn]; ok {
		return summary
	}
	summary := summarizeLockedReturns(fn, c.typesInfo)
	c.lockedReturnCache[fn] = summary
	return summary
}

func summarizeLockedReturns(fn *ast.FuncDecl, info *types.Info) lockedReturnSummary {
	summary := lockedReturnSummary{
		byResult:            make(map[int]string),
		errorResultByLocked: make(map[int]int),
	}
	if fn == nil || fn.Body == nil {
		return summary
	}

	var returns []*ast.ReturnStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		if ret, ok := n.(*ast.ReturnStmt); ok {
			returns = append(returns, ret)
		}
		return true
	})

	namedResults := flattenedNamedResults(fn)
	seen := make(map[int]bool)
	invalid := make(map[int]bool)
	methods := make(map[int]string)
	for _, ret := range returns {
		results := ret.Results
		if len(results) == 0 {
			results = namedResults
		}
		for index, result := range results {
			if result == nil {
				invalid[index] = true
				continue
			}
			result = common.UnwrapParenExpr(result)
			if isNilIdent(result) {
				continue
			}
			ident, ok := result.(*ast.Ident)
			if !ok || ident.Name == "_" {
				invalid[index] = true
				continue
			}
			method, ok := definiteLockBeforeReturn(fn.Body, ret, ident.Name)
			if !ok {
				invalid[index] = true
				continue
			}
			if seen[index] && methods[index] != method {
				invalid[index] = true
				continue
			}
			seen[index] = true
			methods[index] = method
		}
	}

	for index, method := range methods {
		if seen[index] && !invalid[index] {
			summary.byResult[index] = method
		}
	}

	// Record the conventional `(value, error)` correlation only when every
	// explicit return proves it: successful, locked values return nil error;
	// failure returns use a nil value and a non-nil error expression. Callers
	// can then discard the synthetic lock state in the terminating error arm.
	for lockedIndex := range summary.byResult {
		for _, errorIndex := range errorResultIndexes(fn, info) {
			if lockedResultCorrelatesWithError(returns, lockedIndex, errorIndex) {
				summary.errorResultByLocked[lockedIndex] = errorIndex
				break
			}
		}
	}
	return summary
}

func errorResultIndexes(fn *ast.FuncDecl, info *types.Info) []int {
	if fn == nil || fn.Type == nil || fn.Type.Results == nil || info == nil {
		return nil
	}
	errorType := types.Universe.Lookup("error").Type()
	var indexes []int
	index := 0
	for _, field := range fn.Type.Results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		if typ := info.TypeOf(field.Type); typ != nil && types.Identical(typ, errorType) {
			for offset := 0; offset < count; offset++ {
				indexes = append(indexes, index+offset)
			}
		}
		index += count
	}
	return indexes
}

func lockedResultCorrelatesWithError(returns []*ast.ReturnStmt, lockedIndex, errorIndex int) bool {
	sawFailure := false
	for _, ret := range returns {
		// Naked returns need value-flow analysis; decline the correlation rather
		// than infer ownership from mutable named results.
		if len(ret.Results) <= max(lockedIndex, errorIndex) {
			return false
		}
		locked := common.UnwrapParenExpr(ret.Results[lockedIndex])
		errResult := common.UnwrapParenExpr(ret.Results[errorIndex])
		if isNilIdent(locked) {
			if isNilIdent(errResult) {
				return false
			}
			sawFailure = true
			continue
		}
		if !isNilIdent(errResult) {
			return false
		}
	}
	return sawFailure
}

func flattenedNamedResults(fn *ast.FuncDecl) []ast.Expr {
	if fn == nil || fn.Type == nil || fn.Type.Results == nil {
		return nil
	}
	var results []ast.Expr
	for _, field := range fn.Type.Results.List {
		if len(field.Names) == 0 {
			results = append(results, nil)
			continue
		}
		for _, name := range field.Names {
			results = append(results, name)
		}
	}
	return results
}

func isNilIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}

// statementPathFrame identifies one statement list and the statement followed
// on the path to a return. Scanning frames from inner to outer finds the latest
// dominating explicit lock operation without treating conditional operations
// as guaranteed.
type statementPathFrame struct {
	list  []ast.Stmt
	index int
}

func definiteLockBeforeReturn(body *ast.BlockStmt, target *ast.ReturnStmt, varName string) (string, bool) {
	path, ok := findStatementPath(body.List, target)
	if !ok {
		return "", false
	}

	for i := len(path) - 1; i >= 0; i-- {
		frame := path[i]
		for j := frame.index - 1; j >= 0; j-- {
			stmt := frame.list[j]
			if method, direct := directLockEffect(stmt, varName); direct {
				switch method {
				case "Lock", "RLock":
					return method, true
				default:
					return "", false
				}
			}
			if statementContainsLockEffect(stmt, varName) {
				return "", false
			}
		}
	}
	return "", false
}

func directLockEffect(stmt ast.Stmt, varName string) (string, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return "", false
	}
	call, ok := common.UnwrapParenExpr(exprStmt.X).(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != varName {
		return "", false
	}
	switch sel.Sel.Name {
	case "Lock", "RLock", "Unlock", "RUnlock":
		return sel.Sel.Name, true
	default:
		return "", false
	}
}

func statementContainsLockEffect(stmt ast.Stmt, varName string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
		if !ok || common.GetVarName(sel.X) != varName {
			return true
		}
		switch sel.Sel.Name {
		case "Lock", "RLock", "Unlock", "RUnlock", "TryLock", "TryRLock":
			found = true
			return false
		default:
			return true
		}
	})
	return found
}

func findStatementPath(list []ast.Stmt, target ast.Stmt) ([]statementPathFrame, bool) {
	for index, stmt := range list {
		frame := statementPathFrame{list: list, index: index}
		if stmt == target {
			return []statementPathFrame{frame}, true
		}
		for _, children := range statementChildLists(stmt) {
			if path, ok := findStatementPath(children, target); ok {
				return append([]statementPathFrame{frame}, path...), true
			}
		}
	}
	return nil, false
}

// statementChildLists returns synchronous statement lists only. Function
// literals and goroutines are separate execution scopes and are never paths to
// a return belonging to the enclosing function.
func statementChildLists(stmt ast.Stmt) [][]ast.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return [][]ast.Stmt{node.List}
	case *ast.IfStmt:
		children := [][]ast.Stmt{node.Body.List}
		if block, ok := node.Else.(*ast.BlockStmt); ok {
			children = append(children, block.List)
		} else if nested, ok := node.Else.(*ast.IfStmt); ok {
			children = append(children, []ast.Stmt{nested})
		}
		return children
	case *ast.ForStmt:
		return [][]ast.Stmt{node.Body.List}
	case *ast.RangeStmt:
		return [][]ast.Stmt{node.Body.List}
	case *ast.SwitchStmt:
		return clauseStatementLists(node.Body.List)
	case *ast.TypeSwitchStmt:
		return clauseStatementLists(node.Body.List)
	case *ast.SelectStmt:
		return clauseStatementLists(node.Body.List)
	case *ast.LabeledStmt:
		return [][]ast.Stmt{{node.Stmt}}
	default:
		return nil
	}
}

func clauseStatementLists(clauses []ast.Stmt) [][]ast.Stmt {
	children := make([][]ast.Stmt, 0, len(clauses))
	for _, clause := range clauses {
		switch node := clause.(type) {
		case *ast.CaseClause:
			children = append(children, node.Body)
		case *ast.CommClause:
			children = append(children, node.Body)
		}
	}
	return children
}
