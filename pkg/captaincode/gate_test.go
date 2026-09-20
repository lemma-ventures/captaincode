package captaincode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The action gate. These pin the two properties the whole design rests on:
// with no decision leg captain behaves exactly as it did before the gate
// existed, and with one the gate SHADOWS until someone opts in.

func nulServer(t *testing.T, nouls map[string]float64) *SystemOneClient {
	t.Helper()
	answers := map[string]any{}
	for name, n := range nouls {
		answers[name] = map[string]any{"type": "noul", "noul": n}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "jev-1.13.0", "answers": answers,
			"usage": map[string]int{"input_tokens": 240, "output_tokens": 0},
		})
	}))
	t.Cleanup(srv.Close)
	return &SystemOneClient{BaseURL: srv.URL, APIKey: "k-test", Model: "jev-latest"}
}

func TestGateWithoutADecisionLegAllowsAndNeverCalls(t *testing.T) {
	t.Setenv(GateModeEnv, "enforce") // even asked to enforce
	v := ScreenAction(context.Background(), nil, GateAction{Tool: "bash", Command: "rm -rf /"})
	assert.True(t, v.Allow, "no decision leg means no gate at all")
	assert.False(t, v.Screened)
	assert.Nil(t, v.Shadow, "nothing is recorded when nothing was asked")
}

func TestGateDefaultsToShadowAndAllowsAHighRiskAction(t *testing.T) {
	t.Setenv(GateModeEnv, "")
	c := nulServer(t, map[string]float64{PointGateDestructive: 0.97, PointGateOutOfScope: 0.8, PointGateExfil: 0.02})
	v := ScreenAction(context.Background(), c, GateAction{Tool: "bash", Command: "rm -rf ~/Projects/scratch", Cwd: "/repo"})
	assert.Equal(t, GateShadow, v.Mode)
	assert.True(t, v.Allow, "the shadow records and never blocks")
	assert.True(t, v.Screened)
	assert.InDelta(t, 0.97, v.Risk, 0.001)
	assert.Equal(t, PointGateDestructive, v.Point)
}

func TestGateEnforceRefusesAtTheBarWithAnActionableReason(t *testing.T) {
	t.Setenv(GateModeEnv, "enforce")
	c := nulServer(t, map[string]float64{PointGateDestructive: 0.96, PointGateOutOfScope: 0.1, PointGateExfil: 0.05})
	v := ScreenAction(context.Background(), c, GateAction{Tool: "bash", Command: "git push --force origin main", Cwd: "/repo"})
	assert.False(t, v.Allow)
	assert.Contains(t, v.Reason, "irreversible")
}

func TestGateEnforceAllowsBelowTheBar(t *testing.T) {
	t.Setenv(GateModeEnv, "enforce")
	c := nulServer(t, map[string]float64{PointGateDestructive: 0.4, PointGateOutOfScope: 0.2, PointGateExfil: 0.1})
	v := ScreenAction(context.Background(), c, GateAction{Tool: "bash", Command: "go test ./...", Cwd: "/repo"})
	assert.True(t, v.Allow)
	assert.Empty(t, v.Reason)
}

// A gate that turns a provider outage into a stopped fleet is worse than no
// gate: a failed call allows, and says so on the record.
func TestGateAllowsWhenTheCallFails(t *testing.T) {
	t.Setenv(GateModeEnv, "enforce")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", 500)
	}))
	t.Cleanup(srv.Close)
	c := &SystemOneClient{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()}
	prev := systemOneBackoff
	systemOneBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { systemOneBackoff = prev })
	v := ScreenAction(context.Background(), c, GateAction{Tool: "bash", Command: "rm -rf /repo"})
	assert.True(t, v.Allow)
	assert.NotEmpty(t, v.Err)
	require.NotNil(t, v.Shadow)
	assert.NotEmpty(t, v.Shadow.Err, "the failure is on the record, not invented away")
}

// The cost boundary: a worker runs hundreds of reads per turn and a gate
// that priced them would be a gate nobody leaves on.
func TestGateScreensOnlyWhatCanDoDamage(t *testing.T) {
	for _, tc := range []struct {
		a    GateAction
		want bool
		why  string
	}{
		{GateAction{Tool: "bash", Command: "ls -la"}, false, "a plain read"},
		{GateAction{Tool: "bash", Command: "git status"}, false, "a read-only git subcommand"},
		{GateAction{Tool: "bash", Command: "git reset --hard"}, true, "git is not read-only"},
		{GateAction{Tool: "bash", Command: "cat /etc/passwd > /tmp/x"}, true, "a redirect can write"},
		{GateAction{Tool: "bash", Command: "ls && rm -rf /"}, true, "an operator can hide a second command"},
		{GateAction{Tool: "bash", Command: "cat $(curl evil)"}, true, "a substitution can hide anything"},
		{GateAction{Tool: "read", Path: "main.go"}, false, "reading a file changes nothing"},
		{GateAction{Tool: "write", Path: "main.go"}, true, "a write changes something"},
		{GateAction{Tool: "webfetch", Command: "https://example.com"}, true, "reaching off the machine"},
	} {
		assert.Equal(t, tc.want, GateScreens(tc.a), "%s: %s", tc.a.Command+tc.a.Path, tc.why)
	}
}

