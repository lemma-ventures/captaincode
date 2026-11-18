package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ROADMAP Q8: a worker log keeps the exact tool input under its cut status
// line, and every worker run keeps the exact prompt it received in a
// companion .prompt.md file.

// runsHome points the brain at an empty home so the run files land in a
// directory this test owns.
func runsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPTAIN_CWD", home)
	return home
}

// runsDir is where worker logs and their prompt files live under HOME.
func runsDir(home string) string {
	return filepath.Join(home, ".captaincode", "runs")
}

// matchFiles lists the runs-directory files whose names end in suffix.
func matchFiles(t *testing.T, dir, suffix string) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join(dir, "*"+suffix))
	require.NoError(t, err)
	sort.Strings(all)
	return all
}

func TestWorkerLogKeepsFullToolInput(t *testing.T) {
	runsHome(t)
	ws := captaincode.Workspace{Dir: t.TempDir()}
	long := "go test ./pkg/captaincode/ -count=1 -timeout 120s " +
		strings.Repeat("-run TestWorkerLogKeepsFullToolInput ", 6) + "done"
	require.Greater(t, len(long), 80, "the input is longer than the status line allows")
	full := `{"command":"` + long + ` OPENAI_API_KEY=sk-proj-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

	l := openWorkerLog(ws, "grok", "[user]\nrun the suite\n\n")
	require.NotNil(t, l)
	l.status(captaincode.StatusInput("⚙ shell "+long, full))
	l.close(captaincode.Result{Text: "ok"}, nil)

	data, err := os.ReadFile(l.path)
	require.NoError(t, err)
	text := string(data)
	status := strings.Index(text, "⚙ shell ")
	i := strings.Index(text, "input: ")
	require.GreaterOrEqual(t, status, 0, "the log has the status line")
	require.GreaterOrEqual(t, i, 0, "the log has the input line under the status line")
	assert.Greater(t, i, status, "the input follows the status line")
	assert.Contains(t, text, `input: {"command":"`+long, "the input is complete, not cut to 80 characters")
	assert.NotContains(t, text, "sk-proj-", "the key in the input is masked")
	assert.Contains(t, text, "OPENAI_API_KEY=[secret]", "and named as a secret line")
}

func TestWorkerLogWritesAMultiLineInputOnOneLine(t *testing.T) {
	runsHome(t)
	ws := captaincode.Workspace{Dir: t.TempDir()}
	l := openWorkerLog(ws, "codex", "[user]\nrun it\n\n")
	require.NotNil(t, l)
	l.status(captaincode.StatusInput("⚙ shell cat <<EOF", "cat <<EOF\nfirst\nsecond\nEOF"))
	l.delta("The reply starts here.")
	l.close(captaincode.Result{Text: "The reply starts here."}, nil)

	data, err := os.ReadFile(l.path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "input: cat <<EOF\\nfirst\\nsecond\\nEOF\n", "the input is one line")
	assert.NotContains(t, text, "\nfirst\n", "no input line stands alone where the reply would be")
}

func TestToolInputOverSixteenKiBIsCut(t *testing.T) {
	status := captaincode.StatusInput("⚙ edit big.go", strings.Repeat("x", 20_000))
	show, input := captaincode.StatusParts(status)
	assert.Equal(t, "⚙ edit big.go", show, "the status line stays short")
	assert.LessOrEqual(t, len(input), 16*1024+40, "the input is bounded")
	assert.Contains(t, input, "[cut ", "the cut is marked")
	assert.Contains(t, input, "bytes]", "and says how much is missing")
}

func TestPromptFileMode0600(t *testing.T) {
	runsHome(t)
	ws := captaincode.Workspace{Dir: t.TempDir()}
	prompt := "[user]\n" + strings.Repeat("the worker needs this context\n", 40) +
		"PROMPT_TAIL_MARKER_8M4J2\nOPENAI_API_KEY=sk-proj-aaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"

	l := openWorkerLog(ws, "claude", prompt)
	require.NotNil(t, l)
	defer l.f.Close()
	pp := strings.TrimSuffix(l.path, ".log") + ".prompt.md"
	info, err := os.Stat(pp)
	require.NoError(t, err, "the full prompt is saved beside the log")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	body, err := os.ReadFile(pp)
	require.NoError(t, err)
	assert.Contains(t, string(body), "PROMPT_TAIL_MARKER_8M4J2", "the whole prompt is kept")
	assert.NotContains(t, string(body), "sk-proj-", "the key in the prompt is masked")

	head, err := os.ReadFile(l.path)
	require.NoError(t, err)
	assert.Contains(t, string(head), "prompt: "+pp+"\n", "the worker log names the prompt file")
	assert.NotContains(t, string(head), "PROMPT_TAIL_MARKER_8M4J2", "the header keeps only its short task line")
}

func TestPromptFileOver256KiBIsCut(t *testing.T) {
	runsHome(t)
	l := openWorkerLog(captaincode.Workspace{Dir: t.TempDir()}, "claude", strings.Repeat("p", 300_000))
	require.NotNil(t, l)
	defer l.f.Close()
	body, err := os.ReadFile(strings.TrimSuffix(l.path, ".log") + ".prompt.md")
	require.NoError(t, err)
	assert.Less(t, len(body), 300_000, "the file is cut")
	assert.Contains(t, string(body), "[cut ", "the cut is marked")
}

// A rerouted worker receives the whole prompt again; the second run's prompt
// file keeps it even though the log's task line is only a short peek.
func TestReroutedWorkerPromptSaved(t *testing.T) {
	home := runsHome(t)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		if leg == captaincode.LegCodexCLI {
			return leg, captaincode.Result{}, &captaincode.LoginError{Leg: leg, Cmd: "codex-cli login", Detail: "Not signed in."}
		}
		return leg, captaincode.Result{Text: "answer from " + string(leg), DurationMs: 5}, nil
	}
	seg := strings.Repeat("refactor the parser and keep its tests green ", 40) + "REROUTED_PROMPT_TAIL_2QX7"
	prompt := "[user]\nfix the failing build\n\n[user]\n" + seg + "\n"

	ran, res, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegCodexCLI, prompt, nil, nil, "")
	require.NoError(t, err)
	require.NotEqual(t, captaincode.LegCodexCLI, ran, "the task rerouted")
	assert.Contains(t, res.Text, "answer from")

	prompts := matchFiles(t, runsDir(home), ".prompt.md")
	require.NotEmpty(t, prompts, "both runs saved their prompt")
	kept := 0
	for _, p := range prompts {
		body, err := os.ReadFile(p)
		require.NoError(t, err)
		if strings.Contains(string(body), "REROUTED_PROMPT_TAIL_2QX7") {
			kept++
		}
	}
	assert.Equal(t, len(prompts), kept, "every run kept the full prompt, not the header peek")

	logs := matchFiles(t, runsDir(home), ".log")
	require.NotEmpty(t, logs)
	head, err := os.ReadFile(logs[0])
	require.NoError(t, err)
	assert.NotContains(t, string(head), "REROUTED_PROMPT_TAIL_2QX7", "the log header stays a short task line")
}

