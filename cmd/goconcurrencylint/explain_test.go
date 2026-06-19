package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sanbricio/goconcurrencylint/pkg/analyzer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExplain_ByCode(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, explain([]string{"GCL1001"}, &out))

	got := out.String()
	assert.Contains(t, got, "GCL1001")
	assert.Contains(t, got, "lock-without-unlock")
	assert.Contains(t, got, "Why it matters")
	assert.Contains(t, got, "goconcurrencylint:ignore GCL1001")
	assert.Contains(t, got, "docs/checks/GCL1001.md")
}

// The legacy slug must resolve to the same check as its code.
func TestExplain_BySlugMatchesCode(t *testing.T) {
	var byCode, bySlug bytes.Buffer
	require.NoError(t, explain([]string{"GCL2001"}, &byCode))
	require.NoError(t, explain([]string{"add-without-done"}, &bySlug))
	assert.Equal(t, byCode.String(), bySlug.String())
}

func TestExplain_Unknown(t *testing.T) {
	var out bytes.Buffer
	err := explain([]string{"GCL9999"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown check")
}

func TestExplain_ListsEveryCheck(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, explain(nil, &out))

	got := out.String()
	for _, c := range analyzer.Checks() {
		assert.Contains(t, got, c.Code, "listing must mention %s", c.Code)
	}
	assert.Equal(t, len(analyzer.Checks()), strings.Count(got, "\n"),
		"one line per check")
}
