// Package commentfilter provides shared utilities for the concurrency linter
package commentfilter

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
)

const ignoreDirective = "goconcurrencylint:ignore"

// CommentFilter helps filter out code that is within comments and tracks
// per-line `goconcurrencylint:ignore` directives. The directive can be
// either bare (silences every category on the line) or carry a list of
// category IDs separated by spaces or commas, e.g.:
//
//	wg.Wait() // goconcurrencylint:ignore wait-without-add
//	mu.Lock() // goconcurrencylint:ignore lock-without-unlock,defer-lock
//	mu.Lock() // goconcurrencylint:ignore     <- silences every check
type CommentFilter struct {
	fileSet      *token.FileSet
	fileName     string
	comments     []*ast.CommentGroup
	ignoreByLine map[int]*ignoreEntry
}

// ignoreEntry records the categories silenced for a given line. all=true
// means a bare directive that silences every category.
type ignoreEntry struct {
	all        bool
	categories map[string]struct{}
}

// NewCommentFilter creates a new comment filter. fileName is derived from
// the first comment in the file when available; this lets the orchestrator
// route filters to diagnostics by filename without an extra parameter.
func NewCommentFilter(fset *token.FileSet, file *ast.File) *CommentFilter {
	var comments []*ast.CommentGroup
	var fileName string
	if file != nil {
		comments = file.Comments
		if file.Pos().IsValid() && fset != nil {
			fileName = fset.Position(file.Pos()).Filename
		}
	}
	cf := &CommentFilter{
		fileSet:      fset,
		fileName:     fileName,
		comments:     comments,
		ignoreByLine: make(map[int]*ignoreEntry),
	}
	cf.indexIgnoreDirectives()
	return cf
}

// FileName returns the source filename this filter was built from.
func (cf *CommentFilter) FileName() string {
	return cf.fileName
}

// IsInComment checks if a position is within a comment.
func (cf *CommentFilter) IsInComment(pos token.Pos) bool {
	if pos == token.NoPos {
		return false
	}

	position := cf.fileSet.Position(pos)

	for _, group := range cf.comments {
		for _, comment := range group.List {
			if cf.positionInComment(position, comment) {
				return true
			}
		}
	}

	return false
}

// ShouldSkipCall reports whether a call expression sits inside a real
// comment. Inline ignore directives are no longer honored here: they only
// suppress diagnostics at report time so the analyzer can still update its
// state correctly.
func (cf *CommentFilter) ShouldSkipCall(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	return cf.IsInComment(call.Pos())
}

// ShouldSkipStatement reports whether a statement sits inside a real
// comment. See ShouldSkipCall for the rationale on directives.
func (cf *CommentFilter) ShouldSkipStatement(stmt ast.Stmt) bool {
	if stmt == nil {
		return false
	}
	return cf.IsInComment(stmt.Pos())
}

// HasIgnoreDirective reports whether any goconcurrencylint:ignore directive
// appears on the line of pos. It does not consider categories; use
// IsCategoryIgnored to check a specific check ID.
func (cf *CommentFilter) HasIgnoreDirective(pos token.Pos) bool {
	if pos == token.NoPos || cf.fileSet == nil {
		return false
	}
	line := cf.fileSet.Position(pos).Line
	_, ok := cf.ignoreByLine[line]
	return ok
}

// IsCategoryIgnored reports whether cat is silenced at the given line.
// A bare directive on that line silences every category. An empty category
// only matches a bare directive (it never matches a per-rule list).
func (cf *CommentFilter) IsCategoryIgnored(line int, cat category.Category) bool {
	entry, ok := cf.ignoreByLine[line]
	if !ok {
		return false
	}
	if entry.all {
		return true
	}
	if cat == "" {
		return false
	}
	_, ok = entry.categories[string(cat)]
	return ok
}

