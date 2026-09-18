package captaincode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jev, the decision leg. These pin the wire contract (docs.typesafe.ai,
// 2026-09-17), the triage mapping, the vendor's retry advice, and the
// guarantees the rest of captain relies on: registered everywhere a leg is
// listed, never anywhere a task is dispatched.

func s1Server(t *testing.T, handler http.HandlerFunc) *SystemOneClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &SystemOneClient{BaseURL: srv.URL, APIKey: "k-test", Model: "jev-latest"}
}

func s1Classified(class, domain string, cConf, dConf float64) map[string]any {
	return map[string]any{
		"model": "jev-1.13.0",
		"answers": map[string]any{
			"class":  map[string]any{"type": "choice", "choice": class, "probabilities": map[string]float64{class: cConf}, "confidence": cConf},
			"domain": map[string]any{"type": "choice", "choice": domain, "probabilities": map[string]float64{domain: dConf}, "confidence": dConf},
		},
		"usage": map[string]int{"input_tokens": 312, "output_tokens": 6},
	}
}

func noBackoff(t *testing.T) {
	t.Helper()
	prev := systemOneBackoff
	systemOneBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { systemOneBackoff = prev })
}

func TestSystemOneAskSendsTheDocumentedShape(t *testing.T) {
	var got map[string]any
	var auth, ctype string
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		auth, ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		assert.Equal(t, "/v1/systemone", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(s1Classified("medium", "code", 0.9, 0.8))
	})
	resp, res, err := c.Ask(context.Background(), "fix the typo in README", jevClassQuestions())
	require.NoError(t, err)
	assert.Equal(t, "Bearer k-test", auth)
	assert.Equal(t, "application/json", ctype)
	assert.Equal(t, "jev-latest", got["model"])
	assert.Equal(t, "fix the typo in README", got["state"])
	qs := got["questions"].(map[string]any)
	assert.Equal(t, "choice", qs["class"].(map[string]any)["type"])
	assert.Contains(t, qs["class"].(map[string]any)["criteria"], "trivial")
	assert.Contains(t, qs["domain"].(map[string]any)["criteria"], "editorial")
	assert.Equal(t, "jev-1.13.0", resp.Model, "the versioned id that served the call is what gets logged")
	assert.Equal(t, 318, res.Tokens)
	assert.Zero(t, res.CostUSD, "tokens, not dollars: the registry prices them")

	u := CallUsage(LegJev, res.Tokens, res.CostUSD, nil)
	assert.Equal(t, UsageEstimated, u.CostStatus, "priced from the registry, and the record says so")
	assert.Equal(t, "registry:jev", u.PriceSource)
	assert.InDelta(t, 318*0.75*0.042/1e6, u.CostUSD, 1e-12)
}

func TestClassifyWithJevMapsAnswersAndGatesOnTheWeakerConfidence(t *testing.T) {
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(s1Classified("high", "research", 0.95, 0.61))
	})
	tr, _, err := ClassifyWithJev(context.Background(), c, "audit the consensus protocol for liveness bugs")
	require.NoError(t, err)
	assert.Equal(t, ClassHigh, tr.Class)
	assert.Equal(t, DomainResearch, tr.Domain)
	assert.InDelta(t, 0.61, tr.Confidence, 1e-9, "the gate sees the LOWER of the two answers")
	assert.Contains(t, tr.Why, "jev high/research")
	assert.Contains(t, tr.Why, "jev-1.13.0")
}

func TestClassifyWithJevRejectsAnUnknownClassAndDefaultsTheDomain(t *testing.T) {
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(s1Classified("medium", "poetry", 0.9, 0.9))
	})
	tr, _, err := ClassifyWithJev(context.Background(), c, "x")
	require.NoError(t, err)
	assert.Equal(t, DomainGeneral, tr.Domain, "an unknown domain is general, not an error")

	c = s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(s1Classified("enormous", "code", 0.9, 0.9))
	})
	_, _, err = ClassifyWithJev(context.Background(), c, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `class "enormous"`)
}

