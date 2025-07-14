// Package commnetfilter provides shared utilities for the concurrency linter
package commnetfilter

import (
	"go/ast"
	"go/token"
	"strings"
)

// CommentFilter helps filter out code that is within comments
type CommentFilter struct {
	fileSet  *token.FileSet
	comments []*ast.CommentGroup
}

// NewCommentFilter creates a new comment filter
func NewCommentFilter(fset *token.FileSet, file *ast.File) *CommentFilter {
	return &CommentFilter{
		fileSet:  fset,
		comments: file.Comments,
	}
}

// IsInComment checks if a position is within a comment
func (cf *CommentFilter) IsInComment(pos token.Pos) bool {
	if pos == token.NoPos {
		return false
	}

	position := cf.fileSet.Position(pos)

	// Check all comment groups
	for _, group := range cf.comments {
		for _, comment := range group.List {
			if cf.positionInComment(position, comment) {
				return true
			}
		}
	}

	return false
}

// ShouldSkipCall checks if a call expression should be skipped
func (cf *CommentFilter) ShouldSkipCall(call *ast.CallExpr) bool {
	return cf.IsInComment(call.Pos())
}

// ShouldSkipStatement checks if an entire statement should be skipped
func (cf *CommentFilter) ShouldSkipStatement(stmt ast.Stmt) bool {
	return cf.IsInComment(stmt.Pos())
}

// positionInComment checks if a position is within a specific comment
func (cf *CommentFilter) positionInComment(pos token.Position, comment *ast.Comment) bool {
	commentStart := cf.fileSet.Position(comment.Pos())
	commentEnd := cf.fileSet.Position(comment.End())

	// Must be in the same file
	if pos.Filename != commentStart.Filename {
		return false
	}

	// For block comments /* */
	if strings.HasPrefix(comment.Text, "/*") {
		return cf.isInBlockComment(pos, commentStart, commentEnd)
	}

	// For line comments //
	if strings.HasPrefix(comment.Text, "//") {
		return cf.isInLineComment(pos, commentStart)
	}

	return false
}

// isInBlockComment checks if position is within a block comment
func (cf *CommentFilter) isInBlockComment(pos, start, end token.Position) bool {
	// Same line start and end
	if start.Line == end.Line {
		return pos.Line == start.Line &&
			pos.Column >= start.Column &&
			pos.Column <= end.Column
	}

	// Multi-line block comment
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

// isInLineComment checks if position is within a line comment
func (cf *CommentFilter) isInLineComment(pos, commentStart token.Position) bool {
	// Line comments only affect their own line
	if pos.Line != commentStart.Line {
		return false
	}

	// Position must be at or after the comment start
	return pos.Column >= commentStart.Column
}
