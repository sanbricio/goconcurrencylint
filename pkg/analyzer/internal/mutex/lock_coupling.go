package mutex

import (
	"go/ast"
	"go/token"
	"maps"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A loop can walk a linked structure hand over hand (lock coupling): it locks
// the next node while still holding the current one, then moves its cursor
// onto the node it just locked.
//
//	for y.next != nil {
//		n := y.next
//		n.mu.Lock()
//		y.mu.Unlock()
//		y = n
//	}
//
// The analysis tracks locks by how they are spelled, and here one spelling
// names a different lock on every pass: `y.mu` is unlocked in this iteration
// because it was locked as `n.mu` in the previous one. Read by name, each
// iteration releases a lock it never took and takes one it never releases, so
// every report about them would be noise.
//
// What identifies the walk is the cursor being reassigned, inside the loop, to
// a value whose lock is held at that moment: the lock is carried across the
// move rather than released before it. A plain traversal that releases each
// node before moving on (`n.mu.Lock(); ...; n.mu.Unlock(); n = n.next`) keeps
// one name per lock within each iteration and stays checked.

// coupledLockNames returns the tracked locks of body that a loop moves hand
// over hand: the cursor's lock and the lock of the value it moves onto, for
// each such move.
func coupledLockNames(body *ast.BlockStmt, tracked func(string) bool) map[string]bool {
	if body == nil {
		return nil
	}

	var coupled map[string]bool
	var cursors []cursorLock
	ast.Inspect(body, func(n ast.Node) bool {
		var loopBody *ast.BlockStmt
		switch loop := n.(type) {
		case *ast.ForStmt:
			loopBody = loop.Body
		case *ast.RangeStmt:
			loopBody = loop.Body
		}
		if loopBody == nil {
			return true
		}
		forEachStatementList(loopBody, func(list []ast.Stmt) {
			for index, stmt := range list {
				cursor, next, ok := cursorMove(stmt)
				if !ok {
					continue
				}
				for _, suffix := range heldLockSuffixes(list[:index], next) {
					old, moved := cursor+suffix, next+suffix
					if !tracked(old) && !tracked(moved) {
						continue
					}
					if coupled == nil {
						coupled = make(map[string]bool)
					}
					coupled[old], coupled[moved] = true, true
					cursors = append(cursors, cursorLock{cursor: cursor, suffix: suffix})
				}
			}
		})
		return true
	})

	// The walk starts from whatever the cursor was set to (`y := head`), whose
	// lock it releases under the cursor's name.
	for _, start := range cursorStarts(body, cursors) {
		if tracked(start) {
			coupled[start] = true
		}
	}
	return coupled
}

// cursorLock is a lock reached through a cursor a loop moves hand over hand.
type cursorLock struct {
	cursor string
	suffix string
}

// cursorStarts returns the locks of the values body assigns to the cursors
// outside the moves themselves, the starting points of the walks.
func cursorStarts(body *ast.BlockStmt, cursors []cursorLock) []string {
	if len(cursors) == 0 {
		return nil
	}

	var starts []string
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for index, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			value := common.GetVarName(assign.Rhs[index])
			if value == "?" {
				continue
			}
			for _, c := range cursors {
				if c.cursor == ident.Name {
					starts = append(starts, value+c.suffix)
				}
			}
		}
		return true
	})
	return starts
}

// cursorMove reports whether stmt moves a cursor variable onto another value
// (`y = n`, `i = i.parent`), returning the cursor and the value's spelling.
func cursorMove(stmt ast.Stmt) (string, string, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return "", "", false
	}
	cursor, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || cursor.Name == "_" {
		return "", "", false
	}
	next := common.GetVarName(assign.Rhs[0])
	if next == "?" || next == cursor.Name {
		return "", "", false
	}
	return cursor.Name, next, true
}

// heldLockSuffixes returns, for the locks below base (base itself or a field
// path under it), the paths that the straight-line statements before the move
// leave held: acquired and not released again.
func heldLockSuffixes(before []ast.Stmt, base string) []string {
	held := make(map[string]int)
	for _, stmt := range before {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := common.UnwrapParenExpr(expr.X).(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
		if !ok {
			continue
		}
		suffix, ok := strings.CutPrefix(common.GetVarName(sel.X), base)
		if !ok || (suffix != "" && !strings.HasPrefix(suffix, ".")) {
			continue
		}
		switch lockMethodSide(sel.Sel.Name) {
		case acquireLockSide:
			held[suffix]++
		case releaseLockSide:
			held[suffix]--
		}
	}

	var suffixes []string
	for suffix, count := range held {
		if count > 0 {
			suffixes = append(suffixes, suffix)
		}
	}
	return suffixes
}

// forEachStatementList calls visit for every statement list nested in block,
// block's own included, without entering function literals.
func forEachStatementList(block *ast.BlockStmt, visit func([]ast.Stmt)) {
	ast.Inspect(block, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BlockStmt:
			visit(node.List)
		case *ast.CaseClause:
			visit(node.Body)
		case *ast.CommClause:
			visit(node.Body)
		}
		return true
	})
}

// withoutLockNames returns names without the ones in drop, copying only when
// something is dropped: the tracked sets are shared and never modified.
func withoutLockNames(names, drop map[string]bool) map[string]bool {
	var result map[string]bool
	for name := range drop {
		if !names[name] {
			continue
		}
		if result == nil {
			result = maps.Clone(names)
		}
		delete(result, name)
	}
	if result == nil {
		return names
	}
	return result
}
