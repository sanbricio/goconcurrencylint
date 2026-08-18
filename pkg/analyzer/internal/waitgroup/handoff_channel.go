package waitgroup

import (
	"go/ast"
	"go/token"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// handoffIndex records the Adds followed by a select that sends the work on a
// channel and gives the count back with a Done when the send cannot proceed.
// Both paths are complete: the receiver owns the Done, or it happens on the
// spot. Built in one pass — asking per WaitGroup and per Add rescanned the
// whole body each time.
type handoffIndex struct {
	byName map[string]map[token.Pos]bool
}

func (b *balanceValidator) newHandoffIndex() *handoffIndex {
	index := &handoffIndex{byName: make(map[string]map[token.Pos]bool)}
	fn := b.function
	if fn == nil || fn.Body == nil {
		return index
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		var list []ast.Stmt
		switch node := n.(type) {
		case *ast.BlockStmt:
			list = node.List
		case *ast.CaseClause:
			list = node.Body
		case *ast.CommClause:
			list = node.Body
		default:
			return true
		}

		for i := 0; i+1 < len(list); i++ {
			wgName, ok := waitGroupAddStatementName(list[i])
			if !ok {
				continue
			}
			sel, ok := list[i+1].(*ast.SelectStmt)
			if !ok || !b.selectHandsOffWaitGroup(sel, wgName) {
				continue
			}
			if index.byName[wgName] == nil {
				index.byName[wgName] = make(map[token.Pos]bool)
			}
			index.byName[wgName][list[i].Pos()] = true
		}
		return true
	})
	return index
}

// addIsHandedOffThroughChannel reports whether an Add hands the work off to a
// channel. A valid addPos narrows the question to that one Add; token.NoPos
// asks whether any Add of this WaitGroup is handed off this way.
func (b *balanceValidator) addIsHandedOffThroughChannel(wgName string, addPos token.Pos) bool {
	if b.handoff == nil {
		b.handoff = b.newHandoffIndex()
	}

	positions := b.handoff.byName[wgName]
	if len(positions) == 0 {
		return false
	}
	if !addPos.IsValid() {
		return true
	}
	return positions[addPos]
}

// selectHandsOffWaitGroup reports whether a select sends on a channel in one
// branch and gives the count back with a Done in another.
func (b *balanceValidator) selectHandsOffWaitGroup(sel *ast.SelectStmt, wgName string) bool {
	if sel.Body == nil {
		return false
	}

	sends := false
	returnsCount := false
	for _, stmt := range sel.Body.List {
		clause, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}
		if _, ok := clause.Comm.(*ast.SendStmt); ok {
			sends = true
			continue
		}
		if b.clauseReturnsCount(clause.Body, wgName) {
			returnsCount = true
		}
	}
	return sends && returnsCount
}

// clauseReturnsCount reports whether a select branch gives the count back,
// through containsDoneCall so every Done form the checker knows counts here.
func (b *balanceValidator) clauseReturnsCount(stmts []ast.Stmt, wgName string) bool {
	for _, stmt := range stmts {
		if b.containsDoneCall(stmt, wgName) {
			return true
		}
	}
	return false
}

// waitGroupAddStatementName returns the WaitGroup an `x.Add(...)` statement
// counts up.
func waitGroupAddStatementName(stmt ast.Stmt) (string, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return "", false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" {
		return "", false
	}
	name := common.GetVarName(sel.X)
	if name == "" || name == "?" {
		return "", false
	}
	return name, true
}
