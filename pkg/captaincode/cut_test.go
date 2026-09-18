package captaincode

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestCutsNeverSplitARune(t *testing.T) {
	s := "route → glm · done — ✓"
	for n := 0; n <= len(s)+1; n++ {
		h, tl := CutHead(s, n), CutTail(s, n)
		assert.True(t, utf8.ValidString(h), "head %d: %q", n, h)
		assert.True(t, utf8.ValidString(tl), "tail %d: %q", n, tl)
		assert.LessOrEqual(t, len(h), n)
		assert.LessOrEqual(t, len(tl), n)
	}
	assert.Equal(t, "route ", CutHead(s, 7), "the arrow's first byte would be at 6..8: cut before it")
	assert.Equal(t, " ✓", CutTail(s, 4), "the check mark is three bytes: keep it whole with its space")
	assert.Equal(t, s, CutHead(s, 1000))
	assert.Equal(t, "", CutHead(s, 0))
}
