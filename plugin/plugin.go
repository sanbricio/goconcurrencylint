// Package plugin is the entry point for the goconcurrencylint golangci-lint module plugin.
// It exposes the New function required by golangci-lint's module plugin system.
//
// Usage with golangci-lint:
//
//  1. Create a .custom-gcl.yml in your project root.
//     While the plugin is still local to this repository, use a local path:
//
//     version: v2.11.4
//     plugins:
//     - module: "github.com/sanbricio/goconcurrencylint"
//     import: "github.com/sanbricio/goconcurrencylint/plugin"
//     path: .
//
//  2. Build a custom golangci-lint binary:
//
//     golangci-lint custom
//
//  3. Enable the linter in your .golangci.yml:
//
//     linters:
//     enable:
//     - goconcurrencylint
//
//  4. Run the custom binary:
//
//     ./custom-gcl run --config=.golangci.plugin.yml ./...
package plugin

import (
	"github.com/golangci/plugin-module-register/register"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
	"golang.org/x/tools/go/analysis"
)

type linterPlugin struct{}

func init() {
	register.Plugin(analyzer.Analyzer.Name, New)
}

// New is the entry point required by golangci-lint's module plugin system.
func New(_ any) (register.LinterPlugin, error) {
	return &linterPlugin{}, nil
}

// BuildAnalyzers returns the analyzers provided by this plugin.
func (p *linterPlugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{analyzer.Analyzer}, nil
}

// GetLoadMode declares the data needed by the analyzer.
func (p *linterPlugin) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
