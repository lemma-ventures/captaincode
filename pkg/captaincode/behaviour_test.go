package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBehaviourReportNamesWhatNeedsAChange(t *testing.T) {
	l := NewLedger(filepath.Join(t.TempDir(), "state.json"))
	for i := 0; i < 5; i++ {
		l.Record(Event{Leg: LegLuna, Class: ClassTrivial, Outcome: "ok", Duration: 2700})
		l.Record(Event{Leg: LegStep, Class: ClassTrivial, Outcome: "ok", Duration: 16000})
	}
	in := func(leg Leg) bool { return leg != LegLuna }
	b := BuildBehaviour(l, in, time.Now())
	assert.Equal(t, 10, b.Events)
	require.NotEmpty(t, b.Notes)
	assert.Contains(t, strings.Join(b.Notes, "\n"), "luna is the fastest leg measured on trivial work (2.7 s) and CAPTAIN_LEGS leaves it out")
	var luna LegBehaviour
	for _, lb := range b.Legs {
		if lb.Leg == LegLuna {
			luna = lb
		}
	}
	assert.False(t, luna.InLegs)
	assert.Equal(t, SpeedStat{N: 5, MedianMs: 2700}, luna.Speed[ClassTrivial])
	assert.Contains(t, luna.Tiers["frontier"], "@max")
	md := b.Markdown()
	assert.Contains(t, md, "| luna | no |")
	assert.Contains(t, md, "## Tiers")
}

func TestBehaviourIsDueOnNewLegsHostsAndEveryFiftyRuns(t *testing.T) {
	prev := Behaviour{Events: 100, Legs: []LegBehaviour{{Leg: LegStep, Host: "huggingface"}}}
	cur := Behaviour{Events: 120, Legs: []LegBehaviour{{Leg: LegStep, Host: "huggingface"}}}
	due, _ := BehaviourDue(prev, true, cur)
	assert.False(t, due)
	cur.Events = 150
	due, why := BehaviourDue(prev, true, cur)
	assert.True(t, due)
	assert.Contains(t, why, "50 runs")
	cur = Behaviour{Events: 101, Legs: []LegBehaviour{{Leg: LegStep, Host: "openrouter"}}}
	due, why = BehaviourDue(prev, true, cur)
	assert.True(t, due, "a leg on a new host")
	assert.Contains(t, why, "step on openrouter")
	due, _ = BehaviourDue(Behaviour{}, false, cur)
	assert.True(t, due, "the first report")
}

func TestWriteBehaviourKeepsJSONAndMarkdown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "behaviour.json")
	require.NoError(t, WriteBehaviour(p, Behaviour{At: time.Now(), Events: 3, Reason: "first report"}))
	got, ok := LoadBehaviour(p)
	require.True(t, ok)
	assert.Equal(t, 3, got.Events)
	md, err := os.ReadFile(filepath.Join(dir, "reports", "behaviour-latest.md"))
	require.NoError(t, err)
	assert.Contains(t, string(md), "# Model behaviour")
}
