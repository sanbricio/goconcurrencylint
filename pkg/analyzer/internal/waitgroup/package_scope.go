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

// fieldDoneIndex records, for each struct field that holds a WaitGroup, the
// functions that only release it: they call Done on the field and never Add or
// Go on it. Such a function finishes work that someone else counted. Built
// once per package, like packageCounterIndex, but keyed by the field object
// instead of a name: the same field is spelled `ctx.wg` where the work is
// dispatched and `uw.ctx.wg` where it finishes.
type fieldDoneIndex struct {
	releasers map[*types.Var][]*ast.FuncDecl
}

func newFieldDoneIndex(files []*ast.File, typesInfo *types.Info) *fieldDoneIndex {
	index := &fieldDoneIndex{releasers: make(map[*types.Var][]*ast.FuncDecl)}
	if typesInfo == nil {
		return index
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			done := make(map[*types.Var]bool)
			counted := make(map[*types.Var]bool)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				field := waitGroupFieldOf(sel.X, typesInfo)
				if field == nil {
					return true
				}
				switch sel.Sel.Name {
				case "Done":
					done[field] = true
				case "Add", "Go":
					counted[field] = true
				}
				return true
			})
			for field := range done {
				if !counted[field] {
					index.releasers[field] = append(index.releasers[field], fn)
				}
			}
		}
	}
	return index
}

func (i *fieldDoneIndex) releasersOf(field *types.Var) []*ast.FuncDecl {
	if i == nil {
		return nil
	}
	return i.releasers[field]
}

// waitGroupFieldOf returns the struct field a WaitGroup receiver expression
// selects, or nil when the receiver is not a field. Generic instantiations are
// folded onto their origin so every use of one declared field shares a key.
func waitGroupFieldOf(expr ast.Expr, typesInfo *types.Info) *types.Var {
	sel, ok := common.UnwrapParenExpr(expr).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	field, ok := typesInfo.ObjectOf(sel.Sel).(*types.Var)
	if !ok || !field.IsField() || !common.IsWaitGroup(field.Type()) {
		return nil
	}
	return field.Origin()
}

// fieldWaitGroupDoneIsElsewhere reports whether a WaitGroup this function only
// Adds to is a struct field whose Done runs in another function of the
// package. The Add travels with the struct — stored in a map, queued as a work
// item, returned to the caller — and the Done runs where the work finishes, so
// this function alone holds half of the balance:
//
//	ctx.wg.Add(1)
//	uw.ctx = ctx
//	schedule(uw) // uw's callback calls uw.ctx.wg.Done()
//
// Two things must hold for that reading. Some function must exist that only
// releases the field: a method that Adds and Dones its own work says nothing
// about this Add. And the struct must leave this function: a value created
// here and never handed on cannot reach any other function's Done.
func (c *Checker) fieldWaitGroupDoneIsElsewhere(wgName string) bool {
	if c.typesInfo == nil || c.pkg == nil || c.function == nil || c.function.Body == nil {
		return false
	}
	var receiver *ast.SelectorExpr
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if receiver != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Add" && common.GetVarName(sel.X) == wgName {
			receiver, _ = common.UnwrapParenExpr(sel.X).(*ast.SelectorExpr)
		}
		return receiver == nil
	})
	if receiver == nil {
		return false
	}
	field := waitGroupFieldOf(receiver, c.typesInfo)
	if field == nil {
		return false
	}

	releasedElsewhere := false
	for _, fn := range c.fieldDones.releasersOf(field) {
		if fn != c.function {
			releasedElsewhere = true
			break
		}
	}
	return releasedElsewhere && c.ownerLeavesFunction(receiver)
}

// ownerLeavesFunction reports whether the value at the root of a field
// selector (`ctx` in `ctx.wg`, `uw` in `uw.ctx.wg`) can be reached from outside
// the current function. The receiver and package-level variables always can.
// Anything else must be handed on: stored in a field, map or slice, sent on a
// channel this function did not create, returned, passed to a call, captured
// by a closure, or used as the receiver of one of the package's own methods.
// Local names it is copied into are followed.
func (c *Checker) ownerLeavesFunction(sel *ast.SelectorExpr) bool {
	root := selectorRoot(sel)
	if root == nil {
		return false
	}
	rootObj := c.typesInfo.ObjectOf(root)
	if rootObj == nil {
		return false
	}
	if rootObj.Parent() == c.pkg.Scope() || c.isReceiverObject(rootObj) {
		return true
	}

	owners := map[types.Object]bool{rootObj: true}
	refersToOwner := func(node ast.Node) bool {
		found := false
		ast.Inspect(node, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && owners[c.typesInfo.ObjectOf(ident)] {
				found = true
			}
			return !found
		})
		return found
	}
	// assigned records `x := <owner...>` as one more name for the owner and
	// reports whether the store itself leaves the function.
	assigned := func(lhs, rhs ast.Expr) bool {
		if !refersToOwner(rhs) {
			return false
		}
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			return true // field, index or pointer store
		}
		if obj := c.typesInfo.ObjectOf(ident); obj != nil {
			if obj.Parent() == c.pkg.Scope() {
				return true
			}
			owners[obj] = true
		}
		return false
	}

	leaves := false
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		if leaves {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			leaves = refersToOwner(node.Body)
			return false
		case *ast.AssignStmt:
			if len(node.Lhs) == len(node.Rhs) {
				for i := range node.Lhs {
					leaves = leaves || assigned(node.Lhs[i], node.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i := range node.Values {
				if i < len(node.Names) {
					leaves = leaves || assigned(node.Names[i], node.Values[i])
				}
			}
		case *ast.SendStmt:
			if refersToOwner(node.Value) {
				name := common.GetVarName(node.Chan)
				leaves = c.worker == nil || !c.worker.isLocallyCreatedChannel(name)
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				leaves = leaves || refersToOwner(result)
			}
		case *ast.CallExpr:
			leaves = c.callHandsOnOwner(node, refersToOwner)
		}
		return !leaves
	})
	return leaves
}

// callHandsOnOwner reports whether a call can carry the owner out of the
// function: as an argument of anything but a builtin, or as the receiver of a
// method the package declares. Calls on the WaitGroup itself (`ctx.wg.Add`)
// do neither.
func (c *Checker) callHandsOnOwner(call *ast.CallExpr, refersToOwner func(ast.Node) bool) bool {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if _, builtin := c.typesInfo.ObjectOf(ident).(*types.Builtin); builtin {
			return false
		}
	}
	for _, arg := range call.Args {
		if refersToOwner(arg) {
			return true
		}
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !refersToOwner(sel.X) || common.IsWaitGroup(c.typesInfo.TypeOf(sel.X)) {
		return false
	}
	return c.callTargetsThisPackage(call)
}

// selectorRoot returns the identifier a selector chain starts from: `uw` in
// `uw.ctx.wg`, `p` in `(*p).wg`.
func selectorRoot(expr ast.Expr) *ast.Ident {
	for {
		switch e := common.UnwrapParenExpr(expr).(type) {
		case *ast.Ident:
			return e
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		default:
			return nil
		}
	}
}

func (c *Checker) isReceiverObject(obj types.Object) bool {
	recv := c.function.Recv
	if recv == nil {
		return false
	}
	for _, field := range recv.List {
		for _, name := range field.Names {
			if c.typesInfo.ObjectOf(name) == obj {
				return true
			}
		}
	}
	return false
}
