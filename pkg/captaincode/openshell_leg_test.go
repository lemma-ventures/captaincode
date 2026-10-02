package captaincode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openShellLegEnv(t *testing.T) *OpenShellRunner {
	t.Helper()
	r := newFakeOpenShell(t)
	for _, key := range []string{"PREPARED", "PILOT", "REPO", "REVISION", "RUNTIME", "CONCURRENCY", "DIRECTOR", "PROFILE", "ALLOWED", "PROTECTED", "VERIFY", "BASELINE"} {
		t.Setenv("CAPTAIN_OPENSHELL_"+key, "")
	}
	t.Setenv("CAPTAIN_OPENSHELL_PREPARED", r.Prepared)
	t.Setenv("CAPTAIN_OPENSHELL_PILOT", r.Pilot)
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "a.txt")
	t.Setenv("CAPTAIN_OPENSHELL_VERIFY", `["check", "a,b", "argument with spaces"]`)
	return r
}

func TestOpenShellLegPinsSnapshotAndPreservesArguments(t *testing.T) {
	r := openShellLegEnv(t)
	require.NoError(t, os.Mkdir(filepath.Join(r.Repo, "nested"), 0o755))
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(r.Repo, alias))
	t.Setenv("CAPTAIN_OPENSHELL_PROTECTED", " b.txt, c.txt ")
	runner, team, err := openShellConfig(context.Background(), filepath.Join(alias, "nested"), `{"write":{"a.txt":"updated\n"}}`)
	require.NoError(t, err)
	assert.Equal(t, r.Repo, runner.Repo)
	assert.Equal(t, r.Revision, runner.Revision)
	assert.Equal(t, []string{"check", "a,b", "argument with spaces"}, team.Tasks[0].Verify)
	assert.Equal(t, []string{"b.txt", "c.txt"}, team.Tasks[0].Protected)
	assert.Equal(t, "any", team.Tasks[0].Baseline)
	require.NoError(t, team.Validate())
	require.NoError(t, exec.Command("git", "-C", r.Repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "advance HEAD").Run())
	assert.Equal(t, r.Revision, runner.Revision)
}

func TestOpenShellLegRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, key, value, want string }{
		{"missing verification", "VERIFY", "", "CAPTAIN_OPENSHELL_VERIFY"},
		{"empty verification", "VERIFY", "[]", "verify"},
		{"null verification", "VERIFY", "null", "verify"},
		{"shell string", "VERIFY", "go test ./...", "CAPTAIN_OPENSHELL_VERIFY"},
		{"trailing data", "VERIFY", `["check"] []`, "CAPTAIN_OPENSHELL_VERIFY"},
		{"empty argument", "VERIFY", `["check", ""]`, "verify"},
		{"missing scope", "ALLOWED", "", "CAPTAIN_OPENSHELL_ALLOWED"},
		{"repository-chosen pilot", "PILOT", "", "CAPTAIN_OPENSHELL_PILOT"},
		{"escaping scope", "ALLOWED", "../secret", "inside the repository"},
		{"git scope", "ALLOWED", ".git/config", "inside the repository"},
		{"overlapping scope", "PROTECTED", "a.txt", "disjoint"},
		{"bad revision", "REVISION", "--all", "revision"},
		{"unknown revision", "REVISION", "missing-ref", "commit"},
		{"bad runtime", "RUNTIME", "host", "runtime"},
		{"bad concurrency", "CONCURRENCY", "invalid", "CONCURRENCY"},
		{"zero concurrency", "CONCURRENCY", "0", "CONCURRENCY"},
		{"high concurrency", "CONCURRENCY", "9", "CONCURRENCY"},
		{"bad director", "DIRECTOR", "missing", "DIRECTOR"},
		{"bad baseline", "BASELINE", "skip", "baseline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellLegEnv(t)
			t.Setenv("CAPTAIN_OPENSHELL_"+tc.key, tc.value)
			_, _, err := openShellConfig(context.Background(), r.Repo, "fix a.txt")
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, openShellStates(t, r))
		})
	}
}

