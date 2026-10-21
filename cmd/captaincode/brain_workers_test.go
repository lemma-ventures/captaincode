package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// /v1/workers is what the sidebar polls to show which legs are working, idle or
// cooling. The brain never served it (404), so the panel sat empty - and on
// stock opencode that panel is the ONLY live feedback there is: the progress
// the brain streams on the reasoning channel is dropped by the ai-sdk
// openai-compatible path, so a long run otherwise looks like nothing happened.

type workersResp struct {
	Workers []struct {
		Leg          string `json:"leg"`
		Status       string `json:"status"`
		Task         string `json:"task"`
		Runs         int    `json:"runs"`
		ElapsedMs    int64  `json:"elapsed_ms"`
		CoolingUntil int64  `json:"coolingUntil"`
	} `json:"workers"`
}

func getWorkers(t *testing.T, b *brain) workersResp {
	t.Helper()
	rec := httptest.NewRecorder()
	b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers", nil))
	require.Equal(t, 200, rec.Code)
	var out workersResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestWorkersReportsEveryLegAndItsState(t *testing.T) {
	b := teamBrain()
	b.ledger.Cooldowns[captaincode.LegGrok] = time.Now().Add(20 * time.Minute)

	got := getWorkers(t, b)
	require.NotEmpty(t, got.Workers, "the panel needs one row per leg, not just the busy ones")
	byLeg := map[string]string{}
	for _, w := range got.Workers {
		byLeg[w.Leg] = w.Status
	}
	assert.Equal(t, "cooling", byLeg["grok"], "a cooled leg is not idle: that is why it is being skipped")
	assert.Equal(t, "idle", byLeg["claude"])
}

func TestWorkersShowsWhatABusyLegIsDoing(t *testing.T) {
	b := teamBrain()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	b.runWorkerFn = func(leg captaincode.Leg, brief string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		once.Do(func() { close(started) })
		<-release
		return leg, captaincode.Result{Text: "done enough to count as a deliverable"}, nil
	}
	go func() {
		b.runWorkerRerouted(defaultWorkspace(), captaincode.LegGLM, "[user]\nrewrite the storage layer", nil, nil, "")
	}()
	<-started

	got := getWorkers(t, b)
	var found bool
	for _, w := range got.Workers {
		if w.Leg == "glm" {
			found = true
			assert.Equal(t, "busy", w.Status)
			assert.Contains(t, w.Task, "rewrite the storage layer", "the panel says WHAT it is working on")
			assert.GreaterOrEqual(t, w.ElapsedMs, int64(0), "…and for how long, so a slow run is visibly alive")
		}
	}
	assert.True(t, found, "the busy leg is in the list")
	close(release)
}

// Two TUIs can run the same leg at once. Keyed by leg, the second run was
// invisible, and the first run to end removed both: a claude worker that ran
// for an hour in one folder disappeared from every panel when a short claude
// run elsewhere finished, and repoBusy saw its checkout as idle.
func TestConcurrentRunsOfOneLegStayVisibleUntilEachEnds(t *testing.T) {
	b := teamBrain()
	long, short := gitRepo(t), gitRepo(t)
	endLong := b.active.begin(captaincode.Workspace{Dir: long, Origin: long}, captaincode.LegClaude, "[user]\nport the engine")
	endShort := b.active.begin(captaincode.Workspace{Dir: short, Origin: short}, captaincode.LegClaude, "[user]\nfix the readme")
	t.Cleanup(endLong)

	runs := func(query string) []runRow {
		t.Helper()
		rec := httptest.NewRecorder()
		b.workers(rec, httptest.NewRequest(http.MethodGet, "/v1/workers"+query, nil))
		require.Equal(t, 200, rec.Code)
		var out struct {
			Busy int      `json:"busy"`
			Runs []runRow `json:"runs"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, len(out.Runs), out.Busy, "busy counts runs, not legs")
		return out.Runs
	}

	all := runs("")
	require.Len(t, all, 2, "a machine-wide view lists every run")
	assert.Equal(t, long, all[0].Dir, "oldest first")
	assert.Equal(t, short, all[1].Dir)
	assert.NotZero(t, all[0].Started)
	mine := runs("?cwd=" + url.QueryEscape(short))
	require.Len(t, mine, 1, "a sidebar sees its own folder's run only")
	assert.Contains(t, mine[0].Task, "fix the readme")

	endShort()
	endShort() // ending twice is harmless
	left := runs("")
	require.Len(t, left, 1, "ending one run leaves the other")
	assert.Equal(t, long, left[0].Dir)
	busy, why := b.repoBusy(long)
	assert.True(t, busy, "the long run's checkout is still busy")
	assert.Contains(t, why, "claude is running there")
	idle, _ := b.repoBusy(short)
	assert.False(t, idle)
}
