package captaincode

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Euclid index is what makes a brain searchable: build-catalog.py reads
// the repo's docs, journal and sources (the roots in .euclid/euclid.yml) into
// .euclid/index/catalog.jsonl + graph.json, and build-dashboard.py renders
// the dashboard from it. Both are derived, cheap (well under a second on the
// brains here) and deterministic, so they are simply rebuilt at every launch
// and on demand from the dashboard's own Regenerate button - "the index is
// stale, run a command in a shell" is not an acceptable state (2026-09-12).

// IndexStep is one script run of a rebuild, in the shape the Euclid
// dashboard's Regenerate button renders (dashboard-serve.py's contract).
type IndexStep struct {
	Step       string  `json:"step"`
	Script     string  `json:"script"`
	OK         bool    `json:"ok"`
	ReturnCode int     `json:"returncode"`
	DurationS  float64 `json:"duration_s"`
	Tail       string  `json:"tail"`
}

// IndexResult is the outcome of rebuilding one brain's index and dashboard.
type IndexResult struct {
	OK         bool        `json:"ok"`
	Root       string      `json:"root"`
	Steps      []IndexStep `json:"steps"`
	FinishedAt string      `json:"finished_at"`
	Error      string      `json:"error,omitempty"`
}

// EuclidEngine locates a Euclid checkout able to build any brain's index and
// dashboard: CAPTAIN_EUCLID_ENGINE, else ~/Gits/euclid, else
// $CAPTAIN_WORKSPACE_ROOT/euclid. "" when none is installed.
func EuclidEngine() string {
	var candidates []string
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_EUCLID_ENGINE")); v != "" {
		candidates = append(candidates, v)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Gits", "euclid"))
	}
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_WORKSPACE_ROOT")); v != "" {
		candidates = append(candidates, filepath.Join(v, "euclid"))
	}
	for _, c := range candidates {
		if isFile(filepath.Join(c, "engine", "build-catalog.py")) && isFile(filepath.Join(c, "dashboard", "build-dashboard.py")) {
			return c
		}
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// indexScripts returns the catalog and dashboard builders for a brain: the
// copy vendored in <root>/bin when the brain carries one (it matches that
// brain's format), else the engine's. Static dashboard files are copied from
// staticFrom when the brain has none.
func indexScripts(root string) (catalog, dashboard, staticFrom string) {
	vendored := filepath.Join(root, "bin")
	if isFile(filepath.Join(vendored, "build-catalog.py")) && isFile(filepath.Join(vendored, "build-dashboard.py")) {
		return filepath.Join(vendored, "build-catalog.py"), filepath.Join(vendored, "build-dashboard.py"), vendored
	}
	engine := EuclidEngine()
	if engine == "" {
		return "", "", ""
	}
	return filepath.Join(engine, "engine", "build-catalog.py"), filepath.Join(engine, "dashboard", "build-dashboard.py"), filepath.Join(engine, "dashboard")
}

// IsBrainRoot says whether root is a Euclid brain directory this machine may
// rebuild: the main brain, or a <repo>/.euclid with registers in it. The
// dashboard's Regenerate button names the root it was built from, so the
// check keeps a stray request from running the engine anywhere else.
func IsBrainRoot(root string) bool {
	if root == "" || !filepath.IsAbs(root) || !isDir(root) {
		return false
	}
	root = filepath.Clean(root)
	if mb := MainBrainPath(); mb != "" && root == filepath.Clean(mb) {
		return true
	}
	return filepath.Base(root) == ".euclid" && isFile(filepath.Join(root, "BRAIN.md"))
}

// Reindex rebuilds root's catalog and, when the engine is available, its
// dashboard. root is the brain directory (…/.euclid); the scripts run in its
// host with EUCLID_ROOT set, exactly as the dashboard's own server does.
func Reindex(root string, timeout time.Duration) IndexResult {
	res := IndexResult{Root: root}
	if !IsBrainRoot(root) {
		res.Error = "not a Euclid brain: " + root
		res.FinishedAt = time.Now().Format(time.RFC3339)
		return res
	}
	catalog, dashboard, staticFrom := indexScripts(root)
	if catalog == "" {
		res.Error = "no Euclid engine found (CAPTAIN_EUCLID_ENGINE, ~/Gits/euclid or $CAPTAIN_WORKSPACE_ROOT/euclid)"
		res.FinishedAt = time.Now().Format(time.RFC3339)
		return res
	}
	host := filepath.Dir(root)
	env := append(os.Environ(), "EUCLID_ROOT="+host, "EUCLID_NO_AUTOBUILD=1")
	res.OK = true
	for _, script := range []string{catalog, dashboard} {
		step := runIndexScript(script, host, env, timeout)
		res.Steps = append(res.Steps, step)
		if !step.OK {
			res.OK = false
			break
		}
	}
	if res.OK && staticFrom != "" {
		// The page itself (index.html, app.js, style.css) is copied beside the
		// data when the brain has none, and refreshed when the engine's copy
		// is newer - a dashboard must not keep running last month's page.
		dir := filepath.Join(root, "dashboard")
		for _, f := range []string{"index.html", "app.js", "style.css"} {
			src, dst := filepath.Join(staticFrom, f), filepath.Join(dir, f)
			if !isFile(src) {
				continue
			}
			if si, err := os.Stat(src); err == nil {
				if di, err := os.Stat(dst); err == nil && !si.ModTime().After(di.ModTime()) {
					continue
				}
			}
			if b, err := os.ReadFile(src); err == nil {
				_ = os.MkdirAll(dir, 0o755)
				_ = os.WriteFile(dst, b, 0o644)
			}
		}
	}
	res.FinishedAt = time.Now().Format(time.RFC3339)
	return res
}

func runIndexScript(script, cwd string, env []string, timeout time.Duration) IndexStep {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	start := time.Now()
	cmd := exec.Command("python3", script)
	cmd.Dir = cwd
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	step := IndexStep{Step: strings.TrimSuffix(filepath.Base(script), ".py"), Script: filepath.Base(script)}
	if err := cmd.Start(); err != nil {
		step.ReturnCode, step.Tail = -1, err.Error()
		step.DurationS = time.Since(start).Seconds()
		return step
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				step.ReturnCode = ee.ExitCode()
			} else {
				step.ReturnCode = -1
			}
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		step.ReturnCode = -1
		out.WriteString("\n(killed after " + timeout.String() + ")")
	}
	step.OK = step.ReturnCode == 0
	step.DurationS = float64(time.Since(start).Milliseconds()) / 1000
	step.Tail = tailOf(out.String(), 2000)
	return step
}

