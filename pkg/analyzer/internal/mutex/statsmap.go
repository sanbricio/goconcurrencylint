package mutex

import (
	"go/token"
	"slices"
)

func clearStats(stats map[string]*Stats) {
	for name := range stats {
		stats[name] = &Stats{}
	}
}

func emptyStatsLike(stats map[string]*Stats) map[string]*Stats {
	empty := make(map[string]*Stats, len(stats))
	for name := range stats {
		empty[name] = &Stats{}
	}
	return empty
}

// cloneStatsMap returns a new map containing deep copies of every Stats in original.
func cloneStatsMap(original map[string]*Stats) map[string]*Stats {
	copy := make(map[string]*Stats)
	copyStatsMap(copy, original)
	return copy
}

// copyStatsMap copies every entry from src into dst, performing a deep copy
// of each Stats value via copyStats. Keys present in dst but not in src are
// left untouched (merge semantics, not full replacement).
func copyStatsMap(dst, src map[string]*Stats) {
	for name, srcStats := range src {
		if _, exists := dst[name]; !exists {
			dst[name] = &Stats{}
		}
		copyStats(dst[name], srcStats)
	}
}

// initialStats builds a fresh stats map with an empty *Stats entry for every
// known mutex and rwmutex name.
func initialStats(mutexNames, rwMutexNames map[string]bool) map[string]*Stats {
	stats := make(map[string]*Stats)
	for mutexName := range mutexNames {
		stats[mutexName] = &Stats{}
	}
	for rwMutexName := range rwMutexNames {
		stats[rwMutexName] = &Stats{}
	}
	return stats
}

// cloneStats creates a deep copy of a single Stats object.
// If the input is nil, it returns a new initialized empty Stats instance.
func cloneStats(stats *Stats) *Stats {
	if stats == nil {
		return &Stats{}
	}

	clone := &Stats{}
	copyStats(clone, stats)

	return clone
}

// remainingLockCount returns the net lock count after accounting for deferred
// unlocks. If the deferred unlocks cover all locks, it returns 0.
func remainingLockCount(lockCount, deferredUnlocks int) int {
	if lockCount <= deferredUnlocks {
		return 0
	}
	return lockCount - deferredUnlocks
}

// copyStats copies all fields from src into dst, cloning slice fields so
// the two instances do not share backing arrays. It is a no-op if either
// src or dst is nil.
func copyStats(dst, src *Stats) {
	if src == nil || dst == nil {
		return
	}

	dst.lock = src.lock
	dst.rlock = src.rlock
	dst.borrowedLock = src.borrowedLock
	dst.borrowedRLock = src.borrowedRLock
	dst.deferUnlock = src.deferUnlock
	dst.deferRUnlock = src.deferRUnlock
	dst.lockPos = slices.Clone(src.lockPos)
	dst.rlockPos = slices.Clone(src.rlockPos)
	dst.borrowedUnlockPos = slices.Clone(src.borrowedUnlockPos)
	dst.borrowedRUnlockPos = slices.Clone(src.borrowedRUnlockPos)
}

// mergeBranchStats folds the two branch states of an if/else into dst, taking
// pick of each counter and the union of the recorded positions.
//
// It serves the mirrored-condition pairs: when the branches acquire and a later
// `if` on the same condition releases, pick is max, so whichever half ran is
// modelled as held and the mirror's release lands on it. When the branches are
// the release half, pick is min, so the state returns to unheld however the
// mirror took the lock.
func mergeBranchStats(dst, a, b map[string]*Stats, pick func(int, int) int) {
	for name, stat := range dst {
		x, y := a[name], b[name]
		switch {
		case x == nil && y == nil:
			continue
		case x == nil:
			copyStats(stat, y)
		case y == nil:
			copyStats(stat, x)
		default:
			copyStats(stat, foldStats(x, y, pick))
		}
	}
}

// maxCount and minCount wrap the builtins so they can be passed as values.
func maxCount(a, b int) int { return max(a, b) }

func minCount(a, b int) int { return min(a, b) }

func foldStats(a, b *Stats, pick func(int, int) int) *Stats {
	return &Stats{
		lock:               pick(a.lock, b.lock),
		rlock:              pick(a.rlock, b.rlock),
		borrowedLock:       pick(a.borrowedLock, b.borrowedLock),
		borrowedRLock:      pick(a.borrowedRLock, b.borrowedRLock),
		deferUnlock:        pick(a.deferUnlock, b.deferUnlock),
		deferRUnlock:       pick(a.deferRUnlock, b.deferRUnlock),
		lockPos:            unionPositions(a.lockPos, b.lockPos),
		rlockPos:           unionPositions(a.rlockPos, b.rlockPos),
		borrowedUnlockPos:  unionPositions(a.borrowedUnlockPos, b.borrowedUnlockPos),
		borrowedRUnlockPos: unionPositions(a.borrowedRUnlockPos, b.borrowedRUnlockPos),
	}
}

func unionPositions(a, b []token.Pos) []token.Pos {
	if len(b) == 0 {
		return slices.Clone(a)
	}
	merged := slices.Clone(a)
	for _, pos := range b {
		if !slices.Contains(merged, pos) {
			merged = append(merged, pos)
		}
	}
	slices.Sort(merged)
	return merged
}
