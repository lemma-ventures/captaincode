package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func btwReq(dir, model, text string) *http.Request {
	body, _ := json.Marshal(map[string]any{"model": model, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": text}}})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set(workspaceHeader, dir)
	return r
}

// The full round trip of a /btw typed while a claude worker runs: the
// plugin's out-of-band POST reaches the worker mid-run; the TUI's queued
// copy of the same words is acknowledged, not run; a note nobody took runs
// as the ordinary follow-up turn, without the word.
func TestBtwReachesTheRunningWorkerAndItsQueuedCopyIsAcknowledged(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	dir := t.TempDir()
	release := make(chan struct{})
	var mu sync.Mutex
	var prompts []string
	var noteSeen []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			// Stand in for the claude transport: attach a channel to the
			// turn's handle and wait for the note, as a real run would.
			b.steers.mu.Lock()
			var s *captaincode.Steer
			for k := range b.steers.live {
				s = k
			}
			b.steers.mu.Unlock()
			require.NotNil(t, s)
			detach := s.Attach(leg, func(note string) error {
				mu.Lock()
				noteSeen = append(noteSeen, note)
				mu.Unlock()
				return nil
			})
			defer detach()
			<-release
		}
		return leg, captaincode.Result{Text: "done", DurationMs: 10}, nil
	}

	turnDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, btwReq(dir, "claude", "implement the parser"))
		turnDone <- rec
	}()
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		for s := range b.steers.live {
			if len(s.Attached()) > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the worker is running and attached")

	// The plugin's out-of-band POST, the moment the user typed it.
	body, _ := json.Marshal(map[string]string{"text": "also handle unicode escapes"})
	rec := httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"delivered":true`)
	assert.Contains(t, rec.Body.String(), "taken mid-run")
	mu.Lock()
	assert.Equal(t, []string{"also handle unicode escapes"}, noteSeen)
	mu.Unlock()

	// A note for another folder finds no worker there.
	rec = httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(t.TempDir()), bytes.NewReader(body)))
	assert.Contains(t, rec.Body.String(), `"delivered":false`)
	assert.Contains(t, rec.Body.String(), "no worker is running")

	close(release)
	require.Equal(t, 200, (<-turnDone).Code)
	b.steers.mu.Lock()
	assert.Empty(t, b.steers.live, "the turn's handle is closed with the turn")
	b.steers.mu.Unlock()

	// The TUI's queued copy: acknowledged, no worker runs.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "claude", "/btw also handle unicode escapes"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "taken by claude")
	mu.Lock()
	assert.Len(t, prompts, 1, "the acknowledged copy ran nothing")
	mu.Unlock()

	// The same words a second time were not delivered to anyone: an
	// ordinary follow-up turn, the word stripped from what the worker reads.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "claude", "/btw also handle unicode escapes"))
	require.Equal(t, 200, rec.Code)
	mu.Lock()
	require.Len(t, prompts, 2, "an undelivered note runs as the next turn")
	assert.Contains(t, prompts[1], "also handle unicode escapes")
	assert.NotContains(t, prompts[1], "/btw")
	mu.Unlock()

	// Bare /btw explains itself.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "claude", "/btw"))
	assert.Contains(t, rec.Body.String(), "a note for the worker that is already running")
	mu.Lock()
	assert.Len(t, prompts, 2)
	mu.Unlock()
}

// A leg with no mid-run channel (codex exec, cursor-agent) is running but
// deaf: the caller is told the note runs as the next turn, and the queued
// copy then does exactly that.
func TestBtwOnADeafLegRunsAsTheNextTurn(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	dir := t.TempDir()
	release := make(chan struct{})
	var mu sync.Mutex
	var prompts []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			<-release
		}
		return leg, captaincode.Result{Text: "done", DurationMs: 10}, nil
	}
	turnDone := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, btwReq(dir, "codex-cli", "port the module"))
		close(turnDone)
	}()
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		for s := range b.steers.live {
			if len(s.Running()) > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	body, _ := json.Marshal(map[string]string{"text": "keep the old API"})
	rec := httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"delivered":false`)
	assert.Contains(t, rec.Body.String(), "codex-cli is running and takes no notes mid-run")
	close(release)
	<-turnDone

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "codex-cli", "/btw keep the old API"))
	require.Equal(t, 200, rec.Code)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, prompts, 2)
	assert.True(t, strings.Contains(prompts[1], "keep the old API"))
	assert.NotContains(t, prompts[1], "/btw")
}

