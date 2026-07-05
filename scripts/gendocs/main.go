// Command gendocs regenerates the check documentation from the catalogue in
// pkg/analyzer. It is the single generator behind:
//
//   - docs/checks/README.md          the catalogue index
//   - docs/checks/GCLxxxx.md         one page per check
//   - README.md                      the "## Checks" table (between markers)
//
// Every output is derived from analyzer.Checks(), so the registry stays the
// single source of truth. Run it with `go generate ./...` (wired from
// pkg/analyzer) or directly:
//
//	go run ./scripts/gendocs           # rewrite docs in place
//	go run ./scripts/gendocs -check    # fail if docs are stale (for CI)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
)

const (
	tableBeginMarker = "<!-- BEGIN GENERATED CHECKS TABLE -->"
	tableEndMarker   = "<!-- END GENERATED CHECKS TABLE -->"
	generatedNote    = "<sub>Generated from the check registry by `scripts/gendocs` — do not edit by hand.</sub>"
)

// group describes one primitive bucket, identified by the leading digit of the
// code (GCL1xxx, GCL2xxx, ...).
type group struct {
	prefix byte
	title  string
}

var groups = []group{
	{'1', "sync.Mutex / sync.RWMutex"},
	{'2', "sync.WaitGroup"},
	{'3', "sync.Once"},
	{'4', "sync.Cond"},
	{'5', "sync.Pool"},
	{'6', "Channels"},
	{'9', "Cross-cutting"},
}

func main() {
	root := flag.String("root", "", "repository root (default: auto-detect from go.mod)")
	check := flag.Bool("check", false, "verify docs are up to date instead of writing them")
	flag.Parse()

	dir := *root
	if dir == "" {
		var err error
		if dir, err = findRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "gendocs:", err)
			os.Exit(1)
		}
	}

	if err := run(dir, *check); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

// findRoot walks up from the working directory until it finds the module root
// (the directory holding go.mod), so the tool works from any package dir
// (e.g. when invoked via `go generate`).
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func run(root string, check bool) error {
	checks := analyzer.Checks()

	files := map[string]string{
		filepath.Join(root, "docs", "checks", "README.md"): genIndex(checks),
	}
	for _, c := range checks {
		files[filepath.Join(root, "docs", "checks", c.Code+".md")] = genPage(c)
	}

	readmePath := filepath.Join(root, "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("reading README: %w", err)
	}
	updated, err := replaceTable(string(readme), genReadmeTable(checks))
	if err != nil {
		return err
	}
	files[readmePath] = updated

	if check {
		return verify(files)
	}
	return writeAll(files)
}

func writeAll(files map[string]string) error {
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

func verify(files map[string]string) error {
	var stale []string
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			stale = append(stale, path)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("docs are out of date, run `go generate ./...`:\n  %s",
			strings.Join(stale, "\n  "))
	}
	return nil
}

func genIndex(checks []analyzer.Check) string {
	var b strings.Builder
	b.WriteString("# Check catalogue\n\n")
	b.WriteString("Every diagnostic `goconcurrencylint` can emit, with its stable code. ")
	b.WriteString("The code is shown in the message (e.g. `GCL1001: ...`) and carried as the ")
	b.WriteString("[`analysis.Diagnostic.Category`](https://pkg.go.dev/golang.org/x/tools/go/analysis#Diagnostic). ")
	b.WriteString("Run `goconcurrencylint explain <code>` for any check.\n")

	for _, g := range groups {
		rows := filterByPrefix(checks, g.prefix)
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", g.title)
		b.WriteString("| Code | Slug | Description |\n|------|------|-------------|\n")
		for _, c := range rows {
			fmt.Fprintf(&b, "| [%s](%s.md) | `%s` | %s |\n", c.Code, c.Code, c.Slug, c.Summary)
		}
	}

	fmt.Fprintf(&b, "\n%s\n", generatedNote)
	return b.String()
}

func genPage(c analyzer.Check) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s\n\n", c.Code, c.Slug)
	fmt.Fprintf(&b, "> %s\n\n", c.Summary)

	b.WriteString("|           |                              |\n")
	b.WriteString("|-----------|------------------------------|\n")
	fmt.Fprintf(&b, "| Code      | `%s` |\n", c.Code)
	fmt.Fprintf(&b, "| Slug      | `%s` |\n", c.Slug)
	fmt.Fprintf(&b, "| Primitive | %s |\n", codeSpans(c.Primitive))

	fmt.Fprintf(&b, "\n## Why it matters\n\n%s\n", c.Why)

	b.WriteString("\n## Examples\n\n")
	b.WriteString("The linter flags code like this:\n\n")
	fmt.Fprintf(&b, "```go\n%s\n```\n\n", strings.TrimSpace(c.Bad))
	b.WriteString("Write it like this instead:\n\n")
	fmt.Fprintf(&b, "```go\n%s\n```\n", strings.TrimSpace(c.Good))

	b.WriteString("\n## Suppressing this check\n\n")
	b.WriteString("Add an inline directive on the offending line. Either the canonical code ")
	b.WriteString("or the legacy slug is accepted:\n\n")
	b.WriteString("```go\n")
	fmt.Fprintf(&b, "foo() // goconcurrencylint:ignore %s\n", c.Code)
	fmt.Fprintf(&b, "foo() // goconcurrencylint:ignore %s\n", c.Slug)
	b.WriteString("```\n")

	b.WriteString("\nSee the [check catalogue](README.md) for every check.\n")

	fmt.Fprintf(&b, "\n%s\n", generatedNote)
	return b.String()
}

func genReadmeTable(checks []analyzer.Check) string {
	var b strings.Builder
	b.WriteString("| Code | Slug | Primitive | Description |\n")
	b.WriteString("|------|------|-----------|-------------|\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "| [`%s`](docs/checks/%s.md) | `%s` | %s | %s |\n",
			c.Code, c.Code, c.Slug, codeSpans(c.Primitive), c.Summary)
	}
	return b.String()
}

// replaceTable swaps the content between the table markers in the README,
// keeping everything else byte-for-byte.
func replaceTable(readme, table string) (string, error) {
	start := strings.Index(readme, tableBeginMarker)
	end := strings.Index(readme, tableEndMarker)
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("README is missing the %s / %s markers", tableBeginMarker, tableEndMarker)
	}
	var b strings.Builder
	b.WriteString(readme[:start])
	b.WriteString(tableBeginMarker)
	b.WriteString("\n")
	b.WriteString(table)
	b.WriteString(tableEndMarker)
	b.WriteString(readme[end+len(tableEndMarker):])
	return b.String(), nil
}

func filterByPrefix(checks []analyzer.Check, prefix byte) []analyzer.Check {
	var out []analyzer.Check
	for _, c := range checks {
		// Code is "GCL" + digits; the first digit picks the primitive group.
		if len(c.Code) > 3 && c.Code[3] == prefix {
			out = append(out, c)
		}
	}
	return out
}

// codeSpans renders a comma-separated primitive list as Markdown code spans.
func codeSpans(primitive string) string {
	parts := strings.Split(primitive, ", ")
	for i, p := range parts {
		parts[i] = "`" + p + "`"
	}
	return strings.Join(parts, ", ")
}
