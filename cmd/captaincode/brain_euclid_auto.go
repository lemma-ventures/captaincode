package main

// Automatic consolidation. The journal filled up (49 entries in the main
// brain, 2026-09-11) and nothing ever distilled it, because distillation only
// ran when someone typed /euclid distill. A memory that only consolidates when
// asked is a log, not a memory.
//
// After each journaled run, once the write brain has CAPTAIN_EUCLID_AUTODISTILL
// undistilled entries (default 5; 0 disables), the brain distils them in the
// background on the distill leg (gemini by default: cheap, fast). At most one
// distillation runs at a time, and never more than once per cooldown per
// brain, so a burst of runs costs one model call, not five.
//
// When the write brain is a repo's developer subtree, the main brain would
// otherwise never hear about that project again. Each consolidation leaves
// one dated line in the main brain's MEMORIES: the high-level view across
// projects that ~/.euclid is for.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const autoDistillCooldown = 30 * time.Minute

func autoDistillThreshold() int {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_EUCLID_AUTODISTILL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 5
}

type autoDistillState struct {
	mu      sync.Mutex
	running bool
	last    map[string]time.Time // brain root → last automatic distillation
	wg      sync.WaitGroup
}

// maybeAutoDistill is called after every journaled run. It returns at once;
// the distillation, if any, runs in the background.
func (b *brain) maybeAutoDistill(ws captaincode.Workspace) {
	n := autoDistillThreshold()
	if n == 0 || os.Getenv("CAPTAIN_EUCLID") == "0" {
		return
	}
	cwd := ws.Dir
	wb, ok := captaincode.WriteBrain(cwd)
	if !ok {
		return
	}
	entries, err := captaincode.ReadJournal(wb, captaincode.LastDistilledAt(wb))
	if err != nil || len(entries) < n {
		return
	}
	st := &b.autoDistill
	st.mu.Lock()
	if st.last == nil {
		st.last = map[string]time.Time{}
	}
	if st.running || time.Since(st.last[wb.Root]) < autoDistillCooldown {
		st.mu.Unlock()
		return
	}
	st.running = true
	st.last[wb.Root] = time.Now()
	st.mu.Unlock()

	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		defer func() {
			st.mu.Lock()
			st.running = false
			st.mu.Unlock()
		}()
		d, _, err := b.distill(ws, true)
		if err != nil {
			fmt.Printf("captain brain: euclid auto-distill (%d entries) failed: %v\n", len(entries), err)
			return
		}
		fmt.Printf("captain brain: euclid auto-distill: %d entries → %d edit(s) in %s\n", d.Entries, len(d.Edits), wb.Label)
		echoToMainBrain(cwd, wb, d)
	}()
}

// awaitAutoDistill waits for a background distillation to finish (tests).
func (b *brain) awaitAutoDistill() { b.autoDistill.wg.Wait() }

