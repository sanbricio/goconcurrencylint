// Package once detects misuse of sync.Once.
package once

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/primitives"
)

// packageScope holds the package-wide indexes shared by every function in a
// pass: named top-level functions and methods grouped by receiver type, used
// to resolve the function passed to Do.
type packageScope struct {
	topLevelFuncs   map[string]*ast.FuncDecl
	receiverMethods map[string]map[string]*ast.FuncDecl
}

func newPackageScope(files []*ast.File) *packageScope {
	scope := &packageScope{
		topLevelFuncs:   make(map[string]*ast.FuncDecl),
		receiverMethods: common.BuildReceiverMethodMap(files),
	}

	// Only the top-level (receiverless) functions are gathered here; the
	// receiver-method index is shared with mutex via common.
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil || fn.Recv != nil {
				continue
			}
			scope.topLevelFuncs[fn.Name.Name] = fn
		}
	}

	return scope
}

// Checker analyzes sync.Once usage within one function.
type Checker struct {
	pkgOnces       map[string]bool
	errorCollector report.Reporter
	typesInfo      *types.Info
	scope          *packageScope
	function       *ast.FuncDecl
}

// NewChecker creates a sync.Once checker. pkg supplies the package-scope Once
// names: a named top-level function passed to Do can only reach those, never
// the caller's locals.
func NewChecker(errorCollector report.Reporter, pkg *primitives.Result, typesInfo *types.Info, scope *packageScope) *Checker {
	return &Checker{
		pkgOnces:       pkg.Onces,
		errorCollector: errorCollector,
		typesInfo:      typesInfo,
		scope:          scope,
	}
}

func (c *Checker) AnalyzeFunction(fn *ast.FuncDecl) {
	c.function = fn
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Do" || len(call.Args) != 1 {
			return true
		}
		// The type check keeps http.Client.Do and friends out, including
		// when a name collides with a tracked Once.
		if !common.IsOnce(c.typesInfo.TypeOf(sel.X)) {
			return true
		}
		onceName := common.GetVarName(sel.X)
		if onceName == "" || onceName == "?" {
			return true
		}
		c.checkDoCall(call, onceName)
		return true
	})
}

func (c *Checker) checkDoCall(call *ast.CallExpr, onceName string) {
	arg := common.UnwrapParenExpr(call.Args[0])

	if ident, ok := arg.(*ast.Ident); ok && ident.Name == "nil" {
		c.errorCollector.AddError(call.Pos(), category.OnceDoNil,
			"once '"+onceName+"' Do called with nil function (panics at runtime)")
		return
	}

	body, target := c.resolveDoArg(arg, onceName)
	if body == nil {
		return
	}
	if pos, ok := c.blockReachesDo(body, target); ok {
		c.errorCollector.AddError(pos, category.OnceDoDeadlock,
			"once '"+onceName+"' Do called inside its own Do function (sync.Once.Do is not reentrant and deadlocks)")
	}
}

// resolveDoArg resolves the function passed to Do to a body to scan, together
// with the name the same Once goes by inside that body. It returns a nil body
// when the argument cannot be resolved within the package.
func (c *Checker) resolveDoArg(arg ast.Expr, onceName string) (*ast.BlockStmt, string) {
	switch fn := arg.(type) {
	case *ast.FuncLit:
		// A literal closes over the caller's scope: same name, same Once.
		return fn.Body, onceName

	case *ast.Ident:
		// A local `f := func() {...}` also closes over the caller's scope.
		if lit := c.localFunctionLiteral(fn.Name); lit != nil {
			return lit.Body, onceName
		}
		// A named top-level function can only reach package-level Onces.
		if c.pkgOnces[onceName] {
			if decl := c.scope.topLevelFuncs[fn.Name]; decl != nil {
				return decl.Body, onceName
			}
		}

	case *ast.SelectorExpr:
		// Method value, e.g. s.once.Do(s.init): resolve init on s's type and
		// rewrite the Once path from the caller's variable to the method
		// receiver (s.once -> r.once).
		decl := c.methodDecl(fn)
		if decl == nil {
			return nil, ""
		}
		// Rewriting s.once -> r.once needs both a usable caller path and a
		// named receiver; reaching a package-level Once needs neither.
		base := common.GetVarName(fn.X)
		if recv := common.ReceiverName(decl); recv != "" && base != "" && base != "?" {
			if path, ok := strings.CutPrefix(onceName, base+"."); ok {
				return decl.Body, recv + "." + path
			}
		}
		if c.pkgOnces[onceName] {
			return decl.Body, onceName
		}
	}

	return nil, ""
}

// methodDecl resolves a method value expression (x.name) to the package
// method declaration on x's type, or nil.
func (c *Checker) methodDecl(sel *ast.SelectorExpr) *ast.FuncDecl {
	typeName := common.BaseTypeNameFromType(c.typesInfo.TypeOf(sel.X))
	if typeName == "" {
		return nil
	}
	return c.scope.receiverMethods[typeName][sel.Sel.Name]
}

// localFunctionLiteral finds `name := func() {...}` (or var name = func...)
// in the current function body.
func (c *Checker) localFunctionLiteral(name string) *ast.FuncLit {
	if c.function == nil || c.function.Body == nil {
		return nil
	}

	var found *ast.FuncLit
	ast.Inspect(c.function.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != name || i >= len(node.Rhs) {
					continue
				}
				if lit, ok := node.Rhs[i].(*ast.FuncLit); ok {
					found = lit
				}
			}
		case *ast.ValueSpec:
			for i, ident := range node.Names {
				if ident.Name != name || i >= len(node.Values) {
					continue
				}
				if lit, ok := node.Values[i].(*ast.FuncLit); ok {
					found = lit
				}
			}
		}
		return true
	})

	return found
}

