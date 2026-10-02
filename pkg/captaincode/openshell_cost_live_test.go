package captaincode

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shieldBudgetRows reads a worker's Shield audit for the budget evidence:
// each forwarded request's reservation and the bill or reservation it settled
// at. It reads numbers only; the audit holds no content.
func shieldBudgetRows(t *testing.T, audit string) (reserved, settled []float64, refused int) {
	t.Helper()
	f, err := os.Open(audit)
	require.NoError(t, err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var row struct {
			Phase       string   `json:"phase"`
			ReservedUSD *float64 `json:"reserved_usd"`
			SettledUSD  *float64 `json:"settled_usd"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &row))
		switch {
		case row.ReservedUSD != nil:
			reserved = append(reserved, *row.ReservedUSD)
		case row.SettledUSD != nil:
			settled = append(settled, *row.SettledUSD)
		case row.Phase == "budget_refused":
			refused++
		}
	}
	require.NoError(t, scanner.Err())
	return reserved, settled, refused
}

// TestOpenShellStrictCapLiveQualification runs one public fixture worker in
// a VM sandbox through Cerebras twice: under a cap its requests fit, and under
// a cap below one request's worst case, which Shield must refuse before the
// provider is called. Opt in with CAPTAIN_TEST_OPENSHELL_COST_LIVE=1.
func TestOpenShellStrictCapLiveQualification(t *testing.T) {
	if os.Getenv("CAPTAIN_TEST_OPENSHELL_COST_LIVE") != "1" {
		t.Skip("set CAPTAIN_TEST_OPENSHELL_COST_LIVE=1 with a prepared VM runtime and provider key")
	}
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	require.NotEmpty(t, prepared)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("/tmp", "cc-cost-cap-")
	require.NoError(t, err)
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	t.Logf("isolated evidence: %s", root)
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.Mkdir(repo, 0o700))
	pilot, err := filepath.Abs("../../examples/openshell-pilot")
	require.NoError(t, err)
	for _, name := range []string{"roman.py", "test_roman.py"} {
		data, err := os.ReadFile(filepath.Join(pilot, "team", "repo", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), data, 0o600))
	}
	git := func(args ...string) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=romainPellerin", "-c", "user.email=rom@lemma.ventures",
		"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-qm", "Public strict cost cap fixture")
	repo, revision, err := ResolveOpenShellRepo(ctx, repo, "HEAD")
	require.NoError(t, err)
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	require.NoError(t, err)
	edit := OpenShellTask{ID: "roman", Profile: "cerebras", Prompt: "Fix roman.py to implement standard subtractive Roman numerals for integers 1 through 3999 and raise ValueError outside that range. Read and run test_roman.py. Change only roman.py.",
		Allowed: []string{"roman.py"}, Protected: []string{"test_roman.py"},
		Verify: []string{"python3", "-m", "unittest", "-v"}, Baseline: "fail", DeadlineSeconds: 180, VerifySeconds: 30}
	for _, key := range []string{"CAPTAIN_MAX_ATTEMPTS", "CAPTAIN_MAX_WALLTIME"} {
		t.Setenv(key, "")
	}
	t.Setenv("CAPTAIN_STRICT", "1")

	type outcome struct {
		CapUSD      float64                `json:"cap_usd"`
		Verdict     string                 `json:"verdict"`
		Seconds     float64                `json:"seconds"`
		Error       string                 `json:"error,omitempty"`
		CostBudget  *OpenShellCostBudget   `json:"cost_budget"`
		Shield      *OpenShellShield       `json:"shield"`
		Reserved    []float64              `json:"reserved_usd"`
		Settled     []float64              `json:"settled_usd"`
		Refused     int                    `json:"refused_requests"`
		Integrated  bool                   `json:"integrated_passed"`
		Attempts    *OpenShellAttemptUsage `json:"attempt_usage"`
		RunSHA256   string                 `json:"run_record_sha256"`
		PatchSHA256 string                 `json:"patch_sha256,omitempty"`
		provenance  *OpenShellProvenance
	}
	run := func(name string, capUSD float64) outcome {
		t.Setenv("CAPTAIN_MAX_COST", fmt.Sprint(capUSD))
		dir := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(dir, 0o700))
		runner := &OpenShellRunner{Pilot: pilot, Prepared: prepared, StateRoot: "/tmp", RunDir: dir,
			Repo: repo, Revision: revision, Runtime: "vm", Concurrency: 1, RequireAll: true, DirectorName: "none", Log: t.Logf}
		started := time.Now()
		record, err := runner.RunTeam(ctx, OpenShellTeam{Schema: 1, ID: name, Tasks: []OpenShellTask{edit}})
		require.NotNil(t, record, "%v", err)
		data, readErr := os.ReadFile(filepath.Join(dir, "run.json"))
		require.NoError(t, readErr)
		o := outcome{CapUSD: capUSD, Verdict: record.Verdict, Seconds: time.Since(started).Seconds(), CostBudget: record.CostBudget,
			Attempts: record.AttemptUsage, RunSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), provenance: record.Provenance}
		if err != nil {
			o.Error = err.Error()
		}
		require.Len(t, record.Tasks, 1)
		worker := record.Tasks[0]
		require.NotNil(t, worker.Report)
		o.Shield = worker.Report.Shield
		o.Reserved, o.Settled, o.Refused = shieldBudgetRows(t, filepath.Join(worker.Evidence, "shield-audit.jsonl"))
		o.Integrated = record.Integrated != nil && record.Integrated.Passed
		if record.Integrated != nil {
			o.PatchSHA256 = record.Integrated.PatchSHA256
		}
		return o
	}

	fits := run("fits", 0.10)
	require.Equal(t, "pass", fits.Verdict, fits.Error)
	require.True(t, fits.Integrated)
	require.Equal(t, &OpenShellCostBudget{LimitUSD: 0.10, WorkerUSD: 0.10, Workers: 1}, fits.CostBudget)
	require.NotNil(t, fits.Shield)
	require.NotNil(t, fits.Shield.Budget)
	b := fits.Shield.Budget
	require.Equal(t, 0.10, b.LimitUSD)
	require.False(t, b.Breached)
	require.LessOrEqual(t, b.CommittedUSD, b.LimitUSD)
	require.Equal(t, fits.Shield.Requests, len(fits.Reserved), "every forwarded request reserved first")
	require.Equal(t, fits.Shield.Requests, len(fits.Settled), "every forwarded request settled")
	require.Equal(t, fits.Shield.PricedResponses, fits.Shield.Requests)
	require.InDelta(t, fits.Shield.CostUSD, b.CommittedUSD, 1e-9, "all priced: committed is the provider's bill")
	for i := range fits.Settled {
		require.LessOrEqual(t, fits.Settled[i], fits.Reserved[i], "no bill exceeded its reservation")
	}

	refused := run("refused", 0.01)
	require.NotEqual(t, "pass", refused.Verdict)
	require.False(t, refused.Integrated)
	require.NotNil(t, refused.Shield)
	require.NotNil(t, refused.Shield.Budget)
	require.Positive(t, refused.Refused)
	require.Equal(t, refused.Refused, refused.Shield.Budget.Refused)
	require.Zero(t, refused.Shield.Requests, "no request reached the provider")
	require.Empty(t, refused.Reserved)
	require.Zero(t, refused.Shield.Budget.CommittedUSD)

	after, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, index, after, "the host index is untouched")
	report := map[string]any{
		"schema": 1, "verdict": "pass", "date": time.Now().UTC().Format(time.RFC3339),
		"fixture": "Public Roman numeral edit (examples/openshell-pilot/team/repo), one worker per run",
		"runtime": "vm", "profile": "cerebras", "model": "openai/gpt-oss-120b",
		"price_ceiling_per_million": map[string]float64{"prompt": 0.45, "completion": 0.95},
		"runs":                      map[string]outcome{"fits": fits, "refused": refused},
		"provenance":                fits.provenance, "host_index_unchanged": true,
		"limits": "One public fixture on one host and the locally patched VM driver. The refused run shows Shield refusing before the provider; it says nothing about how often real tasks fit a given cap.",
	}
	data, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "qualification.json"), append(data, '\n'), 0o600))
	t.Logf("qualification: %s", filepath.Join(root, "qualification.json"))
}
