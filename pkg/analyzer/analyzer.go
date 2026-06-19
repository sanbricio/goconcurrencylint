// Package analyzer is the public entry point for the goconcurrencylint
// linter. It exposes a single *analysis.Analyzer that the singlechecker
// binary and golangci-lint can consume.
//
// Internally the work is split across sub-analyzers wired together via
// the go/analysis Requires graph. The umbrella Analyzer is the only
// exported one; the rest live under internal/ and are composed through
// pass.ResultOf.
//
//	Analyzer (umbrella, this package)
//	│   re-emits the diagnostic slices below via pass.Report,
//	│   prefixing each message with its check code (e.g. "GCL1001: ...")
//	│
//	├── mutex.SubAnalyzer ──────┐
//	├── waitgroup.SubAnalyzer ──┤── requires primitives.Analyzer ─┐
//	├── once.SubAnalyzer ───────┤── requires filesetup.Analyzer   │
//	└── copycheck.Analyzer ─────┘                                 └── requires inspect.Analyzer
//
// "A requires B" means B runs first and exposes its Result through
// pass.ResultOf[B]. Each sub-analyzer returns its diagnostic slice as a
// Result instead of calling pass.Report directly. The umbrella below
// re-emits them on its own pass so analysistest and any other consumer
// that targets the umbrella sees the complete diagnostic set.
package analyzer

import (
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/copycheck"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/mutex"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/once"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/internal/waitgroup"
	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "goconcurrencylint",
	Doc:  "Detects misuse of sync.Mutex, sync.RWMutex, sync.WaitGroup and sync.Once, plus copy-by-value of sync primitives.",
	Run:  run,
	Requires: []*analysis.Analyzer{
		mutex.SubAnalyzer,
		waitgroup.SubAnalyzer,
		once.SubAnalyzer,
		copycheck.Analyzer,
	},
}

func run(pass *analysis.Pass) (any, error) {
	subs := []*analysis.Analyzer{
		mutex.SubAnalyzer,
		waitgroup.SubAnalyzer,
		once.SubAnalyzer,
		copycheck.Analyzer,
	}
	for _, sub := range subs {
		diags, ok := pass.ResultOf[sub].([]analysis.Diagnostic)
		if !ok {
			continue
		}
		for _, d := range diags {
			// Surface the check code in the message itself (e.g.
			// "GCL1001: ...") so it is visible in plain CLI output, which
			// otherwise prints only file:line:col + message. The Category
			// field already carries the same code for tooling.
			if d.Category != "" {
				d.Message = d.Category + ": " + d.Message
			}
			pass.Report(d)
		}
	}
	return nil, nil
}
