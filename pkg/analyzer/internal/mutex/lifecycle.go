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

// methodCallSite is one call expression with a selector (x.Name(...)) paired
// with the function declaration whose body contains it.
type methodCallSite struct {
	call      *ast.CallExpr
	enclosing *ast.FuncDecl
}

// lifecycleScanCache memoizes per-function AST scans that the lifecycle checks
// repeat for every function in the package on each unbalanced lock/unlock. It is
// shared package-wide (like explicitTransferCache) so each function body is
// scanned at most once instead of O(functions) times.
//
// The *index* fields invert "iterate every package function per query" loops:
// they are built lazily on first use (driver.Run visits functions sequentially,
// so no synchronization is needed) and answer subsequent queries in O(1).
type lifecycleScanCache struct {
	returnedIdents        map[*ast.FuncDecl][]returnedIdent
	returnedCompositeLits map[*ast.FuncDecl][]*ast.CompositeLit
	returnedFuncNames     map[*ast.FuncDecl][]string

	// topLevelFuncIndex maps a name to the package-level (non-method) function.
	topLevelFuncIndex map[string]*ast.FuncDecl
	// returnersOfNameIndex maps an identifier name to the functions that
	// return it.
	returnersOfNameIndex map[string][]*ast.FuncDecl
	// returnersOfTypeIndex maps a type name to the functions that return a
	// composite literal or a typed identifier of that type.
	returnersOfTypeIndex map[string][]*ast.FuncDecl
	// callSitesOfNameIndex maps a selector name to every x.Name(...) call in
	// the package.
	callSitesOfNameIndex map[string][]methodCallSite
}

func newLifecycleScanCache() *lifecycleScanCache {
	return &lifecycleScanCache{
		returnedIdents:        make(map[*ast.FuncDecl][]returnedIdent),
		returnedCompositeLits: make(map[*ast.FuncDecl][]*ast.CompositeLit),
		returnedFuncNames:     make(map[*ast.FuncDecl][]string),
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
	currentType := common.ReceiverTypeName(l.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	prefix := currentReceiver + "."
	if !strings.HasPrefix(mutexName, prefix) {
		return false
	}

	path := strings.TrimPrefix(mutexName, prefix)
	parts := strings.Split(path, ".")

	for _, fn := range l.functionsReturningType(currentType) {
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
		returnedType := common.BaseTypeNameFromType(l.typesInfo.TypeOf(ident))
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
		if common.BaseTypeNameFromType(l.typesInfo.TypeOf(ri.ident)) != currentType {
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
		fn := l.topLevelFunctionNamed(name)
		if fn != nil && functionBodyContainsFieldCall(fn.Body, mutexName, methodNames) {
			return true
		}
	}

	return false
}

// returnsClosureReleasingLock reports whether the function hands the release of
// mutexName to its caller through a returned closure. Two shapes are covered:
// the closure is returned directly (return func() { mu.Unlock() }) or it is a
// field value of a returned composite literal — the guard/RAII pattern
// (return Guard{release: func() { mu.Unlock() }}). In both cases the acquiring
// function is not expected to unlock before returning, so the lock is not a
// leak. The closure must call the matching unlock on the same mutex; a closure
// that merely mentions it does not qualify.
func (l *lifecycleResolver) returnsClosureReleasingLock(mutexName string, unlockMethods []string) bool {
	if l.function == nil || l.function.Body == nil {
		return false
	}

	// Closures stored in a returned composite literal (guard/handle pattern).
	for _, lit := range l.returnedCompositeLiterals(l.function) {
		for _, elt := range lit.Elts {
			value := elt
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				value = kv.Value
			}
			if fnLit, ok := value.(*ast.FuncLit); ok &&
				functionBodyContainsFieldCall(fnLit.Body, mutexName, unlockMethods) {
				return true
			}
		}
	}

	// Closures returned directly: return func() { mu.Unlock() }.
	for _, fnLit := range l.returnedFuncLits(l.function) {
		if functionBodyContainsFieldCall(fnLit.Body, mutexName, unlockMethods) {
			return true
		}
	}

	return false
}

// returnedFuncLits returns the function literals fn hands back to its caller,
// either returned directly or via a local variable that is later returned.
func (l *lifecycleResolver) returnedFuncLits(fn *ast.FuncDecl) []*ast.FuncLit {
	if fn == nil || fn.Body == nil {
		return nil
	}

	localValues := make(map[string]ast.Expr)
	var returned []*ast.FuncLit

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && i < len(node.Rhs) {
					localValues[ident.Name] = node.Rhs[i]
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if fnLit, ok := result.(*ast.FuncLit); ok {
					returned = append(returned, fnLit)
					continue
				}
				if ident, ok := result.(*ast.Ident); ok {
					if fnLit, ok := localValues[ident.Name].(*ast.FuncLit); ok {
						returned = append(returned, fnLit)
					}
				}
			}
		}
		return true
	})

	return returned
}

