package captaincode

// OpenShell team runs (captain openshell): Captain tasks executed inside
// NVIDIA OpenShell microVM sandboxes by the task-mode pilot in
// examples/openshell-pilot/task.py - one task, one sandbox, one verified
// export. The pilot owns the boundary: Landlock, network and inference
// policy, Shield masking and provider pinning, cancellation, restart
// recovery. This file is Captain's half. It pins the revision every sandbox
// snapshots, re-checks each exported patch on the host without executing any
// of it, lands the patches through the manifest → integration candidate →
// ruling path parallel workers use, and has the integrated tree verified in a
// fresh sandbox that no model touched. Nothing a worker wrote runs on the
// host, and the user's working tree is never written: the result is a patch.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	openShellTeamLimit   = 256 << 10
	openShellReportLimit = 1 << 20
	openShellPatchLimit  = 1 << 20 // task.py PATCH_LIMIT: the pilot refuses larger exports
	openShellAnswerLimit = 8 << 10
	openShellFileLimit   = 8 << 20 // per evidence file and per pilot log
	openShellMaxTasks    = 64
	openShellArgLimit    = 1024 // task.py: every verify argument is 1-1024 characters
	openShellVerifyCap   = 900  // task.py: verify_seconds is at most 900
	// openShellWaitDelay is how long a cancelled pilot gets to delete its
	// sandbox and stop its gateway. The pilot gives its recovery process 120s
	// to do the same, so this must be longer or the recovery is orphaned.
	openShellWaitDelay = 3 * time.Minute
)

// Outcomes of one task in a team run.
const (
	OpenShellLanded    = "landed"    // its patch is in the integrated result
	OpenShellUnchanged = "unchanged" // passed without changing a file
	OpenShellDropped   = "dropped"   // passed, but a ruling kept another task's overlapping change
	OpenShellFailed    = "failed"    // the pilot or the host-side checks rejected it
)

var (
	openShellID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) // task.py TASK_ID
	openShellProfile = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)            // task.py's --profile choices decide
	openShellBareArg = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)
	openShellObject  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// openShellTaskChecks is task.py's TASK_CHECKS, and a verify-mode run
// performs the first eight; TestOpenShellChecksMatchTheTaskPilot keeps the
// lists identical. Captain lands nothing unless every one of them passed.
var openShellTaskChecks = []string{"landlock", "worker_tools", "baseline", "filesystem_denied", "network_denied",
	"provider_path_denied", "cancellation_requested", "cancellation_descendants", "worker_exit",
	"protected_unchanged", "sandbox_verify", "shield_mediated", "shield_unavailable_denied",
	"checkpoint_reloaded", "restart_recovery", "diff_scope", "export_ready"}

var openShellVerifyChecks = openShellTaskChecks[:8]

// openShellForbidden are the patch lines of anything but a text edit to an
// existing regular file; task.py refuses the same set before export.
var openShellForbidden = []string{"old mode ", "new mode ", "new file mode ", "deleted file mode ",
	"GIT binary patch", "Binary files ", "rename from ", "rename to ", "copy from ", "copy to ", "similarity index "}

// openShellEnvKeys is everything a pilot inherits from Captain's
// environment. The pilot hands the profile's key to the gateway's credential
// store and passes none of these into the sandbox.
var openShellEnvKeys = []string{"PATH", "HOME", "TMPDIR", "USER", "LOGNAME", "LANG", "LC_ALL", "SSL_CERT_FILE",
	"DOCKER_HOST", "DOCKER_CONTEXT", "NVIDIA_API_KEY", "OPENROUTER_API_KEY"}

// OpenShellTeam is a batch of sandboxed tasks against one pinned revision.
type OpenShellTeam struct {
	Schema int             `json:"schema"`
	ID     string          `json:"id"`
	Tasks  []OpenShellTask `json:"tasks"`
	// Verify replaces the integrated check, which by default runs every
	// landed task's own verify command against the integrated tree.
	Verify []string `json:"verify,omitempty"`
}

// OpenShellTask is one sandboxed edit: a task.py spec without the fields
// Captain pins itself (repo, revision). Zero values take task.py's
// defaults.
type OpenShellTask struct {
	ID              string   `json:"id"`
	Mode            string   `json:"mode,omitempty"`
	Profile         string   `json:"profile"`
	Prompt          string   `json:"prompt"`
	Verify          []string `json:"verify"`
	Allowed         []string `json:"allowed"`
	Protected       []string `json:"protected,omitempty"`
	Baseline        string   `json:"baseline,omitempty"`
	RepairAttempts  int      `json:"repair_attempts,omitempty"`
	DeadlineSeconds int      `json:"deadline_seconds,omitempty"`
	VerifySeconds   int      `json:"verify_seconds,omitempty"`
}

