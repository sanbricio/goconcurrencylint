package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// analyzeBlock analyzes a block statement starting from the provided state and
// returns the resulting stats after executing that block.
func (c *Checker) analyzeBlock(block *ast.BlockStmt, initial map[string]*Stats) map[string]*Stats {
	return c.analyzeStatementList(block.List, initial)
}

func (c *Checker) analyzeStatementList(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := cloneStatsMap(initial)
	skip := make(map[token.Pos]bool)
	terminatingTail := c.terminatingTailByIndex(stmts)

	for i, stmt := range stmts {
		if c.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if skip[stmt.Pos()] {
			continue
		}
		if c.skipBalancedGuardedLock(stmt, stmts[i+1:], skip) {
			continue
		}
		c.analyzeStatementWithTail(stmt, blockStats, terminatingTail[i+1])
	}

	return blockStats
}

func (c *Checker) analyzeStatementWithTail(stmt ast.Stmt, stats map[string]*Stats, tailTerminates bool) {
	if _, ok := stmt.(*ast.IfStmt); !ok || !tailTerminates {
		c.analyzeStatement(stmt, stats)
		return
	}

	c.terminatingTailDepth++
	defer func() { c.terminatingTailDepth-- }()
	c.analyzeStatement(stmt, stats)
}

func (c *Checker) terminatingTailByIndex(stmts []ast.Stmt) []bool {
	tail := make([]bool, len(stmts)+1)
	for i := range slices.Backward(stmts) {
		tail[i] = tail[i+1] || c.termination.statementAlwaysTerminates(stmts[i])
	}
	return tail
}

// analyzeStatement analyzes individual statements
func (c *Checker) analyzeStatement(stmt ast.Stmt, stats map[string]*Stats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		c.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		c.analyzeExpressionStatement(s, stats)
	case *ast.AssignStmt:
		c.analyzeAssignStatement(s, stats)
	case *ast.DeclStmt:
		c.analyzeDeclStatement(s, stats)
	case *ast.DeferStmt:
		c.analyzeDeferStatement(s, stats)
	case *ast.ReturnStmt:
		c.tryLock.markReturnedChecked(s)
		c.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		c.analyzeReturnStatement(s, stats)
	case *ast.IfStmt:
		c.analyzeIfStatement(s, stats)
	case *ast.GoStmt:
		c.analyzeGoStatement(s, stats)
	case *ast.ForStmt:
		c.analyzeForStatement(s, stats)
	case *ast.RangeStmt:
		c.analyzeRangeStatement(s, stats)
	case *ast.SwitchStmt:
		c.analyzeSwitchStatement(s, stats)
	case *ast.TypeSwitchStmt:
		c.analyzeTypeSwitchStatement(s, stats)
	case *ast.SelectStmt:
		c.analyzeSelectStatement(s, stats)
	case *ast.LabeledStmt:
		if s.Label != nil {
			c.applyLabelSnapshot(s.Label.Name, stats)
		}
		c.analyzeStatement(s.Stmt, stats)
	case *ast.BranchStmt:
		if s.Tok == token.GOTO && s.Label != nil {
			c.captureGotoSnapshot(s.Label.Name, stats)
		}
	case *ast.BlockStmt:
		nestedStats := c.analyzeBlock(s, stats)
		copyStatsMap(stats, nestedStats)
	}
}

func (c *Checker) skipBalancedGuardedLock(stmt ast.Stmt, rest []ast.Stmt, skip map[token.Pos]bool) bool {
	guard, varName, methodName, ok := c.guardedMutexCall(stmt)
	if !ok || !isLockMethod(methodName) {
		return false
	}

	releaseMethod := matchingUnlockMethod(methodName)
	if releaseMethod == "" {
		return false
	}

	for _, later := range rest {
		if c.guardedReleaseMatches(later, guard, varName, releaseMethod) {
			skip[later.Pos()] = true
			return true
		}
		if c.statementMayExit(later) {
			return false
		}
	}

	return false
}

// guardedReleaseMatches reports whether `stmt` releases `varName` under
// `guard` on every reachable path.
func (c *Checker) guardedReleaseMatches(stmt ast.Stmt, guard, varName, releaseMethod string) bool {
	if laterGuard, laterVar, laterMethod, ok := c.guardedMutexCall(stmt); ok {
		return laterGuard == guard && laterVar == varName && laterMethod == releaseMethod
	}

	cond, body, ok := c.guardedIf(stmt)
	if !ok || cond != guard {
		return false
	}
	return c.bodyReleasesOnEveryPath(body, varName, releaseMethod)
}

func (c *Checker) statementMayExit(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.DeferStmt, *ast.GoStmt:
			return false
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = true
			return false
		case *ast.BranchStmt:
			if node.Tok == token.GOTO || node.Tok == token.BREAK || node.Tok == token.CONTINUE {
				found = true
				return false
			}
		case *ast.CallExpr:
			if c.termination.callTerminatesExecution(node) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// guardedIf returns the condition and body for a plain `if cond { body }`.
func (c *Checker) guardedIf(stmt ast.Stmt) (string, *ast.BlockStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return "", nil, false
	}
	return exprString(ifStmt.Cond), ifStmt.Body, true
}

// guardedMutexCall detects `if cond { mu.Lock() }` and
// `if cond { mu.Unlock() }` forms with one mutex call.
func (c *Checker) guardedMutexCall(stmt ast.Stmt) (string, string, string, bool) {
	cond, body, ok := c.guardedIf(stmt)
	if !ok {
		return "", "", "", false
	}

	var varName, methodName string
	foundCalls := 0
	for _, bodyStmt := range body.List {
		if c.statementMayExit(bodyStmt) {
			return "", "", "", false
		}
		ast.Inspect(bodyStmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || c.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			candidateVarName := common.GetVarName(sel.X)
			if !c.mutexNames[candidateVarName] && !c.rwMutexNames[candidateVarName] {
				return true
			}
			candidateMethodName := sel.Sel.Name
			if !isLockMethod(candidateMethodName) && !isUnlockMethod(candidateMethodName) {
				return true
			}
			foundCalls++
			varName = candidateVarName
			methodName = candidateMethodName
			return true
		})
	}
	if foundCalls != 1 {
		return "", "", "", false
	}

	return cond, varName, methodName, true
}

// bodyReleasesOnEveryPath reports whether `body` unlocks exactly once before
// each reachable exit.
func (c *Checker) bodyReleasesOnEveryPath(body *ast.BlockStmt, varName, methodName string) bool {
	if body == nil {
		return false
	}
	sim := pathReleaseSimulator{analyzer: c, varName: varName, method: methodName}
	count, terminated, ok := sim.run(body.List, 0)
	if !ok {
		return false
	}
	if terminated {
		return true
	}
	return count == 1
}
