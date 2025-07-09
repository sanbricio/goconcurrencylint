package checker

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestMutexAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "mutex")
}

func TestWaitGroupAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "waitgroup")
}
