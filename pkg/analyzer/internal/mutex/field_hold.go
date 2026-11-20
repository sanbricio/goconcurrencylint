package mutex

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
)

// A type can keep a lock held between calls and record that it does in a
// field of its own, so a later call knows whether to give it back:
//
//	func (r *Rows) Scan() error {
//		r.mu.RLock()
//		if keep { r.held = true } else { r.mu.RUnlock() }
//		...
//	}
//	func (r *Rows) releaseHeld() {
//		if r.held { r.held = false; r.mu.RUnlock() }
//	}
//
// A pointer field does the same job when what it holds is the reason for the
// lock (`c.pending = &item` with the lock held; `if c.pending == nil { return
// }; c.pending = nil; c.mu.Unlock()`).
//
// The field is a record of the hold exactly when the package releases the
// lock only where the field says it is held, and clears the field there. Then
// setting the field while holding the lock hands the lock over to it, and the
// guarded release gives back a lock the record says is held.

// holdRecord is a field that records a hold of the lock at path, below the same
// object, released with unlock.
type holdRecord struct {
	path   string
	unlock string
}

// holdRecords returns, for each field of the package that records a lock
// hold, the holds it records.
func (l *lifecycleResolver) holdRecords() map[types.Object][]holdRecord {
	if l.scanCache.holdRecords != nil {
		return l.scanCache.holdRecords
	}
	records := make(map[types.Object][]holdRecord)
	if l.typesInfo != nil {
		for _, fn := range l.functions {
			if fn == nil || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := common.UnwrapParenExpr(call.Fun).(*ast.SelectorExpr)
				if !ok || !slices.Contains(anyReleaseMethods, sel.Sel.Name) {
					return true
				}
				lock := common.GetVarName(sel.X)
				base, path, ok := strings.Cut(lock, ".")
				if !ok {
					return true
				}
				// The release gives back a lock taken elsewhere: a function
				// that takes it itself is releasing its own acquisition.
				if functionBodyContainsFieldCall(fn.Body, lock, anyAcquireMethods) {
					return true
				}
				field, ok := l.releaseGuardField(fn, call.Pos(), base)
				if !ok || !clearsField(l.typesInfo, fn.Body, field) {
					return true
				}
				record := holdRecord{path: path, unlock: sel.Sel.Name}
				if !slices.Contains(records[field], record) {
					records[field] = append(records[field], record)
				}
				return true
			})
		}
	}
	l.scanCache.holdRecords = records
	return records
}

// releaseGuardField returns the field of base that guards the release at pos
// in fn: the release sits in the arm of `if base.f` / `if base.f != nil`, or
// after an `if !base.f` / `if base.f == nil` that returns.
func (l *lifecycleResolver) releaseGuardField(fn *ast.FuncDecl, pos token.Pos, base string) (types.Object, bool) {
	var target ast.Stmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if target != nil {
			return false
		}
		if stmt, ok := n.(*ast.ExprStmt); ok && stmt.X.Pos() == pos {
			target = stmt
		}
		return true
	})
	if target == nil {
		return nil, false
	}
	path, ok := findStatementPath(fn.Body.List, target)
	if !ok {
		return nil, false
	}
	for k, frame := range slices.Backward(path) {
		for j := frame.index - 1; j >= 0; j-- {
			guard, ok := frame.list[j].(*ast.IfStmt)
			if !ok || guard.Else != nil || guard.Init != nil || !blockEndsInReturn(guard.Body) {
				continue
			}
			if field, held, ok := l.fieldTest(guard.Cond, base); ok && !held {
				return field, true
			}
		}
		if k == 0 {
			break
		}
		parent, ok := path[k-1].list[path[k-1].index].(*ast.IfStmt)
		if !ok || parent.Init != nil || !sameStatementList(frame.list, parent.Body.List) {
			continue
		}
		if field, held, ok := l.fieldTest(parent.Cond, base); ok && held {
			return field, true
		}
	}
	return nil, false
}

