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
				d.reportValueSpec(vs)
			}
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				d.reportAssign(s)
			}
		}
	}
}

func (d *loopMutexDetector) reportValueSpec(vs *ast.ValueSpec) {
	for _, name := range vs.Names {
		obj := d.typesInfo.Defs[name]
		if obj == nil {
			continue
		}
		typ := obj.Type()
		switch {
		case common.IsMutex(typ):
			d.reporter.AddError(name.Pos(),
				category.MutexInLoop, "mutex '"+name.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		case common.IsRWMutex(typ):
			d.reporter.AddError(name.Pos(),
				category.MutexInLoop, "rwmutex '"+name.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		}
	}
}

func (d *loopMutexDetector) reportAssign(s *ast.AssignStmt) {
	for i, lhs := range s.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || i >= len(s.Rhs) {
			continue
		}
		typ := d.typesInfo.TypeOf(s.Rhs[i])
		if typ == nil {
			continue
		}
		typ = common.DerefOnceAndUnalias(typ)
		switch {
		case common.IsMutex(typ):
			d.reporter.AddError(ident.Pos(),
				category.MutexInLoop, "mutex '"+ident.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		case common.IsRWMutex(typ):
			d.reporter.AddError(ident.Pos(),
				category.MutexInLoop, "rwmutex '"+ident.Name+"' declared inside loop, each iteration creates a new mutex that cannot protect shared state")
		}
	}
}
