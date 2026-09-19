package mutex

import (
	"go/ast"
	"slices"

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

	sibling := c.sameConditionSibling(stmt)
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

	sibling := c.sameConditionSibling(stmt)
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

// mirrorRole says how an if/else relates to the `if` on the same condition that
// mirrors it.
type mirrorRole int

const (
	// noMirror: the branches are not paired by a same-condition `if`.
	noMirror mirrorRole = iota
	// mirrorReleasesLater: these branches take the locks, the mirror releases them.
	mirrorReleasesLater
	// mirrorAcquiredEarlier: these branches release what the mirror took.
	mirrorAcquiredEarlier
)

// sameConditionMirrorRole reports how both halves of an if/else are paired by
// another `if` on the same condition. Both halves must be paired the same way:
// a pair where only one branch lines up is not the mirrored shape and its
// imbalance is real.
func (c *Checker) sameConditionMirrorRole(stmt *ast.IfStmt, initial, thenStats, elseStats map[string]*Stats) mirrorRole {
	sibling := c.sameConditionSibling(stmt)
	if sibling == nil || sibling.Else == nil {
		return noMirror
	}
	elseBody := elseBranchBlock(sibling.Else)
	if elseBody == nil {
		return noMirror
	}

	switch {
	case c.branchDeltaMatched(initial, thenStats, sibling.Body, true) &&
		c.branchDeltaMatched(initial, elseStats, elseBody, true):
		return mirrorReleasesLater
	case c.branchDeltaMatched(initial, thenStats, sibling.Body, false) &&
		c.branchDeltaMatched(initial, elseStats, elseBody, false):
		return mirrorAcquiredEarlier
	default:
		return noMirror
	}
}

func (c *Checker) branchDeltaMatched(initial, final map[string]*Stats, body *ast.BlockStmt, releases bool) bool {
	var delta map[string]lockKind
	if releases {
		delta = heldLockDelta(c.mutexNames, c.rwMutexNames, initial, final)
	} else {
		delta = releasedLockDelta(c.mutexNames, c.rwMutexNames, initial, final)
	}
	if len(delta) == 0 || body == nil {
		return false
	}
	for name, kind := range delta {
		method := kind.acquire()
		if releases {
			method = kind.release()
		}
		if countMutexMethodCalls(body, name, method) == 0 {
			return false
		}
	}
	return true
}

func elseBranchBlock(stmt ast.Stmt) *ast.BlockStmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return node
	case *ast.IfStmt:
		return &ast.BlockStmt{List: []ast.Stmt{node}}
	default:
		return nil
	}
}

// sameConditionSibling returns the `if` that mirrors stmt: the one in stmt's own
// statement list when there is one, otherwise the mirror of an `if` that
// encloses it.
//
// The nested form appears whenever a caller decides once whether to lock and
// then repeats that decision to release:
//
//	if doLock {
//		if cacheEnabled { s.Lock() } else { s.RLock() }
//	}
//	...
//	if doLock {
//		if cacheEnabled { s.Unlock() } else { s.RUnlock() }
//	}
//
// The inner `if` has no sibling of its own, but the outer one does, and the
// same argument applies to it: both blocks run together or neither does.
func (c *Checker) sameConditionSibling(stmt *ast.IfStmt) *ast.IfStmt {
	if sibling := c.siblingIfWithSameCondition(stmt); sibling != nil {
		return sibling
	}

	for _, enclosing := range c.enclosingIfStatements(stmt) {
		sibling := c.siblingIfWithSameCondition(enclosing)
		if sibling == nil {
			continue
		}
		candidate, ok := correspondingStatement(enclosing.Body.List, stmt, sibling.Body.List).(*ast.IfStmt)
		if !ok || !sameStableCondition(stmt, candidate) || (stmt.Else == nil) != (candidate.Else == nil) {
			continue
		}
		return candidate
	}

	return nil
}

