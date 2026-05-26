// Package filesetup runs the per-file bookkeeping that every sub-analyzer
// would otherwise repeat: identifying generated files (so they can be
// skipped) and building one CommentFilter per source file (so inline
// //nolint-style directives can be consulted at report time).
//
// It exists as its own analysis.Analyzer so the work happens once per
// package. The mutex, waitgroup and copycheck sub-analyzers declare it in
// their Requires and consume *Result via pass.ResultOf[filesetup.Analyzer].
package filesetup

import (
	"go/token"
	"reflect"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/category"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/common/report"
	"golang.org/x/tools/go/analysis"
)

// Result groups the per-file information consumed by the sub-analyzers.
// Generated is keyed by *token.File so callers can test membership with
// the token.File they already have from pass.Fset.File(pos). Filters is
// keyed by the file name (the same string CommentFilter exposes via
// FileName) so it can be looked up from a diagnostic's filename.
type Result struct {
	Generated map[*token.File]struct{}
	Filters   map[string]*commentfilter.CommentFilter
}

// Analyzer computes Result once per package.
var Analyzer = &analysis.Analyzer{
	Name:       "goconcurrencylint_filesetup",
	Doc:        "Identifies generated files and builds per-file CommentFilters reused by the sync sub-analyzers.",
	Run:        run,
	ResultType: reflect.TypeFor[*Result](),
}

func run(pass *analysis.Pass) (any, error) {
	res := &Result{
		Generated: make(map[*token.File]struct{}),
		Filters:   make(map[string]*commentfilter.CommentFilter, len(pass.Files)),
	}
	for _, file := range pass.Files {
		tokFile := pass.Fset.File(file.Pos())
		if common.IsGeneratedFile(file) {
			if tokFile != nil {
				res.Generated[tokFile] = struct{}{}
			}
			continue
		}
		cf := commentfilter.NewCommentFilter(pass.Fset, file)
		if name := cf.FileName(); name != "" {
			res.Filters[name] = cf
		}
	}
	return res, nil
}

// IsGenerated reports whether tokFile is a generated file scanned at setup
// time. A nil tokFile is treated as not-generated.
func (r *Result) IsGenerated(tokFile *token.File) bool {
	if r == nil || tokFile == nil {
		return false
	}
	_, ok := r.Generated[tokFile]
	return ok
}

// FilterFor returns the CommentFilter for tokFile, or nil if the file has
// no associated filter (generated files, files without a name, or a nil
// tokFile).
func (r *Result) FilterFor(tokFile *token.File) *commentfilter.CommentFilter {
	if r == nil || tokFile == nil {
		return nil
	}
	return r.Filters[tokFile.Name()]
}

// IgnoreFunc returns a report.IgnoreFunc that consults the per-file
// CommentFilters for inline directives. Returns nil when no filters were
// registered, so report.ErrorCollector skips filtering entirely.
func (r *Result) IgnoreFunc() report.IgnoreFunc {
	if r == nil || len(r.Filters) == 0 {
		return nil
	}
	return func(filename string, line int, cat category.Category) bool {
		cf, ok := r.Filters[filename]
		if !ok {
			return false
		}
		return cf.IsCategoryIgnored(line, cat)
	}
}
