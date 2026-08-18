package waitgroup

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// packageWaitGroupBalanceIsCrossFunction reports whether a package-level
// WaitGroup has a balance this function cannot see: another function moves the
// counter, and this one handed the work away when it is the side doing the Add.
// A function that only Adds and Waits keeps being validated.
func (c *Checker) packageWaitGroupBalanceIsCrossFunction(wgName string, hasLocalAdd bool) bool {
	if !c.packageLevelWaitGroupNames[wgName] || c.currentFunctionShadowsPackageLevelWaitGroup(wgName) {
		return false
	}
	if c.function == nil || c.function.Body == nil {
		return false
	}
	// An Add is only unaccounted for if this function handed the work away;
	// a function that just releases the counter received its share of it from
	// whoever did the Add, and needs no dispatch of its own.
	if hasLocalAdd && !c.functionDispatchesWork(wgName) {
		return false
	}

	for _, fn := range c.packageCounters.countedBy(wgName) {
		if fn != c.function {
			return true
		}
	}
	return false
}

// packageCounterIndex records which functions move each WaitGroup counter.
// Built once per package: doing it per function rescans every body each time.
type packageCounterIndex struct {
	byName map[string][]*ast.FuncDecl
}

func newPackageCounterIndex(files []*ast.File) *packageCounterIndex {
	index := &packageCounterIndex{byName: make(map[string][]*ast.FuncDecl)}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for name := range waitGroupNamesCountedIn(fn.Body) {
				index.byName[name] = append(index.byName[name], fn)
			}
		}
	}
	return index
}

func (i *packageCounterIndex) countedBy(wgName string) []*ast.FuncDecl {
	if i == nil {
		return nil
	}
	return i.byName[wgName]
}

// waitGroupNamesCountedIn collects the names a body moves the counter of. Wait
// observes the counter without owning any part of the balance, so it does not
// count.
func waitGroupNamesCountedIn(body *ast.BlockStmt) map[string]bool {
	names := make(map[string]bool)
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Add", "Done", "Go":
			if name := common.GetVarName(sel.X); name != "" && name != "?" {
				names[name] = true
			}
		}
		return true
	})
	return names
}

// functionDispatchesWork reports whether the function hands work to something
// that could carry the counter's counterpart. A package-level WaitGroup is only
// nameable inside its own package, so a call into another package — a log line,
// a test assertion — cannot be doing the Done.
func (c *Checker) functionDispatchesWork(wgName string) bool {
	found := false
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.SendStmt:
			found = true
			return false
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && common.GetVarName(sel.X) == wgName {
				return true
			}
			if !c.callTargetsThisPackage(node) {
				return true
			}
			found = true
			return false
		}
		return true
	})
	return found
}

// callTargetsThisPackage reports whether the callee is declared in the package
// under analysis. Builtins and functions from other packages are not.
func (c *Checker) callTargetsThisPackage(call *ast.CallExpr) bool {
	if c.typesInfo == nil || c.pkg == nil {
		return false
	}

	var name *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		name = fun
	case *ast.SelectorExpr:
		name = fun.Sel
	default:
		return false
	}

	obj := c.typesInfo.ObjectOf(name)
	if obj == nil {
		return false
	}
	if _, isBuiltin := obj.(*types.Builtin); isBuiltin {
		return false
	}
	return obj.Pkg() == c.pkg
}
