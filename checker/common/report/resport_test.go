package report

import (
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
)

func TestErrorCollector_ReportAll(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("foo.go", -1, 100)
	pos1, pos2, pos3 := file.Pos(10), file.Pos(30), file.Pos(20)

	t.Run("basic order", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "first error")
		ec.AddError(pos2, "second error")
		ec.AddError(pos3, "third error")

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
		ec.ReportAll(pass)
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
		ec.ReportAll(pass)
		assert.Len(t, reported, 0)
	})

	t.Run("same position", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "err1")
		ec.AddError(pos1, "err2")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass)
		assert.Equal(t, []string{"err1", "err2"}, reported)
	})

	t.Run("different files", func(t *testing.T) {
		fset2 := token.NewFileSet()
		fileA := fset2.AddFile("a.go", -1, 100)
		fileB := fset2.AddFile("b.go", -1, 100)
		posA := fileA.Pos(5)
		posB := fileB.Pos(5)
		ec := &ErrorCollector{}
		ec.AddError(posB, "errB")
		ec.AddError(posA, "errA")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset2,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass)
		assert.Equal(t, []string{"errA", "errB"}, reported)
	})

	t.Run("different lines in same file", func(t *testing.T) {
		ec := &ErrorCollector{}
		// Creamos archivo con al menos 5 líneas
		file2 := fset.AddFile("foo2.go", -1, 100)
		// AddLine añade inicio de línea en offset creciente, para simular saltos de línea
		file2.AddLine(10)              // línea 2
		file2.AddLine(20)              // línea 3
		file2.AddLine(30)              // línea 4
		file2.AddLine(40)              // línea 5
		posLine1 := file2.LineStart(1) // línea 1
		posLine5 := file2.LineStart(5) // línea 5

		ec.AddError(posLine5, "err5")
		ec.AddError(posLine1, "err1")

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
		ec.ReportAll(pass)
		assert.Equal(t, 2, len(reported))
		assert.Equal(t, 1, reported[0].Line)
		assert.Equal(t, "err1", reported[0].Message)
		assert.Equal(t, 5, reported[1].Line)
		assert.Equal(t, "err5", reported[1].Message)
	})

	t.Run("empty message", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "")
		var reported []string
		pass := &analysis.Pass{
			Fset: fset,
			Report: func(d analysis.Diagnostic) {
				reported = append(reported, d.Message)
			},
		}
		ec.ReportAll(pass)
		assert.Equal(t, []string{""}, reported)
	})

	t.Run("error report struct", func(t *testing.T) {
		rep := ErrorReport{Pos: token.Pos(42), Message: "msg"}
		assert.Equal(t, token.Pos(42), rep.Pos)
		assert.Equal(t, "msg", rep.Message)
	})

	t.Run("direct access to errors field", func(t *testing.T) {
		ec := &ErrorCollector{}
		ec.AddError(pos1, "foo")
		ec.AddError(pos2, "bar")
		assert.Len(t, ec.errors, 2)
		assert.Equal(t, "foo", ec.errors[0].Message)
		assert.Equal(t, "bar", ec.errors[1].Message)
	})
}
