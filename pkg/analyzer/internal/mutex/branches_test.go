package mutex

import "testing"

// TestSameBranchState documents the merge invariant: two branch states are
// equivalent when they leave the same NET outstanding lock counts, so a branch
// that defers its release (lock=1, deferUnlock=1) matches a sibling that releases
// directly (lock=0). Borrowed counts must still match exactly.
func TestSameBranchState(t *testing.T) {
	c := &Checker{}

	tests := []struct {
		name string
		a, b *Stats
		want bool
	}{
		{
			name: "deferred release equals direct release (same net write lock)",
			a:    &Stats{lock: 1, deferUnlock: 1},
			b:    &Stats{},
			want: true,
		},
		{
			name: "different net write-lock counts differ",
			a:    &Stats{lock: 1},
			b:    &Stats{},
			want: false,
		},
		{
			name: "deferred read release equals direct read release",
			a:    &Stats{rlock: 1, deferRUnlock: 1},
			b:    &Stats{},
			want: true,
		},
		{
			name: "borrowed write unlock must match exactly",
			a:    &Stats{borrowedLock: 1},
			b:    &Stats{},
			want: false,
		},
		{
			name: "borrowed read unlock must match exactly",
			a:    &Stats{borrowedRLock: 1},
			b:    &Stats{},
			want: false,
		},
		{
			name: "both nil are equal",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "nil and non-nil differ",
			a:    nil,
			b:    &Stats{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.sameBranchState(tc.a, tc.b); got != tc.want {
				t.Errorf("sameBranchState(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