func (cf *CommentFilter) indexIgnoreDirectives() {
	if cf.fileSet == nil {
		return
	}

	for _, group := range cf.comments {
		for _, comment := range group.List {
			commentStart := cf.fileSet.Position(comment.Pos())
			for lineOffset, textLine := range strings.Split(comment.Text, "\n") {
				cats, ok, all := parseIgnoreDirective(textLine)
				if !ok {
					continue
				}
				cf.recordIgnore(commentStart.Line+lineOffset, cats, all)
			}
		}
	}
}

func (cf *CommentFilter) recordIgnore(line int, categories []string, all bool) {
	entry, ok := cf.ignoreByLine[line]
	if !ok {
		entry = &ignoreEntry{categories: make(map[string]struct{})}
		cf.ignoreByLine[line] = entry
	}
	if all {
		entry.all = true
		return
	}
	for _, c := range categories {
		entry.categories[c] = struct{}{}
	}
}

// parseIgnoreDirective scans a single comment line for the directive and
// returns the categories listed after it. all=true is returned for a bare
// directive (no recognised categories) — including the case where what
// follows is a human-readable note.
func parseIgnoreDirective(textLine string) (categories []string, found bool, all bool) {
	start := 0
	for {
		idx := strings.Index(textLine[start:], ignoreDirective)
		if idx < 0 {
			return nil, false, false
		}

		afterStart := start + idx + len(ignoreDirective)
		after := textLine[afterStart:]
		switch {
		case after == "":
			return nil, true, true
		case after[0] == ' ', after[0] == '\t':
			cats := parseCategories(after)
			return cats, true, len(cats) == 0
		case strings.HasPrefix(after, "*/"):
			return nil, true, true
		}
		start = afterStart
	}
}

// parseCategories extracts known check IDs from the tail after the
// directive. Tokens are read until the first one that does not match a
// registered category — anything beyond that is treated as a human-readable
// note (so `// goconcurrencylint:ignore lock-without-unlock because legacy`
// silences only `lock-without-unlock`, while
// `// goconcurrencylint:ignore explained later` is treated as a bare
// directive that silences every check on the line).
//
// Recognised separators are spaces, tabs, commas and semicolons. The block
// comment terminator "*/" ends parsing.
func parseCategories(tail string) []string {
	if idx := strings.Index(tail, "*/"); idx >= 0 {
		tail = tail[:idx]
	}
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return nil
	}

	fields := strings.FieldsFunc(tail, func(r rune) bool {
		switch r {
		case ' ', '\t', ',', ';':
			return true
		}
		return false
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !category.IsKnown(f) {
			break
		}
		out = append(out, f)
	}
	return out
}

// positionInComment checks if a position is within a specific comment
func (cf *CommentFilter) positionInComment(pos token.Position, comment *ast.Comment) bool {
	commentStart := cf.fileSet.Position(comment.Pos())
	commentEnd := cf.fileSet.Position(comment.End())

	if pos.Filename != commentStart.Filename {
		return false
	}

	if strings.HasPrefix(comment.Text, "/*") {
		return cf.isInBlockComment(pos, commentStart, commentEnd)
	}

	if strings.HasPrefix(comment.Text, "//") {
		return cf.isInLineComment(pos, commentStart)
	}

	return false
}

func (cf *CommentFilter) isInBlockComment(pos, start, end token.Position) bool {
	if start.Line == end.Line {
		return pos.Line == start.Line &&
			pos.Column >= start.Column &&
			pos.Column <= end.Column
	}

	if pos.Line > start.Line && pos.Line < end.Line {
		return true
	}

	if pos.Line == start.Line {
		return pos.Column >= start.Column
	}

	if pos.Line == end.Line {
		return pos.Column <= end.Column
	}

	return false
}

func (cf *CommentFilter) isInLineComment(pos, commentStart token.Position) bool {
	if pos.Line != commentStart.Line {
		return false
	}
	return pos.Column >= commentStart.Column
}
