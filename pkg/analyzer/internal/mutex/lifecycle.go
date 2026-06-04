package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// lifecycleResolver recognizes ownership-transfer patterns where a lock is
// acquired in one function but intentionally released by a returned handle or
// by a caller that owns the acquire/release protocol.
type lifecycleResolver struct {
	receiverMethods       map[string]map[string]*ast.FuncDecl
	functions             []*ast.FuncDecl
	typesInfo             *types.Info
	explicitTransferCache map[*ast.BlockStmt]map[token.Pos]struct{}
	function              *ast.FuncDecl
	callerManagedCache    map[callerManagedKey]bool
}

type callerManagedKey struct {
	mutexName string
	isRW      bool
}

func newLifecycleResolver(
	receiverMethods map[string]map[string]*ast.FuncDecl,
	functions []*ast.FuncDecl,
	typesInfo *types.Info,
	explicitTransferCache map[*ast.BlockStmt]map[token.Pos]struct{},
	function *ast.FuncDecl,
) *lifecycleResolver {
	if explicitTransferCache == nil {
		explicitTransferCache = make(map[*ast.BlockStmt]map[token.Pos]struct{})
	}

	return &lifecycleResolver{
		receiverMethods:       receiverMethods,
		functions:             functions,
		typesInfo:             typesInfo,
		explicitTransferCache: explicitTransferCache,
		function:              function,
		callerManagedCache:    make(map[callerManagedKey]bool),
	}
}

func (l *lifecycleResolver) returnsHandleFor(mutexName string, methodNames []string) bool {
	baseVar, suffix, ok := splitBaseAndSuffix(mutexName)
	if !ok || l.function == nil {
		return false
	}

	for _, lit := range l.returnedCompositeLiterals(l.function) {
		returnedType := compositeLiteralTypeName(lit)
		if returnedType == "" {
			continue
		}

		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}

			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}

			if common.GetVarName(kv.Value) != baseVar {
				continue
			}

			if l.releaseMethodUnlocks(returnedType, key.Name, suffix, methodNames) {
				return true
			}
		}

		for _, sourceVar := range compositeLiteralUnkeyedVarNames(lit) {
			if sourceVar == baseVar && l.releaseMethodUnlocks(returnedType, "", suffix, methodNames) {
				return true
			}
		}
	}

	return false
}

func (l *lifecycleResolver) isReleaseFor(mutexName string, methodNames []string) bool {
	if l.function == nil || l.function.Name == nil || !methodNameMatchesAnyHint(l.function.Name.Name, []string{"Close", "Unlock", "Release"}) {
		return false
	}

	currentReceiver := common.ReceiverName(l.function)
	currentType := receiverTypeName(l.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	prefix := currentReceiver + "."
	if !strings.HasPrefix(mutexName, prefix) {
		return false
	}

	path := strings.TrimPrefix(mutexName, prefix)
	parts := strings.Split(path, ".")

	for _, fn := range l.functions {
		if fn == nil || fn.Body == nil || fn == l.function {
			continue
		}

		for _, lit := range l.returnedCompositeLiterals(fn) {
			if compositeLiteralTypeName(lit) != currentType {
				continue
			}

			if len(parts) == 1 {
				for _, sourceVar := range compositeLiteralUnkeyedVarNames(lit) {
					targetVar := sourceVar + "." + path
					if functionBodyContainsFieldCall(fn.Body, targetVar, methodNames) {
						return true
					}
				}
				continue
			}

			fieldName := parts[0]
			suffix := strings.Join(parts[1:], ".")
			sourceVar := compositeLiteralFieldVarName(lit, fieldName)
			if sourceVar == "" || sourceVar == "?" {
				continue
			}

			targetVar := sourceVar + "." + suffix
			if functionBodyContainsFieldCall(fn.Body, targetVar, methodNames) {
				return true
			}
		}
	}

	return false
}

func (l *lifecycleResolver) isCallerManagedReleaseFor(mutexName string, methodNames []string) bool {
	if l.function == nil || l.function.Name == nil {
		return false
	}

	key := callerManagedKey{mutexName, len(methodNames) > 0 && methodNames[0] == "RLock"}
	if v, ok := l.callerManagedCache[key]; ok {
		return v
	}

	v := l.computeCallerManagedReleaseFor(mutexName, methodNames)
	l.callerManagedCache[key] = v
	return v
}

func (l *lifecycleResolver) computeCallerManagedReleaseFor(mutexName string, methodNames []string) bool {
	currentReceiver := common.ReceiverName(l.function)
	currentType := receiverTypeName(l.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	relativePath, ok := relativeMutexPath(mutexName, currentReceiver)
	if !ok {
		return false
	}

	totalCallSites := 0

	for _, fn := range l.functions {
		if fn == nil || fn.Body == nil || fn == l.function {
			continue
		}

		explicitTransferPositions := l.explicitTransferCallPositions(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			baseVar, ok := l.callTargetsCurrentMethod(call, currentType)
			if !ok {
				return true
			}

			totalCallSites++
			if _, ok := explicitTransferPositions[call.Pos()]; !ok {
				totalCallSites = -1
				return false
			}
			if !functionBodyContainsFieldCallBefore(fn.Body, baseVar+"."+relativePath, methodNames, call.Pos()) {
				totalCallSites = -1
				return false
			}
			return true
		})
		if totalCallSites < 0 {
			return false
		}
	}

	return totalCallSites > 0
}

func (l *lifecycleResolver) releaseMethodUnlocks(returnedType, fieldName, suffix string, methodNames []string) bool {
	methods := l.receiverMethods[returnedType]
	if len(methods) == 0 {
		return false
	}

	for methodName, fn := range methods {
		if fn == nil || fn.Body == nil || !methodNameMatchesAnyHint(methodName, []string{"Close", "Unlock", "Release"}) {
			continue
		}

		recv := common.ReceiverName(fn)
		if recv == "" {
			continue
		}

		targetVar := recv + "." + suffix
		if fieldName != "" {
			targetVar = recv + "." + fieldName + "." + suffix
		}
		if functionBodyContainsFieldCall(fn.Body, targetVar, methodNames) {
			return true
		}
	}

	return false
}

func (l *lifecycleResolver) returnedCompositeLiterals(fn *ast.FuncDecl) []*ast.CompositeLit {
	if fn == nil || fn.Body == nil {
		return nil
	}

	localValues := make(map[string]ast.Expr)
	var returned []*ast.CompositeLit

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				localValues[ident.Name] = node.Rhs[i]
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if i >= len(node.Values) {
					continue
				}
				localValues[name.Name] = node.Values[i]
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if lit := compositeLiteralFromExpr(result); lit != nil {
					returned = append(returned, lit)
					continue
				}

				ident, ok := result.(*ast.Ident)
				if !ok {
					continue
				}

				if lit := compositeLiteralFromExpr(localValues[ident.Name]); lit != nil {
					returned = append(returned, lit)
				}
			}
		}
		return true
	})

	return returned
}

