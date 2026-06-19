package main

import (
	"fmt"
	"io"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
)

// explain implements the `goconcurrencylint explain` subcommand. With no
// arguments it lists the whole catalogue; with one or more ids (codes or
// legacy slugs) it prints the detail for each.
func explain(args []string, out io.Writer) error {
	ew := &errWriter{w: out}

	if len(args) == 0 {
		listChecks(ew)
		return ew.err
	}

	for i, id := range args {
		c, ok := analyzer.Lookup(id)
		if !ok {
			return fmt.Errorf("unknown check %q (run `goconcurrencylint explain` to list every check)", id)
		}
		if i > 0 {
			ew.printf("\n")
		}
		writeCheck(ew, c)
	}
	return ew.err
}

// listChecks prints one aligned row per check: code, slug, summary.
func listChecks(ew *errWriter) {
	checks := analyzer.Checks()
	slugWidth := 0
	for _, c := range checks {
		if len(c.Slug) > slugWidth {
			slugWidth = len(c.Slug)
		}
	}
	for _, c := range checks {
		// Codes are a fixed width (GCLxxxx); pad the slug column to align.
		ew.printf("%-7s  %-*s  %s\n", c.Code, slugWidth, c.Slug, c.Summary)
	}
}

func writeCheck(ew *errWriter, c analyzer.Check) {
	ew.printf("%s  %s\n", c.Code, c.Slug)
	ew.printf("Primitive: %s\n\n", c.Primitive)
	ew.printf("%s\n", c.Summary)
	ew.printf("\nWhy it matters:\n  %s\n", c.Why)
	ew.printf("\nSuppress:  // goconcurrencylint:ignore %s\n", c.Code)
	ew.printf("Docs:      docs/checks/%s.md\n", c.Code)
}

// errWriter defers error handling for a sequence of formatted writes: once a
// write fails, later printf calls are no-ops and the first error is kept.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, a...)
}
