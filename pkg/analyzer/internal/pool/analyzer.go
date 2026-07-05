// Package pool detects misuse of sync.Pool.
package pool

import (
	"go/ast"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// Checker reports non-pointer values placed in a sync.Pool: a Put whose
// argument, or a New function whose return, is not pointer-shaped. Such a value
// is boxed into the pool's internal interface, which heap-allocates on every
// call and defeats the purpose of the pool. Storing a pointer (or any other
// pointer-shaped value) is always the fix, so the check has no false positives.
// The Put case is what staticcheck flags as SA6002; the New case extends the
// same reasoning to the pool's constructor.
type Checker struct {
	errorCollector report.Reporter
	typesInfo      *types.Info
}

// NewChecker creates a sync.Pool checker. typesInfo is the pass-wide type
// information used to confirm a Put call is really on a sync.Pool and to
// classify the argument's shape.
func NewChecker(errorCollector report.Reporter, typesInfo *types.Info) *Checker {
	return &Checker{
		errorCollector: errorCollector,
		typesInfo:      typesInfo,
	}
}

// CheckCall flags p.Put(v) (GCL5001) when v's type is not pointer-shaped. The
// type check confirms the receiver is a real sync.Pool and not a same-named
// Put method on another type.
func (c *Checker) CheckCall(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Put" || len(call.Args) != 1 {
		return
	}
	if !common.IsPool(c.typesInfo.TypeOf(sel.X)) {
		return
	}

	argType := c.typesInfo.TypeOf(call.Args[0])
	if argType == nil {
		return
	}
	// Put(nil) stores a nil interface, which does not allocate.
	if basic, ok := argType.Underlying().(*types.Basic); ok && basic.Kind() == types.UntypedNil {
		return
	}
	if isPointerShaped(argType) {
		return
	}

	c.errorCollector.AddError(call.Args[0].Pos(), category.PoolNonPointerValue,
		"sync.Pool.Put stores non-pointer value of type "+typeName(argType)+", which allocates on every call; store a pointer instead")
}

// CheckCompositeLit flags a sync.Pool{New: func() any { return v }} whose New
// returns a non-pointer value: the returned value is boxed into the pool's
// interface on every miss, exactly like a non-pointer Put.
func (c *Checker) CheckCompositeLit(cl *ast.CompositeLit) {
	// Cheap reject first, like CheckCall: most composite literals in a package
	// have no "New" key at all, so look for one before paying for a type
	// lookup on every literal.
	fn := newFuncLitElt(cl)
	if fn == nil {
		return
	}
	if !common.IsPool(c.typesInfo.TypeOf(cl)) {
		return
	}
	c.checkNewValue(fn)
}

// newFuncLitElt returns the function literal assigned to the "New" key in a
// composite literal, or nil if there is no such key or its value is not a
// literal.
func newFuncLitElt(cl *ast.CompositeLit) *ast.FuncLit {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "New" {
			if fn, ok := kv.Value.(*ast.FuncLit); ok {
				return fn
			}
		}
	}
	return nil
}

// CheckAssign flags p.New = func() any { return v } for the same reason as
// CheckCompositeLit.
func (c *Checker) CheckAssign(assign *ast.AssignStmt) {
	for i, lhs := range assign.Lhs {
		sel, ok := lhs.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "New" || i >= len(assign.Rhs) {
			continue
		}
		if !common.IsPool(c.typesInfo.TypeOf(sel.X)) {
			continue
		}
		if fn, ok := assign.Rhs[i].(*ast.FuncLit); ok {
			c.checkNewValue(fn)
		}
	}
}

// checkNewValue reports a New function literal that returns a non-pointer value.
// To stay conservative it only reasons about a single, unconditional return of
// one value whose type is known; anything more complex (multiple returns,
// unresolved types) is left alone.
func (c *Checker) checkNewValue(fn *ast.FuncLit) {
	expr, ok := singleReturnExpr(fn)
	if !ok {
		return
	}
	t := c.typesInfo.TypeOf(expr)
	if t == nil {
		return
	}
	if basic, ok := t.Underlying().(*types.Basic); ok && basic.Kind() == types.UntypedNil {
		return
	}
	if isPointerShaped(t) {
		return
	}
	c.errorCollector.AddError(expr.Pos(), category.PoolNonPointerValue,
		"sync.Pool.New returns non-pointer value of type "+typeName(t)+", which allocates on every call; return a pointer instead")
}

// singleReturnExpr returns the single returned expression of fn when its body
// has exactly one return statement of exactly one value, ignoring returns
// inside nested function literals. The second result is false otherwise.
func singleReturnExpr(fn *ast.FuncLit) (ast.Expr, bool) {
	if fn.Body == nil {
		return nil, false
	}
	var returns []*ast.ReturnStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false // a nested literal's returns are not fn's returns
		case *ast.ReturnStmt:
			returns = append(returns, node)
		}
		return true
	})
	if len(returns) != 1 || len(returns[0].Results) != 1 {
		return nil, false
	}
	return returns[0].Results[0], true
}

// isPointerShaped reports whether a value of type t is stored directly in an
// interface without a heap allocation. Two independent runtime rules make that
// true: t is "direct interface" shaped (pointers, channels, maps, funcs,
// unsafe.Pointer, interfaces, and single-field structs / single-element arrays
// that are themselves pointer-shaped), or t is zero-size (empty struct, [0]T),
// in which case every value shares runtime.zerobase and boxing never
// allocates regardless of shape. Anything else — basic values, slices,
// strings, multi-field structs — is boxed and allocates on every Put. Unknown
// shapes (type parameters resolve to their interface constraint) fall into the
// interface case and are treated as pointer-shaped, so the check never fires
// on them.
func isPointerShaped(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Pointer, *types.Chan, *types.Map, *types.Signature, *types.Interface:
		return true
	case *types.Basic:
		return u.Kind() == types.UnsafePointer
	case *types.Struct:
		if u.NumFields() == 0 {
			return true // struct{}: zero-size, never allocates
		}
		return u.NumFields() == 1 && isPointerShaped(u.Field(0).Type())
	case *types.Array:
		if u.Len() == 0 {
			return true // [0]T: zero-size, never allocates
		}
		return u.Len() == 1 && isPointerShaped(u.Elem())
	default:
		return false
	}
}

// typeName renders t with package names (not full import paths) for a readable
// diagnostic, e.g. "bytes.Buffer" rather than the fully qualified path.
func typeName(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}