func TestOpenShellLegReturnsOnlyVerifiedExports(t *testing.T) {
	for _, tc := range []struct{ name, prompt, want string }{
		{"pass", `{"write":{"a.txt":"updated\n"}}`, ""},
		{"worker failure", `{"fail":true}`, "no task produced a change"},
		{"integrated failure", `{"write":{"a.txt":"BREAK\n"}}`, "baseline exited 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openShellLegEnv(t)
			require.NoError(t, os.WriteFile(filepath.Join(r.Repo, "a.txt"), []byte("user's staged edit\n"), 0o644))
			require.NoError(t, exec.Command("git", "-C", r.Repo, "add", "a.txt").Run())
			indexBefore, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
			require.NoError(t, err)
			runner, team, err := openShellConfig(context.Background(), r.Repo, tc.prompt)
			require.NoError(t, err)
			runner.RunDir, runner.StateRoot = r.RunDir, r.StateRoot
			res, err := runOpenShellTeam(context.Background(), runner, team, nil)
			if tc.want == "" {
				require.NoError(t, err)
				require.NotNil(t, res.Export)
				assert.Equal(t, r.Repo, res.Export.Repository)
				assert.Equal(t, r.Revision, res.Export.Manifest.BaseRevision)
				assert.Equal(t, []string{"a.txt"}, res.Export.Manifest.ChangedFiles)
				assert.Equal(t, filepath.Join(r.RunDir, "integrated.patch"), res.Export.Manifest.DiffPath)
				assert.Equal(t, filepath.Join(r.RunDir, "run.json"), res.Export.RunRecord)
				assert.Equal(t, "vm", res.Export.Runtime)
				require.NotNil(t, res.Export.Manifest.Check)
				assert.Equal(t, team.Tasks[0].Verify, res.Export.Manifest.Check.Command)
				assert.True(t, res.Export.Manifest.Check.Passed)
				assert.Empty(t, res.Export.Manifest.WorktreeDir)
				assert.Contains(t, res.Text, "apply with:")
				patch, err := os.ReadFile(filepath.Join(r.RunDir, "integrated.patch"))
				require.NoError(t, err)
				assert.Contains(t, string(patch), "+updated")
				assert.NotContains(t, string(patch), "user's staged edit")
			} else {
				require.ErrorContains(t, err, tc.want)
				assert.Nil(t, res.Export)
				assert.NotContains(t, res.Text, "apply with:")
			}
			assert.Contains(t, res.Text, "snapshot: "+r.Revision)
			assert.Contains(t, res.Text, filepath.Join(r.RunDir, "run.json"))
			var run OpenShellRun
			data, err := os.ReadFile(filepath.Join(r.RunDir, "run.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &run))
			if run.Tasks[0].Report.Export != nil {
				assert.Equal(t, []string{"check", "a,b", "argument with spaces"}, run.Tasks[0].Report.Export.Verify.Argv)
			}
			indexAfter, err := os.ReadFile(filepath.Join(r.Repo, ".git", "index"))
			require.NoError(t, err)
			assert.Equal(t, indexBefore, indexAfter)
			file, err := os.ReadFile(filepath.Join(r.Repo, "a.txt"))
			require.NoError(t, err)
			assert.Equal(t, "user's staged edit\n", string(file))
		})
	}
}

func TestOpenShellLegRequiresVerificationBeforeStarting(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("CAPTAIN_OPENSHELL_VERIFY", "")
	_, err := runOpenShell(r.Repo, "fix a.txt", time.Minute, time.Minute, nil)
	require.ErrorContains(t, err, "CAPTAIN_OPENSHELL_VERIFY")
}

func TestOpenShellLegDispatchRequiresVerification(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("CAPTAIN_OPENSHELL_VERIFY", "")
	d := NewDispatcher(1)
	d.Dir = r.Repo
	_, err := d.Run(LegOpenShell, "fix a.txt")
	require.ErrorContains(t, err, "CAPTAIN_OPENSHELL_VERIFY")
	ws := Workspace{Dir: r.Repo}
	_, err = ws.RunWorkerStreamHooks(LegOpenShell, "fix a.txt", 1, nil, nil)
	require.ErrorContains(t, err, "CAPTAIN_OPENSHELL_VERIFY")
}

func TestOpenShellLegInterruptWaitsForCleanup(t *testing.T) {
	r := openShellLegEnv(t)
	runner, team, err := openShellConfig(context.Background(), r.Repo, `{"hang":true}`)
	require.NoError(t, err)
	runner.RunDir, runner.StateRoot = r.RunDir, r.StateRoot
	steer := NewSteer(r.Repo)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := runOpenShellTeam(ctx, runner, team, steer)
		done <- outcome{res, err}
	}()
	require.Eventually(t, func() bool {
		started, _ := filepath.Glob(filepath.Join(r.StateRoot, "cc-os-*", "started"))
		return len(started) == 1
	}, 5*time.Second, 10*time.Millisecond)
	asked, stopped := steer.Interrupt("stop the sandbox")
	assert.Empty(t, asked)
	assert.Equal(t, []Leg{LegOpenShell}, stopped)
	select {
	case out := <-done:
		require.ErrorIs(t, out.err, ErrInterrupted)
		assert.Nil(t, out.res.Export)
		assert.True(t, out.res.Partial)
		assert.NotContains(t, out.res.Text, "apply with:")
	case <-ctx.Done():
		t.Fatal("sandbox controller did not finish cleanup")
	}
	states := openShellStates(t, r)
	require.Len(t, states, 1)
	assert.FileExists(t, filepath.Join(states[0], "cleaned"))
	_, stopped = steer.Interrupt("already stopped")
	assert.Empty(t, stopped)
}

