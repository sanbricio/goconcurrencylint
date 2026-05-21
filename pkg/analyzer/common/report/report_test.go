package report

import (
	"go/token"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/category"
	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
)

func TestErrorCollector_ReportAll(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("foo.go", -1, 100)
	pos1, pos2, pos3 := file.Pos(10), file.Pos(30), file.Pos(20)

	t.Run("basic order", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "cat", "first error")
		ec.AddError(pos2, "cat", "second error")
		ec.AddError(pos3, "cat", "third error")

		var reported []struct {
			Pos     token.Position
			Message string
		}
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, struct {
					Pos     token.Position
					Message string
				}{
					Pos:     fset.Position(d.Pos),
					Message: d.Message,
				})
			},
		}
		ec.ReportAll(pass, nil)
		assert.Len(t, reported, 3)
		assert.Equal(t, "foo.go", reported[0].Pos.Filename)
		assert.Equal(t, "first error", reported[0].Message)
		assert.Equal(t, "foo.go", reported[1].Pos.Filename)
		assert.Equal(t, "third error", reported[1].Message)
		assert.Equal(t, "foo.go", reported[2].Pos.Filename)
		assert.Equal(t, "second error", reported[2].Message)
		assert.True(t, reported[0].Pos.Offset < reported[1].Pos.Offset)
		assert.True(t, reported[1].Pos.Offset < reported[2].Pos.Offset)
	})

	t.Run("no errors", func(t *testing.T) {
		ec := &ErrorCollector{}
		var reported []struct {
			Pos     token.Position
			Message string
		}
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, struct {
					Pos     token.Position
					Message string
				}{
					Pos:     fset.Position(d.Pos),
					Message: d.Message,
				})
			},
		}
		ec.ReportAll(pass, nil)
		assert.Len(t, reported, 0)
	})

	t.Run("same position", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "cat", "err1")
		ec.AddError(pos1, "cat", "err2")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, []string{"err1", "err2"}, reported)
	})

	t.Run("different files", func(t *testing.T) {
		fset2 := token.NewFileSet()
		fileA := fset2.AddFile("a.go", -1, 100)
		fileB := fset2.AddFile("b.go", -1, 100)
		posA := fileA.Pos(5)
		posB := fileB.Pos(5)
		ec := &ErrorCollector{}
		ec.AddError(posB, "cat", "errB")
		ec.AddError(posA, "cat", "errA")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset2,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, []string{"errA", "errB"}, reported)
	})

	t.Run("different lines in same file", func(t *testing.T) {
		ec := &ErrorCollector{}
		file2 := fset.AddFile("foo2.go", -1, 100)
		file2.AddLine(10)
		file2.AddLine(20)
		file2.AddLine(30)
		file2.AddLine(40)
		posLine1 := file2.LineStart(1)
		posLine5 := file2.LineStart(5)

		ec.AddError(posLine5, "cat", "err5")
		ec.AddError(posLine1, "cat", "err1")

		var reported []struct {
			Line    int
			Message string
		}
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, struct {
					Line    int
					Message string
				}{
					Line:    fset.Position(d.Pos).Line,
					Message: d.Message,
				})
			},
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, 2, len(reported))
		assert.Equal(t, 1, reported[0].Line)
		assert.Equal(t, "err1", reported[0].Message)
		assert.Equal(t, 5, reported[1].Line)
		assert.Equal(t, "err5", reported[1].Message)
	})

	t.Run("empty message", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "cat", "")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, []string{""}, reported)
	})

	t.Run("category preserved", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "lock-without-unlock", "msg")
		var got string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				got = d.Category
			},
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, "lock-without-unlock", got)
	})

	t.Run("ignore func filters by category", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "lock-without-unlock", "kept")
		ec.AddError(pos2, "wait-without-add", "dropped")
		var got []string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				got = append(got, d.Message)
			},
		}
		ignore := func(_ string, _ int, cat category.Category) bool {
			return cat == "wait-without-add"
		}
		ec.ReportAll(pass, ignore)
		assert.Equal(t, []string{"kept"}, got)
	})

	t.Run("dedup by pos+category+message", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "cat", "msg")
		ec.AddError(pos1, "cat", "msg")          // exact dup
		ec.AddError(pos1, "other-cat", "msg")    // different category, kept
		ec.AddError(pos1, "cat", "different")    // different message, kept
		var n int
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) { n++ },
		}
		ec.ReportAll(pass, nil)
		assert.Equal(t, 3, n)
	})
}
