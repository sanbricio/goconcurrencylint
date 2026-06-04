package mutex

import (
	"go/token"
	"testing"
)

func TestStatsMap_CloneStatsMapIsDeepCopy(t *testing.T) {
	orig := map[string]*Stats{
		"mu": {lock: 1, lockPos: []token.Pos{42}},
	}

	c := cloneStatsMap(orig)

	if c["mu"].lock != 1 {
		t.Fatalf("expected c[\"mu\"].lock == 1, got %d", c["mu"].lock)
	}
	if len(c["mu"].lockPos) == 0 || c["mu"].lockPos[0] != 42 {
		t.Fatalf("expected c[\"mu\"].lockPos[0] == 42, got %v", c["mu"].lockPos)
	}

	// Mutate the clone and verify the original is untouched.
	c["mu"].lock = 99
	c["mu"].lockPos[0] = 7

	if orig["mu"].lock != 1 {
		t.Errorf("orig[\"mu\"].lock was mutated: expected 1, got %d", orig["mu"].lock)
	}
	if orig["mu"].lockPos[0] != 42 {
		t.Errorf("orig[\"mu\"].lockPos[0] was mutated: expected 42, got %d", orig["mu"].lockPos[0])
	}
}

func TestStatsMap_CopyStatsMapMergeSemantics(t *testing.T) {
	dst := map[string]*Stats{"a": {lock: 5}}
	src := map[string]*Stats{"b": {lock: 2}}

	copyStatsMap(dst, src)

	if dst["a"].lock != 5 {
		t.Errorf("dst[\"a\"].lock should be untouched: expected 5, got %d", dst["a"].lock)
	}
	if dst["b"].lock != 2 {
		t.Errorf("dst[\"b\"].lock should be copied from src: expected 2, got %d", dst["b"].lock)
	}
}

func TestStatsMap_ClearStats(t *testing.T) {
	m := map[string]*Stats{"mu": {lock: 3}}

	clearStats(m)

	if _, ok := m["mu"]; !ok {
		t.Fatal("expected key \"mu\" to still be present after clearStats")
	}
	if m["mu"].lock != 0 {
		t.Errorf("expected m[\"mu\"].lock == 0 after clearStats, got %d", m["mu"].lock)
	}
}

func TestStatsMap_EmptyStatsLikeDoesNotMutateOriginal(t *testing.T) {
	orig := map[string]*Stats{"mu": {lock: 3}}

	e := emptyStatsLike(orig)

	if _, ok := e["mu"]; !ok {
		t.Fatal("expected key \"mu\" in result of emptyStatsLike")
	}
	if e["mu"].lock != 0 {
		t.Errorf("expected e[\"mu\"].lock == 0, got %d", e["mu"].lock)
	}
	if orig["mu"].lock != 3 {
		t.Errorf("orig[\"mu\"].lock was mutated: expected 3, got %d", orig["mu"].lock)
	}
}

func TestStatsMap_InitialStats(t *testing.T) {
	s := initialStats(map[string]bool{"mu": true}, map[string]bool{"rw": true})

	if len(s) != 2 {
		t.Fatalf("expected 2 keys in initialStats result, got %d", len(s))
	}
	if _, ok := s["mu"]; !ok {
		t.Error("expected key \"mu\" in initialStats result")
	}
	if _, ok := s["rw"]; !ok {
		t.Error("expected key \"rw\" in initialStats result")
	}
	if s["mu"].lock != 0 {
		t.Errorf("expected s[\"mu\"].lock == 0, got %d", s["mu"].lock)
	}
	if s["rw"].lock != 0 {
		t.Errorf("expected s[\"rw\"].lock == 0, got %d", s["rw"].lock)
	}
}
