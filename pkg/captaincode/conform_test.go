package captaincode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The open decision-leg backend, and the check that has to pass before
// captain routes anything through one.

func TestKeylessURLIsADecisionLeg(t *testing.T) {
	t.Setenv(SystemOneKeyEnv, "")
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	t.Setenv(SystemOneURLEnv, "http://127.0.0.1:14113")
	c := SystemOneFromEnv()
	require.NotNil(t, c, "a System One-shaped endpoint is a decision leg with or without a key")
	assert.True(t, c.Keyless())
	assert.Equal(t, "127.0.0.1:14113", c.Backend())
	assert.Equal(t, "http://127.0.0.1:14113", c.BaseURL)
}

// The case the whole task turns on: no key, no URL, no decision leg - and
// every caller of SystemOneFromEnv already handles nil.
func TestNoKeyAndNoURLIsNoDecisionLeg(t *testing.T) {
	t.Setenv(SystemOneKeyEnv, "")
	t.Setenv(SystemOneURLEnv, "")
	t.Setenv("CAPTAIN_SRC", t.TempDir())
	t.Chdir(t.TempDir())
	assert.Nil(t, SystemOneFromEnv())
}

func TestKeylessClientSendsNoAuthorizationHeader(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "local-1", "answers": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	c := &SystemOneClient{BaseURL: srv.URL}
	_, _, err := c.Ask(context.Background(), "x", map[string]S1Question{"q": {Type: "noul", Instructions: "?"}})
	require.NoError(t, err)
	assert.Empty(t, auth, "an open backend takes no credential and must not be sent one")
}

func TestBackendNamesTheVendorAndAnythingElseByHost(t *testing.T) {
	assert.Equal(t, SystemOneVendor, (&SystemOneClient{}).Backend())
	assert.Equal(t, SystemOneVendor, (&SystemOneClient{BaseURL: "https://api.typesafe.ai"}).Backend())
	assert.Equal(t, "nimble.internal:8080", (&SystemOneClient{BaseURL: "http://nimble.internal:8080"}).Backend())
}

// The shadow record stamps the backend, so a calibration can refuse to pool
// two of them.
func TestShadowStampsTheBackendThatAnswered(t *testing.T) {
	c := nulServer(t, map[string]float64{PointGateDestructive: 0.9})
	sh := shadowFrom(c, S1Response{Model: "jev-1.13.0"}, Result{}, nil, nil)
	assert.Equal(t, c.Backend(), sh.Backend)
	assert.NotEmpty(t, sh.Backend)
}

// A bar is a property of one backend at one version. `jev-latest` is an alias
// that moves, and an open re-implementation is a different model entirely:
// pooling them would quote a number no single configuration ever produced.
func TestSuggestedBarDeclinesWhenTwoBackendsArePooled(t *testing.T) {
	agree := func(backend, model string) ShadowRecord {
		return ShadowRecord{Point: PointNoteRoute, Shadow: Shadow{Backend: backend, Model: model,
			Answers: map[string]ShadowAnswer{PointNoteRoute: {Choice: "claude", Confidence: 0.99, Actual: "claude", Agree: true, By: "director"}}}}
	}
	var one, two []ShadowRecord
	for i := 0; i < 25; i++ {
		one = append(one, agree("typesafe", "jev-1.13.0"))
		two = append(two, agree("typesafe", "jev-1.13.0"))
	}
	two = append(two, agree("127.0.0.1:14113", "nimble-1"))

	got := ShadowCalibration(nil, one, nil)[0]
	_, ok := got.SuggestedBar(0.9, 20)
	assert.True(t, ok, "one backend, one model: a bar can be read off it")
	assert.Equal(t, "typesafe jev-1.13.0", got.Served())

	mixed := ShadowCalibration(nil, two, nil)[0]
	assert.True(t, mixed.Mixed())
	_, ok = mixed.SuggestedBar(0.9, 20)
	assert.False(t, ok, "two backends are two samples, not one")
	assert.Contains(t, FormatShadowCalibration([]PointCalibration{mixed}, 0.9, 20), "more than one backend")
}

func TestConformSuiteCoversEveryCapabilityCaptainAsksAbout(t *testing.T) {
	seen := map[string]int{}
	for _, c := range ConformSuite() {
		seen[c.Capability]++
		require.NotEmpty(t, c.Expect, "%s: a case with no expected answer checks nothing", c.Name)
		for name := range c.Expect {
			assert.Contains(t, c.Questions, name, "%s expects %q but never asks it", c.Name, name)
		}
	}
	for _, capability := range ConformCaps {
		assert.Greater(t, seen[capability], 0, "no conformance case for %s", capability)
	}
}

// A backend that answers everything right is usable; one that misses a single
// case is reported as not usable for that capability, because sixteen obvious
// questions do not support a percentage.
func TestConformReportsUsabilityPerCapability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			State     string                `json:"state"`
			Questions map[string]S1Question `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		answers := map[string]any{}
		for name, q := range in.Questions {
			switch {
			case q.Type == "noul" && name == PointWorkerStuck && strings.Contains(in.State, "cargo build"):
				answers[name] = map[string]any{"type": "noul", "noul": 0.95} // wrong: a build is not a stall
			case q.Type == "noul":
				answers[name] = map[string]any{"type": "noul", "noul": nulTruth(name, in.State)}
			default:
				answers[name] = map[string]any{"type": "choice", "choice": choiceTruth(name, in.State), "confidence": 0.95}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fake-1", "answers": answers,
			"usage": map[string]int{"input_tokens": 100}})
	}))
	t.Cleanup(srv.Close)
	rep := RunConform(context.Background(), &SystemOneClient{BaseURL: srv.URL}, ConformSuite())
	assert.True(t, rep.Keyless)
	assert.Equal(t, "fake-1", rep.Model)
	assert.False(t, rep.Usable(CapSupervise), "it called a release build a stall")
	out := FormatConformReport(rep)
	assert.Contains(t, out, "NOT usable")
	assert.Contains(t, out, "long build is not a stall")
	assert.Contains(t, out, "It does NOT mean captain", "a passing suite must not read as a licence to route")
}

// nulTruth/choiceTruth answer the suite the way a good backend would, so the
// fixture above can be wrong in exactly one place.
func nulTruth(name, state string) float64 {
	yes := map[string]bool{
		PointGateDestructive: strings.Contains(state, "rm -rf"),
		PointGateOutOfScope:  strings.Contains(state, "rm -rf"),
		PointGateExfil:       strings.Contains(state, "curl -F"),
		PointWorkerStuck:     strings.Contains(state, "read pkg/captaincode/pool.go\n  - read"),
		PointWorkOffTrack:    false,
		PointNeedsHuman:      strings.Contains(state, "gh auth status"),
		"keep":               strings.Contains(state, "We decided") || strings.Contains(state, "panic:"),
	}
	if yes[name] {
		return 0.95
	}
	return 0.05
}

func choiceTruth(name, state string) string {
	switch name {
	case PointClass:
		if strings.Contains(state, "audit") {
			return "high"
		}
		return "trivial"
	case PointDomain:
		switch {
		case strings.Contains(state, "announcement"):
			return "editorial"
		case strings.Contains(state, "compare the three"):
			return "research"
		}
		return "code"
	case PointShape:
		if strings.Contains(state, "three angles") {
			return ShapeTeam
		}
		return ShapeSolo
	case PointLeg:
		return string(LegFree)
	}
	return ""
}