// LoadOpenShellTeam reads and validates a team spec. Unknown fields are
// errors: a misspelt "protected" must not silently protect nothing.
func LoadOpenShellTeam(file string) (OpenShellTeam, error) {
	var team OpenShellTeam
	data, err := readOpenShellFile(file, openShellTeamLimit)
	if err != nil {
		return team, fmt.Errorf("openshell team: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&team); err != nil {
		return team, fmt.Errorf("openshell team: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return team, errors.New("openshell team: trailing data after the spec")
	}
	return team, team.Validate()
}

// Validate applies task.py's rules on the host, so a bad spec fails before
// any sandbox starts.
func (team OpenShellTeam) Validate() error {
	if team.Schema != 1 {
		return errors.New("openshell team: schema must be 1")
	}
	if !openShellID.MatchString(team.ID) {
		return fmt.Errorf("openshell team: id %q must be 1-64 letters, digits, dots, dashes or underscores", team.ID)
	}
	if len(team.Tasks) == 0 || len(team.Tasks) > openShellMaxTasks {
		return fmt.Errorf("openshell team: needs 1-%d tasks", openShellMaxTasks)
	}
	if team.Verify != nil {
		if err := checkOpenShellArgv(team.Verify); err != nil {
			return fmt.Errorf("openshell team: verify: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, t := range team.Tasks {
		if err := t.Validate(); err != nil {
			return err
		}
		if seen[t.ID] {
			return fmt.Errorf("openshell team: task id %s is used twice", t.ID)
		}
		seen[t.ID] = true
	}
	return nil
}

// Validate checks one task against task.py's validate_task.
func (t OpenShellTask) Validate() error {
	if !openShellID.MatchString(t.ID) {
		return fmt.Errorf("openshell task %q: id must be 1-64 letters, digits, dots, dashes or underscores", t.ID)
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("openshell task %s: %s", t.ID, fmt.Sprintf(format, args...))
	}
	switch t.Mode {
	case "", "edit":
	case "review":
		if len(t.Allowed) != 0 || (t.Baseline != "" && t.Baseline != "pass") || t.RepairAttempts != 0 {
			return fail("review needs no edit scope, a passing baseline and no repair attempts")
		}
	default:
		return fail("mode must be edit or review")
	}
	if !openShellProfile.MatchString(t.Profile) {
		return fail("profile %q is not a profile name", t.Profile)
	}
	if n := utf8.RuneCountInString(t.Prompt); n == 0 || n > 16384 || strings.ContainsRune(t.Prompt, 0) {
		return fail("prompt must be 1-16384 characters")
	}
	if err := checkOpenShellArgv(t.Verify); err != nil {
		return fail("verify: %v", err)
	}
	if (t.Mode != "review" && len(t.Allowed) == 0) || len(t.Allowed) > 64 || len(t.Protected) > 256 {
		return fail("allowed needs 1-64 paths and protected at most 256")
	}
	seen := map[string]bool{}
	for _, p := range slices.Concat(t.Allowed, t.Protected) {
		if err := checkOpenShellPath(p); err != nil {
			return fail("%v", err)
		}
		if seen[p] {
			return fail("allowed and protected paths must be unique and disjoint")
		}
		seen[p] = true
	}
	switch t.Baseline {
	case "", "fail", "pass", "any":
	default:
		return fail("baseline must be fail, pass or any")
	}
	if t.RepairAttempts != 0 && t.RepairAttempts != 1 {
		return fail("repair_attempts must be 0 or 1")
	}
	if t.DeadlineSeconds != 0 && (t.DeadlineSeconds < 60 || t.DeadlineSeconds > 3600) {
		return fail("deadline_seconds must be 60-3600")
	}
	if t.VerifySeconds != 0 && (t.VerifySeconds < 10 || t.VerifySeconds > openShellVerifyCap) {
		return fail("verify_seconds must be 10-%d", openShellVerifyCap)
	}
	return nil
}

func checkOpenShellArgv(argv []string) error {
	if len(argv) == 0 || len(argv) > 32 {
		return errors.New("must be an argv list of 1-32 arguments")
	}
	for _, arg := range argv {
		if n := utf8.RuneCountInString(arg); n == 0 || n > openShellArgLimit || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("every argument must be 1-%d characters", openShellArgLimit)
		}
	}
	return nil
}

// checkOpenShellPath mirrors task.py's relative_path: a short, normalized,
// relative POSIX path that stays in the repository and out of .git.
func checkOpenShellPath(p string) error {
	if n := utf8.RuneCountInString(p); n == 0 || n > 255 || strings.Contains(p, `\`) {
		return fmt.Errorf("path %q must be a short relative POSIX path", p)
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return fmt.Errorf("path %q holds a control character", p)
		}
	}
	if strings.HasPrefix(p, "/") || p == "." || path.Clean(p) != p {
		return fmt.Errorf("path %q must be a normalized path inside the repository", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("path %q must be a normalized path inside the repository", p)
		}
	}
	return nil
}

// OpenShellReport is the part of a pilot's report.json Captain reads.
type OpenShellReport struct {
	Verdict          string                    `json:"verdict"`
	Mode             string                    `json:"mode"`
	TaskID           string                    `json:"task_id"`
	Inference        string                    `json:"inference,omitempty"`
	Model            string                    `json:"model,omitempty"`
	WorkerAttempts   *int                      `json:"worker_attempts"`
	TaskSuccesses    int                       `json:"task_successes"`
	BaselineExitCode *int                      `json:"baseline_exit_code,omitempty"`
	Timings          map[string]float64        `json:"timings_seconds,omitempty"`
	Checks           map[string]OpenShellCheck `json:"checks"`
	Shield           *OpenShellShield          `json:"shield,omitempty"`
	Export           *OpenShellExport          `json:"export,omitempty"`
	Error            string                    `json:"error,omitempty"`
	Cleanup          string                    `json:"cleanup,omitempty"`
}

// OpenShellCheck is one pilot check.
type OpenShellCheck struct {
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
}

// OpenShellShield summarizes what crossed the Shield middleware. The token
// and cost fields are what the provider itself reported on each response
// (OpenRouter's usage block), not the worker's account of its own calls.
type OpenShellShield struct {
	Requests         int      `json:"requests"`
	Responses        int      `json:"responses"`
	Blocked          int      `json:"blocked"`
	SecretsMasked    int      `json:"secrets_masked"`
	IdentitiesMasked int      `json:"identities_masked"`
	ServedBy         []string `json:"served_by,omitempty"`
	PricedResponses  int      `json:"priced_responses,omitempty"`
	PromptTokens     int      `json:"prompt_tokens,omitempty"`
	CompletionTokens int      `json:"completion_tokens,omitempty"`
	ReasoningTokens  int      `json:"reasoning_tokens,omitempty"` // a subset of CompletionTokens
	CostUSD          float64  `json:"cost_usd,omitempty"`
	// Budget is present under a strict cap: what this worker's Shield
	// committed against its share, refused requests and any breached bound.
	Budget *OpenShellShieldBudget `json:"budget,omitempty"`
}

// OpenShellShieldBudget is one Shield's strict allocation. Committed counts
// each forwarded request at its bill once a priced response settled it, and
// at its whole worst-case reservation otherwise, so it never understates.
type OpenShellShieldBudget struct {
	LimitUSD     float64 `json:"limit_usd"`
	CommittedUSD float64 `json:"committed_usd"`
	Refused      int     `json:"refused"`
	Breached     bool    `json:"breached"`
}

// Spend totals what the providers billed every worker in the run. It is
// complete only when each request that crossed Shield came back with a
// price; a blocked, failed or unpriced call, or a worker that stopped before
// Shield was tallied, leaves the bill unknown rather than understated.
// Stage runs that recovery set aside count too.
func (run *OpenShellRun) Spend() (tokens int, costUSD float64, complete bool) {
	requests, tokens, costUSD, complete := openShellSpend(run.Tasks)
	for _, s := range run.SetAside {
		requests += s.Requests
		tokens += s.Tokens
		costUSD += s.CostUSD
		complete = complete && s.Priced
	}
	return tokens, costUSD, complete && requests > 0
}

func openShellSpend(tasks []*OpenShellResult) (requests, tokens int, costUSD float64, priced bool) {
	priced = true
	for _, t := range tasks {
		if t == nil || t.Report == nil {
			continue
		}
		s := t.Report.Shield
		if s == nil {
			if t.Report.WorkerAttempts == nil || *t.Report.WorkerAttempts > 0 {
				priced = false
			}
			continue
		}
		requests += s.Requests
		tokens += s.PromptTokens + s.CompletionTokens
		costUSD += s.CostUSD
		if s.PricedResponses != s.Requests {
			priced = false
		}
	}
	return requests, tokens, costUSD, priced
}

// OpenShellExport describes the patch a task's sandbox exported.
type OpenShellExport struct {
	Patch        string   `json:"patch"`
	PatchSHA256  string   `json:"patch_sha256"`
	ChangedFiles []string `json:"changed_files"`
	BaseRevision string   `json:"base_revision"`
	Tree         string   `json:"tree"`
	Verify       struct {
		Argv     []string `json:"argv"`
		ExitCode int      `json:"exit_code"`
		Seconds  float64  `json:"seconds"`
	} `json:"verify"`
}

// OpenShellResult is one task's outcome in a team run.
type OpenShellResult struct {
	NotStarted bool             `json:"not_started,omitempty"`
	Task       string           `json:"task"`
	Mode       string           `json:"mode,omitempty"`
	Profile    string           `json:"profile"`
	Outcome    string           `json:"outcome"`
	Error      string           `json:"error,omitempty"`
	Seconds    float64          `json:"seconds"`
	State      string           `json:"state,omitempty"` // kept after a failure, for inspection
	Evidence   string           `json:"evidence"`
	Report     *OpenShellReport `json:"report,omitempty"`
	Manifest   *PatchManifest   `json:"manifest,omitempty"`

	task     OpenShellTask
	answer   string
	worktree *Worktree
}

// OpenShellRuling is the outcome of one group of tasks that changed the same
// files: one winner lands whole, the others are dropped whole.
type OpenShellRuling struct {
	Attempts   *int     `json:"attempts,omitempty"`
	Contenders []string `json:"contenders"`
	Files      []string `json:"files"`
	Winner     string   `json:"winner,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Dropped    []string `json:"dropped"`
	Error      string   `json:"error,omitempty"`
}

// OpenShellIntegrated is the landed result and its fresh-sandbox check.
type OpenShellIntegrated struct {
	Patch        string           `json:"patch"`
	PatchSHA256  string           `json:"patch_sha256"`
	ChangedFiles []string         `json:"changed_files"`
	Tree         string           `json:"tree"`
	Verify       []string         `json:"verify"`
	Passed       bool             `json:"passed"`
	Seconds      float64          `json:"seconds"`
	State        string           `json:"state,omitempty"`
	Evidence     string           `json:"evidence"`
	Error        string           `json:"error,omitempty"`
	Report       *OpenShellReport `json:"report,omitempty"`
}

// OpenShellRun is the record of a team run (run.json).
type OpenShellRun struct {
	Version        int                     `json:"version"`
	Team           string                  `json:"team"`
	Repo           string                  `json:"repo"`
	Revision       string                  `json:"revision"`
	Runtime        string                  `json:"runtime"`
	Director       string                  `json:"director"`
	Concurrency    int                     `json:"concurrency"`
	RequireAll     bool                    `json:"require_all,omitempty"`
	Provenance     *OpenShellProvenance    `json:"provenance,omitempty"`
	StartedAt      time.Time               `json:"started_at"`
	DeadlineAt     time.Time               `json:"deadline_at,omitempty"`
	AttemptBudget  *OpenShellAttemptBudget `json:"attempt_budget,omitempty"`
	CostBudget     *OpenShellCostBudget    `json:"cost_budget,omitempty"`
	AttemptUsage   *OpenShellAttemptUsage  `json:"attempt_usage,omitempty"`
	Seconds        float64                 `json:"seconds"`
	Verdict        string                  `json:"verdict"`
	Error          string                  `json:"error,omitempty"`
	Tasks          []*OpenShellResult      `json:"tasks"`
	Stages         []OpenShellStageRecord  `json:"stages,omitempty"`
	SetAside       []OpenShellSetAside     `json:"set_aside,omitempty"`
	SequenceSHA256 string                  `json:"sequence_sha256,omitempty"`
	Resumptions    []time.Time             `json:"resumptions,omitempty"`
	Candidate      *IntegrationCandidate   `json:"candidate,omitempty"`
	Rulings        []OpenShellRuling       `json:"rulings,omitempty"`
	Landed         *IntegrationCandidate   `json:"landed,omitempty"`
	Integrated     *OpenShellIntegrated    `json:"integrated,omitempty"`
}

func (run *OpenShellRun) reviewOnly() bool {
	if len(run.Tasks) == 0 {
		return false
	}
	for _, task := range run.Tasks {
		if task == nil || task.Mode != "review" || task.Outcome != OpenShellUnchanged {
			return false
		}
	}
	return true
}

// OpenShellProvenance names the code a run executed, so its record can be
// tied to a commit: Captain's build, the Shield build prepare.py made, and the
// SHA-256 of every pilot script and prepared file as the run found them.
type OpenShellProvenance struct {
	Captain OpenShellBuild    `json:"captain"`
	Shield  OpenShellBuild    `json:"shield"`
	Files   map[string]string `json:"files"`
}

// OpenShellBuild is what a Go binary records about its own build. Revision is
// empty for a build made outside a Git checkout; Modified marks one made from
// a checkout with uncommitted changes, whose revision does not name the code.
type OpenShellBuild struct {
	GoVersion string `json:"go_version,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Modified  bool   `json:"modified"`
}

// OpenShellDirector picks one winner among tasks that changed the same files.
type OpenShellDirector func(ctx context.Context, task string, contenders map[string]Contender) (Ruling, error)

// OpenShellRunner runs a team's tasks, each through task.py in its own state
// directory, and lands what holds up.
type OpenShellRunner struct {
	Pilot       string // directory holding task.py and pilot.py
	Prepared    string // a prepare.py state: bin/, generated/, shield, venv/
	StateRoot   string // parent of the per-task states; short, since the VM socket lives under it
	Runtime     string // "vm" or "docker"
	Repo        string // top level of the repository
	Revision    string // pinned base commit
	RunDir      string // run.json, integrated.patch, diffs and per-task evidence
	Concurrency int
	DeadlineAt  time.Time
	MaxAttempts int
	// MaxCostUSD is a strict dollar cap and WorkerCostUSD each worker's share
	// of it (costBudget), enforced by the worker's Shield; zero means none.
	MaxCostUSD    float64
	WorkerCostUSD float64
	RequireAll    bool
	Pinned        *OpenShellProvenance // a sequence plan's build: a stage under any other refuses to start
	Director      OpenShellDirector    // nil: overlapping tasks are not landed
	DirectorName  string
	Log           func(format string, args ...any)

	landing sync.Mutex // worktree add and capture, one task at a time
}

func (r *OpenShellRunner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// check refuses a runner whose inputs are not what the pilot expects, before
// any sandbox starts.
func (r *OpenShellRunner) check(ctx context.Context) error {
	for _, dir := range []string{r.Pilot, r.Prepared, r.StateRoot, r.Repo, r.RunDir} {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("openshell: %q must be an absolute path", dir)
		}
	}
	for _, file := range []string{
		filepath.Join(r.Pilot, "task.py"), filepath.Join(r.Pilot, "pilot.py"),
		filepath.Join(r.Prepared, "bin", "openshell"), filepath.Join(r.Prepared, "bin", "openshell-gateway"),
		filepath.Join(r.Prepared, "shield"), filepath.Join(r.Prepared, "venv", "bin", "python"),
	} {
		if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("openshell: %s is missing; run prepare.py for --prepared and point --pilot at examples/openshell-pilot", file)
		}
	}
	if r.Runtime != "vm" && r.Runtime != "docker" {
		return fmt.Errorf("openshell: runtime %q is not vm or docker", r.Runtime)
	}
	if !pinnedRevision(r.Revision) {
		return fmt.Errorf("openshell: revision %q is not a pinned commit sha", r.Revision)
	}
	top, err := gitOutput(ctx, r.Repo, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("openshell: %s is not a git repository: %w", r.Repo, err)
	}
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(string(top))); err != nil || resolved != r.Repo {
		return fmt.Errorf("openshell: repo must be the repository's top level (%s)", strings.TrimSpace(string(top)))
	}
	if r.Concurrency < 1 || r.Concurrency > 8 {
		return fmt.Errorf("openshell: concurrency must be 1-8")
	}
	return nil
}

// provenance hashes what the run is about to execute: the pilot's scripts
// (not its tests), the Dockerfile its worker image is built from, the Shield
// binary, the OpenShell binaries and the generated protocol modules. The
// builds come from the stamps go build leaves in a binary; a release's
// BuildRevision stands in for Captain's, as it does for the release checks.
func (r *OpenShellRunner) provenance() (*OpenShellProvenance, error) {
	p := &OpenShellProvenance{Files: map[string]string{}}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sum, err := fileSHA256(binary)
	if err != nil {
		return nil, err
	}
	p.Files["captain/binary"] = sum
	if info, ok := debug.ReadBuildInfo(); ok {
		p.Captain = openShellBuild(info)
	}
	if BuildRevision != "" {
		p.Captain.Revision, p.Captain.Modified = BuildRevision, false
	}
	if info, err := buildinfo.ReadFile(filepath.Join(r.Prepared, "shield")); err == nil {
		p.Shield = openShellBuild(info)
	}
	for _, set := range []struct{ name, root, pattern string }{
		{"pilot", r.Pilot, "*.py"},
		{"pilot", r.Pilot, "Dockerfile"},
		{"pilot", r.Pilot, "*.yaml"},
		{"pilot", r.Pilot, "requirements.txt"},
		{"pilot", r.Pilot, "artifacts.lock.json"},
		{"prepared", r.Prepared, "shield"},
		{"prepared", r.Prepared, "bin/*"},
		{"prepared", r.Prepared, "generated/*.py"},
		{"prepared", r.Prepared, "venv/bin/python"},
	} {
		files, _ := filepath.Glob(filepath.Join(set.root, set.pattern)) // constant patterns
		for _, file := range files {
			if set.name == "pilot" && strings.HasPrefix(filepath.Base(file), "test_") {
				continue
			}
			if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
				continue
			}
			sum, err := fileSHA256(file)
			if err != nil {
				return nil, fmt.Errorf("openshell: provenance: %w", err)
			}
			rel, _ := filepath.Rel(set.root, file)
			p.Files[set.name+"/"+filepath.ToSlash(rel)] = sum
		}
	}
	return p, nil
}

func openShellBuild(info *debug.BuildInfo) OpenShellBuild {
	b := OpenShellBuild{GoVersion: info.GoVersion}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// RunTeam runs every task in its own sandbox, at most Concurrency at once,
// lands the tasks whose exports hold up, and verifies the integrated tree in
// a fresh sandbox. The record is written to RunDir/run.json either way.
func (r *OpenShellRunner) RunTeam(ctx context.Context, team OpenShellTeam) (*OpenShellRun, error) {
	ctx, cancel, err := OpenShellBudgetContext(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer cancel()
	ctx, stop := r.deadlineContext(ctx)
	defer stop()
	run := &OpenShellRun{Version: ArtifactVersion, Team: team.ID, Repo: r.Repo, Revision: r.Revision,
		Runtime: r.Runtime, Director: r.DirectorName, Concurrency: r.Concurrency, RequireAll: r.RequireAll, StartedAt: time.Now(), DeadlineAt: r.DeadlineAt, Verdict: "fail"}
	run.AttemptBudget, err = r.attemptBudget(ctx, []OpenShellTeam{team})
	if err == nil {
		run.CostBudget, err = r.costBudget(ctx, []OpenShellTeam{team})
	}
	if err == nil && run.CostBudget != nil {
		r.MaxCostUSD, r.WorkerCostUSD = run.CostBudget.LimitUSD, run.CostBudget.WorkerUSD
	}
	if err == nil {
		err = r.runTeam(ctx, team, run)
	}
	if err != nil {
		run.Error = err.Error()
	}
	run.Seconds = seconds(time.Since(run.StartedAt))
	run.AttemptUsage = run.measuredAttempts()
	data, _ := json.MarshalIndent(run, "", "  ")
	if werr := writeOpenShellFile(filepath.Join(r.RunDir, "run.json"), append(data, '\n')); werr != nil && err == nil {
		err = werr
	}
	return run, err
}

func (r *OpenShellRunner) runTeam(ctx context.Context, team OpenShellTeam, run *OpenShellRun) error {
	if err := team.Validate(); err != nil {
		return err
	}
	if err := r.check(ctx); err != nil {
		return err
	}
	provenance, err := r.provenance()
	if err != nil {
		return err
	}
	run.Provenance = provenance
	if r.Pinned != nil && !reflect.DeepEqual(provenance, r.Pinned) {
		return errors.New("openshell: runtime or pilot changed since the sequence plan was saved; start a new workflow")
	}
	// Tasks start in spec order, at most Concurrency at once.
	run.Tasks = make([]*OpenShellResult, len(team.Tasks))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(r.Concurrency, len(team.Tasks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				t := team.Tasks[i]
				if err := ctx.Err(); err != nil {
					run.Tasks[i] = &OpenShellResult{Task: t.ID, Profile: t.Profile, Outcome: OpenShellFailed,
						Error: "not started: " + err.Error(), NotStarted: true, task: t}
					continue
				}
				run.Tasks[i] = r.runTask(ctx, team.ID, t)
			}
		}()
	}
	for i := range team.Tasks {
		next <- i
	}
	close(next)
	wg.Wait()
	defer func() {
		for _, res := range run.Tasks {
			res.worktree.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.RequireAll {
		for _, res := range run.Tasks {
			if res.Outcome == OpenShellFailed {
				return fmt.Errorf("openshell: required worker %s failed: %s", res.Task, res.Error)
			}
		}
	}
	return r.integrate(ctx, team, run)
}

// openShellBudget bounds one pilot process: startup and cleanup, every worker
// attempt, and every verify run (baseline, per attempt, after the restart).
func openShellBudget(t OpenShellTask) time.Duration {
	deadline, verify := cmpOr(t.DeadlineSeconds, 600), cmpOr(t.VerifySeconds, 120)
	return time.Duration(600+(1+t.RepairAttempts)*deadline+(3+t.RepairAttempts)*verify) * time.Second
}

func cmpOr(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

// runTask executes one task in its own sandbox and, when the pilot's report
// and export hold up on the host, captures the patch as a manifest in a
// worktree of the base revision. The worktree stays open for conflict
// detection; RunTeam closes it.
func (r *OpenShellRunner) runTask(ctx context.Context, teamID string, t OpenShellTask) *OpenShellResult {
	start := time.Now()
	res := &OpenShellResult{Task: t.ID, Mode: t.Mode, Profile: t.Profile, Outcome: OpenShellFailed,
		Evidence: filepath.Join(r.RunDir, "tasks", t.ID), task: t}
	defer func() {
		res.Seconds = seconds(time.Since(start))
		r.logf("%s: %s in %.0fs", t.ID, res.Outcome, res.Seconds)
	}()
	spec := map[string]any{"schema": 1, "mode": "edit", "id": t.ID, "repo": r.Repo, "revision": r.Revision,
		"prompt": t.Prompt, "verify": t.Verify, "allowed": t.Allowed}
	if t.Mode == "review" {
		spec["mode"], spec["allowed"], spec["baseline"] = "review", []string{}, "pass"
	}
	if len(t.Protected) > 0 {
		spec["protected"] = t.Protected
	}
	if t.Baseline != "" {
		spec["baseline"] = t.Baseline
	}
	if t.DeadlineSeconds != 0 {
		spec["deadline_seconds"] = t.DeadlineSeconds
	}
	if t.VerifySeconds != 0 {
		spec["verify_seconds"] = t.VerifySeconds
	}
	state, report, err := r.pilot(ctx, spec, t.Profile, t.RepairAttempts, res.Evidence, openShellBudget(t), r.WorkerCostUSD)
	res.Report = report
	if err == nil {
		err = checkOpenShellReport(report, t.ID, openShellTaskChecks)
	}
	if err == nil {
		err = r.checkCostReport(report)
	}
	if err == nil {
		err = r.land(ctx, teamID, t, state, report, res)
	}
	if err != nil {
		res.Error, res.State = err.Error(), state
		return res
	}
	res.Outcome = OpenShellUnchanged
	if res.Manifest.HasChanges() {
		res.Outcome = OpenShellLanded // until a ruling says otherwise
	}
	if err := os.RemoveAll(state); err != nil {
		res.State = state
	}
	return res
}

// pilot runs task.py once in a fresh state directory and returns the state
// and its report. The process gets a minimal environment and its own process
// group, so a terminal interrupt reaches Captain alone; Captain then sends
// the pilot one SIGTERM, which it answers by deleting its sandbox and
// stopping its gateway. Under a strict cap, costUSD is the dollar allocation
// the pilot's Shield enforces; a verify sandbox calls no model and gets zero.
func (r *OpenShellRunner) pilot(ctx context.Context, spec map[string]any, profile string, repairs int, evidence string, budget time.Duration, costUSD float64) (string, *OpenShellReport, error) {
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		return "", nil, err
	}
	state, err := r.newState()
	if err != nil {
		return "", nil, err
	}
	specPath := filepath.Join(state, "task.json")
	data, err := json.Marshal(spec)
	if err == nil {
		err = writeOpenShellFile(specPath, data)
	}
	if err != nil {
		return state, nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(evidence, "pilot.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return state, nil, err
	}
	defer logFile.Close()
	c, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	args := []string{"-B", filepath.Join(r.Pilot, "task.py"), "--state", state, "--runtime", r.Runtime, "--profile", profile,
		"--repair-attempts", strconv.Itoa(repairs), "--task", specPath}
	if r.WorkerCostUSD > 0 {
		args = append(args, "--max-cost-usd", strconv.FormatFloat(costUSD, 'g', -1, 64))
	}
	cmd := exec.CommandContext(c, filepath.Join(state, "venv", "bin", "python"), args...)
	cmd.Env = openShellEnv()
	out := &cappedWriter{w: logFile, n: openShellFileLimit}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = openShellWaitDelay
	runErr := cmd.Run()
	collectOpenShellEvidence(state, evidence)
	report, err := readOpenShellReport(filepath.Join(state, "report.json"))
	switch {
	case err != nil:
		return state, nil, fmt.Errorf("pilot left no readable report (%v): %w", runErr, err)
	case c.Err() != nil:
		return state, report, fmt.Errorf("pilot stopped (%v): %s", c.Err(), report.Error)
	case runErr != nil && report.Error != "":
		return state, report, fmt.Errorf("pilot verdict %s: %s", report.Verdict, report.Error)
	case runErr != nil:
		return state, report, fmt.Errorf("pilot verdict %s: %w", report.Verdict, runErr)
	}
	return state, report, nil
}

func openShellEnv() []string {
	env := []string{"PYTHONDONTWRITEBYTECODE=1"}
	for _, key := range openShellEnvKeys {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// newState lays out a fresh pilot state the way prepare.py does: its own
// copies of the OpenShell binaries and the Shield, and the prepared Python
// environment by reference.
func (r *OpenShellRunner) newState() (string, error) {
	state, err := os.MkdirTemp(r.StateRoot, "cc-os-")
	if err != nil {
		return "", fmt.Errorf("openshell state: %w", err)
	}
	err = copyOpenShellFiles(filepath.Join(r.Prepared, "bin"), filepath.Join(state, "bin"), nil)
	if err == nil {
		err = copyOpenShellFiles(filepath.Join(r.Prepared, "generated"), filepath.Join(state, "generated"),
			func(name string) bool { return strings.HasSuffix(name, ".py") })
	}
	if err == nil {
		err = copyOpenShellFile(filepath.Join(r.Prepared, "shield"), filepath.Join(state, "shield"), 0o700)
	}
	if err == nil {
		err = os.Symlink(filepath.Join(r.Prepared, "venv"), filepath.Join(state, "venv"))
	}
	if err != nil {
		os.RemoveAll(state)
		return "", fmt.Errorf("openshell state: %w", err)
	}
	return state, nil
}

func copyOpenShellFiles(src, dst string, keep func(string) bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || (keep != nil && !keep(e.Name())) {
			continue
		}
		mode := os.FileMode(0o600)
		if info, err := e.Info(); err == nil && info.Mode()&0o100 != 0 {
			mode = 0o700
		}
		if err := copyOpenShellFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), mode); err != nil {
			return err
		}
	}
	return nil
}

func copyOpenShellFile(src, dst string, mode os.FileMode) error {
	in, err := openRegular(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// openRegular opens a file for reading without following a final symlink,
// and only if it is a regular file.
func openRegular(file string) (*os.File, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", file)
	}
	return f, nil
}

// readOpenShellFile reads at most limit bytes of a regular file, and fails
// rather than truncate.
func readOpenShellFile(file string, limit int64) ([]byte, error) {
	f, err := openRegular(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(file), limit)
	}
	return data, nil
}

// writeOpenShellFile creates a record once and makes it durable before
// returning: a sequence saves the checkpoint that names a stage's records
// right after them, and recovery rereads and rehashes them after a crash.
func writeOpenShellFile(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncOpenShellDir(filepath.Dir(file))
}

func syncOpenShellDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// openShellEvidence is what a run keeps from each pilot state: the report,
// the export, the verification logs, the worker's own answer (untrusted
// text), the Shield audit and OpenShell's own sandbox audit.
var openShellEvidence = []string{"report.json", "result.patch", "answer.txt", "baseline-verify.log", "verify.log",
	"recovery-verify.log", "shield-audit.jsonl", "sandbox-audit.log", "worker.jsonl"}

func collectOpenShellEvidence(state, evidence string) {
	for _, name := range openShellEvidence {
		if data, err := readOpenShellFile(filepath.Join(state, name), openShellFileLimit); err == nil {
			writeOpenShellFile(filepath.Join(evidence, name), data)
		}
	}
}

func readOpenShellReport(file string) (*OpenShellReport, error) {
	data, err := readOpenShellFile(file, openShellReportLimit)
	if err != nil {
		return nil, err
	}
	var report OpenShellReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

// checkOpenShellReport accepts a report only when it is this task's, it
// passed, and every check of its mode passed.
func checkOpenShellReport(report *OpenShellReport, id string, checks []string) error {
	if report.Verdict != "pass" {
		return fmt.Errorf("pilot verdict %s: %s", report.Verdict, report.Error)
	}
	if report.Mode != "task" || report.TaskID != id || report.TaskSuccesses != 1 {
		return fmt.Errorf("the pilot's report is not task %s's", id)
	}
	for _, name := range checks {
		if report.Checks[name].Verdict != "pass" {
			return fmt.Errorf("pilot check %s did not pass", name)
		}
	}
	return nil
}

// land re-checks a task's export on the host - the digest the report
// promised, text edits to allowed files only, a clean apply to the pinned
// revision - and captures it as a manifest in a worktree of that revision.
// The export came out of a sandbox a model worked in, so the pilot's own
// checks are not taken on trust; nothing in the patch is executed.
func (r *OpenShellRunner) land(ctx context.Context, teamID string, t OpenShellTask, state string, report *OpenShellReport, res *OpenShellResult) error {
	e := report.Export
	if e == nil || e.Patch != "result.patch" || e.BaseRevision != r.Revision || e.Verify.ExitCode != 0 ||
		!slices.Equal(e.Verify.Argv, t.Verify) {
		return errors.New("the pilot's export does not match the task")
	}
	patch, err := readOpenShellFile(filepath.Join(state, "result.patch"), openShellPatchLimit)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	if digest := sha256.Sum256(patch); hex.EncodeToString(digest[:]) != e.PatchSHA256 {
		return errors.New("export: the patch is not the one the report describes")
	}
	if t.Mode == "review" {
		if len(patch) != 0 {
			return errors.New("export: review changed repository files")
		}
	} else if len(patch) == 0 {
		return errors.New("export: edit produced no change")
	}
	files, err := checkOpenShellPatch(ctx, r.Repo, patch, t.Allowed)
	if err != nil {
		return err
	}
	if !slices.Equal(files, sortedCopy(e.ChangedFiles)) {
		return errors.New("export: the patch changes other files than the report lists")
	}
	if answer, err := readOpenShellFile(filepath.Join(state, "answer.txt"), openShellAnswerLimit); err == nil {
		res.answer = string(answer)
	}
	verifyLog, _ := readOpenShellFile(filepath.Join(state, "recovery-verify.log"), openShellFileLimit)

	r.landing.Lock()
	defer r.landing.Unlock()
	wt, err := NewWorktree(ctx, r.Repo, r.Revision)
	if err != nil {
		return err
	}
	if len(patch) > 0 {
		for _, check := range []bool{true, false} {
			if err := gitApply(ctx, wt.Dir, patch, check); err != nil {
				wt.Close()
				return err
			}
		}
	}
	m, err := CaptureManifest(ctx, wt.Dir, filepath.Join(r.RunDir, "diffs"), r.Revision, teamID, "openshell", t.ID, "openshell-"+t.Profile)
	if err == nil && !slices.Equal(m.ChangedFiles, files) {
		err = errors.New("export: the applied patch changed other files than it lists")
	}
	if err != nil {
		wt.Close()
		return err
	}
	m.Worker = t.ID
	m.RecordCheck(t.Verify, e.Verify.ExitCode, true, string(verifyLog))
	res.Manifest, res.worktree = &m, wt
	return nil
}

// checkOpenShellPatch returns the files a patch changes, sorted, when it holds
// only text edits to existing files the task allows.
func checkOpenShellPatch(ctx context.Context, repo string, patch []byte, allowed []string) ([]string, error) {
	if len(patch) > openShellPatchLimit {
		return nil, errors.New("export: the patch exceeds the limit")
	}
	if len(patch) == 0 {
		return nil, nil
	}
	// Split on \r as well as \n: stricter than git's own parser, never looser.
	for _, line := range bytes.FieldsFunc(patch, func(r rune) bool { return r == '\n' || r == '\r' }) {
		for _, bad := range openShellForbidden {
			if bytes.HasPrefix(line, []byte(bad)) {
				return nil, errors.New("export: only text edits to existing regular files can land")
			}
		}
	}
	out, err := gitOutput(ctx, repo, nil, patch, "apply", "--numstat", "-z", "-")
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	var files []string
	for _, row := range strings.Split(string(out), "\x00") {
		if row == "" {
			continue
		}
		parts := strings.SplitN(row, "\t", 3)
		if len(parts) != 3 || parts[2] == "" || !slices.Contains(allowed, parts[2]) || slices.Contains(files, parts[2]) {
			return nil, errors.New("export: the patch changes files outside the task's scope")
		}
		files = append(files, parts[2])
	}
	if len(files) == 0 {
		return nil, errors.New("export: the patch changes no file")
	}
	sort.Strings(files)
	return files, nil
}

func gitApply(ctx context.Context, dir string, patch []byte, check bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if check {
		args = append(args, "--check")
	}
	if _, err := gitOutput(ctx, dir, nil, patch, append(args, "-")...); err != nil {
		return fmt.Errorf("export does not apply to the pinned revision: %w", err)
	}
	return nil
}

// gitOutput runs one git command with a timeout and returns its stdout.
func gitOutput(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Captain's plumbing never runs a repository's hooks or fsmonitor: the
	// snapshot repository sits in a writable run directory, and command-line
	// settings outrank anything its config, or a file it includes, sets.
	cmd := exec.CommandContext(c, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	if env != nil {
		cmd.Env = env
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &cappedWriter{w: &stderr, n: 4096}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, truncateStr(oneLine(stderr.String()), 300))
	}
	return out, nil
}

// integrate lands the passing tasks: one integration candidate over their
// manifests, one ruling per group of tasks that changed the same files, the
// survivors replayed into a fresh worktree, and the resulting tree verified
// in a new sandbox with every landed task's check.
func (r *OpenShellRunner) integrate(ctx context.Context, team OpenShellTeam, run *OpenShellRun) error {
	var manifests []PatchManifest
	byID := map[string]*OpenShellResult{}
	for _, res := range run.Tasks {
		if res.Manifest != nil && res.Manifest.HasChanges() {
			manifests = append(manifests, *res.Manifest)
			byID[res.Task] = res
		}
	}
	candidate := BuildIntegrationCandidate(team.ID, "openshell", r.Revision, manifests)
	run.Candidate = &candidate
	dropped := map[string]bool{}
	if candidate.Status == IntegrationConflicted {
		for _, group := range openShellGroups(candidate) {
			ruling := r.arbitrate(ctx, team.ID, group, byID)
			for _, id := range ruling.Dropped {
				dropped[id] = true
			}
			run.Rulings = append(run.Rulings, ruling)
			if r.RequireAll && ruling.Error != "" {
				return fmt.Errorf("openshell: required conflict ruling failed: %s", ruling.Error)
			}
		}
	}
	var kept []PatchManifest
	var landed []OpenShellTask
	for _, res := range run.Tasks {
		if res.Outcome == OpenShellUnchanged && res.task.Mode == "review" {
			landed = append(landed, res.task)
		}
	}
	for _, m := range manifests {
		if dropped[m.Worker] {
			byID[m.Worker].Outcome = OpenShellDropped
			continue
		}
		kept = append(kept, m)
		landed = append(landed, byID[m.Worker].task)
	}
	final := BuildIntegrationCandidate(team.ID, "openshell", r.Revision, kept)
	run.Landed = &final
	if final.Status == IntegrationEmpty && len(landed) != len(run.Tasks) {
		return errors.New("no task produced a change that could land")
	}
	if final.Status != IntegrationClean && final.Status != IntegrationEmpty {
		return fmt.Errorf("integration: candidate is %s after the rulings", final.Status)
	}
	integrated, err := r.applyAndVerify(ctx, team, final, landed)
	run.Integrated = integrated
	if err != nil {
		return err
	}
	if integrated.Passed {
		run.Verdict = "pass"
	}
	return nil
}

// openShellGroups splits a conflicted candidate's contested manifests into
// groups that share files, directly or through another member; each group
// gets its own ruling.
func openShellGroups(c IntegrationCandidate) [][]PatchManifest {
	contested := map[string]bool{}
	for _, id := range c.Contested() {
		contested[id] = true
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		if parent[id] == id {
			return id
		}
		parent[id] = find(parent[id])
		return parent[id]
	}
	owner := map[string]string{}
	for _, m := range c.Manifests {
		id := m.ManifestID()
		if !contested[id] {
			continue
		}
		parent[id] = id
		for _, f := range m.ChangedFiles {
			if first, ok := owner[f]; ok {
				parent[find(id)] = find(first)
			} else {
				owner[f] = id
			}
		}
	}
	index := map[string]int{}
	var groups [][]PatchManifest
	for _, m := range c.Manifests {
		if !contested[m.ManifestID()] {
			continue
		}
		root := find(m.ManifestID())
		i, ok := index[root]
		if !ok {
			i = len(groups)
			index[root] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], m)
	}
	return groups
}

// arbitrate settles one conflict group. Without a director, or when the
// director fails, no member of the group lands: an overlapping change is
// never guessed at.
func (r *OpenShellRunner) arbitrate(ctx context.Context, teamID string, group []PatchManifest, byID map[string]*OpenShellResult) OpenShellRuling {
	ruling := OpenShellRuling{}
	files := map[string]bool{}
	contenders := map[string]Contender{}
	var brief strings.Builder
	fmt.Fprintf(&brief, "Captain team %s ran these tasks in separate OpenShell sandboxes. Each one passed its own verification in its sandbox, and their patches change the same files:\n", teamID)
	for _, m := range group {
		res := byID[m.Worker]
		ruling.Contenders = append(ruling.Contenders, m.Worker)
		for _, f := range m.ChangedFiles {
			files[f] = true
		}
		fmt.Fprintf(&brief, "- %s: %s\n", m.Worker, truncateStr(oneLine(res.task.Prompt), 400))
		diff, _ := readOpenShellFile(m.DiffPath, openShellPatchLimit)
		evidence := fmt.Sprintf("verified in its sandbox after a restart (%s exit 0)", strings.Join(res.task.Verify, " "))
		if rep := res.Report; rep != nil && rep.WorkerAttempts != nil {
			evidence += fmt.Sprintf("; %d worker attempt(s); inference %s", *rep.WorkerAttempts, rep.Inference)
		}
		contenders[m.Worker] = Contender{Leg: Leg(m.Leg), Files: m.ChangedFiles, Evidence: evidence,
			Text: "Patch:\n" + truncateStr(string(diff), 2000) + "\n\nThe worker's own report (unverified):\n" + res.answer}
	}
	for f := range files {
		ruling.Files = append(ruling.Files, f)
	}
	sort.Strings(ruling.Files)
	if r.Director == nil {
		ruling.Attempts = new(int)
		ruling.Dropped = ruling.Contenders
		ruling.Error = "no director: tasks that changed the same files are not landed"
		return ruling
	}
	candidate := BuildIntegrationCandidate(teamID, "openshell", r.Revision, group)
	attempts := &openShellDirectorAttempts{}
	ctx = context.WithValue(ctx, openShellDirectorAttemptsKey{}, attempts)
	verdict, err := r.Director(ctx, brief.String(), contenders)
	if attempts.known {
		ruling.Attempts = &attempts.count
	}
	if err == nil {
		candidate, err = candidate.Resolve(verdict.Winner, verdict.Reason)
	}
	if err != nil {
		ruling.Dropped = ruling.Contenders
		ruling.Error = err.Error()
		return ruling
	}
	ruling.Winner, ruling.Reason, ruling.Dropped = candidate.Winner, candidate.Ruling, candidate.Dropped
	r.logf("ruling: %s over %s", ruling.Winner, strings.Join(ruling.Dropped, ", "))
	return ruling
}

// applyAndVerify replays the landed candidate into a fresh worktree - the
// user's own working tree is never written - takes the integrated patch from
// it, and has base + patch verified in a new verify-mode sandbox. What passes
// there is byte for byte the patch the user applies.
func (r *OpenShellRunner) applyAndVerify(ctx context.Context, team OpenShellTeam, final IntegrationCandidate, landed []OpenShellTask) (*OpenShellIntegrated, error) {
	integrated := &OpenShellIntegrated{Patch: filepath.Join(r.RunDir, "integrated.patch"),
		Evidence: filepath.Join(r.RunDir, "integrated"), ChangedFiles: final.ChangedFiles()}
	argv, verifySeconds, err := openShellIntegratedVerify(team, landed)
	if err != nil {
		return integrated, err
	}
	integrated.Verify = argv
	wt, err := NewWorktree(ctx, r.Repo, r.Revision)
	if err != nil {
		return integrated, err
	}
	defer wt.Close()
	if final.Status != IntegrationEmpty {
		if err := ApplyIntegrationCandidate(ctx, wt.Dir, final); err != nil {
			return integrated, err
		}
	}
	patch, err := gitOutput(ctx, wt.Dir, nil, nil, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--no-renames", r.Revision, "--")
	if err != nil {
		return integrated, err
	}
	digest := sha256.Sum256(patch)
	integrated.PatchSHA256 = hex.EncodeToString(digest[:])
	if err := writeOpenShellFile(integrated.Patch, patch); err != nil {
		return integrated, err
	}
	tree, err := r.treeWith(ctx, patch)
	if err != nil {
		return integrated, err
	}
	integrated.Tree = tree
	start := time.Now()
	spec := map[string]any{"schema": 1, "mode": "verify", "id": "integrated", "repo": r.Repo, "revision": tree,
		"verify": argv, "baseline": "pass", "verify_seconds": verifySeconds}
	budget := time.Duration(600+3*verifySeconds) * time.Second
	state, report, err := r.pilot(ctx, spec, landed[0].Profile, 0, integrated.Evidence, budget, 0)
	integrated.Seconds, integrated.Report = seconds(time.Since(start)), report
	if err == nil {
		err = checkOpenShellReport(report, "integrated", openShellVerifyChecks)
	}
	if err == nil && (report.BaselineExitCode == nil || *report.BaselineExitCode != 0) {
		err = errors.New("the integrated tree did not pass its check")
	}
	if err != nil {
		integrated.Error, integrated.State = err.Error(), state
		r.logf("integrated: failed in %.0fs", integrated.Seconds)
		return integrated, nil
	}
	integrated.Passed = true
	os.RemoveAll(state)
	r.logf("integrated: verified in %.0fs", integrated.Seconds)
	return integrated, nil
}

// treeWith writes the tree of the pinned revision plus a patch through a
// temporary index: no commit, no ref, no hook, no working-tree filter.
func (r *OpenShellRunner) treeWith(ctx context.Context, patch []byte) (string, error) {
	dir, err := os.MkdirTemp("", "captain-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
	if _, err := gitOutput(ctx, r.Repo, env, nil, "read-tree", r.Revision); err != nil {
		return "", err
	}
	if len(patch) > 0 {
		if _, err := gitOutput(ctx, r.Repo, env, patch, "apply", "--cached", "--whitespace=nowarn", "-"); err != nil {
			return "", err
		}
	}
	out, err := gitOutput(ctx, r.Repo, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !openShellObject.MatchString(tree) {
		return "", fmt.Errorf("git write-tree returned %q", tree)
	}
	return tree, nil
}

// openShellIntegratedVerify is the integrated tree's check: the team's own
// command when it names one, otherwise every landed task's verify command in
// turn, so a patch that breaks another landed task fails the run.
func openShellIntegratedVerify(team OpenShellTeam, landed []OpenShellTask) ([]string, int, error) {
	total := 0
	var distinct [][]string
	for _, t := range landed {
		total += cmpOr(t.VerifySeconds, 120)
		if !slices.ContainsFunc(distinct, func(argv []string) bool { return slices.Equal(argv, t.Verify) }) {
			distinct = append(distinct, t.Verify)
		}
	}
	total = min(max(total, 10), openShellVerifyCap)
	switch {
	case len(team.Verify) > 0:
		return team.Verify, total, nil
	case len(distinct) == 1:
		return distinct[0], total, nil
	}
	script := "set -e"
	for _, argv := range distinct {
		script += "; " + shellJoin(argv)
	}
	if n := utf8.RuneCountInString(script); n > openShellArgLimit {
		return nil, 0, fmt.Errorf("the landed tasks' checks make a %d-character script; the limit is %d, so set \"verify\" in the team spec", n, openShellArgLimit)
	}
	return []string{"sh", "-c", script}, total, nil
}

// shellJoin quotes an argv for sh: bare where that is unambiguous, single
// quotes otherwise.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if openShellBareArg.MatchString(arg) {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

// ToolLessClaudeDirector arbitrates with claude -p stripped of every tool,
// MCP server, customization, permission and session, in an empty directory.
// Worker output reaches its prompt, so the director gets nothing to act
// with: its reply can only name one of the contenders (checkRuling), and the
// change that lands is verified again in a fresh sandbox.
func ToolLessClaudeDirector(ctx context.Context, task string, contenders map[string]Contender) (Ruling, error) {
	if attempts, ok := ctx.Value(openShellDirectorAttemptsKey{}).(*openShellDirectorAttempts); ok {
		attempts.known = true
	}
	if len(contenders) < 2 {
		return Ruling{}, fmt.Errorf("arbitrate: need two or more contenders, got %d", len(contenders))
	}
	prompt := directorConstraint + arbitrationPrompt(task, contenders)
	var r Ruling
	text, err := toolLessClaude(ctx, prompt)
	if err != nil {
		return Ruling{}, err
	}
	if extractJSON(text, &r) != nil {
		retry := prompt + "\n\nYour previous reply did not contain valid JSON - it was:\n" + truncateStr(text, 300) +
			"\n\nThat is not acceptable. Reply again with ONLY the JSON object, no other text."
		if text, err = toolLessClaude(ctx, retry); err != nil {
			return Ruling{}, err
		}
		if err := extractJSON(text, &r); err != nil {
			return Ruling{}, fmt.Errorf("no valid JSON in director reply after retry (%w)", err)
		}
	}
	return checkRuling(r, contenders)
}

func toolLessClaude(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "captain-director-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	c, cancel := context.WithTimeout(ctx, directorTimeout)
	defer cancel()
	cmd := exec.CommandContext(c, "claude", "-p", "--output-format", "json", "--tools", "", "--safe-mode",
		"--strict-mcp-config", "--permission-mode", "dontAsk", "--no-session-persistence")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &cappedWriter{w: &stderr, n: 4096}
	if attempts, ok := ctx.Value(openShellDirectorAttemptsKey{}).(*openShellDirectorAttempts); ok {
		attempts.count++
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("director: claude: %w: %s", err, truncateStr(oneLine(stderr.String()), 200))
	}
	var reply struct {
		Subtype string `json:"subtype"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return "", fmt.Errorf("director: claude printed no result: %w", err)
	}
	if reply.IsError || reply.Subtype != "success" {
		return "", fmt.Errorf("director: claude returned %s", reply.Subtype)
	}
	return reply.Result, nil
}

// cappedWriter keeps the first n bytes and discards the rest, so a runaway
// process cannot fill the disk through its log.
type cappedWriter struct {
	w io.Writer
	n int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n > 0 {
		keep := p
		if int64(len(keep)) > c.n {
			keep = keep[:c.n]
		}
		if _, err := c.w.Write(keep); err != nil {
			return 0, err
		}
		c.n -= int64(len(keep))
	}
	return len(p), nil
}

func sortedCopy(items []string) []string {
	out := slices.Clone(items)
	sort.Strings(out)
	return out
}

func seconds(d time.Duration) float64 {
	return float64(d.Milliseconds()) / 1000
}

func ResolveOpenShellRepo(ctx context.Context, repo, revision string) (string, string, error) {
	if strings.HasPrefix(revision, "-") || revision == "" {
		return "", "", fmt.Errorf("openshell: %q is not a revision", revision)
	}
	top, err := gitOutput(ctx, repo, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("openshell: %s is not a git repository: %w", repo, err)
	}
	dir, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return "", "", err
	}
	sha, err := gitOutput(ctx, dir, nil, nil, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("openshell: %s is not a commit in %s: %w", revision, dir, err)
	}
	pinned := strings.TrimSpace(string(sha))
	if !pinnedRevision(pinned) {
		return "", "", fmt.Errorf("openshell: revision %q did not resolve to a commit sha", revision)
	}
	return dir, pinned, nil
}

func openShellConfig(ctx context.Context, dir, task string) (*OpenShellRunner, OpenShellTeam, error) {
	var team OpenShellTeam
	prepared := os.Getenv("CAPTAIN_OPENSHELL_PREPARED")
	if strings.TrimSpace(prepared) == "" {
		return nil, team, fmt.Errorf("openshell: set CAPTAIN_OPENSHELL_PREPARED to the prepare.py state directory")
	}
	// The controller runs on the host, so its scripts never default to the
	// target repository: a repository being sandboxed must not choose them.
	if strings.TrimSpace(os.Getenv("CAPTAIN_OPENSHELL_PILOT")) == "" {
		return nil, team, fmt.Errorf("openshell: set CAPTAIN_OPENSHELL_PILOT to the examples/openshell-pilot directory of a Captain checkout you trust")
	}
	var verify []string
	rawVerify := os.Getenv("CAPTAIN_OPENSHELL_VERIFY")
	if err := json.Unmarshal([]byte(rawVerify), &verify); err != nil {
		return nil, team, fmt.Errorf("openshell: set CAPTAIN_OPENSHELL_VERIFY to a JSON argv array, such as [\"python3\",\"-m\",\"unittest\"]")
	}
	if err := checkOpenShellArgv(verify); err != nil {
		return nil, team, fmt.Errorf("openshell: verify (CAPTAIN_OPENSHELL_VERIFY): %w", err)
	}
	paths := func(key string) []string {
		var result []string
		for _, p := range strings.Split(os.Getenv(key), ",") {
			if p = strings.TrimSpace(p); p != "" {
				result = append(result, p)
			}
		}
		return result
	}
	allowed := paths("CAPTAIN_OPENSHELL_ALLOWED")
	if len(allowed) == 0 {
		return nil, team, fmt.Errorf("openshell: set CAPTAIN_OPENSHELL_ALLOWED to the comma-separated paths the worker may change")
	}
	value := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	concurrency := 1
	if v := os.Getenv("CAPTAIN_OPENSHELL_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 8 {
			return nil, team, fmt.Errorf("openshell: CAPTAIN_OPENSHELL_CONCURRENCY must be 1-8")
		}
		concurrency = n
	}
	runner := &OpenShellRunner{
		Prepared:     prepared,
		Pilot:        os.Getenv("CAPTAIN_OPENSHELL_PILOT"),
		Runtime:      value("CAPTAIN_OPENSHELL_RUNTIME", "vm"),
		StateRoot:    "/tmp",
		Concurrency:  concurrency,
		DirectorName: value("CAPTAIN_OPENSHELL_DIRECTOR", "none"),
	}
	if runner.Runtime != "vm" && runner.Runtime != "docker" {
		return nil, team, fmt.Errorf("openshell: runtime must be vm or docker")
	}
	switch runner.DirectorName {
	case "none":
	case "claude":
		runner.Director = ToolLessClaudeDirector
	default:
		return nil, team, fmt.Errorf("openshell: CAPTAIN_OPENSHELL_DIRECTOR must be none or claude")
	}
	taskID := fmt.Sprintf("brain-%x", sha256.Sum256([]byte(task)))[:12]
	team = OpenShellTeam{Schema: 1, ID: taskID, Tasks: []OpenShellTask{{
		ID: taskID, Profile: value("CAPTAIN_OPENSHELL_PROFILE", "glm-cheap-z-ai-fp8"),
		Prompt: task, Verify: verify, Allowed: allowed,
		Protected:      paths("CAPTAIN_OPENSHELL_PROTECTED"),
		Baseline:       value("CAPTAIN_OPENSHELL_BASELINE", "any"),
		RepairAttempts: 1, DeadlineSeconds: 600, VerifySeconds: 120,
	}}}
	if err := team.Validate(); err != nil {
		return nil, team, err
	}
	var err error
	runner.Repo, runner.Revision, err = ResolveOpenShellRepo(ctx, value("CAPTAIN_OPENSHELL_REPO", dir), value("CAPTAIN_OPENSHELL_REVISION", "HEAD"))
	if err != nil {
		return nil, team, err
	}
	for _, p := range []*string{&runner.Pilot, &runner.Prepared} {
		if *p, err = filepath.Abs(*p); err != nil {
			return nil, team, err
		}
	}
	return runner, team, nil
}

func runOpenShell(dir, task string, base, ceil time.Duration, steer *Steer) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), base)
	defer cancel()
	return (Workspace{Dir: dir, Steer: steer}).RunOpenShell(ctx, task)
}

