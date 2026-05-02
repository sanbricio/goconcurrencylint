package mutex

import (
	"go/ast"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
)

func (ma *Analyzer) analyzeAssignStatement(stmt *ast.AssignStmt, stats map[string]*Stats) {
	ma.recordCollectionLengthsFromAssign(stmt)
	ma.reportPotentialPanicWhileLocked(stmt, stats)

	for i, rhs := range stmt.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		result := ma.tryLockResultFromCall(call)
		if result == nil {
			continue
		}
		if i >= len(stmt.Lhs) {
			ma.reportUncheckedTryLockResult(result)
			continue
		}
		ident, ok := stmt.Lhs[i].(*ast.Ident)
		if !ok || ident.Name == "_" {
			ma.reportUncheckedTryLockResult(result)
			continue
		}
		if ma.tryLockResults == nil {
			ma.tryLockResults = make(map[string]*tryLockResult)
		}
		ma.tryLockResults[ident.Name] = result
	}
}

func (ma *Analyzer) analyzeDeclStatement(stmt *ast.DeclStmt, stats map[string]*Stats) {
	ma.recordCollectionLengthsFromDecl(stmt)
	ma.reportPotentialPanicWhileLocked(stmt, stats)
}

func (ma *Analyzer) tryLockResultFromCall(call *ast.CallExpr) *tryLockResult {
	if call == nil || ma.commentFilter.ShouldSkipCall(call) {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	varName := common.GetVarName(sel.X)
	switch sel.Sel.Name {
	case "TryLock":
		if ma.mutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryLock", pos: call.Pos()}
		}
		if ma.rwMutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryLock", pos: call.Pos(), isRWMutex: true}
		}
	case "TryRLock":
		if ma.rwMutexNames[varName] {
			return &tryLockResult{varName: varName, method: "TryRLock", pos: call.Pos(), isRWMutex: true}
		}
	}
	return nil
}

func (ma *Analyzer) markReturnedTryLockResultsChecked(stmt *ast.ReturnStmt) {
	for _, result := range stmt.Results {
		ident, ok := result.(*ast.Ident)
		if !ok {
			continue
		}
		if tryResult := ma.tryLockResults[ident.Name]; tryResult != nil {
			tryResult.checked = true
		}
	}
}

func (ma *Analyzer) reportUncheckedTryLockResults() {
	for _, result := range ma.tryLockResults {
		if result != nil && !result.checked {
			ma.reportUncheckedTryLockResult(result)
		}
	}
}

func (ma *Analyzer) reportUncheckedTryLockResult(result *tryLockResult) {
	if result == nil {
		return
	}
	mutexType := "mutex"
	if result.isRWMutex {
		mutexType = "rwmutex"
	}
	ma.errorCollector.AddError(result.pos, mutexType+" '"+result.varName+"' "+result.method+" return value not checked, lock may not be held")
}

func (ma *Analyzer) applyTryLockResultToBranch(cond ast.Expr, stats map[string]*Stats) bool {
	ident, ok := cond.(*ast.Ident)
	if !ok {
		return false
	}
	result := ma.tryLockResults[ident.Name]
	if result == nil {
		return false
	}

	result.checked = true
	st := stats[result.varName]
	if st == nil {
		return true
	}
	switch result.method {
	case "TryLock":
		st.lock++
		st.lockPos = append(st.lockPos, result.pos)
	case "TryRLock":
		st.rlock++
		st.rlockPos = append(st.rlockPos, result.pos)
	}
	return true
}
