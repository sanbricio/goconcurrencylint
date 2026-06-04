package mutex

import (
	"go/ast"
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
}

func newWrapperResolver(receiverMethods map[string]map[string]*ast.FuncDecl, function *ast.FuncDecl, rawBodyEffects bool) *wrapperResolver {
	return &wrapperResolver{
		receiverMethods: receiverMethods,
		function:        function,
		rawBodyEffects:  rawBodyEffects,
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

func methodNameLooksLikeWrapper(fnName, syncMethod string) bool {
	return methodNameMatchesAnyHint(fnName, []string{syncMethod})
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
