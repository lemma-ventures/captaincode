package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The 2026-10-07 run, replayed: asked to "build it", the worker pushed a
// commit that failed CI and published a release one minute before it
// delivered. A later run that asked for a release is left alone.
func TestAuditPenalizesWhatWeFoundByHand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	end := time.Now().Add(-time.Hour)
	b.ledger.RecordOutcome(captaincode.OutcomeEvidence{TaskID: "t-build", Task: "build it", Leg: "ds4-flash", Dir: "/w/cc",
		Status: captaincode.AcceptancePending, DeliveredAt: end, UpdatedAt: end})
	b.ledger.RecordOutcome(captaincode.OutcomeEvidence{TaskID: "t-rel", Task: "cut a release v2", Leg: "claude", Dir: "/w/other",
		Status: captaincode.AcceptanceAccepted, DeliveredAt: end, UpdatedAt: end})
	b.ledger.RecordOutcome(captaincode.OutcomeEvidence{TaskID: "t-ok", Task: "fix the docs", Leg: "glm", Dir: "/w/third",
		Status: captaincode.AcceptanceAccepted, DeliveredAt: end, UpdatedAt: end})
	b.auditRunFn = func(dir string, args ...string) (string, error) {
		cmd := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(cmd, "gh repo view"):
			return "o/" + dir[len("/w/"):], nil
		case strings.HasPrefix(cmd, "gh release list") && dir != "/w/third":
			return `[{"tagName":"v9","createdAt":"` + end.Add(-time.Minute).Format(time.RFC3339) + `"}]`, nil
		case strings.HasPrefix(cmd, "git log") && dir == "/w/cc":
			return "9601359aaaa\n", nil
		case strings.HasPrefix(cmd, "git log") && dir == "/w/third":
			return "abc1234ffff\n", nil
		case strings.HasPrefix(cmd, "gh run list") && strings.Contains(cmd, "9601359aaaa"):
			return `[{"status":"completed","conclusion":"failure"}]`, nil
		case strings.HasPrefix(cmd, "gh run list"):
			return `[{"status":"completed","conclusion":"success"}]`, nil
		}
		return "", nil
	}
	captaincode.AppendConduct(captaincode.ConductEvent{At: end.Add(-5 * time.Minute), Rule: "refused: create or push a tag - the user did not ask for a release",
		Command: "git tag v9", Dir: "/w/cc"})

	found := b.audit(end.Add(-3 * time.Hour))
	rules := map[string]string{}
	for _, f := range found {
		rules[f.Rule] = f.TaskID
	}
	assert.Equal(t, "t-build", rules["published a release without a request"])
	assert.Equal(t, "t-build", rules["pushed a commit that failed CI"])
	assert.Equal(t, "t-build", rules["tried to publish without a request"])
	assert.Len(t, found, 3, "the requested release and the green commit are not findings")
	assert.Equal(t, captaincode.AcceptanceRejected, b.ledger.OutcomeFor("t-build").Status)
	assert.Equal(t, captaincode.AcceptanceAccepted, b.ledger.OutcomeFor("t-ok").Status)
	assert.True(t, feedSays(b, "route", "audit penalized ds4-flash"))

	assert.Empty(t, b.audit(end.Add(-3*time.Hour)), "each finding is penalized once")
}

// A run the user had accepted is marked regressed, not overruled.
func TestAuditRegressesAnAcceptedRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	end := time.Now().Add(-time.Hour)
	b.ledger.RecordOutcome(captaincode.OutcomeEvidence{TaskID: "t-acc", Task: "add a leg", Leg: "ds4-flash", Dir: "/w/cc",
		Status: captaincode.AcceptanceAccepted, DeliveredAt: end, UpdatedAt: end})
	b.auditRunFn = func(string, ...string) (string, error) { return "", nil }
	captaincode.AppendConduct(captaincode.ConductEvent{At: end.Add(-time.Minute), Rule: "refused: this checkout had uncommitted changes", Command: "git add -A", Dir: "/w/cc/pkg"})
	found := b.audit(end.Add(-time.Hour))
	require.Len(t, found, 1)
	assert.Equal(t, "tried to bulk-stage other sessions' work", found[0].Rule)
	assert.Equal(t, captaincode.AcceptanceRegressed, b.ledger.OutcomeFor("t-acc").Status)
}
