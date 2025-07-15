package mutex

import (
	"go/ast"
	"go/token"

	commnetfilter "github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/commentfilter"
	"github.com/sanbricio/goconcurrencylint/pkg/analyzer/common/report"
)

// Analyzer handles the analysis of mutex and rwmutex usage
type Analyzer struct {
	mutexNames     map[string]bool
	rwMutexNames   map[string]bool
	errorCollector *report.ErrorCollector
	stats          map[string]*Stats
	deferErrors    *deferErrorCollector
	commentFilter  *commnetfilter.CommentFilter
}

// Stats tracks the state of a mutex within a block
type Stats struct {
	lock, rlock       int
	lockPos, rlockPos []token.Pos
}

// deferErrorCollector tracks defer-related errors to avoid duplicate reporting
type deferErrorCollector struct {
	badDeferUnlock  map[string]bool
	badDeferRUnlock map[string]bool
}

// NewAnalyzer creates a new mutex analyzer
func NewAnalyzer(mutexNames, rwMutexNames map[string]bool, errorCollector *report.ErrorCollector, cf *commnetfilter.CommentFilter) *Analyzer {
	return &Analyzer{
		mutexNames:     mutexNames,
		rwMutexNames:   rwMutexNames,
		errorCollector: errorCollector,
		commentFilter:  cf,
		deferErrors: &deferErrorCollector{
			badDeferUnlock:  make(map[string]bool),
			badDeferRUnlock: make(map[string]bool),
		},
	}
}

// AnalyzeFunction analyzes mutex usage in a function
func (ma *Analyzer) AnalyzeFunction(fn *ast.FuncDecl) {
	ma.initializeStats()
	finalStats := ma.analyzeBlock(fn.Body)
	ma.reportUnmatchedLocks(finalStats)
}

// initializeStats initializes the stats map for all known mutexes
func (ma *Analyzer) initializeStats() {
	ma.stats = make(map[string]*Stats)

	for mutexName := range ma.mutexNames {
		ma.stats[mutexName] = &Stats{}
	}

	for rwMutexName := range ma.rwMutexNames {
		ma.stats[rwMutexName] = &Stats{}
	}
}

// copyStats creates a deep copy of the stats map
func (ma *Analyzer) copyStats(original map[string]*Stats) map[string]*Stats {
	copy := make(map[string]*Stats)
	for name, stats := range original {
		copy[name] = &Stats{
			lock:     stats.lock,
			rlock:    stats.rlock,
			lockPos:  append([]token.Pos{}, stats.lockPos...),
			rlockPos: append([]token.Pos{}, stats.rlockPos...),
		}
	}
	return copy
}

// mergeStats merges stats from a nested block into parent stats
func (ma *Analyzer) mergeStats(parent, child map[string]*Stats) {
	for name, childStats := range child {
		if parentStats, exists := parent[name]; exists {
			parentStats.lock += childStats.lock
			parentStats.rlock += childStats.rlock
			parentStats.lockPos = append(parentStats.lockPos, childStats.lockPos...)
			parentStats.rlockPos = append(parentStats.rlockPos, childStats.rlockPos...)
		}
	}
}

// removeFirstLockPos removes the first lock position from the list
func (ma *Analyzer) removeFirstLockPos(stats *Stats) {
	if len(stats.lockPos) > 0 {
		stats.lockPos = stats.lockPos[1:]
	}
}

// removeFirstRLockPos removes the first rlock position from the list
func (ma *Analyzer) removeFirstRLockPos(stats *Stats) {
	if len(stats.rlockPos) > 0 {
		stats.rlockPos = stats.rlockPos[1:]
	}
}