// /interrupt end to end: the out-of-band call asks the attached worker to
// hand off; its answer is the turn's result and lands in memory; the queued
// copy prints the outcome. A deaf worker is stopped with its partial.
func TestInterruptAsksForAHandoffAndRecordsIt(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	b := teamBrain()
	dir := t.TempDir()
	release := make(chan string)
	var mu sync.Mutex
	var noteSeen string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		b.steers.mu.Lock()
		var s *captaincode.Steer
		for k := range b.steers.live {
			s = k
		}
		b.steers.mu.Unlock()
		require.NotNil(t, s)
		detach := s.Attach(leg, func(note string) error {
			mu.Lock()
			noteSeen = note
			mu.Unlock()
			go func() { release <- "HANDOFF: implemented lexer.go; left: parser tests; resume: go test ./..." }()
			return nil
		})
		defer detach()
		text := <-release
		return leg, captaincode.Result{Text: text, DurationMs: 10}, nil
	}
	turnDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, btwReq(dir, "claude", "implement the parser"))
		turnDone <- rec
	}()
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		for s := range b.steers.live {
			if len(s.Attached()) > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	body, _ := json.Marshal(map[string]string{"reason": "need the machine"})
	rec := httptest.NewRecorder()
	b.interruptHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/interrupt?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"asked":["claude"]`)
	assert.Contains(t, rec.Body.String(), "hand off")
	turn := <-turnDone
	require.Equal(t, 200, turn.Code)
	assert.Contains(t, turn.Body.String(), "HANDOFF: implemented lexer.go", "the handoff is the turn's answer")
	mu.Lock()
	assert.Contains(t, noteSeen, "[captain /interrupt]")
	assert.Contains(t, noteSeen, "need the machine")
	mu.Unlock()

	entries, err := captaincode.ReadJournal(captaincode.EuclidBrain{Root: filepath.Join(home, ".euclid")}, timeZero())
	require.NoError(t, err)
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "interrupt")
	mem, _ := os.ReadFile(filepath.Join(home, ".euclid", "memory", "MEMORIES.md"))
	assert.Contains(t, string(mem), "/interrupt on")
	assert.Contains(t, string(mem), "implemented lexer.go")

	// The queued copy prints the outcome, and runs nothing.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "claude", "/interrupt need the machine"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "asked to stop and hand off")

	// Nothing running: the queued copy says so.
	rec = httptest.NewRecorder()
	b.chatCompletions(rec, btwReq(dir, "claude", "/interrupt"))
	assert.Contains(t, rec.Body.String(), "no worker is running")
}

func TestInterruptStopsADeafWorkerAndKeepsItsPartial(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	home := euclidTestHome(t)
	_, err := captaincode.Scaffold(filepath.Join(home, ".euclid"), "main")
	require.NoError(t, err)
	b := teamBrain()
	dir := t.TempDir()
	stopped, attached := make(chan struct{}), make(chan struct{})
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		// Two things race the interrupt here, and the test must wait for the
		// second one. The turn registers its steer around the moment the
		// worker starts, so read the map until it is there rather than once.
		// Then the stop this worker attaches is what /interrupt actually
		// calls - and it lands after the brain already lists the worker as
		// running, so waiting on Running() let the interrupt arrive first,
		// find nothing to call, and leave the wait below hanging until the
		// suite's alarm. Closing attached after AttachStop is the signal.
		var s *captaincode.Steer
		for deadline := time.Now().Add(5 * time.Second); s == nil && time.Now().Before(deadline); {
			b.steers.mu.Lock()
			for k := range b.steers.live {
				s = k
			}
			b.steers.mu.Unlock()
			if s == nil {
				time.Sleep(time.Millisecond)
			}
		}
		if s == nil {
			return leg, captaincode.Result{}, fmt.Errorf("no steer was registered for the turn")
		}
		detach := s.AttachStop(leg, func() { close(stopped) })
		defer detach()
		close(attached)
		<-stopped
		return leg, captaincode.Result{Text: "ported half the module", Partial: true, DurationMs: 90_000},
			fmt.Errorf("codex exec stopped: %w", captaincode.ErrInterrupted)
	}
	turnDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, btwReq(dir, "codex-cli", "port the module"))
		turnDone <- rec
	}()
	select {
	case <-attached:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker never attached its stop to the turn's steer")
	}
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		for s := range b.steers.live {
			if len(s.Running()) > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	rec := httptest.NewRecorder()
	b.interruptHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/interrupt?cwd="+url.QueryEscape(dir), bytes.NewReader([]byte(`{}`))))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"stopped":["codex-cli"]`)
	turn := <-turnDone
	require.Equal(t, 200, turn.Code)
	assert.Contains(t, turn.Body.String(), "ported half the module", "the partial is delivered")
	assert.Contains(t, turn.Body.String(), "stopped by /interrupt")
	mem, _ := os.ReadFile(filepath.Join(home, ".euclid", "memory", "MEMORIES.md"))
	assert.Contains(t, string(mem), "stopped): ported half the module")
}

