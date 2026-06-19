package main

import (
	"fmt"
	"os"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	// Intercept the `explain` subcommand before handing off to the
	// go/analysis driver, which owns flag parsing for the linting path.
	if len(os.Args) > 1 && os.Args[1] == "explain" {
		if err := explain(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "goconcurrencylint:", err)
			os.Exit(2)
		}
		return
	}

	singlechecker.Main(analyzer.Analyzer)
}
