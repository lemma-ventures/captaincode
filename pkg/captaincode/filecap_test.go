package captaincode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveCapsChangedFileLists(t *testing.T) {
	many := make([]string, 5000)
	for i := range many {
		many[i] = fmt.Sprintf(".worktrees/run/%05d.json", i)
	}
	l := NewLedger(filepath.Join(t.TempDir(), "state.json"))
	l.Outcomes = []OutcomeEvidence{{TaskID: "t1", ChangedFiles: many}}
	l.AttemptStates = []AttemptState{{AttemptID: "a1", ChangedFiles: many}}
	l.Handoffs = []HandoffBrief{{TaskID: "t1", Artifacts: []ArtifactSummary{{ChangedFiles: many}}, Integration: &IntegrationSummary{ChangedFiles: many}}}
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(l.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 100_000 {
		t.Errorf("state.json is %d bytes with four capped lists", fi.Size())
	}
	for name, n := range map[string]int{
		"outcome":     len(l.Outcomes[0].ChangedFiles),
		"attempt":     len(l.AttemptStates[0].ChangedFiles),
		"artifact":    len(l.Handoffs[0].Artifacts[0].ChangedFiles),
		"integration": len(l.Handoffs[0].Integration.ChangedFiles),
	} {
		if n != maxRecordedFiles {
			t.Errorf("%s keeps %d files, want %d", name, n, maxRecordedFiles)
		}
	}
}
