package commentfilter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommentFilter_BasicFunctionality(t *testing.T) {
	src := `package main

import "sync"

func main() {
	var mu sync.Mutex
	mu.Lock()   // This should be detected
	// mu.Unlock() - this is just text, not parsed
	
	/* 
	This is a comment block
	with some text
	*/
	
	mu.Lock()   // This should be detected
	mu.Unlock() // This should be detected
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Test that actual code is not in comments
	callCount := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					inComment := cf.IsInComment(call.Pos())

					t.Logf("Call %s.%s at line %d, col %d - InComment: %v",
						ident.Name, sel.Sel.Name, pos.Line, pos.Column, inComment)

					// All actual code should NOT be in comments
					assert.False(t, inComment, "Real code should not be detected as in comment at line %d", pos.Line)
					callCount++
				}
			}
		}
		return true
	})

	assert.Equal(t, 3, callCount, "Should find 3 actual function calls")
}

func TestCommentFilter_CodeInsideComments(t *testing.T) {
	src := `package main

func main() {
	/* This is a block comment spanning multiple lines
	   and we want to test positions within this range
	   to verify our comment detection logic works */

	mu.Lock()   // This is clearly outside
	mu.Unlock() // This is also outside
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					assert.False(t, cf.IsInComment(call.Pos()),
						"Real function calls should not be in comments")
				}
			}
		}
		return true
	})

	// Pos values taken straight from parsed comments must be detected as
	// "in comment" by IsInComment — this is the contract callers rely on.
	require.Greater(t, len(file.Comments), 0, "Should have at least one comment")
	comment := file.Comments[0].List[0]
	assert.True(t, cf.IsInComment(comment.Pos()), "comment.Pos() should be in comment")
	assert.True(t, cf.IsInComment(comment.End()-1), "byte before comment.End() should be in comment")
}

func TestCommentFilter_SpecificCommentRanges(t *testing.T) {
	src := `package main

// Line comment here
func main() {
	/* Block comment */ var x int
	mu.Lock() // Another line comment
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Test each comment individually
	for i, group := range file.Comments {
		for j, comment := range group.List {
			commentStart := fset.Position(comment.Pos())
			commentEnd := fset.Position(comment.End())

			t.Logf("Comment %d.%d: %q at line %d col %d to line %d col %d",
				i, j, comment.Text, commentStart.Line, commentStart.Column,
				commentEnd.Line, commentEnd.Column)

			// Test that the comment start position is detected as in comment
			assert.True(t, cf.IsInComment(comment.Pos()),
				"Comment start should be detected as in comment: %q", comment.Text)
		}
	}

	// Test actual code positions
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					inComment := cf.IsInComment(call.Pos())

					t.Logf("mu.Lock() at line %d col %d - InComment: %v",
						pos.Line, pos.Column, inComment)

					// This should NOT be in comment (it's real code)
					assert.False(t, inComment, "Real mu.Lock() should not be in comment")
				}
			}
		}
		return true
	})
}

func TestShouldSkipCall(t *testing.T) {
	src := `package main

import "sync"

func main() {
	var mu sync.Mutex
	mu.Lock()
	/* comment block */ mu.Unlock() // mixed
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	var normalCalls, skippedCalls int

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					if cf.ShouldSkipCall(call) {
						skippedCalls++
					} else {
						normalCalls++
					}
				}
			}
		}
		return true
	})

	assert.Equal(t, 2, normalCalls, "Expected 2 normal calls")
	assert.Equal(t, 0, skippedCalls, "Expected 0 skipped calls for this test")
}

func TestWaitGroupCommentFilter(t *testing.T) {
	src := `package main

import "sync"

func main() {
	var wg sync.WaitGroup
	wg.Add(1)     // This should be detected
	/* block */ wg.Done() /* comment */     // This should still be detected
	
	wg.Add(1)     // This should be detected  
	wg.Done()     // This should be detected
	wg.Wait()     // This should be detected
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	var addCalls, doneCalls, waitCalls int

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "wg" {
					if !cf.ShouldSkipCall(call) {
						switch sel.Sel.Name {
						case "Add":
							addCalls++
						case "Done":
							doneCalls++
						case "Wait":
							waitCalls++
						}
					}
				}
			}
		}
		return true
	})

	assert.Equal(t, 2, addCalls, "Expected 2 Add calls")
	assert.Equal(t, 2, doneCalls, "Expected 2 Done calls")
	assert.Equal(t, 1, waitCalls, "Expected 1 Wait call")
}

func TestMultiLineCommentFilter(t *testing.T) {
	src := `package main

import "sync"

func main() {
	var mu sync.Mutex
	/*
	This is a multi-line comment
	*/ mu.Lock() /*
	More comment
	*/
	mu.Unlock() // This should be detected
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	results := make(map[int]bool)

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					results[pos.Line] = cf.IsInComment(call.Pos())
				}
			}
		}
		return true
	})

	// We should find calls and check their comment status
	assert.Greater(t, len(results), 0, "Should find at least one call")

	for line, inComment := range results {
		t.Logf("Line %d: inComment=%v", line, inComment)
	}
}

func TestCommentFilter_NoPos(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", "package main", parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Test token.NoPos should return false
	assert.False(t, cf.IsInComment(token.NoPos), "token.NoPos should not be considered in comment")
}

