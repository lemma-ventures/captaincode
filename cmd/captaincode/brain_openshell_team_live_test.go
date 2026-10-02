package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/require"
)

// TestOpenShellHTTPPlannedTeamLiveQualification sends "/team /openshell" to
// the brain on the public numbers fixture: the tool-less claude director
// splits the task, and the planned workers run in VM sandboxes through
// Cerebras. Opt in with CAPTAIN_TEST_OPENSHELL_TEAM_LIVE=1.
func TestOpenShellHTTPPlannedTeamLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_TEAM_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_TEAM_LIVE=1 with prepared VM runtime, provider key and claude")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	pinOpenShellLiveDocker(t)
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "cc-http-team-")
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	// The isolated HOME keeps the brain's ledger and runs out of the real
	// one; the tool-less director still needs the operator's claude login.
	u, err := user.Current()
	require.NoError(t, err)
	claude, err := exec.LookPath("claude")
	require.NoError(t, err)
	shim := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(shim, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(shim, "claude"), []byte("#!/bin/sh\nHOME='"+u.HomeDir+"' exec '"+claude+"' \"$@\"\n"), 0o700))
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := openShellHTTPBrain(t)
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(repo, 0o700))
	for key, value := range map[string]string{
		"HOME": home, "CAPTAIN_OPENSHELL_PREPARED": prepared, "CAPTAIN_OPENSHELL_PILOT": pilot,
		"CAPTAIN_OPENSHELL_REPO": "", "CAPTAIN_OPENSHELL_REVISION": "HEAD", "CAPTAIN_OPENSHELL_RUNTIME": "vm",
		"CAPTAIN_OPENSHELL_PROFILE": "cerebras", "CAPTAIN_OPENSHELL_ALLOWED": "factorial.py,fibonacci.py",
		"CAPTAIN_OPENSHELL_VERIFY":    `["python3","-m","unittest","-v"]`,
		"CAPTAIN_OPENSHELL_PROTECTED": "test_numbers.py", "CAPTAIN_OPENSHELL_BASELINE": "pass",
		"CAPTAIN_OPENSHELL_DIRECTOR": "claude", "CAPTAIN_OPENSHELL_CONCURRENCY": "2",
	} {
		t.Setenv(key, value)
	}
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	git := func(dir string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	files := map[string]string{
		"factorial.py": "def factorial(n):\n    if type(n) is not int:\n        raise TypeError('integer required')\n    if n < 0:\n        raise ValueError('nonnegative required')\n    return 1 if n < 2 else n * factorial(n - 1)\n",
		"fibonacci.py": "def fibonacci(n):\n    if type(n) is not int:\n        raise TypeError('integer required')\n    if n < 0:\n        raise ValueError('nonnegative required')\n    return n if n < 2 else fibonacci(n - 1) + fibonacci(n - 2)\n",
		"test_numbers.py": `import unittest
from factorial import factorial
from fibonacci import fibonacci

class Numbers(unittest.TestCase):
    def test_factorial(self):
        self.assertEqual([factorial(n) for n in range(8)], [1, 1, 2, 6, 24, 120, 720, 5040])
    def test_fibonacci(self):
        self.assertEqual([fibonacci(n) for n in range(10)], [0, 1, 1, 2, 3, 5, 8, 13, 21, 34])
    def test_negative(self):
        for fn in [factorial, fibonacci]:
            with self.assertRaises(ValueError):
                fn(-1)
    def test_type(self):
        for fn in [factorial, fibonacci]:
            for bad in [True, 1.5, '1', None]:
                with self.assertRaises(TypeError):
                    fn(bad)
`,
	}
	for name, data := range files {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(data), 0o600))
	}
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "-c", "user.name=romainPellerin", "-c", "user.email=rom@lemma.ventures", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public planned team fixture")
	before := map[string][]byte{}
	for _, name := range []string{"factorial.py", "fibonacci.py", "test_numbers.py", ".git/index"} {
		before[name], err = os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
	}
	server := httptest.NewServer(http.HandlerFunc(b.chatCompletions))
	defer server.Close()
	task := "/team /openshell Refactor factorial.py to compute factorial with an iterative loop instead of recursion, and refactor fibonacci.py to compute fibonacci in linear time with an iterative loop. Keep each function's validation and public API. Run python3 -m unittest -v."
	r := openShellHTTPRequest(t, repo, "team", task, false)
	r.RequestURI = ""
	r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	resp, err := server.Client().Do(r.WithContext(ctx))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	wall := time.Since(started).Seconds()
	require.NoError(t, os.WriteFile(filepath.Join(root, "response.txt"), body, 0o600))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), "the director planned")
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	attempt := stored.AttemptStateFor(resp.Header.Get("X-Captain-Attempt-ID"))
	require.NotNil(t, attempt)
	require.Equal(t, captaincode.StateSucceeded, attempt.State)
	require.NotNil(t, attempt.Export)
	require.NotNil(t, attempt.OpenShellAttempts)
	data, err := os.ReadFile(attempt.Export.RunRecord)
	require.NoError(t, err)
	var run captaincode.OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	require.Equal(t, "pass", run.Verdict)
	require.True(t, run.RequireAll)
	require.NotEmpty(t, run.Tasks)
	require.LessOrEqual(t, len(run.Tasks), captaincode.MaxStageWidth)
	usage := run.AttemptUsage
	require.NotNil(t, usage)
	planned := attempt.OpenShellAttempts.Directors - usage.Directors
	require.GreaterOrEqual(t, planned, 1, "the plan's director calls settle with the task")
	require.LessOrEqual(t, planned, 2)
	require.Equal(t, usage.Workers, attempt.OpenShellAttempts.Workers)
	plan := attempt.OpenShellPlan
	require.NotNil(t, plan, "the plan is kept with the task")
	require.Len(t, plan.Assignments, len(run.Tasks))
	require.Equal(t, planned, plan.DirectorAttempts)
	for _, worker := range run.Tasks {
		require.NotNil(t, worker.Report)
		require.Len(t, worker.Report.Checks, 17)
		for name, check := range worker.Report.Checks {
			require.Equal(t, "pass", check.Verdict, name)
		}
		require.Equal(t, run.Revision, worker.Manifest.BaseRevision)
	}
	require.Len(t, run.Integrated.Report.Checks, 8)
	for name, check := range run.Integrated.Report.Checks {
		require.Equal(t, "pass", check.Verdict, name)
	}
	for _, name := range attempt.Export.Manifest.ChangedFiles {
		require.Contains(t, []string{"factorial.py", "fibonacci.py"}, name)
	}
	landing := filepath.Join(root, "landing")
	git(root, "clone", "--quiet", "--no-hardlinks", repo, landing)
	git(landing, "apply", "--index", attempt.Export.Manifest.DiffPath)
	require.Equal(t, run.Integrated.Tree, git(landing, "write-tree"))
	for name, expected := range before {
		actual, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
		require.Equal(t, expected, actual, name)
	}
	tokens, cost, complete := run.Spend()
	rulings := make([]string, 0, len(run.Rulings))
	for _, ruling := range run.Rulings {
		rulings = append(rulings, ruling.Winner+": "+ruling.Reason)
	}
	summary, err := json.MarshalIndent(map[string]any{
		"wall_seconds": wall, "run_record": attempt.Export.RunRecord, "run_seconds": run.Seconds,
		"planned_workers": len(run.Tasks), "plan_director_attempts": planned, "settled_attempts": attempt.OpenShellAttempts,
		"changed_files": attempt.Export.Manifest.ChangedFiles, "rulings": rulings, "host_preservation": true,
		"spend_complete": complete, "reported_cost_usd": cost, "reported_tokens": tokens, "plan": plan,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "summary.json"), summary, 0o600))
	t.Logf("qualification summary: %s", summary)
}
