package mutex

import (
	"go/token"
	"slices"
)

// captureGotoSnapshot records lock state at a `goto label`.
func (ma *Analyzer) captureGotoSnapshot(labelName string, stats map[string]*Stats) {
	if labelName == "" || stats == nil {
		return
	}
	if ma.labelGotoSnapshots == nil {
		ma.labelGotoSnapshots = make(map[string]map[string]*Stats)
	}

	existing := ma.labelGotoSnapshots[labelName]
	if existing == nil {
		ma.labelGotoSnapshots[labelName] = ma.cloneStatsMap(stats)
		return
	}
	mergeStatsByMax(existing, stats)
}

// applyLabelSnapshot merges lock state captured for `labelName`.
func (ma *Analyzer) applyLabelSnapshot(labelName string, stats map[string]*Stats) {
	if labelName == "" || ma.labelGotoSnapshots == nil {
		return
	}
	snapshot, ok := ma.labelGotoSnapshots[labelName]
	if !ok {
		return
	}
	mergeStatsByMax(stats, snapshot)
}

// mergeStatsByMax keeps the largest count and matching positions per mutex.
func mergeStatsByMax(dst, src map[string]*Stats) {
	for name, srcStats := range src {
		if srcStats == nil {
			continue
		}
		if dst[name] == nil {
			dst[name] = &Stats{}
		}
		mergeStatByMax(dst[name], srcStats)
	}
}

func mergeStatByMax(dst, src *Stats) {
	if src.lock > dst.lock {
		dst.lock = src.lock
		dst.lockPos = clonePositions(src.lockPos)
	}
	if src.rlock > dst.rlock {
		dst.rlock = src.rlock
		dst.rlockPos = clonePositions(src.rlockPos)
	}
	if src.borrowedLock > dst.borrowedLock {
		dst.borrowedLock = src.borrowedLock
		dst.borrowedUnlockPos = clonePositions(src.borrowedUnlockPos)
	}
	if src.borrowedRLock > dst.borrowedRLock {
		dst.borrowedRLock = src.borrowedRLock
		dst.borrowedRUnlockPos = clonePositions(src.borrowedRUnlockPos)
	}
	if src.deferUnlock > dst.deferUnlock {
		dst.deferUnlock = src.deferUnlock
	}
	if src.deferRUnlock > dst.deferRUnlock {
		dst.deferRUnlock = src.deferRUnlock
	}
}

func clonePositions(positions []token.Pos) []token.Pos {
	return slices.Clone(positions)
}