func TestCommentFilter_ShouldSkipStatement(t *testing.T) {
	src := `package main

func main() {
	var mu sync.Mutex
	mu.Lock()
	/* comment */ mu.Unlock()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	var normalStmt, skippedStmt int

	ast.Inspect(file, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.ExprStmt); ok {
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
						if cf.ShouldSkipStatement(stmt) {
							skippedStmt++
						} else {
							normalStmt++
						}
					}
				}
			}
		}
		return true
	})

	assert.Equal(t, 2, normalStmt, "Expected 2 normal statements")
	assert.Equal(t, 0, skippedStmt, "Expected 0 skipped statements for this simple test")
}

func TestCommentFilter_DifferentFiles(t *testing.T) {
	src1 := `package main
// comment in file1
func main() {}
`
	src2 := `package main
func other() {
	mu.Lock()
}
`

	fset := token.NewFileSet()
	file1, err := parser.ParseFile(fset, "file1.go", src1, parser.ParseComments)
	require.NoError(t, err)

	file2, err := parser.ParseFile(fset, "file2.go", src2, parser.ParseComments)
	require.NoError(t, err)

	// Create filter with file1's comments
	cf := NewCommentFilter(fset, file1)

	// Check positions in file2 - should not be affected by file1's comments
	ast.Inspect(file2, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			assert.False(t, cf.IsInComment(call.Pos()),
				"Position in different file should not be affected by comments from another file")
		}
		return true
	})
}

func TestCommentFilter_SingleLineBlockComment(t *testing.T) {
	src := `package main

func main() {
	/* start */ mu.Lock() /* end */
	mu.Unlock()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	results := make(map[int]bool)

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					results[pos.Line] = cf.IsInComment(call.Pos())
				}
			}
		}
		return true
	})

	// Should find both calls
	assert.Equal(t, 2, len(results), "Should find 2 calls")

	for line, inComment := range results {
		t.Logf("Line %d: inComment=%v", line, inComment)
		switch line {
		case 4:
			// mu.Lock() is between comments, so should not be in comment
			assert.False(t, inComment, "mu.Lock() should not be in comment")
		case 5:
			assert.False(t, inComment, "mu.Unlock() should not be in comment")
		}
	}
}

func TestCommentFilter_RealCommentPositions(t *testing.T) {
	src := `package main
// Line comment
/* Block comment */ func main() {
	mu.Lock()  // End of line comment
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Test with real comment positions
	for _, group := range file.Comments {
		for _, comment := range group.List {
			commentPos := fset.Position(comment.Pos())
			t.Logf("Comment at line %d, col %d: %q", commentPos.Line, commentPos.Column, comment.Text)

			// Test positions within the comment
			assert.True(t, cf.IsInComment(comment.Pos()), "Comment start should be in comment")
		}
	}

	// Test actual function calls
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					inComment := cf.IsInComment(call.Pos())
					t.Logf("mu.Lock() at line %d, col %d - InComment: %v", pos.Line, pos.Column, inComment)

					// This mu.Lock() should not be in comment
					assert.False(t, inComment, "Real function call should not be in comment")
				}
			}
		}
		return true
	})
}

func TestCommentFilter_NoComments(t *testing.T) {
	src := `package main

func main() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// No comments, so nothing should be filtered
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			assert.False(t, cf.ShouldSkipCall(call),
				"No calls should be skipped when there are no comments")
		}
		return true
	})
}

func TestCommentFilter_IgnoreDirectiveOnSameLine(t *testing.T) {
	src := `package main

func main() {
	wg.Wait() // goconcurrencylint:ignore wait-without-add
	wg.Wait()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)
	// ShouldSkipCall ignores directives on purpose: they only filter at
	// report time so the analyzer can keep tracking state.
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			assert.False(t, cf.ShouldSkipCall(call), "ShouldSkipCall must not honor ignore directives")
		}
		return true
	})

	var directiveLine int
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			pos := fset.Position(call.Pos())
			if cf.HasIgnoreDirective(call.Pos()) {
				directiveLine = pos.Line
			}
		}
		return true
	})
	assert.NotZero(t, directiveLine, "expected to locate the directive line")
	assert.True(t, cf.IsCategoryIgnored(directiveLine, "wait-without-add"))
	assert.False(t, cf.IsCategoryIgnored(directiveLine, "lock-without-unlock"))
	// Other lines unaffected.
	assert.False(t, cf.IsCategoryIgnored(directiveLine+1, "wait-without-add"))
}

func TestCommentFilter_IgnoreDirectiveRequiresBoundary(t *testing.T) {
	src := `package main

func main() {
	wg.Wait() // see goconcurrencylint:ignore-rationale
	wg.Wait() // goconcurrencylint:ignored-list
	wg.Wait() // goconcurrencylint:ignore
	wg.Wait()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)
	directiveLines := map[int]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if cf.HasIgnoreDirective(call.Pos()) {
			directiveLines[fset.Position(call.Pos()).Line] = true
		}
		return true
	})

	assert.Len(t, directiveLines, 1, "only the bare ignore directive should match; near-misses must not trigger")
}

func TestCommentFilter_IgnoreDirectiveBareSilencesEverything(t *testing.T) {
	src := `package main

func main() {
	mu.Lock() // goconcurrencylint:ignore  any human note here
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	var line int
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			line = fset.Position(call.Pos()).Line
		}
		return true
	})
	require.NotZero(t, line)

	assert.True(t, cf.IsCategoryIgnored(line, "lock-without-unlock"))
	assert.True(t, cf.IsCategoryIgnored(line, "anything"))
}

func TestCommentFilter_IgnoreDirectiveMultipleCategories(t *testing.T) {
	src := `package main

func main() {
	mu.Lock() // goconcurrencylint:ignore lock-without-unlock, defer-lock
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	var line int
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			line = fset.Position(call.Pos()).Line
		}
		return true
	})
	require.NotZero(t, line)

	assert.True(t, cf.IsCategoryIgnored(line, "lock-without-unlock"))
	assert.True(t, cf.IsCategoryIgnored(line, "defer-lock"))
	assert.False(t, cf.IsCategoryIgnored(line, "wait-without-add"))
}