func (ws Workspace) RunOpenShell(ctx context.Context, task string) (Result, error) {
	ctx, cancel, err := OpenShellBudgetContext(ctx, nil)
	if err != nil {
		return openShellRefused(err)
	}
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, workerTimeout())
	defer stop()
	runner, team, err := openShellConfig(ctx, ws.Dir, task)
	if err != nil {
		return openShellRefused(err)
	}
	return runConfiguredOpenShell(ctx, runner, team, ws.Steer)
}

func runConfiguredOpenShell(ctx context.Context, runner *OpenShellRunner, team OpenShellTeam, steer *Steer) (Result, error) {
	if err := configureOpenShellRunDir(runner, team.ID); err != nil {
		return openShellRefused(err)
	}
	return runOpenShellTeam(ctx, runner, team, steer)
}

func configureOpenShellRunDir(runner *OpenShellRunner, id string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("openshell: cannot find home directory: %w", err)
	}
	baseDir := filepath.Join(home, ".captaincode", "openshell")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return fmt.Errorf("openshell: cannot create state dir: %w", err)
	}
	runner.RunDir, err = os.MkdirTemp(baseDir, time.Now().UTC().Format("20060102T150405Z")+"-"+id+"-")
	if err != nil {
		return fmt.Errorf("openshell: cannot create run dir: %w", err)
	}
	runner.Log = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "openshell: "+format+"\n", args...)
	}
	return nil
}

