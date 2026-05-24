package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
)

// Analyzer handles the analysis of mutex and rwmutex usage
type Analyzer struct {
	mutexNames             map[string]bool
	rwMutexNames           map[string]bool
	errorCollector         report.Reporter
	stats                  map[string]*Stats
	deferErrors            *deferErrorCollector
	commentFilter          *commentfilter.CommentFilter
	function               *ast.FuncDecl
	typesInfo              *types.Info
	rawBodyEffects         bool
	receiverMethods        map[string]map[string]*ast.FuncDecl
	functions              []*ast.FuncDecl
	simulationStack        map[methodSimulationKey]bool
	localFuncStack         map[*ast.FuncLit]bool
	goroutineLockConflicts []goroutineLockConflict
	tryLockResults         map[string]*tryLockResult
	collectionLengths      map[string]int
	terminatingTailDepth   int

	// Memoize hot validation lookups. The cached map in explicitTransferCache
	// is shared by reference; callers must treat it as read-only.
	callerManagedCache    map[callerManagedKey]bool
	explicitTransferCache map[*ast.BlockStmt]map[token.Pos]struct{}

	// labelGotoSnapshots stores lock state from `goto label` edges.
	labelGotoSnapshots map[string]map[string]*Stats
}

// goroutineLockConflict records a goroutine that was launched while the parent
// held a mutex that the goroutine also tries to acquire.
type goroutineLockConflict struct {
	varName        string
	pos            token.Pos
	isRWMutex      bool
	parentReadLock bool
	requestMethod  string
}

type tryLockResult struct {
	varName   string
	method    string
	pos       token.Pos
	checked   bool
	isRWMutex bool
}

type methodSimulationKey struct {
	fn        *ast.FuncDecl
	varName   string
	isRWMutex bool
}

// Stats tracks the state of a mutex within a block
type Stats struct {
	lock, rlock                 int
	borrowedLock, borrowedRLock int
	deferUnlock, deferRUnlock   int
	lockPos, rlockPos           []token.Pos
	borrowedUnlockPos           []token.Pos
	borrowedRUnlockPos          []token.Pos
}

// deferErrorCollector tracks defer-related errors to avoid duplicate reporting
type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

// NewAnalyzer creates a new mutex analyzer
func NewAnalyzer(mutexNames, rwMutexNames map[string]bool, errorCollector report.Reporter, cf *commentfilter.CommentFilter, typesInfo *types.Info, files []*ast.File) *Analyzer {
	return &Analyzer{
		mutexNames:            mutexNames,
		rwMutexNames:          rwMutexNames,
		errorCollector:        errorCollector,
		commentFilter:         cf,
		typesInfo:             typesInfo,
		receiverMethods:       buildReceiverMethodMap(files),
		functions:             collectFunctionDecls(files),
		callerManagedCache:    make(map[callerManagedKey]bool),
		explicitTransferCache: make(map[*ast.BlockStmt]map[token.Pos]struct{}),
		deferErrors: &deferErrorCollector{
			badDeferUnlock:  make(map[string]bool),
			badDeferRUnlock: make(map[string]bool),
		},
	}
}

// AnalyzeFunction analyzes mutex usage in a function
func (ma *Analyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	ma.function = fn
	ma.rawBodyEffects = false
	ma.goroutineLockConflicts = nil
	ma.tryLockResults = make(map[string]*tryLockResult)
	ma.collectionLengths = make(map[string]int)
	ma.labelGotoSnapshots = nil
	ma.terminatingTailDepth = 0
	ma.deferErrors = &deferErrorCollector{
		badDeferUnlock:  make(map[string]bool),
		badDeferRUnlock: make(map[string]bool),
	}
	ma.initializeStats()
	ma.checkLockOrderCycles(fn.Body)
	finalStats := ma.analyzeBlock(fn.Body, ma.stats)
	ma.reportUncheckedTryLockResults()
	ma.reportUnmatchedLocks(finalStats)
}