// echoToMainBrain leaves one dated line in the main brain's MEMORIES when a
// repo brain was consolidated, naming the repo and the distiller's summary.
func echoToMainBrain(cwd string, wb captaincode.EuclidBrain, d captaincode.Distillation) {
	if wb.Kind == "main" || strings.TrimSpace(d.Summary) == "" {
		return
	}
	main := captaincode.MainBrainPath()
	if _, err := os.Stat(main); err != nil {
		return
	}
	repo := captaincode.RepoRoot(cwd)
	name := filepath.Base(repo)
	if repo == "" {
		name = filepath.Base(cwd)
	}
	line := fmt.Sprintf("\n## %s - %s\n\n- %s\n", time.Now().Format("2006-01-02"), name, strings.TrimSpace(d.Summary))
	p := filepath.Join(main, "memory", "MEMORIES.md")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

// ── bootstrap ────────────────────────────────────────────────────────────────

// bootstrap fills a repo brain's strategy registers from the repository's own
// docs, once. Targets the SHARED repo brain (<repo>/.euclid): VISION and MAP
// are the team's, not one developer's.
func (b *brain) bootstrap(ws captaincode.Workspace, apply bool) (captaincode.Distillation, string, error) {
	cwd := ws.Dir
	repo := captaincode.RepoRoot(cwd)
	if repo == "" {
		return captaincode.Distillation{}, "", fmt.Errorf("%s is not inside a git repository", cwd)
	}
	root := filepath.Join(repo, ".euclid")
	if _, err := os.Stat(root); err != nil {
		return captaincode.Distillation{}, "", fmt.Errorf("no repo brain at %s - run `captain euclid init --repo`", root)
	}
	brain := captaincode.EuclidBrain{Root: root, Kind: "repo", Label: "repo:" + filepath.Base(repo)}
	prompt, docs := captaincode.BootstrapPrompt(brain, repo)
	if len(docs) == 0 {
		return captaincode.Distillation{Brain: brain}, "nothing to bootstrap from: no README.md, CLAUDE.md, AGENTS.md or docs/*.md in " + repo, nil
	}
	leg := b.distillLeg()
	var res captaincode.Result
	var err error
	if b.runWorkerFn != nil {
		_, res, err = b.runWorkerFn(leg, prompt, nil, nil)
	} else {
		res, err = ws.RunWorkerStreamHooks(leg, prompt, opencodePort, nil, nil)
	}
	if h := b.chargeOwnTask("memory: bootstrap " + brain.Label); h != nil {
		h(leg, "bootstrap", res, err)
	}
	if err != nil {
		return captaincode.Distillation{}, "", fmt.Errorf("bootstrap on %s: %w", leg, err)
	}
	d, err := captaincode.ParseBootstrap(res.Text)
	if err != nil {
		return captaincode.Distillation{}, "", err
	}
	d.Brain, d.Entries = brain, len(docs)
	report := fmt.Sprintf("bootstrap of %s from %d doc(s):\n%s", brain.Label, len(docs), captaincode.RenderPatch(brain, d))
	if !apply {
		return d, report + "\n(dry run - `captain euclid bootstrap --apply` writes VISION/MAP/BRAIN, only while they are still templates)\n", nil
	}
	touched, err := captaincode.ApplyBootstrap(brain, d.Edits)
	if err != nil {
		return d, report, err
	}
	if len(touched) == 0 {
		report += "\nnothing written: the strategy registers were already curated\n"
	} else {
		report += "\nwritten: " + strings.Join(touched, ", ") + "\n"
	}
	fmt.Printf("captain brain: euclid bootstrap %s from %d docs → %s\n", brain.Label, len(docs), strings.Join(touched, ","))
	// The benchmark gold sets, from the registers just filled: without them
	// the dashboard's Performance tab is a row of "not captured" notices.
	if written, err := b.authorProbes(ws, brain, repo); err != nil {
		fmt.Printf("captain brain: euclid probes for %s: %v\n", brain.Label, err)
		report += "\nprobes not written: " + err.Error() + "\n"
	} else if len(written) > 0 {
		report += "\nbenchmark probes: " + strings.Join(written, ", ") + "\n"
	}
	return d, report, nil
}

// authorProbes runs the probe pass of a bootstrap: one model call, every
// probe verified against the registers and the repository before it is
// written (ParseProbes). Existing gold sets are never overwritten - a set
// the operator curated is theirs.
func (b *brain) authorProbes(ws captaincode.Workspace, brain captaincode.EuclidBrain, repo string) ([]string, error) {
	if isDirPath(filepath.Join(brain.Root, "probes")) {
		return nil, nil
	}
	prompt, docs := captaincode.ProbePrompt(brain, repo)
	if len(docs) == 0 {
		return nil, nil
	}
	leg := b.distillLeg()
	var res captaincode.Result
	var err error
	if b.runWorkerFn != nil {
		_, res, err = b.runWorkerFn(leg, prompt, nil, nil)
	} else {
		res, err = ws.RunWorkerStreamHooks(leg, prompt, opencodePort, nil, nil)
	}
	if h := b.chargeOwnTask("memory: probes " + brain.Label); h != nil {
		h(leg, "probes", res, err)
	}
	if err != nil {
		return nil, fmt.Errorf("probes on %s: %w", leg, err)
	}
	ps, err := captaincode.ParseProbes(res.Text, brain, repo)
	if err != nil {
		return nil, err
	}
	written, err := captaincode.WriteProbes(brain, ps)
	if err != nil {
		return written, err
	}
	fmt.Printf("captain brain: euclid probes for %s: %d orientation, %d recall, %d composition (%d dropped as unverifiable) → %d file(s)\n",
		brain.Label, len(ps.Orientation), len(ps.Recall), len(ps.Composition), ps.Dropped, len(written))
	return written, nil
}

// euclidBootstrapHTTP: POST /v1/euclid/bootstrap {"apply":bool}.
func (b *brain) euclidBootstrapHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apply bool `json:"apply"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	d, report, err := b.bootstrap(workspaceOf(r), req.Apply)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"bootstrap": d, "report": report})
}
