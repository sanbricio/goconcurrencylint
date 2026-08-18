package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A lock taken in `if cond { mu.Lock() }` and released in a later
// `if cond { mu.Unlock() }` is balanced: both blocks run together or not at
// all. siblingIfWithSameCondition finds the second block; the pairing breaks if
// anything in between reassigns the condition or ends execution.

// releasedByLaterSiblingWithSameCondition reports whether the locks this
// branch is left holding are released by a later `if` on the same condition.
func (c *Checker) releasedByLaterSiblingWithSameCondition(stmt *ast.IfStmt, initial, final map[string]*Stats) bool {
	held := heldLockDelta(c.mutexNames, c.rwMutexNames, initial, final)
	if len(held) == 0 {
		return false
	}

	sibling := c.siblingIfWithSameCondition(stmt)
	if sibling == nil {
		return false
	}

	for name, kind := range held {
		if countMutexMethodCalls(sibling.Body, name, kind.release()) == 0 {
			return false
		}
	}
	return true
}

// acquiredByEarlierSiblingWithSameCondition is the mirror: the releases in this
// branch belong to an earlier `if` on the same condition that took the locks.
func (c *Checker) acquiredByEarlierSiblingWithSameCondition(stmt *ast.IfStmt, initial, final map[string]*Stats) bool {
	released := releasedLockDelta(c.mutexNames, c.rwMutexNames, initial, final)
	if len(released) == 0 {
		return false
	}

	sibling := c.siblingIfWithSameCondition(stmt)
	if sibling == nil {
		return false
	}

	for name, kind := range released {
		if countMutexMethodCalls(sibling.Body, name, kind.acquire()) == 0 {
			return false
		}
	}
	return true
}

type lockKind int

const (
	writeLock lockKind = iota
	readLock
)

func (k lockKind) acquire() string {
	if k == readLock {
		return "RLock"
	}
	return "Lock"
}

func (k lockKind) release() string {
	if k == readLock {
		return "RUnlock"
	}
	return "Unlock"
}

// heldLockDelta lists the locks final holds beyond initial.
func heldLockDelta(mutexNames, rwMutexNames map[string]bool, initial, final map[string]*Stats) map[string]lockKind {
	return lockDelta(mutexNames, rwMutexNames, initial, final, false)
}

// releasedLockDelta lists the locks final holds fewer of than initial.
func releasedLockDelta(mutexNames, rwMutexNames map[string]bool, initial, final map[string]*Stats) map[string]lockKind {
	return lockDelta(mutexNames, rwMutexNames, initial, final, true)
}

func lockDelta(mutexNames, rwMutexNames map[string]bool, initial, final map[string]*Stats, released bool) map[string]lockKind {
	delta := make(map[string]lockKind)
	record := func(name string, readAllowed bool) {
		before, after := statsOrEmpty(initial[name]), statsOrEmpty(final[name])
		if released {
			before, after = after, before
		}
		if after.lock > before.lock {
			delta[name] = writeLock
			return
		}
		if readAllowed && after.rlock > before.rlock {
			delta[name] = readLock
		}
	}

	for name := range mutexNames {
		record(name, false)
	}
	for name := range rwMutexNames {
		record(name, true)
	}
	return delta
}

func statsOrEmpty(stats *Stats) *Stats {
	if stats == nil {
		return &Stats{}
	}
	return stats
}

// siblingIfWithSameCondition returns the other `if` in the same statement list
// that tests the very same condition, provided nothing between the two can
// have changed the answer.
func (c *Checker) siblingIfWithSameCondition(stmt *ast.IfStmt) *ast.IfStmt {
	key, ok := stableConditionKey(stmt.Cond)
	if !ok || stmt.Init != nil {
		return nil
	}

	list, index, ok := c.statementListContaining(stmt)
	if !ok {
		return nil
	}

	for offset := 1; index+offset < len(list) || index-offset >= 0; offset++ {
		for _, i := range []int{index + offset, index - offset} {
			if i < 0 || i >= len(list) || i == index {
				continue
			}
			if sibling := matchingConditionalIf(list[i], key); sibling != nil {
				if conditionMayChangeBetween(list, index, i, key.root) ||
					c.executionEndsBetween(list, index, i) {
					return nil
				}
				return sibling
			}
		}
	}
	return nil
}