// fieldTest reads cond as a test of a field of base: `base.f` or `base.f !=
// nil` (held is true), `!base.f` or `base.f == nil` (held is false).
func (l *lifecycleResolver) fieldTest(cond ast.Expr, base string) (types.Object, bool, bool) {
	held := true
	cond = common.UnwrapParenExpr(cond)
	if unary, ok := cond.(*ast.UnaryExpr); ok && unary.Op == token.NOT {
		held = false
		cond = common.UnwrapParenExpr(unary.X)
	}
	if binary, ok := cond.(*ast.BinaryExpr); ok && (binary.Op == token.EQL || binary.Op == token.NEQ) {
		if !isNilIdent(common.UnwrapParenExpr(binary.Y)) {
			return nil, false, false
		}
		if binary.Op == token.EQL {
			held = !held
		}
		cond = common.UnwrapParenExpr(binary.X)
	}
	sel, ok := cond.(*ast.SelectorExpr)
	if !ok || common.GetVarName(sel.X) != base {
		return nil, false, false
	}
	selection := l.typesInfo.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return nil, false, false
	}
	return selection.Obj(), held, true
}

// clearsField reports whether body itself, outside function literals, sets
// field to false or nil.
func clearsField(info *types.Info, body *ast.BlockStmt, field types.Object) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for index, lhs := range assign.Lhs {
			sel, ok := common.UnwrapParenExpr(lhs).(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if s := info.Selections[sel]; s == nil || s.Obj() != field {
				continue
			}
			rhs := common.UnwrapParenExpr(assign.Rhs[index])
			if isNilIdent(rhs) {
				found = true
			} else if ident, ok := rhs.(*ast.Ident); ok && ident.Name == "false" {
				found = true
			}
		}
		return true
	})
	return found
}

// applyHoldRecord hands a held lock to the field that records it: `x.f =
// true` (or a non-nil value) where f records holds of x.path, with x.path
// held here.
func (c *Checker) applyHoldRecord(stmt *ast.AssignStmt, stats map[string]*Stats) {
	if c.lifecycle == nil || c.typesInfo == nil || len(stmt.Lhs) != len(stmt.Rhs) {
		return
	}
	for index, lhs := range stmt.Lhs {
		sel, ok := common.UnwrapParenExpr(lhs).(*ast.SelectorExpr)
		if !ok {
			continue
		}
		selection := c.typesInfo.Selections[sel]
		if selection == nil || selection.Kind() != types.FieldVal {
			continue
		}
		records := c.lifecycle.holdRecords()[selection.Obj()]
		if len(records) == 0 {
			continue
		}
		if set, known := c.valueOf(stmt.Rhs[index]); !known || !set {
			continue
		}
		base := common.GetVarName(sel.X)
		for _, record := range records {
			st := stats[base+"."+record.path]
			if st == nil {
				continue
			}
			switch {
			case record.unlock == "RUnlock" && st.rlock > 0:
				st.rlock--
				if n := len(st.rlockPos); n > 0 {
					st.rlockPos = st.rlockPos[:n-1]
				}
			case record.unlock == "Unlock" && st.lock > 0:
				st.lock--
				if n := len(st.lockPos); n > 0 {
					st.lockPos = st.lockPos[:n-1]
				}
			}
		}
	}
}

// releasesRecordedHold reports whether the release at pos gives back a hold a
// field records (see holdRecords).
func (c *Checker) releasesRecordedHold(varName string, pos token.Pos, method string) bool {
	if c.lifecycle == nil || c.function == nil || c.function.Body == nil || c.typesInfo == nil {
		return false
	}
	base, path, ok := strings.Cut(varName, ".")
	if !ok {
		return false
	}
	field, ok := c.lifecycle.releaseGuardField(c.function, pos, base)
	if !ok {
		return false
	}
	return slices.Contains(c.lifecycle.holdRecords()[field], holdRecord{path: path, unlock: method})
}