// Indexes generated files too so sibling-method lookups (e.g. wrapper
// Lock/Unlock pairs split across the boundary) resolve.
func buildReceiverMethodMap(files []*ast.File) map[string]map[string]*ast.FuncDecl {
	methods := make(map[string]map[string]*ast.FuncDecl)

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil {
				continue
			}

			receiverType := receiverTypeName(fn)
			if receiverType == "" {
				continue
			}

			if methods[receiverType] == nil {
				methods[receiverType] = make(map[string]*ast.FuncDecl)
			}
			methods[receiverType][fn.Name.Name] = fn
		}
	}

	return methods
}

// Includes generated files so functionIsCallerManagedReleaseFor sees every
// call site of the current method.
func collectFunctionDecls(files []*ast.File) []*ast.FuncDecl {
	var functions []*ast.FuncDecl

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			functions = append(functions, fn)
		}
	}

	return functions
}

func receiverName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	if len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	return baseTypeName(fn.Recv.List[0].Type)
}

func baseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return baseTypeName(e.X)
	case *ast.IndexExpr:
		return baseTypeName(e.X)
	case *ast.IndexListExpr:
		return baseTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

func baseTypeNameFromType(typ types.Type) string {
	if typ == nil {
		return ""
	}

	switch t := types.Unalias(typ).(type) {
	case *types.Pointer:
		return baseTypeNameFromType(t.Elem())
	case *types.Named:
		if obj := t.Obj(); obj != nil {
			return obj.Name()
		}
	}

	return ""
}

func (ma *Analyzer) isBorrowedWrapperCall(varName, methodName string) bool {
	if ma.rawBodyEffects {
		return false
	}

	if ma.function == nil || ma.function.Name == nil {
		return false
	}

	currentReceiver := receiverName(ma.function)
	if currentReceiver == "" {
		return false
	}

	oppositeMethods := oppositeMutexMethods(methodName)
	if len(oppositeMethods) == 0 || ma.currentMethodContainsFieldCall(varName, oppositeMethods) {
		return false
	}

	if _, fieldSuffix, ok := splitBaseAndSuffix(varName); ok &&
		methodNameLooksLikeWrapper(ma.function.Name.Name, methodName) &&
		ma.anySiblingMethodContainsFieldSuffix(fieldSuffix, ma.function.Name.Name, oppositeMethods, oppositeMethods) {
		return true
	}

	suffix, ok := strings.CutPrefix(varName, currentReceiver)
	if !ok || !strings.HasPrefix(suffix, ".") {
		return false
	}

	switch ma.function.Name.Name {
	case "Lock", "TryLock":
		return (methodName == "Lock" || methodName == "TryLock") &&
			ma.siblingMethodContainsFieldCall(suffix, []string{"Unlock"}, []string{"Unlock"})
	case "Unlock":
		return methodName == "Unlock" &&
			ma.siblingMethodContainsFieldCall(suffix, []string{"Lock", "TryLock"}, []string{"Lock", "TryLock"})
	case "RLock", "TryRLock":
		return (methodName == "RLock" || methodName == "TryRLock") &&
			ma.siblingMethodContainsFieldCall(suffix, []string{"RUnlock"}, []string{"RUnlock"})
	case "RUnlock":
		return methodName == "RUnlock" &&
			ma.siblingMethodContainsFieldCall(suffix, []string{"RLock", "TryRLock"}, []string{"RLock", "TryRLock"})
	default:
		if !methodNameLooksLikeWrapper(ma.function.Name.Name, methodName) {
			return false
		}
		return ma.anySiblingMethodContainsFieldCall(suffix, ma.function.Name.Name, oppositeMethods, oppositeMethods)
	}
}

func (ma *Analyzer) currentMethodContainsFieldCall(varName string, methodNames []string) bool {
	return functionBodyContainsFieldCall(ma.function.Body, varName, methodNames)
}