// The out-of-band /btw that lands while the turn is still compacting or
// routing is held for the worker and reported as such, so the plugin
// refuses the queued copy instead of letting it wait behind the run.
func TestBtwDuringTurnPreparationIsHeld(t *testing.T) {
	b := teamBrain()
	dir := t.TempDir()
	s := b.steerOpen(dir) // a turn has started; no worker yet
	defer b.steerClose(s)
	body, _ := json.Marshal(map[string]string{"text": "Cerebras, not Cerberus"})
	rec := httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(dir), bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"delivered":true`)
	assert.Contains(t, rec.Body.String(), "held for the worker about to start")
	assert.Equal(t, 1, s.Held())
	var got string
	s.Attach(captaincode.LegGrok, func(n string) error { got = n; return nil })
	assert.Equal(t, "Cerebras, not Cerberus", got)
}

// A /btw during a team turn is routed: to the worker the user addressed, else
// to the worker whose brief the director says it concerns - and the turn's
// stream shows who took it.
func TestBtwInATeamTurnIsRoutedAndAnnounced(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	t.Setenv("CAPTAIN_TEAM", "1")
	b := teamBrain()
	dir := gitDirWithCommit(t) // two workers run in parallel only in isolated worktrees, which need a revision
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		return captaincode.Plan{Class: captaincode.ClassHigh, Rationale: "two fronts", Workers: []captaincode.Worker{
			{Leg: captaincode.LegGrok, Brief: "run the GPU benchmark on the available SKU"},
			{Leg: captaincode.LegGLM, Brief: "write the deployment docs"},
		}}, nil
	}
	b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
		return captaincode.Assessment{Quality: 7, Verdict: "good"}, nil
	}
	b.routeNoteFn = func(note string, briefs map[captaincode.Leg]string) ([]captaincode.Leg, string, error) {
		assert.Len(t, briefs, 2)
		return []captaincode.Leg{captaincode.LegGrok}, "the note is about the benchmark hardware", nil
	}
	var mu sync.Mutex
	notes := map[captaincode.Leg][]string{}
	release := make(chan struct{})
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		b.steers.mu.Lock()
		var s *captaincode.Steer
		for k := range b.steers.live {
			s = k
		}
		b.steers.mu.Unlock()
		detach := s.Attach(leg, func(n string) error {
			mu.Lock()
			notes[leg] = append(notes[leg], n)
			mu.Unlock()
			return nil
		})
		defer detach()
		<-release
		return leg, captaincode.Result{Text: "done by " + string(leg), DurationMs: 10}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "team", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "/team benchmark and document the deployment"}}})
	turnDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		r.Header.Set(workspaceHeader, dir)
		b.chatCompletions(rec, r)
		turnDone <- rec
	}()
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		for s := range b.steers.live {
			if len(s.Attached()) == 2 {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "both team workers attached")

	// Director-routed: only grok gets the hardware note.
	nb, _ := json.Marshal(map[string]string{"text": "Cerebras, not Cerberus - the SKU is the CS-3"})
	rec := httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(dir), bytes.NewReader(nb)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"took":["grok"]`)
	assert.Contains(t, rec.Body.String(), "the note is about the benchmark hardware")

	// User-addressed: "@glm" goes to glm without asking the director.
	b.routeNoteFn = func(string, map[captaincode.Leg]string) ([]captaincode.Leg, string, error) {
		t.Error("the director must not be asked when the user addressed a worker")
		return nil, "", nil
	}
	nb, _ = json.Marshal(map[string]string{"text": "@glm mention the CS-3 in the docs"})
	rec = httptest.NewRecorder()
	b.btwHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/btw?cwd="+url.QueryEscape(dir), bytes.NewReader(nb)))
	assert.Contains(t, rec.Body.String(), `"took":["glm"]`)

	close(release)
	turn := <-turnDone
	require.Equal(t, 200, turn.Code)
	mu.Lock()
	assert.Len(t, notes[captaincode.LegGrok], 1)
	assert.Contains(t, notes[captaincode.LegGrok][0], "Cerebras, not Cerberus")
	assert.Len(t, notes[captaincode.LegGLM], 1)
	assert.Contains(t, notes[captaincode.LegGLM][0], "mention the CS-3")
	assert.NotContains(t, notes[captaincode.LegGLM][0], "@glm", "the address is stripped from what the worker reads")
	mu.Unlock()
	out := turn.Body.String()
	assert.Contains(t, out, "/btw** → grok (director: the note is about the benchmark hardware): Cerebras, not Cerberus", "the turn shows who took it: %s", out)
	assert.Contains(t, out, "/btw** → glm (addressed by the user): mention the CS-3")
}