func runOpenShellTeam(ctx context.Context, runner *OpenShellRunner, team OpenShellTeam, steer *Steer) (Result, error) {
	ctx, stopped, detach := interruptible(ctx, steer, LegOpenShell)
	defer detach()
	if !steer.Interrupted().IsZero() {
		return openShellRefused(ErrInterrupted)
	}
	run, err := runner.RunTeam(ctx, team)
	return finishOpenShellRun(ctx, runner, run, err, stopped.Load())
}

func finishOpenShellRun(ctx context.Context, runner *OpenShellRunner, run *OpenShellRun, err error, stopped bool) (Result, error) {
	if run == nil {
		return openShellRefused(err)
	}
	ctx, cancel := runner.deadlineContext(ctx)
	defer cancel()
	res := Result{Text: openShellResultText(run, runner.RunDir), DurationMs: int64(run.Seconds * 1000)}
	if run.AttemptUsage != nil {
		usage := *run.AttemptUsage
		res.OpenShellAttempts = &usage
	}
	if tokens, cost, complete := run.Spend(); complete {
		res.Tokens, res.CostUSD = tokens, cost
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		res.Partial = true
		res.Text = "OpenShell deadline reached: no verified export delivered.\nrun record: " + filepath.Join(runner.RunDir, "run.json") + "\n"
		return res, context.DeadlineExceeded
	}
	if stopped || ctx.Err() != nil {
		res.Partial = true
		res.Text = "OpenShell interrupted: no verified export delivered.\nrun record: " + filepath.Join(runner.RunDir, "run.json") + "\n"
		return res, ErrInterrupted
	}
	if err != nil {
		return res, err
	}
	if run.Verdict != "pass" || run.Integrated == nil || !run.Integrated.Passed {
		detail := run.Error
		if run.Integrated != nil && run.Integrated.Error != "" {
			detail = run.Integrated.Error
		}
		return res, fmt.Errorf("openshell: integrated verification did not pass: %s", detail)
	}
	res.Export, err = runner.verifiedExport(ctx, run)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		res.Export = nil
		res.Text = "OpenShell export rejected: " + err.Error() + "\nrun record: " + filepath.Join(runner.RunDir, "run.json") + "\n"
		return res, err
	}
	if res.Export.Manifest.HasChanges() {
		res.Text += fmt.Sprintf("exported (not applied)\napply with: %s\n", shellJoin([]string{"git", "-C", res.Export.Repository, "apply", res.Export.Manifest.DiffPath}))
	} else {
		res.Text += "verified unchanged snapshot; nothing to apply\n"
	}
	return res, nil
}