func (ma *Analyzer) siblingMethodContainsFieldCall(fieldSuffix string, siblingMethods, fieldMethods []string) bool {
	receiverType := receiverTypeName(ma.function)
	if receiverType == "" {
		return false
	}

	methods := ma.receiverMethods[receiverType]
	if len(methods) == 0 {
		return false
	}

	for _, siblingMethod := range siblingMethods {
		fn := methods[siblingMethod]
		if fn == nil || fn.Body == nil {
			continue
		}

		siblingReceiver := receiverName(fn)
		if siblingReceiver == "" {
			continue
		}

		targetVar := siblingReceiver + fieldSuffix
		if functionBodyContainsFieldCall(fn.Body, targetVar, fieldMethods) {
			return true
		}
	}

	return false
}

func (ma *Analyzer) anySiblingMethodContainsFieldCall(fieldSuffix, excludeMethod string, fieldMethods, nameHints []string) bool {
	receiverType := receiverTypeName(ma.function)
	if receiverType == "" {
		return false
	}

	methods := ma.receiverMethods[receiverType]
	if len(methods) == 0 {
		return false
	}

	for methodName, fn := range methods {
		if methodName == excludeMethod || fn == nil || fn.Body == nil {
			continue
		}
		if !methodNameMatchesAnyHint(methodName, nameHints) {
			continue
		}

		siblingReceiver := receiverName(fn)
		if siblingReceiver == "" {
			continue
		}

		targetVar := siblingReceiver + fieldSuffix
		if functionBodyContainsFieldCall(fn.Body, targetVar, fieldMethods) {
			return true
		}
	}

	return false
}

func (ma *Analyzer) anySiblingMethodContainsFieldSuffix(fieldSuffix, excludeMethod string, fieldMethods, nameHints []string) bool {
	receiverType := receiverTypeName(ma.function)
	if receiverType == "" {
		return false
	}

	methods := ma.receiverMethods[receiverType]
	if len(methods) == 0 {
		return false
	}

	for methodName, fn := range methods {
		if methodName == excludeMethod || fn == nil || fn.Body == nil {
			continue
		}
		if !methodNameMatchesAnyHint(methodName, nameHints) {
			continue
		}
		if functionBodyContainsFieldSuffixCall(fn.Body, fieldSuffix, fieldMethods) {
			return true
		}
	}

	return false
}

func oppositeMutexMethods(methodName string) []string {
	switch methodName {
	case "Lock", "TryLock":
		return []string{"Unlock"}
	case "Unlock":
		return []string{"Lock", "TryLock"}
	case "RLock", "TryRLock":
		return []string{"RUnlock"}
	case "RUnlock":
		return []string{"RLock", "TryRLock"}
	default:
		return nil
	}
}

func methodNameLooksLikeWrapper(fnName, syncMethod string) bool {
	return methodNameMatchesAnyHint(fnName, []string{syncMethod})
}

func methodNameMatchesAnyHint(fnName string, hints []string) bool {
	lowerName := strings.ToLower(fnName)
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		lowerHint := strings.ToLower(hint)
		if strings.Contains(lowerName, lowerHint) {
			return true
		}
		if lowerHint == "rlock" && strings.Contains(lowerName, "readlock") {
			return true
		}
		if lowerHint == "runlock" && strings.Contains(lowerName, "readunlock") {
			return true
		}
	}
	return false
}

func functionBodyContainsFieldCall(body *ast.BlockStmt, varName string, methodNames []string) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
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

func functionBodyContainsFieldSuffixCall(body *ast.BlockStmt, fieldSuffix string, methodNames []string) bool {
	if body == nil || fieldSuffix == "" {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !containsMethod(methodNames, sel.Sel.Name) {
			return true
		}

		_, suffix, ok := splitBaseAndSuffix(common.GetVarName(sel.X))
		if ok && suffix == fieldSuffix {
			found = true
			return false
		}

		return true
	})

	return found
}

func containsMethod(methodNames []string, methodName string) bool {
	return slices.Contains(methodNames, methodName)
}