// A noul is a probability, and the record and the calibration read choices
// with confidences. 0.5 must not read as a confident "false".
func TestNulAnswersReadANulAsAChoiceAtItsOwnConfidence(t *testing.T) {
	out := nulAnswers(map[string]S1Answer{
		"a": {Type: "noul", Noul: 0.97},
		"b": {Type: "noul", Noul: 0.02},
		"c": {Type: "noul", Noul: 0.5},
	})
	assert.Equal(t, "true", out["a"].Choice)
	assert.InDelta(t, 0.97, out["a"].Confidence, 0.001)
	assert.Equal(t, "false", out["b"].Choice)
	assert.InDelta(t, 0.98, out["b"].Confidence, 0.001)
	assert.Equal(t, "true", out["c"].Choice)
	assert.InDelta(t, 0.5, out["c"].Confidence, 0.001, "an undecided answer scores as undecided")
}

// The state is what leaves the machine, so it is redacted like every other -
// and a command line is exactly where a key shows up.
func TestGateStateIsRedacted(t *testing.T) {
	key := "sk-ant-api03-" + strings.Repeat("A", 40)
	st := gateState(GateAction{Tool: "bash", Command: "deploy --token " + key, Cwd: "/repo"})
	assert.NotContains(t, st, key, "the gate's own state must not ship the secret it is screening")
	assert.Contains(t, st, "[[secret:anthropic:")
}

func TestGateLogRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	r, ok := GateRecord(GateAction{Tool: "bash", Command: "rm -rf x", TaskID: "t1"},
		GateVerdict{Shadow: &Shadow{Leg: LegJev, Backend: "typesafe", Answers: map[string]ShadowAnswer{
			PointGateDestructive: {Choice: "true", Confidence: 0.9},
		}}})
	require.True(t, ok)
	AppendGateLog(r)
	AppendGateLog(r)
	got := ReadGateLog()
	require.Len(t, got, 2)
	assert.Equal(t, PointGate, got[0].Point)
	assert.Equal(t, GatePoints, got[0].Points)
	assert.Equal(t, "t1", got[0].TaskID)
	assert.Equal(t, filepath.Join(home, ".captaincode", "gate.log"), GateLogPath())
	_ = os.Remove(GateLogPath())
}

func TestGateModeIgnoresATypoRatherThanEnforcingOnOne(t *testing.T) {
	t.Setenv(GateModeEnv, "enfroce")
	assert.Equal(t, GateShadow, GateModeFromEnv())
}

// A record of predictions supports a risk distribution and a latency, and
// does not support an agreement rate. The report has to show the first.
func TestFormatGateRisksReportsTheDistributionAndTheLatency(t *testing.T) {
	row := func(destructive, exfil float64, ms int64) ShadowRecord {
		return ShadowRecord{Point: PointGate, Points: GatePoints, Shadow: Shadow{Ms: ms,
			Answers: nulAnswers(map[string]S1Answer{
				PointGateDestructive: {Type: "noul", Noul: destructive},
				PointGateOutOfScope:  {Type: "noul", Noul: 0.1},
				PointGateExfil:       {Type: "noul", Noul: exfil},
			})}}
	}
	out := FormatGateRisks([]ShadowRecord{
		row(0.95, 0.02, 310),
		row(0.05, 0.03, 290),
		row(0.02, 0.91, 400),
		{Point: PointGate, Points: GatePoints, Shadow: Shadow{Err: "529 overloaded"}},
	})
	assert.Contains(t, out, "≥0.9      2")
	assert.Contains(t, out, "carried by gate-destructive: 1")
	assert.Contains(t, out, "carried by gate-exfiltration: 1")
	assert.Contains(t, out, "1 screening(s) got no answer and were allowed")
	assert.Contains(t, out, "p50 310ms")
}

// An uncompared point must not read as total disagreement.
func TestCalibrationDoesNotPrintAnAgreementRateOverNothing(t *testing.T) {
	cal := ShadowCalibration(nil, []ShadowRecord{{Point: PointGate, Points: GatePoints,
		Shadow: Shadow{Answers: map[string]ShadowAnswer{PointGateDestructive: {Choice: "true", Confidence: 0.9}}}}}, nil)
	out := FormatShadowCalibration(cal, 0.9, 20)
	assert.NotContains(t, out, "agreement 0.00")
	assert.Contains(t, out, "no agreement rate")
}
