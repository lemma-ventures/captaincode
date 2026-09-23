package captaincode

import (
	"os"
	"path/filepath"
	"testing"
)

// The repository publishes its own skills under skills/ for other runtimes to
// load. Captain's parser is stricter than any of theirs - unknown fields
// refused, caps on size and files, no links out of the tree - so a skill it
// would not stage is not one to hand anyone else.
func TestPublishedSkillsPassCaptainsOwnRules(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("published skills: %v", err)
	}
	listing, n := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		s, err := ParseSkill(filepath.Join(root, e.Name()))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if s.Name != e.Name() {
			t.Errorf("%s: frontmatter name %q must match its directory", e.Name(), s.Name)
		}
		if s.HasScripts {
			t.Errorf("%s: ships scripts/, which captain quarantines", s.Name)
		}
		listing += len(s.Name) + len(s.Description)
	}
	if n == 0 {
		t.Fatal("skills/ holds no skills")
	}
	if listing > skillListingBudget {
		t.Errorf("published skills list at %d bytes of startup context, over the %d-byte shelf budget", listing, skillListingBudget)
	}
}
