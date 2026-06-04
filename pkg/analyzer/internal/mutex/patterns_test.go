package mutex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOppositeMutexMethods(t *testing.T) {
	tests := []struct {
		method string
		want   []string
	}{
		{"Lock", []string{"Unlock"}},
		{"TryLock", []string{"Unlock"}},
		{"Unlock", []string{"Lock", "TryLock"}},
		{"RLock", []string{"RUnlock"}},
		{"TryRLock", []string{"RUnlock"}},
		{"RUnlock", []string{"RLock", "TryRLock"}},
		{"Foo", nil},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := oppositeMutexMethods(tt.method)
			assert.Equal(t, got, tt.want)
		})
	}
}

func TestMutexMethodGroup(t *testing.T) {
	tests := []struct {
		method string
		want   []string
	}{
		{"Lock", []string{"Lock", "TryLock"}},
		{"TryLock", []string{"Lock", "TryLock"}},
		{"Unlock", []string{"Unlock"}},
		{"RLock", []string{"RLock", "TryRLock"}},
		{"TryRLock", []string{"RLock", "TryRLock"}},
		{"RUnlock", []string{"RUnlock"}},
		{"Foo", nil},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := mutexMethodGroup(tt.method)
			assert.Equal(t, got, tt.want)
		})
	}
}
