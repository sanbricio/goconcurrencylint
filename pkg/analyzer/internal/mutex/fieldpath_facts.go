package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A function often decides the same thing twice by reading a field of one of
// its parameters, or of its receiver, rather than a local:
//
//	if opt.Slow { v.lock.RLock() }
//	...
//	if opt.Slow { v.lock.RUnlock() }
//
// A field the function never writes has one value for the whole call, just
// like a parameter it never assigns, so a refined run may assume it (see
// refine.go). The field is named by its path below a stable variable: a
// parameter never assigned, a local assigned once outside loops and closures,
// or the receiver; `opt.Slow` and `s.cfg.raft` are such paths.
//
// The path stops being one value when the function changes it: an assignment
// to it or to any path above it below the root, its address taken, or a
// method called with a pointer receiver on a value that holds it. A method
// called through a pointer on the way down can change what the pointer points
// to, so it invalidates the paths below that pointer, but not the pointer
// itself. A callee handed the root as an argument is assumed to leave the
// field alone, the way options and configuration are passed around.

// fieldPathKey names a field path below a variable.
type fieldPathKey struct {
	root types.Object
	path string
}

// fieldPathOf splits a chain of field selections into its root variable and
// its path below it (".a.b").
func fieldPathOf(expr ast.Expr, info *types.Info) (fieldPathKey, bool) {
	var path []string
	for {
		switch node := common.UnwrapParenExpr(expr).(type) {
		case *ast.SelectorExpr:
			selection := info.Selections[node]
			if selection == nil || selection.Kind() != types.FieldVal {
				return fieldPathKey{}, false
			}
			path = append(path, node.Sel.Name)
			expr = node.X
		case *ast.Ident:
			root := info.ObjectOf(node)
			if root == nil || len(path) == 0 {
				return fieldPathKey{}, false
			}
			var b strings.Builder
			for _, name := range slices.Backward(path) {
				b.WriteString("." + name)
			}
			return fieldPathKey{root: root, path: b.String()}, true
		default:
			return fieldPathKey{}, false
		}
	}
}

// pathPrefixOf reports whether below is path itself or a path under it.
func pathPrefixOf(path, below string, strict bool) bool {
	if below == path {
		return !strict
	}
	return path == "" || strings.HasPrefix(below, path+".")
}

// addFieldPaths gives each field path read in body off a stable variable, and
// never changed there, a variable of its own that facts can hold.
func (s *varScope) addFieldPaths(body *ast.BlockStmt, info *types.Info, stable map[types.Object]bool) {
	if body == nil || info == nil {
		return
	}

	// changed paths: the path and everything below it; changedBelow: only
	// what lies below the path.
	type change struct {
		key    fieldPathKey
		strict bool
	}
	var changes []change
	changed := func(expr ast.Expr, strict bool) {
		expr = common.UnwrapParenExpr(expr)
		if ident, ok := expr.(*ast.Ident); ok {
			if root := info.ObjectOf(ident); root != nil {
				changes = append(changes, change{key: fieldPathKey{root: root}, strict: strict})
			}
			return
		}
		if key, ok := fieldPathOf(expr, info); ok {
			changes = append(changes, change{key: key, strict: strict})
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if _, isIdent := lhs.(*ast.Ident); !isIdent {
					changed(lhs, false)
				}
			}
		case *ast.IncDecStmt:
			changed(node.X, false)
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				changed(node.X, false)
			}
		case *ast.RangeStmt:
			for _, expr := range []ast.Expr{node.Key, node.Value} {
				if expr != nil && node.Tok == token.ASSIGN {
					changed(expr, false)
				}
			}
		case *ast.SelectorExpr:
			selection := info.Selections[node]
			if selection == nil || selection.Kind() != types.MethodVal {
				return true
			}
			if _, isPointer := types.Unalias(info.TypeOf(node.X)).Underlying().(*types.Pointer); isPointer {
				changed(node.X, true)
				return true
			}
			if sig, ok := selection.Obj().Type().(*types.Signature); ok && sig.Recv() != nil {
				if _, ptrRecv := sig.Recv().Type().(*types.Pointer); ptrRecv {
					changed(node.X, false)
				}
			}
		}
		return true
	})

	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		key, ok := fieldPathOf(sel, info)
		if !ok || !stable[key.root] {
			return true
		}
		if _, seen := s.fields[key]; seen {
			return true
		}
		typ := info.TypeOf(sel)
		if !factTypeTracked(typ) {
			return true
		}
		for _, c := range changes {
			if c.key.root == key.root && pathPrefixOf(c.key.path, key.path, c.strict) {
				return true
			}
		}
		if s.fields == nil {
			s.fields = make(map[fieldPathKey]types.Object)
		}
		obj := types.NewVar(sel.Pos(), nil, key.root.Name()+key.path, typ)
		s.fields[key] = obj
		s.trackable[obj] = true
		s.invariant[obj] = true
		return true
	})
}

// fieldPathObject returns the variable standing for the field path sel names,
// if it has one.
func (s *varScope) fieldPathObject(sel *ast.SelectorExpr, info *types.Info) types.Object {
	if s == nil || len(s.fields) == 0 || info == nil {
		return nil
	}
	key, ok := fieldPathOf(sel, info)
	if !ok {
		return nil
	}
	return s.fields[key]
}

// receiverObject returns fn's named receiver.
func receiverObject(fn *ast.FuncDecl, info *types.Info) types.Object {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return nil
	}
	name := fn.Recv.List[0].Names[0]
	if name.Name == "_" {
		return nil
	}
	return info.Defs[name]
}

// receiverReassigned reports whether body assigns the receiver or takes its
// address.
func receiverReassigned(body *ast.BlockStmt, recv types.Object, info *types.Info) bool {
	found := false
	is := func(expr ast.Expr) bool {
		ident, ok := common.UnwrapParenExpr(expr).(*ast.Ident)
		return ok && info.ObjectOf(ident) == recv
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if is(lhs) {
					found = true
				}
			}
		case *ast.UnaryExpr:
			if node.Op == token.AND && is(node.X) {
				found = true
			}
		}
		return !found
	})
	return found
}
