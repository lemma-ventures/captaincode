package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Captain's TUI prefixes (/quality, /claude, …) are ROUTING directives: the
// fork parses them for the route call but forwards the message verbatim, so
// the worker received "/quality rewrite this" and answered about the prefix
// instead of the task - live 2026-07-29: «/quality isn't among the skills
// available in this session, so I treated your message as the request». The
// wrapper strips them from every user turn before dispatching.
func TestCaptainDirectivesAreStrippedFromWorkerPrompts(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"preference prefix", "/quality rewrite the intro", "rewrite the intro"},
		{"colon form", "/quality: rewrite the intro", "rewrite the intro"},
		{"short form", "/q rewrite the intro", "rewrite the intro"},
		{"forced leg", "/claude explain this", "explain this"},
		{"preference then forced", "/quality /team explain this", "explain this"},
		{"case insensitive", "/Best explain this", "explain this"},
		{"leading whitespace", "  /speed explain this", "explain this"},
		{"directive alone stays", "/quality", "/quality"},
		{"not a directive", "/deploy the app", "/deploy the app"},
		{"mid-text is untouched", "run /quality on it", "run /quality on it"},
		{"path-like is untouched", "/usr/bin/claude is missing", "/usr/bin/claude is missing"},
		// The plugin no longer rewrites the stored message, so the brain must be
		// the one that removes the directive the user typed (2026-09-11).
		{in: "/grok slide 8 is not consistent with slide 10: bear 2.0x · base 14.6x", want: "slide 8 is not consistent with slide 10: bear 2.0x · base 14.6x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, stripCaptainDirectives(c.in))
		})
	}
}

// Team workers used to receive the director's brief and NOTHING else, while
// the solo path replays the whole conversation. So "give me a simple sentence
// in my writing style" was decomposed into a generic brief and answered by two
// workers who had never seen a line the user wrote (live 2026-07-29). A worker
// must get the conversation AND its assignment.
func TestTeamWorkersReceiveTheConversationAndTheirBrief(t *testing.T) {
	b := teamBrain()
	var mu sync.Mutex
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return leg, captaincode.Result{Text: "out", DurationMs: 10}, nil
	}
	b.assessMultiFn = func(task string, outputs map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "SYNTH"}, nil
	}
	b.storeTeamPlan("Give me a simple sentence in my writing style", captaincode.Plan{
		Class: captaincode.ClassTrivial, Rationale: "cheap pair",
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegFree, Brief: "write one sentence"},
			{Leg: captaincode.LegGrok, Brief: "write one sentence"},
		}})

	body, _ := json.Marshal(map[string]any{"model": "team", "stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": "I write short blunt sentences with no filler."},
			{"role": "assistant", "content": "Understood."},
			{"role": "user", "content": "Give me a simple sentence in my writing style"},
		}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, prompts, 2)
	for _, p := range prompts {
		assert.Contains(t, p, "I write short blunt sentences", "the worker needs the conversation, not just a brief")
		assert.Contains(t, p, "Give me a simple sentence in my writing style", "including the request being answered")
		assert.Contains(t, p, "write one sentence", "and its own assignment")
		assert.Contains(t, p, deliverableContract, "team workers owe a deliverable too")
	}
}

func TestPromptFromStripsDirectivesFromUserTurnsOnly(t *testing.T) {
	msgs := []oaiMessage{
		{Role: "system", Content: json.RawMessage(`"/quality is a captain prefix"`)},
		{Role: "user", Content: json.RawMessage(`"/quality rewrite the intro"`)},
		{Role: "assistant", Content: json.RawMessage(`"done"`)},
		{Role: "user", Content: json.RawMessage(`"/frontier now expand it"`)},
	}
	got := promptFrom(msgs)
	assert.Contains(t, got, "rewrite the intro")
	assert.Contains(t, got, "now expand it")
	assert.NotContains(t, got, "/quality rewrite")
	assert.NotContains(t, got, "/frontier now")
	assert.Contains(t, got, "/quality is a captain prefix", "system framing is left alone")
}

// User feedback 2026-08-24: answers came back as one wall of prose. The
// contract (appended to EVERY worker prompt - solo, team, workflow) must
// demand scannable markdown, and the clause must survive future rewording.
func TestDeliverableContractDemandsFormatting(t *testing.T) {
	assert.Contains(t, deliverableContract, "markdown")
	assert.Contains(t, deliverableContract, "never one large paragraph")
}

// Captain's prompts carried a task and nothing else. A worker meeting an
// instruction with no setting invites misreading ordinary engineering as
// something else (2026-09-09). The context must be accurate and present on
// every worker path.
func TestWorkerPromptCarriesAccurateContext(t *testing.T) {
	t.Setenv("CAPTAIN_CWD", "/src/arc")
	c := workerContext(defaultWorkspace())
	assert.Contains(t, c, "/src/arc", "names where the work happens")
	assert.Contains(t, c, "user's own")
	assert.Contains(t, c, "ordinary development work")
	assert.Contains(t, c, "reviewing")
	// It describes; it does not plead, promise or characterise the task's risk.
	for _, banned := range []string{"safe", "harmless", "not dangerous", "please", "legitimate", "do not refuse"} {
		assert.NotContains(t, strings.ToLower(c), banned, "context must describe, not persuade (%q)", banned)
	}
}