// enclosingIfStatements returns the `if` statements that contain target,
// innermost first.
func (c *Checker) enclosingIfStatements(target ast.Stmt) []*ast.IfStmt {
	if c.function == nil || c.function.Body == nil || target == nil {
		return nil
	}

	c.ensureControlFlowStructure()
	if ifStmt, ok := target.(*ast.IfStmt); ok && c.enclosingIfs != nil {
		return slices.Clone(c.enclosingIfs[ifStmt])
	}

	var enclosing []*ast.IfStmt
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok || ifStmt == target {
			return true
		}
		if ifStmt.Pos() <= target.Pos() && target.End() <= ifStmt.End() {
			enclosing = append(enclosing, ifStmt)
		}
		return true
	})

	// Inspect walks in pre-order, so the outermost `if` comes first; the nearest
	// enclosing one is the most likely mirror.
	slices.Reverse(enclosing)
	return enclosing
}

func sameStableCondition(a, b *ast.IfStmt) bool {
	if a == nil || b == nil || a.Init != nil || b.Init != nil {
		return false
	}
	x, ok := stableConditionKey(a.Cond)
	if !ok {
		return false
	}
	y, ok := stableConditionKey(b.Cond)
	return ok && x == y
}

type relativeStatementStep struct {
	index int
	child int
}

func correspondingStatement(source []ast.Stmt, target ast.Stmt, destination []ast.Stmt) ast.Stmt {
	path, ok := relativeStatementPath(source, target)
	if !ok {
		return nil
	}
	list := destination
	for _, step := range path {
		if step.index < 0 || step.index >= len(list) {
			return nil
		}
		stmt := list[step.index]
		if step.child < 0 {
			return stmt
		}
		children := statementChildLists(stmt)
		if step.child >= len(children) {
			return nil
		}
		list = children[step.child]
	}
	return nil
}

func relativeStatementPath(list []ast.Stmt, target ast.Stmt) ([]relativeStatementStep, bool) {
	for index, stmt := range list {
		if stmt == target {
			return []relativeStatementStep{{index: index, child: -1}}, true
		}
		for childIndex, children := range statementChildLists(stmt) {
			if path, ok := relativeStatementPath(children, target); ok {
				return append([]relativeStatementStep{{index: index, child: childIndex}}, path...), true
			}
		}
	}
	return nil, false
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
	c.ensureControlFlowStructure()
	if location, ok := c.statementLocations[target]; ok {
		return location.list, location.index, true
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

type statementLocation struct {
	list  []ast.Stmt
	index int
}

func (c *Checker) ensureControlFlowStructure() {
	if c.funcAnalysis == nil || c.statementLocations != nil {
		return
	}
	c.indexControlFlowStructure()
}

func (fa *funcAnalysis) indexControlFlowStructure() {
	fa.statementLocations = make(map[ast.Stmt]statementLocation)
	fa.enclosingIfs = make(map[*ast.IfStmt][]*ast.IfStmt)
	fa.ifInitOwners = make(map[ast.Stmt]*ast.IfStmt)
	if fa.function == nil || fa.function.Body == nil {
		return
	}

	var stack []ast.Node
	ast.Inspect(fa.function.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}

		if current, ok := n.(*ast.IfStmt); ok {
			if current.Init != nil {
				fa.ifInitOwners[current.Init] = current
			}
			for i := len(stack) - 1; i >= 0; i-- {
				if _, boundary := stack[i].(*ast.FuncLit); boundary {
					break
				}
				if parent, ok := stack[i].(*ast.IfStmt); ok {
					fa.enclosingIfs[current] = append(fa.enclosingIfs[current], parent)
				}
			}
		}
		if list := statementListOf(n); len(list) > 0 {
			for index, stmt := range list {
				fa.statementLocations[stmt] = statementLocation{list: list, index: index}
			}
		}

		stack = append(stack, n)
		return true
	})
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
