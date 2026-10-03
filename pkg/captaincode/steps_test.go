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

func TestStepsAreRecordedOnTheTurn(t *testing.T) {
	var none *Steer
	none.RecordStep(Step{Tool: "bash"}) // a run outside a turn: no panic
	assert.Nil(t, none.StepsFrom(0))

	s := NewSteer("/repo")
	s.RecordStep(Step{Tool: "webfetch", Input: "https://consensus.app", Output: strings.Repeat("x", 1000)})
	s.RecordStep(Step{Tool: "bash", Input: "go test ./...", Failed: true})
	all := s.StepsFrom(0)
	require.Len(t, all, 2)
	assert.False(t, all[0].At.IsZero())
	assert.LessOrEqual(t, len(all[0].Output), stepDetailMax+len("…"))
	assert.Len(t, s.StepsFrom(1), 1)
	assert.Empty(t, s.StepsFrom(2))
}

// claude's tool_use carries what it ran, its tool_result what came back.
func TestClaudeStreamRecordsItsSteps(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null &\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"curl -sS https://consensus.app/pricing","description":"Fetch pricing"}}]}}'` + "\n" +
		`echo '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"Pricing: Individual, Team and Enterprise"}]}}'` + "\n" +
		`echo '{"type":"result","result":"done","usage":{"input_tokens":1,"output_tokens":1}}'` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	steer := NewSteer("/repo")
	_, err := runClaudeStreamOpts(t.TempDir(), "price?", time.Minute, 0, nil, func(string) {}, false, "", steer)
	require.NoError(t, err)
	steps := steer.StepsFrom(0)
	require.Len(t, steps, 1)
	assert.Equal(t, "Bash", steps[0].Tool)
	assert.Equal(t, "curl -sS https://consensus.app/pricing", steps[0].Input)
	assert.Contains(t, steps[0].Output, "Individual, Team and Enterprise")
}
