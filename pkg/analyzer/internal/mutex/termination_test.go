package mutex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// These tests exercise terminationAnalyzer in isolation: no Checker, no
// analysis.Pass. We parse small Go snippets, extract AST nodes and feed them
// directly to the collaborator.

// parseBody parses src (a function body, including the outer braces) and
// returns its BlockStmt.
func parseBody(t *testing.T, src string) *ast.BlockStmt {
	t.Helper()
	full := "package p\nfunc f() " + src
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", full, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	return fn.Body
}

// TestBlockContainsReturn verifies that blockContainsReturn returns true when
// a block has a return statement and false when it does not.
func TestBlockContainsReturn(t *testing.T) {
	ta := &terminationAnalyzer{}

	block := parseBody(t, `{ return }`)
	if !ta.blockContainsReturn(block) {
		t.Error("expected blockContainsReturn=true for block with return")
	}

	block = parseBody(t, `{ x := 1; _ = x }`)
	if ta.blockContainsReturn(block) {
		t.Error("expected blockContainsReturn=false for block without return")
	}
}

// TestBlockContainsBreak verifies that blockContainsBreak returns true when a
// break statement is present and false otherwise.
func TestBlockContainsBreak(t *testing.T) {
	ta := &terminationAnalyzer{}

	// Parse a for loop body that contains a break.
	full := `package p
func f() {
	for {
		break
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", full, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	forStmt := fn.Body.List[0].(*ast.ForStmt)
	if !ta.blockContainsBreak(forStmt.Body) {
		t.Error("expected blockContainsBreak=true for loop body with break")
	}

	// A block without break.
	block := parseBody(t, `{ x := 1; _ = x }`)
	if ta.blockContainsBreak(block) {
		t.Error("expected blockContainsBreak=false for block without break")
	}
}

// TestBlockAlwaysTerminates_ReturnTerminates checks that a block ending in
// return is detected as always terminating.
func TestBlockAlwaysTerminates_ReturnTerminates(t *testing.T) {
	ta := &terminationAnalyzer{}

	block := parseBody(t, `{ return }`)
	if !ta.blockAlwaysTerminates(block) {
		t.Error("expected blockAlwaysTerminates=true for block with return")
	}
}

// TestBlockAlwaysTerminates_NoTermination checks that a block with no
// terminating statement is not detected as always terminating.
func TestBlockAlwaysTerminates_NoTermination(t *testing.T) {
	ta := &terminationAnalyzer{}

	block := parseBody(t, `{ x := 1; _ = x }`)
	if ta.blockAlwaysTerminates(block) {
		t.Error("expected blockAlwaysTerminates=false for non-terminating block")
	}
}

// TestBlockAlwaysTerminates_NilBlock checks the nil guard.
func TestBlockAlwaysTerminates_NilBlock(t *testing.T) {
	ta := &terminationAnalyzer{}

	if ta.blockAlwaysTerminates(nil) {
		t.Error("expected blockAlwaysTerminates=false for nil block")
	}
}

// TestBlockAlwaysTerminates_IfElseBothTerminate checks that an if/else where
// both branches terminate is detected as always terminating.
func TestBlockAlwaysTerminates_IfElseBothTerminate(t *testing.T) {
	ta := &terminationAnalyzer{}

	block := parseBody(t, `{
		if true {
			return
		} else {
			return
		}
	}`)
	if !ta.blockAlwaysTerminates(block) {
		t.Error("expected blockAlwaysTerminates=true when both if/else branches return")
	}
}

// TestBlockAlwaysTerminates_IfOnlyTerminates checks that an if without an else
// is NOT detected as always terminating, even if the if branch returns.
func TestBlockAlwaysTerminates_IfOnlyTerminates(t *testing.T) {
	ta := &terminationAnalyzer{}

	block := parseBody(t, `{
		if true {
			return
		}
		x := 1
		_ = x
	}`)
	if ta.blockAlwaysTerminates(block) {
		t.Error("expected blockAlwaysTerminates=false when only if branch (no else) returns")
	}
}

// TestStatementAlwaysTerminates_Panic checks that a panic() call is detected
// as always terminating even when typesInfo is nil.
func TestStatementAlwaysTerminates_Panic(t *testing.T) {
	ta := &terminationAnalyzer{typesInfo: nil}

	block := parseBody(t, `{ panic("oops") }`)
	stmt := block.List[0]
	if !ta.statementAlwaysTerminates(stmt) {
		t.Error("expected statementAlwaysTerminates=true for panic() call")
	}
}

// TestStatementAlwaysTerminates_BreakToken checks that a break statement is
// detected as always terminating via branchTerminatesBlock.
func TestStatementAlwaysTerminates_BreakToken(t *testing.T) {
	ta := &terminationAnalyzer{}

	// Build a bare break BranchStmt by parsing it inside a for loop body.
	full := `package p
func f() {
	for {
		break
	}
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "t.go", full, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	forStmt := fn.Body.List[0].(*ast.ForStmt)
	breakStmt := forStmt.Body.List[0]

	if !ta.statementAlwaysTerminates(breakStmt) {
		t.Error("expected statementAlwaysTerminates=true for break statement")
	}
}

// TestBranchTerminatesBlock checks every branch token individually.
func TestBranchTerminatesBlock(t *testing.T) {
	tests := []struct {
		tok  token.Token
		want bool
	}{
		{token.BREAK, true},
		{token.CONTINUE, true},
		{token.GOTO, true},
		{token.FALLTHROUGH, true},
		{token.RETURN, false},
	}
	for _, tt := range tests {
		got := branchTerminatesBlock(tt.tok)
		if got != tt.want {
			t.Errorf("branchTerminatesBlock(%v) = %v, want %v", tt.tok, got, tt.want)
		}
	}
}

// TestIsFatalMethod checks the set of recognized Fatal* method names.
func TestIsFatalMethod(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Fatal", true},
		{"Fatalf", true},
		{"Fatalln", true},
		{"Error", false},
		{"Log", false},
	}
	for _, tt := range tests {
		got := isFatalMethod(tt.name)
		if got != tt.want {
			t.Errorf("isFatalMethod(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
