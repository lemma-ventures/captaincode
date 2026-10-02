package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/require"
)

func TestOpenShellHTTPParallelLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_PARALLEL_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_PARALLEL_LIVE=1 with prepared VM runtime and provider key")
	}
	testOpenShellHTTPStagesLiveQualification(t, false, false)
}

func TestOpenShellHTTPSequentialLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_SEQUENTIAL_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_SEQUENTIAL_LIVE=1 with prepared VM runtime and provider key")
	}
	testOpenShellHTTPStagesLiveQualification(t, true, false)
}

func TestOpenShellHTTPReviewLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_REVIEW_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_REVIEW_LIVE=1 with prepared VM runtime and provider key")
	}
	testOpenShellHTTPStagesLiveQualification(t, true, true)
}

func testOpenShellHTTPStagesLiveQualification(t *testing.T, sequential, review bool) {
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	pinOpenShellLiveDocker(t)
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	prefix := "cc-http-parallel-"
	if sequential {
		prefix = "cc-http-sequential-"
	}
	if review {
		prefix = "cc-http-review-"
	}
	root, err := os.MkdirTemp("/tmp", prefix)
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	b := openShellHTTPBrain(t)
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(repo, 0o700))
	for key, value := range map[string]string{
		"HOME": home, "CAPTAIN_OPENSHELL_PREPARED": prepared, "CAPTAIN_OPENSHELL_PILOT": pilot,
		"CAPTAIN_OPENSHELL_REPO": "", "CAPTAIN_OPENSHELL_REVISION": "HEAD", "CAPTAIN_OPENSHELL_RUNTIME": "vm",
		"CAPTAIN_OPENSHELL_PROFILE": "cerebras", "CAPTAIN_OPENSHELL_ALLOWED": "factorial.py,fibonacci.py",
		"CAPTAIN_OPENSHELL_VERIFY":    `["python3","-m","unittest","-v"]`,
		"CAPTAIN_OPENSHELL_PROTECTED": "test_numbers.py,operator.txt", "CAPTAIN_OPENSHELL_BASELINE": "pass",
		"CAPTAIN_OPENSHELL_DIRECTOR": "none", "CAPTAIN_OPENSHELL_CONCURRENCY": "2",
	} {
		t.Setenv(key, value)
	}
	b.ledger, err = captaincode.LoadLedger()
	require.NoError(t, err)
	git := func(dir string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
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
		"operator.txt": "baseline\n",
	}
	for name, data := range files {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(data), 0o600))
	}
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "-c", "user.name=romainPellerin", "-c", "user.email=rom@lemma.ventures", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public parallel sandbox fixture")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("staged\n"), 0o600))
	git(repo, "add", "operator.txt")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "factorial.py"), []byte(files["factorial.py"]+"\n\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("local\n"), 0o600))
	before := map[string][]byte{}
	for _, name := range []string{"factorial.py", "fibonacci.py", "test_numbers.py", "operator.txt", "untracked.txt", ".git/index"} {
		before[name], err = os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
	}
	server := httptest.NewServer(http.HandlerFunc(b.chatCompletions))
	defer server.Close()
	task := "/openshell Refactor factorial.py to compute factorial iteratively without recursion, keeping its validation and public API. Only edit factorial.py. Run python3 -m unittest -v. + /openshell Refactor fibonacci.py to compute fibonacci in linear time with an iterative loop, keeping its validation and public API. Only edit fibonacci.py. Run python3 -m unittest -v."
	if sequential {
		task = strings.Replace(task, " + /openshell", " > /openshell", 1)
	}
	changedFiles := []string{"factorial.py", "fibonacci.py"}
	if review {
		task = strings.Split(task, " > /openshell")[0] + " > /openshell --review Inspect factorial.py and test_numbers.py. Confirm factorial is iterative, preserves validation, and passes python3 -m unittest -v. Report findings without editing any file."
		changedFiles = []string{"factorial.py"}
	}
	call := func(ctx context.Context, stream bool) *http.Response {
		r := openShellHTTPRequest(t, repo, "auto", task, stream).WithContext(ctx)
		r.RequestURI = ""
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
		resp, err := server.Client().Do(r)
		require.NoError(t, err)
		return resp
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	resp := call(ctx, false)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), "2/2 task(s) passed")
	wall := time.Since(started).Seconds()
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	attempt := stored.AttemptStateFor(resp.Header.Get("X-Captain-Attempt-ID"))
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.Export)
	require.Equal(t, changedFiles, attempt.Export.Manifest.ChangedFiles)
	data, err := os.ReadFile(attempt.Export.RunRecord)
	require.NoError(t, err)
	var run captaincode.OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	require.Equal(t, "pass", run.Verdict)
	require.True(t, run.RequireAll)
	require.Len(t, run.Tasks, 2)
	tokens, cost, complete := run.Spend()
	require.Positive(t, tokens)
	require.Positive(t, cost)
	var charged []captaincode.Usage
	for _, c := range stored.ChargesFor(attempt.TaskID) {
		if c.Kind == captaincode.KindCall && c.Parent == attempt.AttemptID {
			charged = append(charged, c.Usage)
		}
	}
	require.Len(t, charged, 1)
	if complete {
		require.Equal(t, captaincode.UsageMeasured, charged[0].CostStatus)
		require.InDelta(t, cost, charged[0].CostUSD, 1e-9)
		require.Equal(t, tokens, charged[0].Total)
	} else {
		require.Equal(t, captaincode.UsageUnknown, charged[0].CostStatus)
		t.Log("some Shield requests have no priced response; ledger correctly reports unknown cost")
	}
	for _, worker := range run.Tasks {
		require.NotNil(t, worker.Report)
		require.Len(t, worker.Report.Checks, 17)
		for name, check := range worker.Report.Checks {
			require.Equal(t, "pass", check.Verdict, name)
		}
		if !sequential {
			require.Equal(t, run.Revision, worker.Manifest.BaseRevision)
		}
	}
	if sequential {
		require.Len(t, run.Stages, 2)
		require.Equal(t, run.Revision, run.Stages[0].Revision)
		require.Equal(t, run.Stages[0].NextRevision, run.Stages[1].Revision)
		require.Equal(t, run.Stages[1].Revision, run.Tasks[1].Manifest.BaseRevision)
		snapshot := filepath.Join(filepath.Dir(attempt.Export.RunRecord), "snapshot")
		require.Equal(t, run.Stages[0].Tree, git(snapshot, "rev-parse", run.Stages[1].Revision+"^{tree}"))
		require.Equal(t, run.Stages[1].Tree, run.Integrated.Tree)
		if review {
			require.Equal(t, run.Stages[0].Tree, run.Stages[1].Tree)
			require.Equal(t, "review", run.Tasks[1].Mode)
			require.Equal(t, captaincode.OpenShellUnchanged, run.Tasks[1].Outcome)
			require.Empty(t, run.Tasks[1].Manifest.ChangedFiles)
			require.NotEmpty(t, run.Tasks[1].Report.Export)
			require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", run.Tasks[1].Report.Export.PatchSHA256)
		}
		for _, stage := range run.Stages {
			stageData, err := os.ReadFile(stage.RunRecord)
			require.NoError(t, err)
			var stageRun captaincode.OpenShellRun
			require.NoError(t, json.Unmarshal(stageData, &stageRun))
			require.Len(t, stageRun.Integrated.Report.Checks, 8)
			for name, check := range stageRun.Integrated.Report.Checks {
				require.Equal(t, "pass", check.Verdict, name)
			}
		}
	}
	require.Len(t, run.Integrated.Report.Checks, 8)
	landing := filepath.Join(root, "landing")
	git(root, "clone", "--quiet", "--no-hardlinks", repo, landing)
	git(landing, "apply", "--index", attempt.Export.Manifest.DiffPath)
	require.Equal(t, run.Integrated.Tree, git(landing, "write-tree"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "completed-run.json"), data, 0o600))
	cancelCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	cancelResp := call(cancelCtx, true)
	defer cancelResp.Body.Close()
	cancelAttempt := cancelResp.Header.Get("X-Captain-Attempt-ID")
	require.NotEmpty(t, cancelAttempt)
	var logs []string
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		logs = nil
		pattern := filepath.Join(home, ".captaincode", "openshell", "*", "tasks", "*", "pilot.log")
		if sequential {
			pattern = filepath.Join(home, ".captaincode", "openshell", "*", "stage-*", "tasks", "*", "pilot.log")
		}
		paths, _ := filepath.Glob(pattern)
		for _, path := range paths {
			if strings.HasPrefix(path, filepath.Dir(attempt.Export.RunRecord)+string(filepath.Separator)) {
				continue
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "landlock: pass") {
				logs = append(logs, path)
			}
		}
		if len(logs) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Len(t, logs, 2, "both sandboxes must reach enforcement before disconnect")
	cancelStarted := time.Now()
	disconnect()
	cancelResp.Body.Close()
	for time.Since(cancelStarted) < 3*time.Minute && b.inflightRuns.Load() != 0 {
		time.Sleep(100 * time.Millisecond)
	}
	require.Zero(t, b.inflightRuns.Load(), "controllers did not finish cleanup")
	stored, err = captaincode.LoadLedger()
	require.NoError(t, err)
	cancelled := stored.AttemptStateFor(cancelAttempt)
	require.NotNil(t, cancelled)
	require.Equal(t, captaincode.StateCancelled, cancelled.State)
	require.Nil(t, cancelled.Export)
	cancelRunPath := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(logs[0]))), "run.json")
	if sequential {
		cancelRunPath = filepath.Join(filepath.Dir(filepath.Dir(cancelRunPath)), "run.json")
	}
	cancelRunData, err := os.ReadFile(cancelRunPath)
	require.NoError(t, err)
	var cancelledRun captaincode.OpenShellRun
	require.NoError(t, json.Unmarshal(cancelRunData, &cancelledRun))
	require.Len(t, cancelledRun.Tasks, 2)
	require.Nil(t, cancelledRun.Integrated)
	for _, worker := range cancelledRun.Tasks {
		if sequential && worker.Outcome != captaincode.OpenShellFailed {
			require.Equal(t, "s1-w1-openshell", worker.Task)
			continue
		}
		cleanup, err := os.ReadFile(filepath.Join(worker.State, "cleanup.log"))
		require.NoError(t, err)
		require.Contains(t, string(cleanup), "Stopped sandbox")
	}
	for name, expected := range before {
		actual, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
		require.Equal(t, expected, actual, name)
	}
	summary, err := json.MarshalIndent(map[string]any{
		"wall_seconds": wall, "run_record": attempt.Export.RunRecord, "host_preservation": true,
		"cancel_cleanup_seconds": time.Since(cancelStarted).Seconds(), "cancel_logs": logs,
		"sequential": sequential, "review": review, "cancelled_state": cancelled.State, "cancelled_export": cancelled.Export != nil,
		"spend_complete": complete, "reported_cost_usd": cost, "reported_tokens": tokens,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "summary.json"), summary, 0o600))
	t.Logf("qualification summary: %s", summary)
}
