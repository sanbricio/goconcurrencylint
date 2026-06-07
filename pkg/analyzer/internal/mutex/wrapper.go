package mutex

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// wrapperResolver decides whether a lone Lock/Unlock (etc.) call inside a method
// is a "borrowed" half of a wrapper pair — e.g. a `func (s *S) Lock()` that only
// calls `s.mu.Lock()` while a sibling `func (s *S) Unlock()` calls
// `s.mu.Unlock()`. Such a call is balanced by its sibling and must not be
// flagged as an unmatched lock.
//
// It was extracted from Checker because the heuristic is cohesive and reads a
// well-defined slice of state: the package-wide receiver→methods index
// (configuration) plus the function currently under analysis. rawBodyEffects is
// snapshotted at construction (it is set once per funcAnalysis and never mutated
// mid-analysis); during a simulated run it is true and resolve short-circuits.
type wrapperResolver struct {
	receiverMethods map[string]map[string]*ast.FuncDecl
	function        *ast.FuncDecl
	rawBodyEffects  bool
	typesInfo       *types.Info
}

func newWrapperResolver(receiverMethods map[string]map[string]*ast.FuncDecl, function *ast.FuncDecl, rawBodyEffects bool, typesInfo *types.Info) *wrapperResolver {
	return &wrapperResolver{
		receiverMethods: receiverMethods,
		function:        function,
		rawBodyEffects:  rawBodyEffects,
		typesInfo:       typesInfo,
	}
}

// resolve reports whether the call `<varName>.<methodName>()` is the borrowed
// half of a wrapper pair completed by a sibling method on the same receiver.
func (w *wrapperResolver) resolve(varName, methodName string) bool {
	if w.rawBodyEffects {
		return false
	}

	if w.function == nil || w.function.Name == nil {
		return false
	}

	currentReceiver := common.ReceiverName(w.function)
	if currentReceiver == "" {
		return false
	}

	oppositeMethods := oppositeMutexMethods(methodName)
	if len(oppositeMethods) == 0 || w.currentMethodContainsFieldCall(varName, oppositeMethods) {
		return false
	}

	if methodName == "RLock" && w.isOneWayReadLatch(varName) {
		return true
	}

	if (methodName == "Lock" || methodName == "Unlock") && w.isOneWayWriteBarrier(varName, methodName) {
		return true
	}

	// A single-statement Lock/Unlock split across sibling methods is an
	// intentional cross-method barrier, not a leak.
	if w.isBarrierPairHalf(varName, methodName, oppositeMethods) {
		return true
	}

	if _, fieldSuffix, ok := splitBaseAndSuffix(varName); ok &&
		methodNameLooksLikeWrapper(w.function.Name.Name, methodName) &&
		w.anySiblingMethodContainsFieldSuffix(fieldSuffix, w.function.Name.Name, oppositeMethods, oppositeMethods) {
		return true
	}

	suffix, ok := strings.CutPrefix(varName, currentReceiver)
	if !ok || !strings.HasPrefix(suffix, ".") {
		return false
	}

	if group := mutexMethodGroup(w.function.Name.Name); group != nil {
		return slices.Contains(group, methodName) &&
			w.siblingMethodContainsFieldCall(suffix, oppositeMethods, oppositeMethods)
	}

	if !methodNameLooksLikeWrapper(w.function.Name.Name, methodName) {
		return false
	}

	return w.anySiblingMethodContainsFieldCall(suffix, w.function.Name.Name, oppositeMethods, oppositeMethods)
}

// isOneWayReadLatch recognizes a read lock used as a one-way "start gate": a
// waiter/worker method RLocks and intentionally never RUnlocks because the read
// lock is released elsewhere (e.g. a Broadcast/Release method on the same type
// unlocks the field). This is a name-hint heuristic and only fires when BOTH the
// current method's name looks like a waiter AND a sibling method that actually
// releases the same field exists. The sibling requirement is the safety net:
// a genuinely leaked latch with no releasing sibling is still reported (see
// lonelyOneWayLatch in testdata). Trade-off: the hints are broad, so this favors
// fewer false positives at the cost of an occasional false negative.
func (w *wrapperResolver) isOneWayReadLatch(varName string) bool {
	baseVar, fieldSuffix, ok := splitBaseAndSuffix(varName)
	if !ok {
		return false
	}
	if !methodNameMatchesAnyHint(w.function.Name.Name, []string{"Wait", "Loop", "Worker", "Barrier", "Gate", "Latch"}) {
		return false
	}
	if functionBodyContainsFieldCall(w.function.Body, varName, []string{"RUnlock"}) {
		return false
	}
	if w.anySiblingMethodContainsFieldSuffix(fieldSuffix, w.function.Name.Name, []string{"Unlock"}, []string{"Unlock", "Release", "Broadcast", "Run"}) {
		return true
	}
	if receiverType := w.typeNameForBaseVar(baseVar); receiverType != "" {
		return w.anyMethodOnTypeContainsFieldSuffix(receiverType, "", fieldSuffix, []string{"Unlock"}, []string{"Unlock", "Release", "Broadcast", "Run"})
	}
	return false
}

