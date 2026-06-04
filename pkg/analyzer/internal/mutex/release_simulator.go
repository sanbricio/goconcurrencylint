package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// pathReleaseSimulator checks simple paths for one matching unlock before exit.
type pathReleaseSimulator struct {
	analyzer *Checker
	varName  string
	method   string
}

// run returns the release count, whether all paths terminate, and whether the
// statement list can be modelled.
func (s *pathReleaseSimulator) run(stmts []ast.Stmt, incoming int) (count int, terminated bool, ok bool) {
	count = incoming
	for _, stmt := range stmts {
		if stmt == nil || s.analyzer.commentFilter.ShouldSkipStatement(stmt) {
			continue
		}
		switch n := stmt.(type) {
		case *ast.ExprStmt:
			if s.isMatchingRelease(n.X) {
				count++
				continue
			}
			if s.exprTerminates(n.X) {
				return releasePathTerminated(count)
			}
		case *ast.AssignStmt, *ast.DeclStmt, *ast.IncDecStmt, *ast.SendStmt:
		case *ast.ReturnStmt:
			return releasePathTerminated(count)
		case *ast.BranchStmt:
			if branchTerminatesBlock(n.Tok) {
				return releasePathTerminated(count)
			}
			return 0, false, false
		case *ast.IfStmt:
			next, term, branchOK := s.simulateIf(n, count)
			if !branchOK {
				return 0, false, false
			}
			if term {
				return next, true, true
			}
			count = next
		case *ast.BlockStmt:
			bc, bt, bo := s.run(n.List, count)
			if !bo {
				return 0, false, false
			}
			if bt {
				return bc, true, true
			}
			count = bc
		default:
			return 0, false, false
		}
	}
	return count, false, true
}

func releasePathTerminated(count int) (int, bool, bool) {
	if count != 1 {
		return 0, true, false
	}
	return count, true, true
}

// simulateIf merges the release counts from the then and else branches.
func (s *pathReleaseSimulator) simulateIf(n *ast.IfStmt, incoming int) (int, bool, bool) {
	if n.Init != nil {
		initCount, initTerm, initOK := s.run([]ast.Stmt{n.Init}, incoming)
		if !initOK {
			return 0, false, false
		}
		if initTerm {
			return initCount, true, true
		}
		incoming = initCount
	}

	thenCount, thenTerm, thenOK := s.run(n.Body.List, incoming)
	if !thenOK {
		return 0, false, false
	}

	var elseCount int
	elseTerm := false
	if n.Else != nil {
		ec, et, eo := s.runElse(n.Else, incoming)
		if !eo {
			return 0, false, false
		}
		elseCount, elseTerm = ec, et
	} else {
		// Skipping the `if` keeps the incoming count unchanged.
		elseCount = incoming
	}

	switch {
	case thenTerm && elseTerm:
		return incoming, true, true
	case thenTerm:
		return elseCount, false, true
	case elseTerm:
		return thenCount, false, true
	}

	if thenCount != elseCount {
		return 0, false, false
	}
	return thenCount, false, true
}

func (s *pathReleaseSimulator) runElse(elseNode ast.Stmt, incoming int) (int, bool, bool) {
	switch e := elseNode.(type) {
	case *ast.BlockStmt:
		return s.run(e.List, incoming)
	case *ast.IfStmt:
		return s.run([]ast.Stmt{e}, incoming)
	default:
		return 0, false, false
	}
}

func (s *pathReleaseSimulator) exprTerminates(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && s.analyzer.termination.callTerminatesExecution(call)
}

// isMatchingRelease reports whether `expr` calls the tracked unlock method.
func (s *pathReleaseSimulator) isMatchingRelease(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || s.analyzer.commentFilter.ShouldSkipCall(call) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if common.GetVarName(sel.X) != s.varName {
		return false
	}
	return sel.Sel.Name == s.method
}