func TestOpenShellExportRejectsChangedEvidence(t *testing.T) {
	r := newFakeOpenShell(t)
	team := OpenShellTeam{Schema: 1, ID: "export", Tasks: []OpenShellTask{
		fakeOpenShellTask("edit", "nim", `{"write":{"a.txt":"updated\n"}}`, "a", "a.txt"),
	}}
	run, err := r.RunTeam(context.Background(), team)
	require.NoError(t, err)
	require.Equal(t, "pass", run.Verdict)
	original := *run.Integrated
	patch, err := os.ReadFile(original.Patch)
	require.NoError(t, err)
	for _, fault := range []string{"digest", "files", "snapshot", "tree", "failed", "missing", "symlink", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			copyRun := *run
			copyIntegrated := original
			copyRun.Integrated = &copyIntegrated
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch fault {
			case "digest":
				require.NoError(t, os.WriteFile(original.Patch, append(patch, '\n'), 0o600))
				t.Cleanup(func() { require.NoError(t, os.WriteFile(original.Patch, patch, 0o600)) })
			case "files":
				copyIntegrated.ChangedFiles = []string{"b.txt"}
			case "snapshot":
				copyRun.Revision = "unverified"
			case "tree":
				copyIntegrated.Tree = strings.Repeat("0", 40)
			case "failed":
				copyIntegrated.Passed = false
			case "missing":
				copyRun.Integrated = nil
			case "symlink":
				target := filepath.Join(t.TempDir(), "patch")
				require.NoError(t, os.WriteFile(target, patch, 0o600))
				require.NoError(t, os.Remove(original.Patch))
				require.NoError(t, os.Symlink(target, original.Patch))
				t.Cleanup(func() {
					require.NoError(t, os.Remove(original.Patch))
					require.NoError(t, os.WriteFile(original.Patch, patch, 0o600))
				})
			case "cancelled":
				cancel()
			}
			export, err := r.verifiedExport(ctx, &copyRun)
			require.Error(t, err)
			assert.Nil(t, export)
		})
	}
}

func TestOpenShellInterruptAfterVerificationWithholdsExport(t *testing.T) {
	r := openShellLegEnv(t)
	runner, team, err := openShellConfig(context.Background(), r.Repo, `{"write":{"a.txt":"updated\n"}}`)
	require.NoError(t, err)
	runner.RunDir, runner.StateRoot = r.RunDir, r.StateRoot
	steer := NewSteer("test export")
	runner.Log = func(format string, args ...any) {
		if strings.HasPrefix(format, "integrated: verified") {
			steer.Interrupt("stop before export")
		}
	}
	res, err := runOpenShellTeam(context.Background(), runner, team, steer)
	require.ErrorIs(t, err, ErrInterrupted)
	assert.Nil(t, res.Export)
	assert.NotContains(t, res.Text, "apply with:")
}
