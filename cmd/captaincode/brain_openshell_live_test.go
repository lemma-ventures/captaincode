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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinOpenShellLiveDocker resolves the Docker endpoint from the developer's
// own home. TestMain has already swapped HOME, so a plain lookup finds no
// context and returns /var/run/docker.sock, which Docker Desktop may not
// create; the VM driver then asks the registry for the local-only image.
func pinOpenShellLiveDocker(t *testing.T) {
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		u, err := user.Current()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		cmd.Env = append(os.Environ(), "HOME="+u.HomeDir)
		out, err := cmd.Output()
		require.NoError(t, err, "docker context inspect")
		endpoint = strings.TrimSpace(string(out))
	}
	info, err := os.Stat(strings.TrimPrefix(endpoint, "unix://"))
	require.True(t, strings.HasPrefix(endpoint, "unix://") && err == nil && info.Mode()&os.ModeSocket != 0,
		"the VM runtime needs a local Docker image store; no socket at %q, set DOCKER_HOST", endpoint)
	t.Setenv("DOCKER_HOST", endpoint)
}

func TestOpenShellHTTPLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_HTTP_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_HTTP_LIVE=1 with a prepared VM runtime and provider key")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	pinOpenShellLiveDocker(t)
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "cc-http-")
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	b := openShellHTTPBrain(t)
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(repo, 0o700))
	for key, value := range map[string]string{
		"HOME": home, "CAPTAIN_OPENSHELL_PREPARED": prepared, "CAPTAIN_OPENSHELL_PILOT": pilot,
		"CAPTAIN_OPENSHELL_REPO": "", "CAPTAIN_OPENSHELL_REVISION": "HEAD", "CAPTAIN_OPENSHELL_RUNTIME": "vm",
		"CAPTAIN_OPENSHELL_PROFILE": "cerebras", "CAPTAIN_OPENSHELL_ALLOWED": "roman.py",
		"CAPTAIN_OPENSHELL_VERIFY":    `["python3","-m","unittest","-v"]`,
		"CAPTAIN_OPENSHELL_PROTECTED": "test_roman.py,operator.txt", "CAPTAIN_OPENSHELL_BASELINE": "fail",
		"CAPTAIN_OPENSHELL_DIRECTOR": "none", "CAPTAIN_OPENSHELL_CONCURRENCY": "1",
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
	for _, name := range []string{"roman.py", "test_roman.py"} {
		data, err := os.ReadFile(filepath.Join(pilot, "team", "repo", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("baseline\n"), 0o600))
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "-c", "user.name=captain-test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public sandbox HTTP fixture")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("staged\n"), 0o600))
	git(repo, "add", "operator.txt")
	f, err := os.OpenFile(filepath.Join(repo, "roman.py"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("\n\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("local\n"), 0o600))
	before := map[string][]byte{}
	for _, name := range []string{"roman.py", "operator.txt", "untracked.txt", ".git/index"} {
		before[name], err = os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", b.chatCompletions)
	mux.HandleFunc("/v1/task", b.taskAPIHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(ctx context.Context, task string, stream bool) *http.Response {
		r := openShellHTTPRequest(t, repo, "openshell", task, stream).WithContext(ctx)
		r.RequestURI = ""
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
		resp, err := server.Client().Do(r)
		require.NoError(t, err)
		return resp
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	resp := call(ctx, "/openshell Fix roman.py so to_roman supports subtractive notation and rejects numbers outside 1..3999. Run python3 -m unittest -v. Only edit roman.py.", false)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), "exported (not applied)")
	wall := time.Since(started).Seconds()
	stored, err := captaincode.LoadLedger()
	require.NoError(t, err)
	attempt := stored.AttemptStateFor(resp.Header.Get("X-Captain-Attempt-ID"))
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.Export)
	data, err := os.ReadFile(attempt.Export.RunRecord)
	require.NoError(t, err)
	var run captaincode.OpenShellRun
	require.NoError(t, json.Unmarshal(data, &run))
	require.Equal(t, "pass", run.Verdict)
	require.NoError(t, os.WriteFile(filepath.Join(root, "completed-run.json"), data, 0o600))
	landing := filepath.Join(root, "landing")
	git(root, "clone", "--quiet", "--no-hardlinks", repo, landing)
	git(landing, "apply", "--index", attempt.Export.Manifest.DiffPath)
	assert.Equal(t, run.Integrated.Tree, git(landing, "write-tree"))
	for name, expected := range before {
		actual, err := os.ReadFile(filepath.Join(repo, name))
		require.NoError(t, err)
		assert.Equal(t, expected, actual, name)
	}
	require.Contains(t, captaincode.FormatHandoffBrief(*stored.HandoffFor(attempt.TaskID)), "exported (not applied)")
	cancelCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	cancelResp := call(cancelCtx, "/openshell Fix roman.py and run all tests; this request exercises client disconnect cleanup.", true)
	defer cancelResp.Body.Close()
	cancelAttempt := cancelResp.Header.Get("X-Captain-Attempt-ID")
	require.NotEmpty(t, cancelAttempt)
	var cancelLog string
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		logs, _ := filepath.Glob(filepath.Join(home, ".captaincode", "openshell", "*", "tasks", "*", "pilot.log"))
		for _, log := range logs {
			if strings.HasPrefix(log, filepath.Dir(attempt.Export.RunRecord)+string(filepath.Separator)) {
				continue
			}
			data, _ := os.ReadFile(log)
			if strings.Contains(string(data), "landlock: pass") {
				cancelLog = log
				break
			}
		}
		if cancelLog != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NotEmpty(t, cancelLog, "second sandbox never reached Landlock check")
	cancelStarted := time.Now()
	disconnect()
	cancelResp.Body.Close()
	for time.Since(cancelStarted) < 3*time.Minute {
		if b.inflightRuns.Load() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Zero(t, b.inflightRuns.Load(), "controller did not finish cleanup")
	stored, err = captaincode.LoadLedger()
	require.NoError(t, err)
	cancelled := stored.AttemptStateFor(cancelAttempt)
	require.NotNil(t, cancelled)
	assert.Equal(t, captaincode.StateCancelled, cancelled.State)
	assert.Nil(t, cancelled.Export)
	assert.Empty(t, b.cancelTree.TaskIDs())
	summary, err := json.MarshalIndent(map[string]any{
		"wall_seconds": wall, "run_record": attempt.Export.RunRecord, "host_preservation": !t.Failed(),
		"cancel_cleanup_seconds": time.Since(cancelStarted).Seconds(), "cancel_log": cancelLog,
		"cancelled_state": cancelled.State, "cancelled_export": cancelled.Export != nil,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "qualification.json"), summary, 0o600))
	t.Logf("completed in %.3fs; disconnect cleanup %.3fs; evidence %s", wall, time.Since(cancelStarted).Seconds(), root)
}
