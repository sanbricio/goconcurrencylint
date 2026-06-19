package category

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var codePattern = regexp.MustCompile(`^GCL[0-9]{4}$`)

// TestRegistryIntegrity pins the invariants the rest of the linter relies on:
// the catalogue is the single source of truth, so codes and slugs must be
// unique, well-formed and fully populated.
func TestRegistryIntegrity(t *testing.T) {
	checks := Checks()
	require.NotEmpty(t, checks)

	seenCode := map[Category]bool{}
	seenSlug := map[string]bool{}
	for _, c := range checks {
		assert.Regexp(t, codePattern, string(c.Code), "code must look like GCLxxxx")
		assert.NotEmpty(t, c.Slug, "%s: slug must not be empty", c.Code)
		assert.NotEmpty(t, c.Primitive, "%s: primitive must not be empty", c.Code)
		assert.NotEmpty(t, c.Summary, "%s: summary must not be empty", c.Code)
		assert.NotEmpty(t, c.Why, "%s: why must not be empty", c.Code)
		assert.NotEmpty(t, strings.TrimSpace(c.Bad), "%s: bad example must not be empty", c.Code)
		assert.NotEmpty(t, strings.TrimSpace(c.Good), "%s: good example must not be empty", c.Code)
		assert.NotEqual(t, strings.TrimSpace(c.Bad), strings.TrimSpace(c.Good),
			"%s: bad and good examples must differ", c.Code)

		assert.False(t, seenCode[c.Code], "duplicate code %s", c.Code)
		assert.False(t, seenSlug[c.Slug], "duplicate slug %s", c.Slug)
		seenCode[c.Code] = true
		seenSlug[c.Slug] = true
	}

	assert.Len(t, All(), len(checks), "All() must cover the whole registry")
}

// TestCanonicalAndLookup verifies that both identifier forms resolve to the
// same canonical code and that unknown ids are rejected.
func TestCanonicalAndLookup(t *testing.T) {
	for _, c := range Checks() {
		fromCode, ok := Canonical(string(c.Code))
		require.True(t, ok, "code %s must canonicalise", c.Code)
		assert.Equal(t, c.Code, fromCode)

		fromSlug, ok := Canonical(c.Slug)
		require.True(t, ok, "slug %s must canonicalise", c.Slug)
		assert.Equal(t, c.Code, fromSlug, "slug %s must map to its code", c.Slug)

		assert.True(t, IsKnown(string(c.Code)))
		assert.True(t, IsKnown(c.Slug))

		got, ok := Lookup(c.Code)
		require.True(t, ok)
		assert.Equal(t, c, got)
	}

	_, ok := Canonical("GCL9999")
	assert.False(t, ok, "unknown code must not canonicalise")
	_, ok = Canonical("not-a-real-slug")
	assert.False(t, ok, "unknown slug must not canonicalise")
	assert.False(t, IsKnown(""), "empty id is unknown")
}