// conditionKey identifies a condition that is cheap to compare and cheap to
// invalidate: a plain variable read, optionally negated.
type conditionKey struct {
	root    string
	name    string
	negated bool
}

func stableConditionKey(cond ast.Expr) (conditionKey, bool) {
	expr := common.UnwrapParenExpr(cond)
	negated := false
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op.String() == "!" {
		negated = true
		expr = common.UnwrapParenExpr(unary.X)
	}

	switch value := expr.(type) {
	case *ast.Ident:
		return conditionKey{root: value.Name, name: value.Name, negated: negated}, true
	case *ast.SelectorExpr:
		name := common.GetVarName(value)
		if name == "" || name == "?" {
			return conditionKey{}, false
		}
		return conditionKey{root: rootIdentName(value), name: name, negated: negated}, true
	default:
		return conditionKey{}, false
	}
}

func rootIdentName(expr ast.Expr) string {
	for {
		switch value := common.UnwrapParenExpr(expr).(type) {
		case *ast.Ident:
			return value.Name
		case *ast.SelectorExpr:
			expr = value.X
		default:
			return ""
		}
	}
}

func matchingConditionalIf(stmt ast.Stmt, key conditionKey) *ast.IfStmt {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return nil
	}
	candidate, ok := stableConditionKey(ifStmt.Cond)
	if !ok || candidate != key {
		return nil
	}
	return ifStmt
}

// conditionMayChangeBetween reports whether anything between the two indexes
// assigns to the name the condition reads.
func conditionMayChangeBetween(list []ast.Stmt, from, to int, root string) bool {
	if root == "" {
		return true
	}
	if from > to {
		from, to = to, from
	}

	for i := from + 1; i < to; i++ {
		if statementAssignsIdent(list[i], root) {
			return true
		}
	}
	return false
}

func statementAssignsIdent(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if rootIdentName(lhs) == name {
					found = true
					return false
				}
			}
		case *ast.IncDecStmt:
			if rootIdentName(node.X) == name {
				found = true
				return false
			}
		case *ast.UnaryExpr:
			if node.Op.String() == "&" && rootIdentName(node.X) == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// statementListContaining returns the list the statement belongs to and its
// index in it. Function literals are traversed: the pattern is as common inside
// a goroutine body as at the top level.
func (c *Checker) statementListContaining(target ast.Stmt) ([]ast.Stmt, int, bool) {
	if c.function == nil || c.function.Body == nil {
		return nil, 0, false
	}

	var (
		list  []ast.Stmt
		index int
		found bool
	)
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		for i, stmt := range statementListOf(n) {
			if stmt == target {
				list, index, found = statementListOf(n), i, true
				return false
			}
		}
		return true
	})
	return list, index, found
}

// statementListOf returns the statements a node holds directly.
func statementListOf(n ast.Node) []ast.Stmt {
	switch node := n.(type) {
	case *ast.BlockStmt:
		return node.List
	case *ast.CaseClause:
		return node.Body
	case *ast.CommClause:
		return node.Body
	default:
		return nil
	}
}

// executionEndsBetween reports whether control never reaches the second block:
// a Fatal, panic or return in between means the pairing is only apparent.
func (c *Checker) executionEndsBetween(list []ast.Stmt, from, to int) bool {
	if from > to {
		from, to = to, from
	}
	if to-from < 2 {
		return false
	}
	between := list[from+1 : to]
	return c.termination.blockAlwaysTerminates(&ast.BlockStmt{List: between})
}
