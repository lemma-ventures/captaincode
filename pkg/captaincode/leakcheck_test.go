package captaincode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindLeaksMatchesWholeNamesInAnyCase(t *testing.T) {
	names := []string{"zephyrine", "quill-lab", "oak"}
	text := "fix the loop\nseen in Zephyrine last week\nsee ~/work/quill-lab/notes\nsearch the oaken shelf\n# oak"
	leaks := FindLeaks(text, names)
	require.Len(t, leaks, 3)
	assert.Equal(t, Leak{Name: "zephyrine", Line: 2, Text: "seen in Zephyrine last week"}, leaks[0], "case does not matter")
	assert.Equal(t, "quill-lab", leaks[1].Name, "a hyphenated name inside a path")
	assert.Equal(t, 5, leaks[2].Line, "oak as a word, not inside oaken")

	cased := FindLeaks("port it to the Beacon project\nbeacon the message", []string{"Beacon"})
	require.Len(t, cased, 1, "an entry with capitals matches only that spelling")
	assert.Equal(t, 1, cased[0].Line)

	names = []string{"quill", "!quill-works", "!Quill Works", "!quill.works", "jo@quill.works"}
	leaks = FindLeaks("import \"github.com/quill-works/app\"\nCopyright Quill Works AG\nthe quill chain\nmail jo@quill.works", names)
	require.Len(t, leaks, 2, "an allowed phrase hides only the names it contains")
	assert.Equal(t, "quill", leaks[0].Name)
	assert.Equal(t, 3, leaks[0].Line)
	assert.Equal(t, "jo@quill.works", leaks[1].Name, "a name longer than the allowed phrase is still caught")
}

func TestPrivateNamesReadsTheListOutsideTheRepository(t *testing.T) {
	p := filepath.Join(t.TempDir(), "names")
	require.NoError(t, os.WriteFile(p, []byte("# private\nzephyrine\n\n  quill-lab  \n"), 0o600))
	t.Setenv("CAPTAIN_PRIVATE_NAMES", p)
	assert.Equal(t, []string{"zephyrine", "quill-lab"}, PrivateNames())
	t.Setenv("CAPTAIN_PRIVATE_NAMES", filepath.Join(t.TempDir(), "missing"))
	assert.Empty(t, PrivateNames(), "no list is an empty list")
}