// blockReachesDo reports whether executing block synchronously reaches a
// <target>.Do(...) call, returning the position of that inner call.
func (c *Checker) blockReachesDo(block *ast.BlockStmt, target string) (token.Pos, bool) {
	if block == nil {
		return token.NoPos, false
	}
	for _, stmt := range block.List {
		if pos, ok := c.stmtReachesDo(stmt, target); ok {
			return pos, true
		}
	}
	return token.NoPos, false
}

func (c *Checker) stmtReachesDo(stmt ast.Stmt, target string) (token.Pos, bool) {
	switch s := stmt.(type) {
	case *ast.GoStmt:
		// A goroutine launched inside f runs after the outer Do completes:
		// its Do call blocks until then but does not deadlock the outer Do.
		return token.NoPos, false

	case *ast.ExprStmt:
		return c.exprReachesDo(s.X, target)

	case *ast.DeferStmt:
		// Deferred calls run while the outer Do is still in flight.
		if pos, ok := c.callReachesDo(s.Call, target); ok {
			return pos, true
		}
		return token.NoPos, false

	case *ast.AssignStmt:
		for _, rhs := range s.Rhs {
			if pos, ok := c.exprReachesDo(rhs, target); ok {
				return pos, true
			}
		}

	case *ast.DeclStmt:
		if gen, ok := s.Decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, value := range vs.Values {
					if pos, ok := c.exprReachesDo(value, target); ok {
						return pos, true
					}
				}
			}
		}

	case *ast.ReturnStmt:
		for _, result := range s.Results {
			if pos, ok := c.exprReachesDo(result, target); ok {
				return pos, true
			}
		}

	case *ast.IfStmt:
		if s.Init != nil {
			if pos, ok := c.stmtReachesDo(s.Init, target); ok {
				return pos, true
			}
		}
		if pos, ok := c.exprReachesDo(s.Cond, target); ok {
			return pos, true
		}
		if pos, ok := c.blockReachesDo(s.Body, target); ok {
			return pos, true
		}
		if s.Else != nil {
			if pos, ok := c.stmtReachesDo(s.Else, target); ok {
				return pos, true
			}
		}

	case *ast.ForStmt:
		if s.Init != nil {
			if pos, ok := c.stmtReachesDo(s.Init, target); ok {
				return pos, true
			}
		}
		if s.Cond != nil {
			if pos, ok := c.exprReachesDo(s.Cond, target); ok {
				return pos, true
			}
		}
		if pos, ok := c.blockReachesDo(s.Body, target); ok {
			return pos, true
		}

	case *ast.RangeStmt:
		if pos, ok := c.exprReachesDo(s.X, target); ok {
			return pos, true
		}
		if pos, ok := c.blockReachesDo(s.Body, target); ok {
			return pos, true
		}

	case *ast.SwitchStmt:
		if s.Init != nil {
			if pos, ok := c.stmtReachesDo(s.Init, target); ok {
				return pos, true
			}
		}
		if s.Tag != nil {
			if pos, ok := c.exprReachesDo(s.Tag, target); ok {
				return pos, true
			}
		}
		return c.caseClausesReachDo(s.Body, target)

	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			if pos, ok := c.stmtReachesDo(s.Init, target); ok {
				return pos, true
			}
		}
		return c.caseClausesReachDo(s.Body, target)

	case *ast.SelectStmt:
		for _, clause := range s.Body.List {
			cc, ok := clause.(*ast.CommClause)
			if !ok {
				continue
			}
			for _, st := range cc.Body {
				if pos, ok := c.stmtReachesDo(st, target); ok {
					return pos, true
				}
			}
		}

	case *ast.BlockStmt:
		return c.blockReachesDo(s, target)

	case *ast.LabeledStmt:
		return c.stmtReachesDo(s.Stmt, target)
	}

	return token.NoPos, false
}

func (c *Checker) caseClausesReachDo(body *ast.BlockStmt, target string) (token.Pos, bool) {
	for _, clause := range body.List {
		cc, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, st := range cc.Body {
			if pos, ok := c.stmtReachesDo(st, target); ok {
				return pos, true
			}
		}
	}
	return token.NoPos, false
}

// exprReachesDo scans an expression for Do calls on target. Function literals
// are only entered when they are invoked in place (IIFE); a literal that is
// merely defined does not execute synchronously.
func (c *Checker) exprReachesDo(expr ast.Expr, target string) (token.Pos, bool) {
	if expr == nil {
		return token.NoPos, false
	}

	var (
		foundPos token.Pos
		found    bool
	)
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.FuncLit:
			// Entered explicitly via callReachesDo when invoked in place.
			return false
		case *ast.CallExpr:
			if pos, ok := c.callReachesDo(node, target); ok {
				foundPos, found = pos, true
				return false
			}
		}
		return true
	})
	return foundPos, found
}

// callReachesDo reports whether call is <target>.Do(...) itself or an
// in-place invocation of a literal whose body reaches one.
func (c *Checker) callReachesDo(call *ast.CallExpr, target string) (token.Pos, bool) {
	if c.isDoCallOn(call, target) {
		return call.Pos(), true
	}
	if lit, ok := common.UnwrapParenExpr(call.Fun).(*ast.FuncLit); ok {
		return c.blockReachesDo(lit.Body, target)
	}
	return token.NoPos, false
}

func (c *Checker) isDoCallOn(call *ast.CallExpr, target string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" {
		return false
	}
	if !common.IsOnce(c.typesInfo.TypeOf(sel.X)) {
		return false
	}
	return common.GetVarName(sel.X) == target
}