func TestClassifyWithJevRedactsAndTruncatesTheState(t *testing.T) {
	t.Setenv("CAPTAIN_REDACT", "on")
	var state string
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		state, _ = got["state"].(string)
		_ = json.NewEncoder(w).Encode(s1Classified("trivial", "code", 0.9, 0.9))
	})
	// Assembled at run time so the public-extraction gate does not read a
	// credential-shaped literal in the source.
	secret := strings.Join([]string{"sk", "ant", "api03", strings.Repeat("a", 60)}, "-")
	_, _, err := ClassifyWithJev(context.Background(), c, "rotate "+secret+" then "+strings.Repeat("pad ", 1000))
	require.NoError(t, err)
	assert.NotContains(t, state, secret, "a credential in the task never leaves the machine")
	assert.Less(t, len(state), 2*systemOneStateMax, "only the task's head is sent")
}

func TestSystemOneRetriesOverloadThenSucceeds(t *testing.T) {
	noBackoff(t)
	var n int32
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(529)
			return
		}
		_ = json.NewEncoder(w).Encode(s1Classified("medium", "code", 0.9, 0.9))
	})
	_, _, err := ClassifyWithJev(context.Background(), c, "x")
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&n), "529 is retried, as the vendor asks")
}

func TestSystemOneRateLimitIsTheSharedSentinel(t *testing.T) {
	noBackoff(t)
	var n int32
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, _, err := c.Ask(context.Background(), "x", jevClassQuestions())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimited, "cooled down like any other leg")
	assert.Equal(t, int32(1+systemOneRetries), atomic.LoadInt32(&n))
}

func TestSystemOneBadKeyIsNotRetriedAndNamesTheVariable(t *testing.T) {
	noBackoff(t)
	var n int32
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, _, err := c.Ask(context.Background(), "x", jevClassQuestions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), SystemOneKeyEnv)
	assert.Equal(t, int32(1), atomic.LoadInt32(&n), "a bad key does not get better with retries")
	assert.NotErrorIs(t, err, ErrRateLimited)
}

func TestSystemOneHonoursCancellation(t *testing.T) {
	// The handler is released by cleanup, not by the request context: a
	// server only notices a hung-up client once the body is read, and this
	// handler never reads it.
	release := make(chan struct{})
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // runs before the server's Close (LIFO)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, _, err := c.Ask(ctx, "x", jevClassQuestions())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(t0), 2*time.Second, "the caller's deadline, not the client's 20s, ends the call")
}

func TestSystemOneModelsListsWhatTheKeyReaches(t *testing.T) {
	c := s1Server(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer k-test", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{
			{"name": "jev-latest", "description": "the stable release", "release_date": "2026-09-15"}}})
	})
	ms, err := c.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, ms, 1)
	assert.Equal(t, "jev-latest", ms[0].Name)
}

func TestSystemOneFromEnvNeedsTheKeyAndFollowsTheLegPin(t *testing.T) {
	t.Setenv(SystemOneKeyEnv, "")
	assert.Nil(t, SystemOneFromEnv(), "no key, no client - triage keeps its free-leg classify")
	t.Setenv(SystemOneKeyEnv, "k")
	t.Setenv("CAPTAIN_JEV_MODEL", "")
	t.Setenv(SystemOneURLEnv, "")
	c := SystemOneFromEnv()
	require.NotNil(t, c)
	assert.Equal(t, "https://api.typesafe.ai", c.BaseURL)
	assert.Equal(t, "jev-latest", c.Model, "the registry pin")
	t.Setenv("CAPTAIN_JEV_MODEL", "jev-1.13.0")
	t.Setenv(SystemOneURLEnv, "http://127.0.0.1:1/")
	c = SystemOneFromEnv()
	assert.Equal(t, "jev-1.13.0", c.Model, "repinned like any leg, CAPTAIN_<LEG>_MODEL")
	assert.Equal(t, "http://127.0.0.1:1", c.BaseURL)
}