func relativeMutexPath(varName, prefix string) (string, bool) {
	relative, ok := strings.CutPrefix(varName, prefix+".")
	if !ok || relative == "" {
		return "", false
	}
	return relative, true
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

func splitBaseAndSuffix(varName string) (string, string, bool) {
	parts := strings.Split(varName, ".")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], strings.Join(parts[1:], "."), true
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

func (ma *Analyzer) returnedCompositeLiterals(fn *ast.FuncDecl) []*ast.CompositeLit {
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

func (ma *Analyzer) lifecycleReleaseMethodUnlocks(returnedType, fieldName, suffix string, methodNames []string) bool {
	methods := ma.receiverMethods[returnedType]
	if len(methods) == 0 {
		return false
	}

	for methodName, fn := range methods {
		if fn == nil || fn.Body == nil || !methodNameMatchesAnyHint(methodName, []string{"Close", "Unlock", "Release"}) {
			continue
		}

		recv := receiverName(fn)
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

func (ma *Analyzer) functionReturnsLifecycleHandleFor(mutexName string, methodNames []string) bool {
	baseVar, suffix, ok := splitBaseAndSuffix(mutexName)
	if !ok || ma.function == nil {
		return false
	}

	for _, lit := range ma.returnedCompositeLiterals(ma.function) {
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

			if ma.lifecycleReleaseMethodUnlocks(returnedType, key.Name, suffix, methodNames) {
				return true
			}
		}

		for _, sourceVar := range compositeLiteralUnkeyedVarNames(lit) {
			if sourceVar == baseVar && ma.lifecycleReleaseMethodUnlocks(returnedType, "", suffix, methodNames) {
				return true
			}
		}
	}

	return false
}

func (ma *Analyzer) functionIsLifecycleReleaseFor(mutexName string, methodNames []string) bool {
	if ma.function == nil || ma.function.Name == nil || !methodNameMatchesAnyHint(ma.function.Name.Name, []string{"Close", "Unlock", "Release"}) {
		return false
	}

	currentReceiver := receiverName(ma.function)
	currentType := receiverTypeName(ma.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	prefix := currentReceiver + "."
	if !strings.HasPrefix(mutexName, prefix) {
		return false
	}

	path := strings.TrimPrefix(mutexName, prefix)
	parts := strings.Split(path, ".")

	for _, fn := range ma.functions {
		if fn == nil || fn.Body == nil || fn == ma.function {
			continue
		}

		for _, lit := range ma.returnedCompositeLiterals(fn) {
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

func (ma *Analyzer) callTargetsCurrentMethod(call *ast.CallExpr, receiverType string) (string, bool) {
	if ma.function == nil || ma.function.Name == nil {
		return "", false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != ma.function.Name.Name {
		return "", false
	}

	if baseTypeNameFromType(ma.typesInfo.TypeOf(sel.X)) != receiverType {
		return "", false
	}

	baseVar := common.GetVarName(sel.X)
	if baseVar == "?" {
		return "", false
	}

	return baseVar, true
}

func (ma *Analyzer) explicitTransferCallPositions(body *ast.BlockStmt) map[token.Pos]struct{} {
	if body == nil {
		return nil
	}
	if cached, ok := ma.explicitTransferCache[body]; ok {
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
	ma.explicitTransferCache[body] = positions
	return positions
}

type callerManagedKey struct {
	mutexName string
	isRW      bool
}

func (ma *Analyzer) functionIsCallerManagedReleaseFor(mutexName string, methodNames []string) bool {
	if ma.function == nil || ma.function.Name == nil {
		return false
	}
	key := callerManagedKey{mutexName, len(methodNames) > 0 && methodNames[0] == "RLock"}
	if v, ok := ma.callerManagedCache[key]; ok {
		return v
	}
	v := ma.computeCallerManagedReleaseFor(mutexName, methodNames)
	ma.callerManagedCache[key] = v
	return v
}

func (ma *Analyzer) computeCallerManagedReleaseFor(mutexName string, methodNames []string) bool {
	currentReceiver := receiverName(ma.function)
	currentType := receiverTypeName(ma.function)
	if currentReceiver == "" || currentType == "" {
		return false
	}

	relativePath, ok := relativeMutexPath(mutexName, currentReceiver)
	if !ok {
		return false
	}

	totalCallSites := 0

	for _, fn := range ma.functions {
		if fn == nil || fn.Body == nil || fn == ma.function {
			continue
		}

		explicitTransferPositions := ma.explicitTransferCallPositions(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			baseVar, ok := ma.callTargetsCurrentMethod(call, currentType)
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

// functionIsParameterUnlockHelper reports helpers that only release a mutex
// parameter.
func (ma *Analyzer) functionIsParameterUnlockHelper(varName string, acquireMethods []string) bool {
	if ma.function == nil || ma.function.Body == nil {
		return false
	}
	if !ma.varRootIsFunctionParameter(varName) {
		return false
	}

	releaseSet := make(map[string]struct{})
	for _, acquire := range acquireMethods {
		if release := matchingUnlockMethod(acquire); release != "" {
			releaseSet[release] = struct{}{}
		}
	}
	if len(releaseSet) == 0 {
		return false
	}

	acquireSet := make(map[string]struct{}, len(acquireMethods))
	for _, acquire := range acquireMethods {
		acquireSet[acquire] = struct{}{}
	}

	sawRelease := false
	sawAcquire := false
	ast.Inspect(ma.function.Body, func(n ast.Node) bool {
		if sawAcquire {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if common.GetVarName(sel.X) != varName {
			return true
		}
		if _, ok := acquireSet[sel.Sel.Name]; ok {
			sawAcquire = true
			return false
		}
		if _, ok := releaseSet[sel.Sel.Name]; ok {
			sawRelease = true
		}
		return true
	})
	return sawRelease && !sawAcquire
}

// varRootIsFunctionParameter reports whether `varName` starts at a parameter.
func (ma *Analyzer) varRootIsFunctionParameter(varName string) bool {
	if ma.function == nil || ma.function.Type == nil || ma.function.Type.Params == nil {
		return false
	}
	base := varName
	if before, _, ok := strings.Cut(varName, "."); ok {
		base = before
	}
	for _, field := range ma.function.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == base {
				return true
			}
		}
	}
	return false
}

func (ma *Analyzer) clearStats(stats map[string]*Stats) {
	for name := range stats {
		stats[name] = &Stats{}
	}
}

func (ma *Analyzer) emptyStatsLike(stats map[string]*Stats) map[string]*Stats {
	empty := make(map[string]*Stats, len(stats))
	for name := range stats {
		empty[name] = &Stats{}
	}
	return empty
}

func (ma *Analyzer) simulateMethodEffect(fn *ast.FuncDecl, varName string, isRWMutex bool, initial *Stats) *Stats {
	if fn == nil || fn.Body == nil {
		return nil
	}

	stack := ma.simulationStack
	if stack == nil {
		stack = make(map[methodSimulationKey]bool)
		ma.simulationStack = stack
	}

	key := methodSimulationKey{fn: fn, varName: varName, isRWMutex: isRWMutex}
	if stack[key] {
		return cloneStats(initial)
	}

	stack[key] = true
	defer delete(stack, key)

	mutexNames := map[string]bool{}
	rwMutexNames := map[string]bool{}
	if isRWMutex {
		rwMutexNames[varName] = true
	} else {
		mutexNames[varName] = true
	}

	simulated := &Analyzer{
		mutexNames:      mutexNames,
		rwMutexNames:    rwMutexNames,
		errorCollector:  &report.ErrorCollector{},
		commentFilter:   ma.commentFilter,
		function:        fn,
		typesInfo:       ma.typesInfo,
		rawBodyEffects:  true,
		receiverMethods: ma.receiverMethods,
		functions:       ma.functions,
		simulationStack: stack,
		localFuncStack:  ma.localFuncStack,
		deferErrors: &deferErrorCollector{
			badDeferUnlock:  make(map[string]bool),
			badDeferRUnlock: make(map[string]bool),
		},
	}

	start := map[string]*Stats{varName: cloneStats(initial)}
	final := simulated.analyzeBlock(fn.Body, start)
	simulated.applyFunctionExitDefers(final, start)
	return cloneStats(final[varName])
}

func (ma *Analyzer) applyLocalFunctionLiteralLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}

	fnlit := ma.localFunctionLiteralBefore(ident.Name, call.Pos())
	if fnlit == nil || fnlit.Body == nil {
		return false
	}

	stack := ma.localFuncStack
	if stack == nil {
		stack = make(map[*ast.FuncLit]bool)
		ma.localFuncStack = stack
	}
	if stack[fnlit] {
		return false
	}

	stack[fnlit] = true
	defer delete(stack, fnlit)

	simulated := &Analyzer{
		mutexNames:      ma.mutexNames,
		rwMutexNames:    ma.rwMutexNames,
		errorCollector:  &report.ErrorCollector{},
		commentFilter:   ma.commentFilter,
		function:        ma.function,
		typesInfo:       ma.typesInfo,
		rawBodyEffects:  true,
		receiverMethods: ma.receiverMethods,
		functions:       ma.functions,
		simulationStack: ma.simulationStack,
		localFuncStack:  stack,
		deferErrors: &deferErrorCollector{
			badDeferUnlock:  make(map[string]bool),
			badDeferRUnlock: make(map[string]bool),
		},
	}

	baseline := simulated.cloneStatsMap(stats)
	final := simulated.analyzeBlock(fnlit.Body, baseline)
	simulated.applyFunctionExitDefers(final, baseline)
	ma.copyStatsMap(stats, final)
	return true
}

func (ma *Analyzer) localFunctionLiteralBefore(name string, before token.Pos) *ast.FuncLit {
	if ma.function == nil || ma.function.Body == nil {
		return nil
	}

	var found *ast.FuncLit
	ast.Inspect(ma.function.Body, func(n ast.Node) bool {
		if n == nil || n.Pos() >= before {
			return false
		}

		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != name || i >= len(node.Rhs) {
					continue
				}
				if fnlit, ok := node.Rhs[i].(*ast.FuncLit); ok {
					found = fnlit
				}
			}
		case *ast.ValueSpec:
			for i, ident := range node.Names {
				if ident.Name != name || i >= len(node.Values) {
					continue
				}
				if fnlit, ok := node.Values[i].(*ast.FuncLit); ok {
					found = fnlit
				}
			}
		}

		return true
	})

	return found
}

func (ma *Analyzer) applyFunctionExitDefers(stats, baseline map[string]*Stats) {
	for name, st := range stats {
		baselineDeferUnlocks := 0
		baselineDeferRUnlocks := 0
		if baselineStats := baseline[name]; baselineStats != nil {
			baselineDeferUnlocks = baselineStats.deferUnlock
			baselineDeferRUnlocks = baselineStats.deferRUnlock
		}

		for st.deferUnlock > baselineDeferUnlocks && st.lock > 0 {
			st.deferUnlock--
			st.lock--
			ma.removeFirstLockPos(st)
		}
		st.deferUnlock = baselineDeferUnlocks
		for st.deferRUnlock > baselineDeferRUnlocks && st.rlock > 0 {
			st.deferRUnlock--
			st.rlock--
			ma.removeFirstRLockPos(st)
		}
		st.deferRUnlock = baselineDeferRUnlocks
	}
}

func (ma *Analyzer) applyLocalMethodLifecycleEffects(call *ast.CallExpr, stats map[string]*Stats) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	baseVar := common.GetVarName(sel.X)
	if baseVar == "?" {
		return false
	}

	receiverType := baseTypeNameFromType(ma.typesInfo.TypeOf(sel.X))
	if receiverType == "" {
		return false
	}

	callee := ma.receiverMethods[receiverType][sel.Sel.Name]
	if callee == nil || callee.Body == nil || callee == ma.function {
		return false
	}

	calleeReceiver := receiverName(callee)
	if calleeReceiver == "" {
		return false
	}

	changed := false

	for mutexName := range ma.mutexNames {
		relativePath, ok := relativeMutexPath(mutexName, baseVar)
		if !ok {
			continue
		}

		simulated := ma.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, false, stats[mutexName])
		if simulated == nil {
			continue
		}

		stats[mutexName] = simulated
		changed = true
	}

	for rwMutexName := range ma.rwMutexNames {
		relativePath, ok := relativeMutexPath(rwMutexName, baseVar)
		if !ok {
			continue
		}

		simulated := ma.simulateMethodEffect(callee, calleeReceiver+"."+relativePath, true, stats[rwMutexName])
		if simulated == nil {
			continue
		}

		stats[rwMutexName] = simulated
		changed = true
	}

	return changed
}

// initializeStats initializes the stats map for all known mutexes
func (ma *Analyzer) initializeStats() {
	ma.stats = make(map[string]*Stats)

	for mutexName := range ma.mutexNames {
		ma.stats[mutexName] = &Stats{}
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.stats[rwMutexName] = &Stats{}
	}
}

// cloneStatsMap returns a new map containing deep copies of every Stats in original.
func (ma *Analyzer) cloneStatsMap(original map[string]*Stats) map[string]*Stats {
	copy := make(map[string]*Stats)
	ma.copyStatsMap(copy, original)
	return copy
}

// copyStatsMap copies every entry from src into dst, performing a deep copy
// of each Stats value via copyStats. Keys present in dst but not in src are
// left untouched (merge semantics, not full replacement).
func (ma *Analyzer) copyStatsMap(dst, src map[string]*Stats) {
	for name, srcStats := range src {
		if _, exists := dst[name]; !exists {
			dst[name] = &Stats{}
		}

		copyStats(dst[name], srcStats)
	}
}

// cloneStats creates a deep copy of a single Stats object.
// If the input is nil, it returns a new initialized empty Stats instance.
func cloneStats(stats *Stats) *Stats {
	if stats == nil {
		return &Stats{}
	}

	clone := &Stats{}
	copyStats(clone, stats)

	return clone
}

// copyStats copies all fields from src into dst, cloning slice fields so
// the two instances do not share backing arrays. It is a no-op if either
// src or dst is nil.
func copyStats(dst, src *Stats) {
	if src == nil || dst == nil {
		return
	}

	dst.lock = src.lock
	dst.rlock = src.rlock
	dst.borrowedLock = src.borrowedLock
	dst.borrowedRLock = src.borrowedRLock
	dst.deferUnlock = src.deferUnlock
	dst.deferRUnlock = src.deferRUnlock
	dst.lockPos = slices.Clone(src.lockPos)
	dst.rlockPos = slices.Clone(src.rlockPos)
	dst.borrowedUnlockPos = slices.Clone(src.borrowedUnlockPos)
	dst.borrowedRUnlockPos = slices.Clone(src.borrowedRUnlockPos)
}

// removeFirstLockPos removes the first lock position from the list
func (ma *Analyzer) removeFirstLockPos(stats *Stats) {
	if len(stats.lockPos) > 0 {
		stats.lockPos = stats.lockPos[1:]
	}
}

// removeFirstRLockPos removes the first rlock position from the list
func (ma *Analyzer) removeFirstRLockPos(stats *Stats) {
	if len(stats.rlockPos) > 0 {
		stats.rlockPos = stats.rlockPos[1:]
	}
}

func (ma *Analyzer) removeFirstBorrowedUnlockPos(stats *Stats) {
	if len(stats.borrowedUnlockPos) > 0 {
		stats.borrowedUnlockPos = stats.borrowedUnlockPos[1:]
	}
}

func (ma *Analyzer) removeFirstBorrowedRUnlockPos(stats *Stats) {
	if len(stats.borrowedRUnlockPos) > 0 {
		stats.borrowedRUnlockPos = stats.borrowedRUnlockPos[1:]
	}
}
