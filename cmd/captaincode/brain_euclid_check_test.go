package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The launch check rides every surface Captain Code starts from.

func TestEuclidStatusCarriesTheLaunchCheck(t *testing.T) {
	home := euclidTestHome(t)
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.euclidStatusHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/euclid/status", nil))
	require.Equal(t, 200, rec.Code)
	var st struct {
		Checks []captaincode.BrainCheck `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.Len(t, st.Checks, 2, "main and local, always, even when neither exists")
	assert.False(t, st.Checks[0].Present)
	assert.Equal(t, "captain euclid init", st.Checks[0].Fix)

	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	b.euclidStatusHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/euclid/status", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	assert.True(t, st.Checks[0].Present)
	assert.Contains(t, st.Checks[0].Attention, "template")
	// The rendered status (the `/euclid` control word) says the same in prose.
	assert.Contains(t, renderEuclidStatus(b.euclidStatusNow(defaultWorkspace())), "memory  main")
}

func TestDoctorReportsBothBrains(t *testing.T) {
	euclidTestHome(t)
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		captainEnv:     filepath.Join(t.TempDir(), "env"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
	})
	out := sb.String()
	assert.Contains(t, out, "memory  main", "doctor checks the main brain")
	assert.Contains(t, out, "memory  local", "…and the local one")
	assert.Contains(t, out, "captain euclid init", "a missing brain says how to create it")
}

// A slow status computation runs once per folder, however many polls ask,
// and a poll answers within euclidStatusWait instead of waiting it out.
func TestEuclidStatusComputesOnceAndAnswersFast(t *testing.T) {
	prev := euclidStatusWait
	euclidStatusWait = 20 * time.Millisecond
	defer func() { euclidStatusWait = prev }()
	var c euclidStatusCache
	var runs atomic.Int32
	release := make(chan struct{})
	compute := func() euclidStatus { runs.Add(1); <-release; return euclidStatus{Cwd: "/x"} }
	for i := 0; i < 5; i++ {
		start := time.Now()
		_, ok := c.get("/x", "k", compute, context.Background())
		assert.False(t, ok, "nothing computed yet")
		assert.Less(t, time.Since(start), time.Second)
	}
	close(release)
	require.Eventually(t, func() bool { _, ok := c.get("/x", "k", compute, context.Background()); return ok }, time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), runs.Load(), "one computation for every poll")
	st, ok := c.get("/x", "k", compute, context.Background())
	assert.True(t, ok)
	assert.Equal(t, "/x", st.Cwd)
	assert.Equal(t, int32(1), runs.Load(), "a fresh status is served from the cache")
}