func (l *lifecycleResolver) callTargetsCurrentMethod(call *ast.CallExpr, receiverType string) (string, bool) {
	if l.function == nil || l.function.Name == nil || l.typesInfo == nil {
		return "", false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != l.function.Name.Name {
		return "", false
	}

	if baseTypeNameFromType(l.typesInfo.TypeOf(sel.X)) != receiverType {
		return "", false
	}

	baseVar := common.GetVarName(sel.X)
	if baseVar == "?" {
		return "", false
	}

	return baseVar, true
}

func (l *lifecycleResolver) explicitTransferCallPositions(body *ast.BlockStmt) map[token.Pos]struct{} {
	if body == nil {
		return nil
	}
	if cached, ok := l.explicitTransferCache[body]; ok {
		return cached
	}
	positions := make(map[token.Pos]struct{})
	var (
		visitStmt     func(ast.Stmt)
		visitStmtList func([]ast.Stmt)
		visitElse     func(ast.Stmt)
	)

	recordCall := func(call *ast.CallExpr) {
		if call != nil {
			positions[call.Pos()] = struct{}{}
		}
	}

	visitStmtList = func(stmts []ast.Stmt) {
		for _, stmt := range stmts {
			visitStmt(stmt)
		}
	}

	visitElse = func(stmt ast.Stmt) {
		switch e := stmt.(type) {
		case *ast.BlockStmt:
			visitStmtList(e.List)
		case *ast.IfStmt:
			visitStmt(e)
		}
	}

	visitStmt = func(stmt ast.Stmt) {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			visitStmtList(s.List)
		case *ast.LabeledStmt:
			visitStmt(s.Stmt)
		case *ast.ExprStmt:
			if call, ok := s.X.(*ast.CallExpr); ok {
				recordCall(call)
			}
		case *ast.ReturnStmt:
			for _, result := range s.Results {
				if call, ok := result.(*ast.CallExpr); ok {
					recordCall(call)
				}
			}
		case *ast.GoStmt:
			recordCall(s.Call)
		case *ast.IfStmt:
			visitStmtList(s.Body.List)
			if s.Else != nil {
				visitElse(s.Else)
			}
		case *ast.ForStmt:
			visitStmtList(s.Body.List)
		case *ast.RangeStmt:
			visitStmtList(s.Body.List)
		case *ast.SwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					visitStmtList(cc.Body)
				}
			}
		case *ast.TypeSwitchStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CaseClause); ok {
					visitStmtList(cc.Body)
				}
			}
		case *ast.SelectStmt:
			for _, clause := range s.Body.List {
				if cc, ok := clause.(*ast.CommClause); ok {
					visitStmtList(cc.Body)
				}
			}
		}
	}

	visitStmtList(body.List)
	l.explicitTransferCache[body] = positions
	return positions
}

func functionBodyContainsFieldCallBefore(body *ast.BlockStmt, varName string, methodNames []string, before token.Pos) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() >= before {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if containsMethod(methodNames, sel.Sel.Name) && common.GetVarName(sel.X) == varName {
			found = true
			return false
		}

		return true
	})

	return found
}

func compositeLiteralFromExpr(expr ast.Expr) *ast.CompositeLit {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return e
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			if lit, ok := e.X.(*ast.CompositeLit); ok {
				return lit
			}
		}
	}
	return nil
}

func compositeLiteralTypeName(lit *ast.CompositeLit) string {
	if lit == nil {
		return ""
	}
	return baseTypeName(lit.Type)
}

func compositeLiteralFieldVarName(lit *ast.CompositeLit, fieldName string) string {
	if lit == nil {
		return ""
	}

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}

		return common.GetVarName(kv.Value)
	}

	return ""
}

func compositeLiteralUnkeyedVarNames(lit *ast.CompositeLit) []string {
	if lit == nil {
		return nil
	}

	var names []string
	for _, elt := range lit.Elts {
		if _, ok := elt.(*ast.KeyValueExpr); ok {
			continue
		}

		name := common.GetVarName(elt)
		if name == "" || name == "?" {
			continue
		}
		names = append(names, name)
	}

	return names
}
