package main

import (
	"encoding/json"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
)

func userMsg(s string) oaiMessage { b, _ := json.Marshal(s); return oaiMessage{Role: "user", Content: b} }
func asstMsg(s string) oaiMessage { b, _ := json.Marshal(s); return oaiMessage{Role: "assistant", Content: b} }

// "continue where we left off" is routed on the work it continues, and is
// never rated trivial (it ran on step at low effort for 17 minutes,
// 2026-10-03).
func TestAFollowUpIsRoutedOnTheWorkItContinues(t *testing.T) {
	msgs := []oaiMessage{
		userMsg("refactor the P3 driver: fix the API mismatches against the reference and rerun the measured grid"),
		asstMsg("I started on the driver..."),
		userMsg("continue"),
		asstMsg("..."),
		userMsg("continue where we left off"),
	}
	task, follow := routeTaskFor("continue where we left off", msgs)
	assert.True(t, follow)
	assert.Contains(t, task, "refactor the P3 driver", "the request it continues, skipping the earlier follow-up")
	assert.Contains(t, task, "continue where we left off")

	same, follow := routeTaskFor("fix the flaky test in auth_test.go", msgs)
	assert.False(t, follow)
	assert.Equal(t, "fix the flaky test in auth_test.go", same, "an ordinary turn is routed on itself")

	alone, follow := routeTaskFor("continue", []oaiMessage{userMsg("continue")})
	assert.True(t, follow, "with nothing to continue it is still a follow-up")
	assert.Equal(t, "continue", alone)
}

func TestAFollowUpIsNeverTrivial(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "")
	b := teamBrain()
	noDirector(t, b)
	resp, fail := b.decideRoute(routeReq{Task: "continue", followUp: true, ws: captaincode.Workspace{Dir: t.TempDir()}, planOnly: true})
	assert.Nil(t, fail)
	assert.NotEqual(t, string(captaincode.ClassTrivial), resp.Class)
}
