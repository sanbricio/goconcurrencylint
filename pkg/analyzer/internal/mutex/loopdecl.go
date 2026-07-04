package mutex

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
)

// loopMutexDetector reports sync.Mutex / sync.RWMutex variables that are
// declared directly inside a loop body. Each iteration creates a fresh mutex
// that is invisible to other iterations and therefore cannot protect shared
// state. It depends only on a reporter and type information, so it can be
// constructed and exercised without a full Checker.
type loopMutexDetector struct {
	reporter  report.Reporter
	typesInfo *types.Info
}

func newLoopMutexDetector(reporter report.Reporter, typesInfo *types.Info) *loopMutexDetector {
	return &loopMutexDetector{reporter: reporter, typesInfo: typesInfo}
}

// check examines the top-level statements of a loop body. Nested loops are
// handled when they themselves are analysed as for/range statements. Function
// literals inside the loop are skipped to avoid false positives for patterns
// like `for { go func() { var mu sync.Mutex; … }() }`.
func (d *loopMutexDetector) check(loopBody *ast.BlockStmt) {
	if loopBody == nil || d.typesInfo == nil {
		return
	}
	for _, stmt := range loopBody.List {
		switch s := stmt.(type) {
		case *ast.DeclStmt:
			gen, ok := s.Decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				d.reportValueSpec(vs, loopBody)
			}
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				d.reportAssign(s, loopBody)
			}
		}
	}
}

func (d *loopMutexDetector) reportValueSpec(vs *ast.ValueSpec, loopBody *ast.BlockStmt) {
	for _, name := range vs.Names {
		obj := d.typesInfo.Defs[name]
		if obj == nil {
			continue
		}
		d.reportMutexDecl(obj.Type(), name.Name, name.Pos(), loopBody)
	}
}

func (d *loopMutexDetector) reportAssign(s *ast.AssignStmt, loopBody *ast.BlockStmt) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(s.Rhs) {
			continue
		}
		typ := d.typesInfo.TypeOf(s.Rhs[i])
		if typ == nil {
			continue
		}
		d.reportMutexDecl(common.DerefOnceAndUnalias(typ), ident.Name, ident.Pos(), loopBody)
	}
}

// reportMutexDecl flags a mutex/rwmutex declared inside a loop, unless the mutex
// is shared with per-iteration goroutines that are joined before the iteration
// ends, in which case a fresh mutex per iteration is intentional.
func (d *loopMutexDetector) reportMutexDecl(typ types.Type, name string, pos token.Pos, loopBody *ast.BlockStmt) {
	isMutex := common.IsMutex(typ)
	isRWMutex := common.IsRWMutex(typ)
	if !isMutex && !isRWMutex {
		return
	}

	if mutexProtectsJoinedWorkers(loopBody, name) {
		return
	}

	mutexType := "mutex"
	if isRWMutex {
		mutexType = "rwmutex"
	}
	d.reporter.AddError(pos, category.MutexInLoop,
		mutexType+" '"+name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
}

// mutexProtectsJoinedWorkers reports whether the loop-declared mutex is captured
// by a goroutine in the same iteration that is then joined (a `.Wait()` call), so
// the workers cannot outlive the iteration.
func mutexProtectsJoinedWorkers(loopBody *ast.BlockStmt, varName string) bool {
	return blockContainsWaitCall(loopBody) && goroutineCapturesVar(loopBody, varName)
}

// blockContainsWaitCall reports whether block contains a `.Wait()` call
// (sync.WaitGroup, errgroup.Group, etc.).
func blockContainsWaitCall(block *ast.BlockStmt) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Wait" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// goroutineCapturesVar reports whether block launches a goroutine — via `go` or
// X.Go(...) (WaitGroup.Go, errgroup) — whose call references varName.
func goroutineCapturesVar(block *ast.BlockStmt, varName string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.GoStmt:
			if exprReferencesVar(node.Call, varName) {
				found = true
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Go" {
				for _, arg := range node.Args {
					if exprReferencesVar(arg, varName) {
						found = true
						break
					}
				}
			}
		}
		return !found
	})
	return found
}

// exprReferencesVar reports whether node's subtree contains an identifier named
// varName.
func exprReferencesVar(node ast.Node, varName string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		if ident, ok := n.(*ast.Ident); ok && ident.Name == varName {
			found = true
			return false
		}
		return true
	})
	return found
}
