package main

import (
	"github.com/sanbricio/concurrency-linter/checker"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(checker.Analyzer)
}