// isOneWayWriteBarrier recognizes a write Lock/Unlock split across sibling
// methods as a deliberate cross-method barrier: e.g. Start/Open Locks and
// Stop/Close/Release Unlocks the same field. Like isOneWayReadLatch it is a
// name-hint heuristic guarded by the requirement that a sibling method really
// performs the opposite operation on the field, so an unmatched Lock with no
// releasing sibling is still reported. Trade-off: the hints (Start/Open/Run/
// Stop...) are broad, trading a possible false negative for fewer false
// positives on legitimate barrier pairs.
func (w *wrapperResolver) isOneWayWriteBarrier(varName, methodName string) bool {
	baseVar, fieldSuffix, ok := splitBaseAndSuffix(varName)
	if !ok {
		return false
	}

	var currentHints, siblingHints, siblingMethods []string
	switch methodName {
	case "Lock":
		currentHints = []string{"Create", "Start", "Prepare", "Open"}
		siblingHints = []string{"Unlock", "Release", "Broadcast", "Run", "Close", "Stop"}
		siblingMethods = []string{"Unlock"}
	case "Unlock":
		currentHints = []string{"Unlock", "Release", "Broadcast", "Run", "Close", "Stop"}
		siblingHints = []string{"Create", "Start", "Prepare", "Open"}
		siblingMethods = []string{"Lock"}
	default:
		return false
	}

	if !methodNameMatchesAnyHint(w.function.Name.Name, currentHints) {
		return false
	}

	if w.anySiblingMethodContainsFieldSuffix(fieldSuffix, w.function.Name.Name, siblingMethods, siblingHints) {
		return true
	}
	if receiverType := w.typeNameForBaseVar(baseVar); receiverType != "" {
		return w.anyMethodOnTypeContainsFieldSuffix(receiverType, w.function.Name.Name, fieldSuffix, siblingMethods, siblingHints)
	}
	return false
}

func (w *wrapperResolver) currentMethodContainsFieldCall(varName string, methodNames []string) bool {
	return functionBodyContainsFieldCall(w.function.Body, varName, methodNames)
}

func (w *wrapperResolver) siblingMethodContainsFieldCall(fieldSuffix string, siblingMethods, fieldMethods []string) bool {
	receiverType := receiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	methods := w.receiverMethods[receiverType]
	if len(methods) == 0 {
		return false
	}

	for _, siblingMethod := range siblingMethods {
		fn := methods[siblingMethod]
		if fn == nil || fn.Body == nil {
			continue
		}

		siblingReceiver := common.ReceiverName(fn)
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

func (w *wrapperResolver) anySiblingMethodContainsFieldCall(fieldSuffix, excludeMethod string, fieldMethods, nameHints []string) bool {
	receiverType := receiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	methods := w.receiverMethods[receiverType]
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

		siblingReceiver := common.ReceiverName(fn)
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

func (w *wrapperResolver) anySiblingMethodContainsFieldSuffix(fieldSuffix, excludeMethod string, fieldMethods, nameHints []string) bool {
	receiverType := receiverTypeName(w.function)
	if receiverType == "" {
		return false
	}
	return w.anyMethodOnTypeContainsFieldSuffix(receiverType, excludeMethod, fieldSuffix, fieldMethods, nameHints)
}

func (w *wrapperResolver) anyMethodOnTypeContainsFieldSuffix(receiverType, excludeMethod, fieldSuffix string, fieldMethods, nameHints []string) bool {
	methods := w.receiverMethods[receiverType]
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

func (w *wrapperResolver) typeNameForBaseVar(baseVar string) string {
	if baseVar == "" {
		return ""
	}
	if baseVar == common.ReceiverName(w.function) {
		return receiverTypeName(w.function)
	}
	if w.function == nil || w.function.Body == nil || w.typesInfo == nil {
		return ""
	}

	found := ""
	ast.Inspect(w.function.Body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if !ok || ident.Name != baseVar {
			return true
		}
		found = baseTypeNameFromType(w.typesInfo.TypeOf(ident))
		return found == ""
	})
	return found
}

func methodNameLooksLikeWrapper(fnName, syncMethod string) bool {
	return methodNameMatchesAnyHint(fnName, []string{syncMethod})
}

// isBarrierPairHalf reports whether the current method consists of exactly one
// mutex call on a field, balanced by a sibling method consisting of exactly one
// matching opposite call on the same field. Both halves being single statements
// is the signal that the split across methods is an intentional barrier/latch
// rather than a forgotten unlock, so it holds regardless of the method names.
func (w *wrapperResolver) isBarrierPairHalf(varName, methodName string, oppositeMethods []string) bool {
	_, fieldSuffix, ok := splitBaseAndSuffix(varName)
	if !ok {
		return false
	}
	if !bodyIsSingleFieldSuffixCall(w.function.Body, fieldSuffix, []string{methodName}) {
		return false
	}
	return w.anySiblingBodyIsSingleFieldSuffixCall(fieldSuffix, w.function.Name.Name, oppositeMethods)
}

func (w *wrapperResolver) anySiblingBodyIsSingleFieldSuffixCall(fieldSuffix, excludeMethod string, methodNames []string) bool {
	receiverType := receiverTypeName(w.function)
	if receiverType == "" {
		return false
	}

	for methodName, fn := range w.receiverMethods[receiverType] {
		if methodName == excludeMethod || fn == nil {
			continue
		}
		if bodyIsSingleFieldSuffixCall(fn.Body, fieldSuffix, methodNames) {
			return true
		}
	}

	return false
}

// bodyIsSingleFieldSuffixCall reports whether body is exactly one expression
// statement calling one of methodNames on a field whose suffix equals
// fieldSuffix (e.g. body `{ b.lock.Unlock() }` with suffix "lock").
func bodyIsSingleFieldSuffixCall(body *ast.BlockStmt, fieldSuffix string, methodNames []string) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}

	exprStmt, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}

	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !containsMethod(methodNames, sel.Sel.Name) {
		return false
	}

	_, suffix, ok := splitBaseAndSuffix(common.GetVarName(sel.X))
	return ok && suffix == fieldSuffix
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