func (l *lifecycleResolver) isReturnedFuncReleaseFor(mutexName string, acquireMethods []string) bool {
	if l.function == nil || l.function.Name == nil || l.function.Body == nil {
		return false
	}
	if !functionBodyContainsFieldCall(l.function.Body, mutexName, matchingUnlockMethods(acquireMethods)) {
		return false
	}

	for _, fn := range l.functionsReturningName(l.function.Name.Name) {
		if fn == nil || fn.Body == nil || fn == l.function {
			continue
		}
		if functionBodyContainsFieldCallBefore(fn.Body, mutexName, acquireMethods, l.functionReturnPos(fn, l.function.Name.Name)) {
			return true
		}
	}

	return false
}

// topLevelFunctionNamed returns the package-level (non-method) function with
// the given name through the lazily built package-wide index.
func (l *lifecycleResolver) topLevelFunctionNamed(name string) *ast.FuncDecl {
	if l.scanCache.topLevelFuncIndex == nil {
		idx := make(map[string]*ast.FuncDecl)
		for _, fn := range l.functions {
			if fn != nil && fn.Recv == nil && fn.Name != nil {
				if _, ok := idx[fn.Name.Name]; !ok {
					idx[fn.Name.Name] = fn
				}
			}
		}
		l.scanCache.topLevelFuncIndex = idx
	}
	return l.scanCache.topLevelFuncIndex[name]
}

// functionsReturningName returns the package functions that return an
// identifier with the given name.
func (l *lifecycleResolver) functionsReturningName(name string) []*ast.FuncDecl {
	if l.scanCache.returnersOfNameIndex == nil {
		idx := make(map[string][]*ast.FuncDecl)
		for _, fn := range l.functions {
			if fn == nil || fn.Body == nil {
				continue
			}
			seen := make(map[string]bool)
			for _, returned := range l.returnedFunctionNames(fn) {
				if !seen[returned] {
					seen[returned] = true
					idx[returned] = append(idx[returned], fn)
				}
			}
		}
		l.scanCache.returnersOfNameIndex = idx
	}
	return l.scanCache.returnersOfNameIndex[name]
}

// functionsReturningType returns the package functions that return a composite
// literal of the given type or an identifier typed as it. Only those functions
// can satisfy the ownership-transfer checks in isReleaseFor.
func (l *lifecycleResolver) functionsReturningType(typeName string) []*ast.FuncDecl {
	if l.scanCache.returnersOfTypeIndex == nil {
		idx := make(map[string][]*ast.FuncDecl)
		for _, fn := range l.functions {
			if fn == nil || fn.Body == nil {
				continue
			}
			seen := make(map[string]bool)
			record := func(name string) {
				if name != "" && !seen[name] {
					seen[name] = true
					idx[name] = append(idx[name], fn)
				}
			}
			for _, lit := range l.returnedCompositeLiterals(fn) {
				record(compositeLiteralTypeName(lit))
			}
			if l.typesInfo != nil {
				for _, ri := range l.returnedIdents(fn) {
					record(common.BaseTypeNameFromType(l.typesInfo.TypeOf(ri.ident)))
				}
			}
		}
		l.scanCache.returnersOfTypeIndex = idx
	}
	return l.scanCache.returnersOfTypeIndex[typeName]
}

// callSitesNamed returns every x.<name>(...) call expression in the package
// paired with its enclosing function.
func (l *lifecycleResolver) callSitesNamed(name string) []methodCallSite {
	if l.scanCache.callSitesOfNameIndex == nil {
		idx := make(map[string][]methodCallSite)
		for _, fn := range l.functions {
			if fn == nil || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					idx[sel.Sel.Name] = append(idx[sel.Sel.Name], methodCallSite{call: call, enclosing: fn})
				}
				return true
			})
		}
		l.scanCache.callSitesOfNameIndex = idx
	}
	return l.scanCache.callSitesOfNameIndex[name]
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
	currentType := common.ReceiverTypeName(l.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	relativePath, ok := relativeMutexPath(mutexName, currentReceiver)
	if !ok {
		return false
	}

	totalCallSites := 0

	for _, site := range l.callSitesNamed(l.function.Name.Name) {
		if site.enclosing == l.function {
			continue
		}

		baseVar, ok := l.callTargetsCurrentMethod(site.call, currentType)
		if !ok {
			continue
		}

		totalCallSites++
		if _, ok := l.explicitTransferCallPositions(site.enclosing.Body)[site.call.Pos()]; !ok {
			return false
		}
		if !functionBodyContainsFieldCallBefore(site.enclosing.Body, baseVar+"."+relativePath, methodNames, site.call.Pos()) {
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

	if common.BaseTypeNameFromType(l.typesInfo.TypeOf(sel.X)) != receiverType {
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
	return common.BaseTypeName(lit.Type)
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

// returnedFunctionNames is memoized package-wide: isReturnedFuncReleaseFor and
// the lazy indexes re-request it for every package function.
func (l *lifecycleResolver) returnedFunctionNames(fn *ast.FuncDecl) []string {
	if cached, ok := l.scanCache.returnedFuncNames[fn]; ok {
		return cached
	}

	var names []string
	for _, ri := range l.returnedIdents(fn) {
		if ri.ident.Name != "" && ri.ident.Name != "nil" {
			names = append(names, ri.ident.Name)
		}
	}

	l.scanCache.returnedFuncNames[fn] = names
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
