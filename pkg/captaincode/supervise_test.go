package captaincode

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Supervisor shadow points: the decision leg asked about a worker that is
// still running. The point of these is what they do NOT do - nothing acts on
// an answer, and an answer nothing can check stays uncompared.

func TestSuperviseAsksTheFourNulsAndRecordsThem(t *testing.T) {
	c := nulServer(t, map[string]float64{
		PointWorkerStuck: 0.91, PointWorkOffTrack: 0.12, PointNeedsHuman: 0.08, PointAgentsDrift: 0.3,
	})
	snap := SuperviseSnapshot{Leg: LegClaude, Task: "fix the failing test", TaskID: "t7",
		Elapsed: 11 * time.Minute, Quiet: 4 * time.Second, Guidance: "run go test ./... before finishing",
		Recent: []string{"read pool.go", "read pool.go", "read pool.go"}}
	sh, _, err := SuperviseWithJev(context.Background(), c, snap)
	require.NoError(t, err)
	assert.Equal(t, "true", sh.Answers[PointWorkerStuck].Choice)
	assert.Equal(t, "false", sh.Answers[PointWorkOffTrack].Choice)

	r, ok := SuperviseRecord(snap, sh)
	require.True(t, ok)
	assert.Equal(t, PointSupervise, r.Point)
	assert.Equal(t, SupervisePoints, r.Points)
	assert.Equal(t, "t7", r.TaskID)
}

// The drift question needs the repository's guidance to judge against. With
// no AGENTS.md it is not asked, rather than asked against nothing.
func TestSuperviseSkipsTheDriftQuestionWithoutGuidance(t *testing.T) {
	assert.NotContains(t, JevSuperviseQuestions(false), PointAgentsDrift)
	assert.Contains(t, JevSuperviseQuestions(true), PointAgentsDrift)
}

// A long single command is work, not a stall - the state has to carry the
// facts that distinguish them.
func TestSuperviseStateCarriesElapsedQuietAndTheActionTail(t *testing.T) {
	st := superviseState(SuperviseSnapshot{Leg: LegClaude, Task: "build the release binary",
		Elapsed: 8*time.Minute + 10*time.Second, Quiet: 7*time.Minute + 40*time.Second,
		LastTool: "bash", LastDetail: "cargo build --release"})
	assert.Contains(t, st, "running for: 8m10s")
	assert.Contains(t, st, "quiet for: 7m40s")
	assert.Contains(t, st, "cargo build --release")
}

// The whole argument for the shadow: a prediction is stamped against what
// captain later OBSERVED, and a prediction nothing observes stays uncompared
// rather than being scored against an invented label.
func TestStampSuperviseStampsOnlyWhatCaptainObserves(t *testing.T) {
	r := ShadowRecord{Point: PointSupervise, Points: SupervisePoints, Shadow: Shadow{Answers: map[string]ShadowAnswer{
		PointWorkerStuck:  {Choice: "true", Confidence: 0.9},
		PointWorkOffTrack: {Choice: "true", Confidence: 0.8},
		PointNeedsHuman:   {Choice: "false", Confidence: 0.7},
		PointAgentsDrift:  {Choice: "true", Confidence: 0.6},
	}}}
	StampSupervise(&r, SuperviseOutcome{Stalled: true, Interrupted: false})

	assert.True(t, r.Answers[PointWorkerStuck].Agree, "it said stuck and the watchdog fired")
	assert.Equal(t, "watchdog", r.Answers[PointWorkerStuck].By)
	assert.True(t, r.Answers[PointNeedsHuman].Agree, "it said no and nobody stepped in")
	assert.Empty(t, r.Answers[PointWorkOffTrack].Actual, "settled by the task's acceptance evidence, not here")
	assert.Empty(t, r.Answers[PointAgentsDrift].Actual, "nothing observes drift: the row stays uncompared")
}

// A row carrying four answers must read as four points, not one.
func TestCalibrationReadsEveryPointOnAMultiPointRow(t *testing.T) {
	r := ShadowRecord{Point: PointSupervise, Points: SupervisePoints, TaskID: "t1",
		Shadow: Shadow{Backend: "typesafe", Model: "jev-1.13.0", Answers: map[string]ShadowAnswer{
			PointWorkerStuck: {Choice: "true", Confidence: 0.9, Actual: "true", Agree: true, By: "watchdog"},
			PointNeedsHuman:  {Choice: "false", Confidence: 0.8, Actual: "false", Agree: true, By: "user"},
		}}}
	cal := ShadowCalibration(nil, []ShadowRecord{r}, nil)
	byPoint := map[string]PointCalibration{}
	for _, p := range cal {
		byPoint[p.Point] = p
	}
	assert.Equal(t, 1, byPoint[PointWorkerStuck].Compared)
	assert.Equal(t, 1, byPoint[PointNeedsHuman].Compared)
	assert.NotContains(t, byPoint, PointSupervise, "the bundle is not itself a decision point")
}
