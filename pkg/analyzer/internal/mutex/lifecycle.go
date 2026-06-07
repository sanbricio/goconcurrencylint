package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// returnedIdent pairs an identifier appearing in a return statement's results
// with its enclosing return statement.
type returnedIdent struct {
	ident *ast.Ident
	ret   *ast.ReturnStmt
}

// lifecycleScanCache memoizes per-function AST scans that the lifecycle checks
// repeat for every function in the package on each unbalanced lock/unlock. It is
// shared package-wide (like explicitTransferCache) so each function body is
// scanned at most once instead of O(functions) times.
type lifecycleScanCache struct {
	returnedIdents        map[*ast.FuncDecl][]returnedIdent
	returnedCompositeLits map[*ast.FuncDecl][]*ast.CompositeLit
}

func newLifecycleScanCache() *lifecycleScanCache {
	return &lifecycleScanCache{
		returnedIdents:        make(map[*ast.FuncDecl][]returnedIdent),
		returnedCompositeLits: make(map[*ast.FuncDecl][]*ast.CompositeLit),
	}
}

// lifecycleResolver recognizes ownership-transfer patterns where a lock is
// acquired in one function but intentionally released by a returned handle or
// by a caller that owns the acquire/release protocol.
type lifecycleResolver struct {
	receiverMethods       map[string]map[string]*ast.FuncDecl
	functions             []*ast.FuncDecl
	typesInfo             *types.Info
	explicitTransferCache map[*ast.BlockStmt]map[token.Pos]struct{}
	scanCache             *lifecycleScanCache
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
	scanCache *lifecycleScanCache,
	function *ast.FuncDecl,
) *lifecycleResolver {
	if explicitTransferCache == nil {
		explicitTransferCache = make(map[*ast.BlockStmt]map[token.Pos]struct{})
	}
	if scanCache == nil {
		scanCache = newLifecycleScanCache()
	}

	return &lifecycleResolver{
		receiverMethods:       receiverMethods,
		functions:             functions,
		typesInfo:             typesInfo,
		explicitTransferCache: explicitTransferCache,
		scanCache:             scanCache,
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

	return l.returnsVariableWithReleaseFor(baseVar, suffix, methodNames)
}

func (l *lifecycleResolver) isReleaseFor(mutexName string, methodNames []string) bool {
	if l.function == nil || l.function.Name == nil || l.function.Body == nil {
		return false
	}
	if !functionBodyContainsFieldCall(l.function.Body, mutexName, matchingUnlockMethods(methodNames)) {
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

		if l.functionReturningCurrentReceiverAfterAcquire(fn, currentType, path, methodNames) {
			return true
		}
	}

	return false
}

func (l *lifecycleResolver) returnsVariableWithReleaseFor(baseVar, suffix string, methodNames []string) bool {
	if l.function == nil || l.function.Body == nil || l.typesInfo == nil {
		return false
	}

	for _, ident := range l.returnedIdentsNamed(l.function, baseVar) {
		returnedType := baseTypeNameFromType(l.typesInfo.TypeOf(ident))
		if returnedType == "" {
			continue
		}
		if l.releaseMethodUnlocks(returnedType, "", suffix, methodNames) {
			return true
		}
	}

	return false
}

func (l *lifecycleResolver) functionReturningCurrentReceiverAfterAcquire(fn *ast.FuncDecl, currentType, path string, acquireMethods []string) bool {
	if l.typesInfo == nil {
		return false
	}

	for _, ri := range l.returnedIdents(fn) {
		if baseTypeNameFromType(l.typesInfo.TypeOf(ri.ident)) != currentType {
			continue
		}
		if functionBodyContainsFieldCallBefore(fn.Body, ri.ident.Name+"."+path, acquireMethods, ri.ret.Pos()) {
			return true
		}
	}
	return false
}

func (l *lifecycleResolver) returnsFuncFor(mutexName string, methodNames []string) bool {
	if l.function == nil || l.function.Body == nil {
		return false
	}

	for _, name := range l.returnedFunctionNames(l.function) {
		fn := topLevelFunctionNamed(l.functions, name)
		if fn != nil && functionBodyContainsFieldCall(fn.Body, mutexName, methodNames) {
			return true
		}
	}

	return false
}

func (l *lifecycleResolver) isReturnedFuncReleaseFor(mutexName string, acquireMethods []string) bool {
	if l.function == nil || l.function.Name == nil || l.function.Body == nil {
		return false
	}
	if !functionBodyContainsFieldCall(l.function.Body, mutexName, matchingUnlockMethods(acquireMethods)) {
		return false
	}

	for _, fn := range l.functions {
		if fn == nil || fn.Body == nil || fn == l.function {
			continue
		}
		for _, ret := range l.returnedFunctionNames(fn) {
			if ret != l.function.Name.Name {
				continue
			}
			if functionBodyContainsFieldCallBefore(fn.Body, mutexName, acquireMethods, l.functionReturnPos(fn, ret)) {
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

	for _, fn := range methods {
		if fn == nil || fn.Body == nil {
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
	if cached, ok := l.scanCache.returnedCompositeLits[fn]; ok {
		return cached
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

	l.scanCache.returnedCompositeLits[fn] = returned
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
			if fnlit, ok := s.Call.Fun.(*ast.FuncLit); ok && fnlit.Body != nil {
				visitStmtList(fnlit.Body.List)
			}
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

func matchingUnlockMethods(acquireMethods []string) []string {
	var methods []string
	for _, acquire := range acquireMethods {
		if release := matchingUnlockMethod(acquire); release != "" {
			methods = append(methods, release)
		}
	}
	return methods
}

func topLevelFunctionNamed(functions []*ast.FuncDecl, name string) *ast.FuncDecl {
	for _, fn := range functions {
		if fn != nil && fn.Recv == nil && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// returnedIdents returns every identifier appearing in fn's return statements
// paired with its enclosing return. The scan is memoized per function (shared
// package-wide) because the lifecycle checks re-scan every package function for
// many lock/unlock pairs; without the cache this is the dominant cost on large
// packages.
func (l *lifecycleResolver) returnedIdents(fn *ast.FuncDecl) []returnedIdent {
	if fn == nil || fn.Body == nil {
		return nil
	}
	if cached, ok := l.scanCache.returnedIdents[fn]; ok {
		return cached
	}

	var result []returnedIdent
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, res := range ret.Results {
			if ident, ok := res.(*ast.Ident); ok {
				result = append(result, returnedIdent{ident: ident, ret: ret})
			}
		}
		return true
	})

	l.scanCache.returnedIdents[fn] = result
	return result
}

func (l *lifecycleResolver) returnedFunctionNames(fn *ast.FuncDecl) []string {
	var names []string
	for _, ri := range l.returnedIdents(fn) {
		if ri.ident.Name != "" && ri.ident.Name != "nil" {
			names = append(names, ri.ident.Name)
		}
	}
	return names
}

func (l *lifecycleResolver) returnedIdentsNamed(fn *ast.FuncDecl, name string) []*ast.Ident {
	var idents []*ast.Ident
	for _, ri := range l.returnedIdents(fn) {
		if ri.ident.Name == name {
			idents = append(idents, ri.ident)
		}
	}
	return idents
}

func (l *lifecycleResolver) functionReturnPos(fn *ast.FuncDecl, returnedName string) token.Pos {
	for _, ri := range l.returnedIdents(fn) {
		if ri.ident.Name == returnedName {
			return ri.ret.Pos()
		}
	}
	return token.NoPos
}