// An escalation hands a second worker a new prompt. That prompt must reach
// disk with the run that received it.
func TestEscalationPromptSaved(t *testing.T) {
	home := runsHome(t)
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, _ string, _, _ func(string)) (captaincode.Leg, captaincode.Result, error) {
		return leg, captaincode.Result{Text: "claims it is fixed", DurationMs: 5}, nil
	}
	b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "AGG"}, nil
	}
	b.escalation = captaincode.EscalationPolicy{MaxRepairs: 1, MaxEscalations: 1}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "/grok fix it gate: false"))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	prompts := matchFiles(t, runsDir(home), ".prompt.md")
	require.Len(t, prompts, 3, "the attempt, the repair and the escalation")
	found := 0
	for _, p := range prompts {
		body, err := os.ReadFile(p)
		require.NoError(t, err)
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		if strings.Contains(string(body), "A previous worker") {
			found++
			assert.Contains(t, string(body), "failed the completion gate", "the escalation prompt is saved")
			assert.Contains(t, string(body), "gate `", "with the reason it was sent")
		}
	}
	assert.Equal(t, 1, found, "exactly one of the three prompts is the escalation")
}

// The log keeps what the TUI shows plus the input; both come from one status
// string, so a display that reads it sees only the short line.
func TestStatusPartsLeavesPlainStatusesAlone(t *testing.T) {
	show, input := captaincode.StatusParts("⚙ Read pkg/legs.go")
	assert.Equal(t, "⚙ Read pkg/legs.go", show)
	assert.Equal(t, "", input)
	assert.Equal(t, "", captaincode.StatusInput("", "ignored"))
	assert.Equal(t, "⚙ bash", captaincode.StatusInput("⚙ bash", ""), "no input, no change")
}
