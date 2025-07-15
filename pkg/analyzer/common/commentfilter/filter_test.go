package commnetfilter

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
	// Test with code that's actually positioned inside comment ranges
	// This is tricky because Go parser won't parse invalid syntax inside comments
	// Let's test with artificial positions that fall within comment ranges

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

	// Test actual function calls (should be outside comments)
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "mu" {
					pos := fset.Position(call.Pos())
					inComment := cf.IsInComment(call.Pos())

					t.Logf("Call %s.%s at line %d, col %d - InComment: %v",
						ident.Name, sel.Sel.Name, pos.Line, pos.Column, inComment)

					// These should NOT be in comments
					assert.False(t, inComment, "Real function calls should not be in comments")
				}
			}
		}
		return true
	})

	// Now test with artificial positions that fall within comment ranges
	// Find the comment and test positions within it
	require.Greater(t, len(file.Comments), 0, "Should have at least one comment")

	comment := file.Comments[0].List[0] // First comment
	commentStart := fset.Position(comment.Pos())
	commentEnd := fset.Position(comment.End())

	t.Logf("Comment range: line %d col %d to line %d col %d",
		commentStart.Line, commentStart.Column, commentEnd.Line, commentEnd.Column)

	// Test a position that's clearly inside the comment
	// Let's create a position in the middle of the comment
	midCommentFile := fset.AddFile("test.go", -1, 1000)
	midCommentFile.AddLine(commentStart.Line + 1) // Add line info

	// Create a position in the middle of the comment (line 5, column 10)
	midCommentFile.Pos(50)
	midPosition := token.Position{
		Filename: "test.go",
		Offset:   50,
		Line:     commentStart.Line + 1, // Line 5
		Column:   10,                    // Column 10
	}

	// Test our comment detection with this middle position
	isInComment := cf.positionInComment(midPosition, comment)
	assert.True(t, isInComment, "Position in middle of comment should be detected as in comment")
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

func TestCommentFilter_LineCommentDetection(t *testing.T) {
	src := `package main

func main() {
	mu.Lock()  // This comment starts at column 13
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Find the line comment
	require.Greater(t, len(file.Comments), 0, "Should have a comment")
	comment := file.Comments[0].List[0]
	commentPos := fset.Position(comment.Pos())

	t.Logf("Line comment: %q at line %d col %d", comment.Text, commentPos.Line, commentPos.Column)

	// Test positions on the same line
	testPositions := []struct {
		column   int
		expected bool
		desc     string
	}{
		{1, false, "beginning of line - before comment"},
		{10, false, "before comment starts"},
		{commentPos.Column, true, "at comment start"},
		{commentPos.Column + 5, true, "inside comment"},
		{commentPos.Column + 20, true, "further inside comment"},
	}

	for _, test := range testPositions {
		testPos := token.Position{
			Filename: commentPos.Filename,
			Line:     commentPos.Line,
			Column:   test.column,
		}

		result := cf.positionInComment(testPos, comment)
		assert.Equal(t, test.expected, result,
			"Position at col %d should be %v (%s)", test.column, test.expected, test.desc)
	}
}

func TestCommentFilter_BlockCommentDetection(t *testing.T) {
	src := `package main

func main() {
	/* This is a 
	   multi-line 
	   block comment */
	mu.Lock()
}
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	require.NoError(t, err)

	cf := NewCommentFilter(fset, file)

	// Find the block comment
	require.Greater(t, len(file.Comments), 0, "Should have a comment")
	comment := file.Comments[0].List[0]
	commentStart := fset.Position(comment.Pos())
	commentEnd := fset.Position(comment.End())

	t.Logf("Block comment from line %d col %d to line %d col %d",
		commentStart.Line, commentStart.Column, commentEnd.Line, commentEnd.Column)

	// Test various positions relative to the block comment
	testCases := []struct {
		line     int
		column   int
		expected bool
		desc     string
	}{
		{commentStart.Line, commentStart.Column, true, "start of comment"},
		{commentStart.Line, commentStart.Column + 5, true, "inside first line"},
		{commentStart.Line + 1, 5, true, "middle line of comment"},
		{commentEnd.Line, commentEnd.Column, true, "end of comment"},
		{commentEnd.Line + 1, 2, false, "after comment"},
		{commentStart.Line - 1, 5, false, "before comment"},
	}

	for _, test := range testCases {
		testPos := token.Position{
			Filename: commentStart.Filename,
			Line:     test.line,
			Column:   test.column,
		}

		result := cf.positionInComment(testPos, comment)
		assert.Equal(t, test.expected, result,
			"Position at line %d col %d should be %v (%s)",
			test.line, test.column, test.expected, test.desc)
	}
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

func TestCommentFilter_UnsupportedCommentType(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", "package main\nfunc main() { mu.Lock() }", parser.ParseComments)
	require.NoError(t, err)

	// Create a mock comment that doesn't start with // or /*
	mockComment := &ast.Comment{
		Slash: token.Pos(1),
		Text:  "unknown comment type",
	}

	cf := NewCommentFilter(fset, file)

	// Find a real position to test
	var testPos token.Position
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			testPos = fset.Position(call.Pos())
			return false
		}
		return true
	})

	// Test the positionInComment method directly with unsupported comment
	assert.False(t, cf.positionInComment(testPos, mockComment),
		"Unsupported comment type should return false")
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
