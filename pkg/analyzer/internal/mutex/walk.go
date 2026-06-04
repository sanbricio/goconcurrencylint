package mutex

import (
	"go/ast"
	"go/token"
	"slices"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// analyzeBlock analyzes a block statement starting from the provided state and
// returns the resulting stats after executing that block.
func (ma *Checker) analyzeBlock(block *ast.BlockStmt, initial map[string]*Stats) map[string]*Stats {
	return ma.analyzeStatementList(block.List, initial)
}

func (ma *Checker) analyzeStatementList(stmts []ast.Stmt, initial map[string]*Stats) map[string]*Stats {
	blockStats := cloneStatsMap(initial)
	skip := make(map[token.Pos]bool)
	terminatingTail := ma.terminatingTailByIndex(stmts)

	for i, stmt := range stmts {
		if ma.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		if skip[stmt.Pos()] {
			continue
		}
		if ma.skipBalancedGuardedLock(stmt, stmts[i+1:], skip) {
			continue
		}
		ma.analyzeStatementWithTail(stmt, blockStats, terminatingTail[i+1])
	}

	return blockStats
}

func (ma *Checker) analyzeStatementWithTail(stmt ast.Stmt, stats map[string]*Stats, tailTerminates bool) {
	if _, ok := stmt.(*ast.IfStmt); !ok || !tailTerminates {
		ma.analyzeStatement(stmt, stats)
		return
	}

	ma.terminatingTailDepth++
	defer func() { ma.terminatingTailDepth-- }()
	ma.analyzeStatement(stmt, stats)
}

func (ma *Checker) terminatingTailByIndex(stmts []ast.Stmt) []bool {
	tail := make([]bool, len(stmts)+1)
	for i := range slices.Backward(stmts) {
		tail[i] = tail[i+1] || ma.termination.statementAlwaysTerminates(stmts[i])
	}
	return tail
}

// analyzeStatement analyzes individual statements
func (ma *Checker) analyzeStatement(stmt ast.Stmt, stats map[string]*Stats) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		ma.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeExpressionStatement(s, stats)
	case *ast.AssignStmt:
		ma.analyzeAssignStatement(s, stats)
	case *ast.DeclStmt:
		ma.analyzeDeclStatement(s, stats)
	case *ast.DeferStmt:
		ma.analyzeDeferStatement(s, stats)
	case *ast.ReturnStmt:
		ma.tryLock.markReturnedChecked(s)
		ma.panicDetector.reportPotentialPanicWhileLocked(s, stats)
		ma.analyzeReturnStatement(s, stats)
	case *ast.IfStmt:
		ma.analyzeIfStatement(s, stats)
	case *ast.GoStmt:
		ma.analyzeGoStatement(s, stats)
	case *ast.ForStmt:
		ma.analyzeForStatement(s, stats)
	case *ast.RangeStmt:
		ma.analyzeRangeStatement(s, stats)
	case *ast.SwitchStmt:
		ma.analyzeSwitchStatement(s, stats)
	case *ast.TypeSwitchStmt:
		ma.analyzeTypeSwitchStatement(s, stats)
	case *ast.SelectStmt:
		ma.analyzeSelectStatement(s, stats)
	case *ast.LabeledStmt:
		if s.Label != nil {
			ma.applyLabelSnapshot(s.Label.Name, stats)
		}
		ma.analyzeStatement(s.Stmt, stats)
	case *ast.BranchStmt:
		if s.Tok == token.GOTO && s.Label != nil {
			ma.captureGotoSnapshot(s.Label.Name, stats)
		}
	case *ast.BlockStmt:
		nestedStats := ma.analyzeBlock(s, stats)
		copyStatsMap(stats, nestedStats)
	}
}

func (ma *Checker) skipBalancedGuardedLock(stmt ast.Stmt, rest []ast.Stmt, skip map[token.Pos]bool) bool {
	guard, varName, methodName, ok := ma.guardedMutexCall(stmt)
	if !ok || !isLockMethod(methodName) {
		return false
	}

	releaseMethod := matchingUnlockMethod(methodName)
	if releaseMethod == "" {
		return false
	}

	for _, later := range rest {
		if ma.guardedReleaseMatches(later, guard, varName, releaseMethod) {
			skip[later.Pos()] = true
			return true
		}
		if ma.statementMayExit(later) {
			return false
		}
	}

	return false
}

// guardedReleaseMatches reports whether `stmt` releases `varName` under
// `guard` on every reachable path.
func (ma *Checker) guardedReleaseMatches(stmt ast.Stmt, guard, varName, releaseMethod string) bool {
	if laterGuard, laterVar, laterMethod, ok := ma.guardedMutexCall(stmt); ok {
		return laterGuard == guard && laterVar == varName && laterMethod == releaseMethod
	}

	cond, body, ok := ma.guardedIf(stmt)
	if !ok || cond != guard {
		return false
	}
	return ma.bodyReleasesOnEveryPath(body, varName, releaseMethod)
}

func (ma *Checker) statementMayExit(stmt ast.Stmt) bool {
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
			if ma.termination.callTerminatesExecution(node) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// guardedIf returns the condition and body for a plain `if cond { body }`.
func (ma *Checker) guardedIf(stmt ast.Stmt) (string, *ast.BlockStmt, bool) {
	ifStmt, ok := stmt.(*ast.IfStmt)
	if !ok || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return "", nil, false
	}
	return exprString(ifStmt.Cond), ifStmt.Body, true
}

// guardedMutexCall detects `if cond { mu.Lock() }` and
// `if cond { mu.Unlock() }` forms with one mutex call.
func (ma *Checker) guardedMutexCall(stmt ast.Stmt) (string, string, string, bool) {
	cond, body, ok := ma.guardedIf(stmt)
	if !ok {
		return "", "", "", false
	}

	var varName, methodName string
	foundCalls := 0
	for _, bodyStmt := range body.List {
		if ma.statementMayExit(bodyStmt) {
			return "", "", "", false
		}
		ast.Inspect(bodyStmt, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || ma.commentFilter.ShouldSkipCall(call) {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			candidateVarName := common.GetVarName(sel.X)
			if !ma.mutexNames[candidateVarName] && !ma.rwMutexNames[candidateVarName] {
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
func (ma *Checker) bodyReleasesOnEveryPath(body *ast.BlockStmt, varName, methodName string) bool {
	if body == nil {
		return false
	}
	sim := pathReleaseSimulator{analyzer: ma, varName: varName, method: methodName}
	count, terminated, ok := sim.run(body.List, 0)
	if !ok {
		return false
	}
	if terminated {
		return true
	}
	return count == 1
}