// gitDirWithCommit is a temp repository with one commit (team isolation
// needs a revision to branch worktrees from).
func gitDirWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return dir
}

// /interrupt typed while the turn is still being prepared withdraws it: the
// worker never starts, and the caller is told so.
func TestInterruptWithdrawsATurnStillBeingPrepared(t *testing.T) {
	t.Setenv("CAPTAIN_TRIAGE", "0")
	b := teamBrain()
	dir := t.TempDir()
	gate := make(chan struct{})
	started := false
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		started = true
		return leg, captaincode.Result{Text: "should not run"}, nil
	}
	// A route stub that blocks until the interrupt lands, standing in for a
	// long compaction/director phase.
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		<-gate
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: open[0], Brief: task}}}, nil
	}
	turnDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		b.chatCompletions(rec, btwReq(dir, "auto", "port the module"))
		turnDone <- rec
	}()
	require.Eventually(t, func() bool {
		b.steers.mu.Lock()
		defer b.steers.mu.Unlock()
		return len(b.steers.live) == 1
	}, 5*time.Second, 10*time.Millisecond)
	rec := httptest.NewRecorder()
	b.interruptHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/interrupt?cwd="+url.QueryEscape(dir), bytes.NewReader([]byte(`{}`))))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), `"withdrawn":1`)
	close(gate)
	turn := <-turnDone
	assert.False(t, started, "the worker never started")
	assert.Contains(t, turn.Body.String(), "withdrawn before it started")
}
