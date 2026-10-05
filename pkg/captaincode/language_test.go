package captaincode

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conformance suite: every case in testdata/language/conformance.txt
// parses to its recorded tree.
func TestLanguageConformance(t *testing.T) {
	LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	f, err := os.Open("testdata/language/conformance.txt")
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	input, cases := "", 0
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "":
			continue
		case strings.HasPrefix(line, "= "):
			require.NotEmpty(t, input, "a tree with no input line before it")
			want := strings.TrimPrefix(line, "= ")
			got := ParseTurn(input)
			if strings.HasPrefix(want, "(error ") {
				prefix := strings.TrimSuffix(strings.TrimPrefix(want, `(error "`), `")`)
				assert.Equal(t, "error", got.Kind, input)
				assert.True(t, strings.HasPrefix(got.Text, prefix), "%s\n  want an error starting %q\n  got %s", input, prefix, got)
			} else {
				assert.Equal(t, want, got.String(), input)
			}
			input = ""
			cases++
		default:
			input = line
		}
	}
	require.NoError(t, sc.Err())
	assert.GreaterOrEqual(t, cases, 40, "the suite lost cases")
}

// A word the parser reserves and the spec does not document is a language
// change nobody wrote down.
func TestLanguageEveryReservedWordIsDocumented(t *testing.T) {
	spec, err := os.ReadFile("../../docs/LANGUAGE.md")
	require.NoError(t, err)
	for _, w := range append([]string{"auto"}, slashControlWords...) {
		assert.Contains(t, string(spec), "`/"+w+"`", "docs/LANGUAGE.md does not document /%s", w)
	}
	for w := range repeatControlWords {
		assert.Contains(t, string(spec), "`"+w+"`", "docs/LANGUAGE.md does not document /repeat %s", w)
	}
}

func TestLanguageVersionMatchesTheSpec(t *testing.T) {
	spec, err := os.ReadFile("../../docs/LANGUAGE.md")
	require.NoError(t, err)
	assert.Contains(t, string(spec), "Version **"+LanguageVersion+"**", "bump LanguageVersion and the spec together")
}

func TestRepeatHeadIsAWholeWord(t *testing.T) {
	_, _, ok := SplitRepeat("/repeated runs are slow")
	assert.False(t, ok)
	n, rest, ok := SplitRepeat("/repeat 3 /team audit it")
	require.True(t, ok)
	assert.Equal(t, 3, n)
	assert.Equal(t, "/team audit it", rest)
}

// docs/LANGUAGE.md §9: a new leg may not take a reserved word; a compiled
// leg that shares one may still be re-pointed.
func TestLanguageReservedWordsAreNotLegNames(t *testing.T) {
	for _, id := range []string{"repeat", "btw", "quality", "auto", "frontier", "wf"} {
		err := validateSpec(LegSpec{ID: Leg(id), Transport: TransportOpencode, Provider: "openrouter", Model: "x/y"})
		assert.Error(t, err, id)
	}
	assert.NoError(t, validateSpec(LegSpec{ID: LegOpenShell, Transport: TransportOpencode, Provider: "openrouter", Model: "x/y"}))
	assert.NoError(t, validateSpec(LegSpec{ID: "muse", Transport: TransportOpencode, Provider: "openrouter", Model: "x/y"}))
}