func (r *OpenShellRunner) verifiedExport(ctx context.Context, run *OpenShellRun) (*VerifiedExport, error) {
	if run == nil || run.Verdict != "pass" || run.Integrated == nil || !run.Integrated.Passed || run.Revision != r.Revision {
		return nil, errors.New("openshell: no verified export")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	integrated := run.Integrated
	if integrated.Patch != filepath.Join(r.RunDir, "integrated.patch") {
		return nil, errors.New("openshell: unexpected export path")
	}
	if err := checkOpenShellArgv(integrated.Verify); err != nil {
		return nil, fmt.Errorf("openshell: export verification: %w", err)
	}
	patch, err := readOpenShellFile(integrated.Patch, openShellPatchLimit)
	if err != nil {
		return nil, fmt.Errorf("openshell: export: %w", err)
	}
	digest := sha256.Sum256(patch)
	if hex.EncodeToString(digest[:]) != integrated.PatchSHA256 {
		return nil, errors.New("openshell: export changed after verification")
	}
	files, err := checkOpenShellPatch(ctx, r.Repo, patch, integrated.ChangedFiles)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(files, sortedCopy(integrated.ChangedFiles)) {
		return nil, errors.New("openshell: export file list does not match verified patch")
	}
	if len(files) == 0 && !run.reviewOnly() {
		return nil, errors.New("openshell: empty export requires successful review tasks only")
	}
	tree, err := r.treeWith(ctx, patch)
	if err != nil {
		return nil, err
	}
	if tree != integrated.Tree {
		return nil, errors.New("openshell: export does not reproduce the verified tree")
	}
	return &VerifiedExport{
		Manifest: PatchManifest{
			Version: ArtifactVersion, TaskID: run.Team, Leg: string(LegOpenShell),
			BaseRevision: run.Revision, ChangedFiles: files,
			DiffDigest: integrated.PatchSHA256, DiffPath: integrated.Patch,
			Check: &CheckEvidence{Command: slices.Clone(integrated.Verify), Passed: true,
				Duration: time.Duration(integrated.Seconds * float64(time.Second))},
			CreatedAt: time.Now(),
		},
		Repository: r.Repo, Runtime: run.Runtime, RunRecord: filepath.Join(r.RunDir, "run.json"),
	}, nil
}

func openShellResultText(run *OpenShellRun, runDir string) string {
	if run == nil {
		return "OpenShell: no run record"
	}
	passed := 0
	for _, r := range run.Tasks {
		if r != nil && r.Outcome != OpenShellFailed {
			passed++
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "snapshot: %s (committed files only)\n", run.Revision)
	sb.WriteString(fmt.Sprintf("OpenShell %s: %d/%d task(s) passed in %.1fs\n", run.Verdict, passed, len(run.Tasks), run.Seconds))
	if budget := run.AttemptBudget; budget != nil && budget.Limit > 0 {
		fmt.Fprintf(&sb, "attempt admission: %d worst-case slots / %d cap (not measured usage)\n", budget.Required, budget.Limit)
	}
	if usage := run.AttemptUsage; usage != nil {
		fmt.Fprintf(&sb, "attempt usage: %d workers + %d repairs + %d director calls; %d execution(s) unmeasured\n",
			usage.Workers, usage.Repairs, usage.Directors, usage.Unmeasured)
	}
	if budget := run.CostBudget; budget != nil {
		fmt.Fprintf(&sb, "strict cost cap: $%g, $%g per worker across %d worker(s), enforced per request by each worker's Shield\n",
			budget.LimitUSD, budget.WorkerUSD, budget.Workers)
	}
	for _, res := range run.Tasks {
		if res == nil {
			continue
		}
		detail := ""
		if rep := res.Report; rep != nil && rep.Shield != nil {
			if len(rep.Shield.ServedBy) > 0 {
				detail += " served by " + strings.Join(rep.Shield.ServedBy, "/")
			}
			if b := rep.Shield.Budget; b != nil {
				detail += fmt.Sprintf(", $%.6f of $%g committed", b.CommittedUSD, b.LimitUSD)
				if b.Refused > 0 {
					detail += fmt.Sprintf(", %d request(s) refused at the cap", b.Refused)
				}
			}
		}
		if res.Error != "" {
			detail += " - " + res.Error
		}
		fmt.Fprintf(&sb, "  %s %s %.1fs%s\n", res.Task, res.Outcome, res.Seconds, detail)
	}
	sb.WriteString("run record: " + filepath.Join(runDir, "run.json") + "\n")
	return sb.String()
}
