// Package checker implements a linter for detecting common mistakes
// in the use of sync.Mutex and sync.WaitGroup.
//
// Copyright (c) 2025 Santiago Bricio
// License: MIT
//
// Author: Santiago Bricio (sanbriciorojas11@gmail.com)

package checker

import (
	"go/ast"

	"github.com/sanbricio/concurrency-linter/checker/common"
	"github.com/sanbricio/concurrency-linter/checker/report"
	"golang.org/x/tools/go/analysis"
)

// Analyzer is the main entry point for the goconcurrentlint linter.
// It detects common mistakes in the use of sync.Mutex and sync.WaitGroup:
// - Locks without unlocks
// - Add without Done
var Analyzer = &analysis.Analyzer{
	Name: "goconcurrentlint",
	Doc:  "Detects common mistakes in the use of sync.Mutex and sync.WaitGroup: locks without unlock and Add without Done.",
	Run:  run,
}

func run(pass *analysis.Pass) (interface{}, error) {
	errorCollector := &report.ErrorCollector{}

	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			muNames := map[string]bool{}
			rwNames := map[string]bool{}
			wgNames := map[string]bool{}

			// Find all mutex and waitgroup declarations
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for _, name := range vs.Names {
					typ := pass.TypesInfo.TypeOf(vs.Type)
					if typ == nil && len(vs.Values) > 0 {
						typ = pass.TypesInfo.TypeOf(vs.Values[0])
					}
					if typ == nil {
						continue
					}
					if common.IsMutex(typ) {
						muNames[name.Name] = true
					}
					if common.IsRWMutex(typ) {
						rwNames[name.Name] = true
					}
					if common.IsWaitGroup(typ) {
						wgNames[name.Name] = true
					}
				}
				return true
			})

			// Analyze mutex usage
			errs := &deferErrorCollector{
				badDeferUnlock:  map[string]bool{},
				badDeferRUnlock: map[string]bool{},
			}
			blockStats := AnalyzeMutexBlock(errorCollector, fn.Body, muNames, rwNames, errs)
			for mu, st := range blockStats {
				if errs.badDeferUnlock[mu] {
					continue
				}
				if errs.badDeferRUnlock[mu] {
					continue
				}
				for _, pos := range st.lockPos {
					if rwNames[mu] {
						errorCollector.AddError(pos, "rwmutex '"+mu+"' is locked but not unlocked")
					} else {
						errorCollector.AddError(pos, "mutex '"+mu+"' is locked but not unlocked")
					}
				}
				for _, pos := range st.rlockPos {
					errorCollector.AddError(pos, "rwmutex '"+mu+"' is rlocked but not runlocked")
				}
			}

			// Analyze WaitGroup usage
			wgStats := AnalyzeWaitGroupUsage(errorCollector, fn, wgNames)

			for wgName, stats := range wgStats {
				if isWaitGroupPassedToOtherFunctions(fn, wgName) {
					if stats.doneCount == 0 && !stats.hasDeferDone && len(stats.addCalls) > 0 {
						continue
					}
				}
				checkWaitGroupBalance(errorCollector, map[string]*waitGroupStats{wgName: stats})
			}
		}
	}

	errorCollector.ReportAll(pass)
	return nil, nil
}