// The guarantees the router relies on: a leg everywhere a leg is listed,
// nowhere a task is dispatched.
func TestJevIsADecisionLegNotAWorker(t *testing.T) {
	_, err := LoadRegistry(filepath.Join(t.TempDir(), "none.json"))
	require.NoError(t, err)
	assert.True(t, KnownLeg(LegJev), "doctor, pricing and `captain legs` know it")
	assert.False(t, ServesTasks(LegJev))
	assert.NotContains(t, Rungs, LegJev, "never a worker rung")
	assert.NotContains(t, FrontierChain(LegFree), LegJev, "never a reroute or escalation target either")
	assert.NotContains(t, legNames(), "jev", "a workflow's 'known legs' are the ones a stage can name")
	assert.False(t, OpenWeights(LegJev), "closed weights: outside the /oss pool")
	assert.False(t, DirectorCapable(LegJev), "it cannot plan or review either")
	assert.Equal(t, "no tools support on transport system-one", Requirements{Require: []Capability{CapTools}}.Missing(LegJev))
	assert.Equal(t, SupportYes, SupportsCap(LegJev, CapUsage))
	assert.Equal(t, "jev-latest", ModelID(LegJev))
	assert.InDelta(t, 0.042*0.75, EstimateCost(LegJev, 1_000_000), 1e-9, "priced per input token; the 3:1 split leaves the estimate a little under")

	_, isCommand := defaultCommands()[string(LegJev)]
	assert.False(t, isCommand, "no /jev forcing command")
	_, isModel := captainModelEntries()[string(LegJev)]
	assert.False(t, isModel, "not a model the TUI can send a turn to")

	_, err = ParseWorkflow("/jev decide > /glm do it")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decision leg")

	_, err = DefaultWorkspace().RunWorkerStreamHooks(LegJev, "write the tests", 0, nil, nil)
	assert.ErrorIs(t, err, ErrDecisionLeg, "the backstop for any path the guards miss")

	assert.NoError(t, validateSpec(LegSpec{ID: "s1", Transport: TransportSystemOne, Provider: "typesafe", Model: "jev-preview"}))
	assert.Error(t, validateSpec(LegSpec{ID: "s1", Transport: TransportSystemOne}), "an overlay decision leg needs provider and model")
}

// The console's key download is a one-line `API_KEY=…`. Dropped in as
// jev.env it must work the way aa.env does, and the variable must win over
// the file; what is reported is the source, never the key.
func TestSystemOneKeyFallsBackToAJevEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CAPTAIN_SRC", "")
	t.Setenv(SystemOneKeyEnv, "")
	key, source := SystemOneKey()
	assert.Equal(t, "", key, "no variable, no file")
	assert.Equal(t, "", source)
	assert.Nil(t, SystemOneFromEnv())

	dir := filepath.Join(home, ".config", "captain")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, SystemOneKeyFile)
	require.NoError(t, os.WriteFile(p, []byte("# from the console\nAPI_KEY=\"file-key\"\n"), 0o600))
	key, source = SystemOneKey()
	assert.Equal(t, "file-key", key, "the console's API_KEY= line, quotes stripped")
	assert.Equal(t, p, source, "doctor names the file, never the key")
	c := SystemOneFromEnv()
	require.NotNil(t, c)
	assert.Equal(t, "file-key", c.APIKey)
	assert.Equal(t, p, c.KeySource)

	require.NoError(t, os.WriteFile(p, []byte("export TYPESAFE_API_KEY=sdk-shape\n"), 0o600))
	key, _ = SystemOneKey()
	assert.Equal(t, "sdk-shape", key, "the SDK's own variable name works in the file too")

	t.Setenv(SystemOneKeyEnv, "env-key")
	key, source = SystemOneKey()
	assert.Equal(t, "env-key", key, "the variable wins over the file")
	assert.Equal(t, SystemOneKeyEnv, source)

	src := t.TempDir()
	t.Setenv(SystemOneKeyEnv, "")
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.WriteFile(filepath.Join(src, SystemOneKeyFile), []byte("API_KEY=next-to-the-source\n"), 0o600))
	t.Setenv("CAPTAIN_SRC", src)
	key, source = SystemOneKey()
	assert.Equal(t, "next-to-the-source", key, "CAPTAIN_SRC/jev.env, gitignored there by *.env")
	assert.Equal(t, filepath.Join(src, SystemOneKeyFile), source)
}