func tailOf(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// EnsureReport is what a launch-time ensure did, one line per brain.
type EnsureReport struct {
	Lines []string
	Local string // the local brain's root, "" when cwd is not in a repository
}

// EnsureBrains is the launch-time step: the main brain exists, the folder's
// repo brain exists (scaffolded on first launch - VISION/MAP/BRAIN are then
// bootstrapped by the caller, which needs the brain running), and both
// indexes are fresh. Always rebuilding beats deciding staleness: the catalog
// reads the repo's files, whose changes no register mtime reflects.
func EnsureBrains(cwd string, w io.Writer) EnsureReport {
	var rep EnsureReport
	say := func(format string, a ...any) {
		line := fmt.Sprintf(format, a...)
		rep.Lines = append(rep.Lines, line)
		if w != nil {
			fmt.Fprintln(w, line)
		}
	}
	if !euclidEnabled() {
		say("euclid: off (CAPTAIN_EUCLID=0)")
		return rep
	}
	main := MainBrainPath()
	if main != "" {
		if created, err := Scaffold(main, "main"); err != nil {
			say("euclid main brain: %v", err)
		} else if len(created) > 0 {
			say("euclid main brain: created %s", main)
		}
		if isDir(main) {
			say("euclid main brain: %s", describeIndex(Reindex(main, 0)))
		}
	}
	repo := RepoRoot(cwd)
	if repo == "" {
		say("euclid local brain: none (%s is not a git repository)", cwd)
		return rep
	}
	shared := filepath.Join(repo, ".euclid")
	rep.Local = shared
	fresh := !isDir(shared)
	if created, err := Scaffold(shared, "repo"); err != nil {
		say("euclid local brain: %v", err)
		return rep
	} else if fresh || len(created) > 0 {
		say("euclid local brain: created %s (local, gitignored)", shared)
	}
	dev := filepath.Join(shared, "developers", DeveloperHandle())
	if created, err := Scaffold(dev, "developer"); err == nil && len(created) > 0 {
		say("euclid local brain: created your subtree %s", dev)
	}
	// A brain still carrying the template's corpus indexes another host's
	// folders: point it at this repository's (an edited corpus is kept).
	if b, err := os.ReadFile(filepath.Join(shared, "euclid.yml")); err == nil && IsTemplateCorpus(string(b)) {
		if roots, exts, err := ConfigureCorpus(repo); err == nil {
			say("euclid local brain: corpus set to %s (%s)", strings.Join(roots, ", "), strings.Join(exts, " "))
		}
	}
	say("euclid local brain: %s", describeIndex(Reindex(shared, 0)))
	return rep
}

func describeIndex(r IndexResult) string {
	if r.Error != "" {
		return "index not rebuilt - " + r.Error
	}
	var parts []string
	for _, s := range r.Steps {
		if s.OK {
			parts = append(parts, fmt.Sprintf("%s %.1fs", s.Step, s.DurationS))
		} else {
			parts = append(parts, fmt.Sprintf("%s FAILED (%s)", s.Step, tailOf(s.Tail, 160)))
		}
	}
	return "index rebuilt: " + strings.Join(parts, ", ")
}

// ---------------------------------------------------------- after each write

// A brain that is written every turn but indexed only at launch shows last
// night's dashboard all day ("why is euclid memory not updated???",
// 2026-09-20, the kyoei dashboard a day behind its journal). The rebuild is
// under a second, so every write to a brain schedules one: debounced, so a
// team turn journaling six workers rebuilds once, and coalesced per root, so
// a rebuild already running is followed by exactly one more.
//
// CAPTAIN_EUCLID_AUTOINDEX=0 turns it off (tests do: a background python run
// against a temp brain is not what they measure).

var autoIndexDelay = 3 * time.Second

var autoIndex struct {
	mu      sync.Mutex
	pending map[string]*time.Timer
	running map[string]bool
	again   map[string]bool
}

func autoIndexEnabled() bool { return os.Getenv("CAPTAIN_EUCLID_AUTOINDEX") != "0" }

// indexRootOf is the directory Reindex accepts for a brain: the main brain
// or the repo's .euclid - a developer subtree is indexed with its repo brain,
// which is where the dashboard lives.
func indexRootOf(b EuclidBrain) string {
	switch b.Kind {
	case "developer":
		return filepath.Dir(filepath.Dir(b.Root))
	default:
		return b.Root
	}
}

// ScheduleReindex rebuilds b's index shortly, once per burst of writes.
func ScheduleReindex(b EuclidBrain) {
	if !autoIndexEnabled() {
		return
	}
	root := indexRootOf(b)
	if !IsBrainRoot(root) {
		return
	}
	autoIndex.mu.Lock()
	defer autoIndex.mu.Unlock()
	if autoIndex.pending == nil {
		autoIndex.pending = map[string]*time.Timer{}
		autoIndex.running = map[string]bool{}
		autoIndex.again = map[string]bool{}
	}
	if t, ok := autoIndex.pending[root]; ok {
		t.Reset(autoIndexDelay)
		return
	}
	autoIndex.pending[root] = time.AfterFunc(autoIndexDelay, func() { autoReindex(root) })
}

func autoReindex(root string) {
	autoIndex.mu.Lock()
	delete(autoIndex.pending, root)
	if autoIndex.running[root] {
		autoIndex.again[root] = true // one more after this one, whatever the count
		autoIndex.mu.Unlock()
		return
	}
	autoIndex.running[root] = true
	autoIndex.mu.Unlock()

	for {
		res := Reindex(root, 2*time.Minute)
		if !res.OK {
			fmt.Fprintf(os.Stderr, "captain brain: euclid index of %s not rebuilt - %s\n", root, describeIndex(res))
		}
		autoIndex.mu.Lock()
		if !autoIndex.again[root] {
			autoIndex.running[root] = false
			autoIndex.mu.Unlock()
			return
		}
		autoIndex.again[root] = false
		autoIndex.mu.Unlock()
	}
}
