package captaincode

import (
	"bytes"
	"errors"
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

// EuclidEngine is the Euclid checkout named by CAPTAIN_EUCLID_ENGINE, "" when
// unset or not a checkout. No other directory is searched.
func EuclidEngine() string {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_EUCLID_ENGINE")); v != "" && isEngineCheckout(v) {
		return v
	}
	return ""
}

func isEngineCheckout(dir string) bool {
	return isFile(filepath.Join(dir, "engine", "build-catalog.py")) && isFile(filepath.Join(dir, "dashboard", "build-dashboard.py"))
}

// engineFor is the engine that builds root's index: the brain's own host when
// that host is a Euclid checkout (the Euclid repository's own brain), else
// the configured engine. Both are named by the request or the operator; no
// sibling directory is searched.
func engineFor(root string) string {
	if host := filepath.Dir(filepath.Clean(root)); filepath.Base(root) == ".euclid" && isEngineCheckout(host) {
		return host
	}
	return EuclidEngine()
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
	engine := engineFor(root)
	if engine == "" {
		return "", "", ""
	}
	return filepath.Join(engine, "engine", "build-catalog.py"), filepath.Join(engine, "dashboard", "build-dashboard.py"), filepath.Join(engine, "dashboard")
}

// vendorScripts keeps the command the dashboard prints - the one a human
// runs from the host root,
//
//	python3 .euclid/bin/build-catalog.py && python3 .euclid/bin/build-dashboard.py
//
// true: the first rebuild copies the engine's build scripts, its page and the
// budget checks into <root>/bin, and every later rebuild refreshes a copy the
// engine has moved past (engine mtime newer). The copy is what runs then, so
// a brain keeps building its own dashboard when the engine checkout is not on
// the machine, and `script_path`-style name lookups (benchmarks, .sh checks)
// resolve inside the brain instead of across machines.
func vendorScripts(root string) (copied int, err error) {
	engine := engineFor(root)
	if engine == "" || !isDir(root) {
		return 0, nil
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return 0, err
	}
	static := map[string]bool{"index.html": true, "app.js": true, "style.css": true}
	var trees []string
	for _, d := range []string{"engine", "dashboard", "benchmarks"} {
		if p := filepath.Join(engine, d); isDir(p) {
			trees = append(trees, p)
		}
	}
	for _, dir := range trees {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".py") && !strings.HasSuffix(name, ".sh") && !static[name] {
				continue
			}
			src := filepath.Join(dir, name)
			dst := filepath.Join(bin, name)
			si, serr := os.Stat(src)
			if serr != nil {
				continue
			}
			if di, derr := os.Stat(dst); derr == nil && !si.ModTime().After(di.ModTime()) {
				continue
			}
			b, rerr := os.ReadFile(src)
			if rerr != nil {
				continue
			}
			if werr := os.WriteFile(dst, b, 0o644); werr != nil {
				return copied, werr
			}
			copied++
		}
	}
	if copied > 0 && filepath.Base(root) == ".euclid" {
		ignoreVendoredBin(root)
	}
	return copied, nil
}

// ignoreVendoredBin keeps a repository brain that commits its registers from
// committing the vendored scripts too: they are derived, like index/ and
// dashboard/ in the brain's own .gitignore.
func ignoreVendoredBin(root string) {
	p := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	if stringHasLine(string(b), "bin/") {
		return
	}
	out := b
	if len(out) > 0 && !strings.HasSuffix(string(out), "\n") {
		out = append(out, '\n')
	}
	out = append(out, "bin/\n"...)
	_ = os.WriteFile(p, out, 0o644)
}

func stringHasLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
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
	return reindexWithOptions(root, timeout, false)
}

// ReindexFast rebuilds root's catalog and dashboard, skipping the slow live
// benchmark suite. Used for automatic reindexing (journal bursts, periodic check).
func ReindexFast(root string, timeout time.Duration) IndexResult {
	return reindexWithOptions(root, timeout, true)
}

func reindexWithOptions(root string, timeout time.Duration, fast bool) IndexResult {
	res := IndexResult{Root: root}
	if !IsBrainRoot(root) {
		res.Error = "not a Euclid brain: " + root
		res.FinishedAt = time.Now().Format(time.RFC3339)
		return res
	}
	// Vended first: the path the rebuild reports (and the one the dashboard
	// prints for a manual run) is then the brain's own bin copy, not the
	// engine checkout the page was built from.
	// A copy failure is not a rebuild failure: indexScripts falls back to the
	// engine checkout, and the step report names whichever path really ran.
	_, _ = vendorScripts(root)
	catalog, dashboard, staticFrom := indexScripts(root)
	if catalog == "" {
		res.Error = "no Euclid engine found (CAPTAIN_EUCLID_ENGINE)"
		res.FinishedAt = time.Now().Format(time.RFC3339)
		return res
	}
	host := filepath.Dir(root)
	env := append(os.Environ(), "EUCLID_ROOT="+host, "EUCLID_NO_AUTOBUILD=1")
	if fast {
		env = append(env, "EUCLID_SKIP_BENCHMARKS=1")
	}
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
	if res.OK {
		idxPath := filepath.Join(root, "dashboard", "index.html")
		if isFile(idxPath) {
			now := time.Now()
			_ = os.Chtimes(idxPath, now, now)
		}
	}
	res.FinishedAt = time.Now().Format(time.RFC3339)
	return res
}

func runIndexScript(script, cwd string, env []string, timeout time.Duration) IndexStep {
	name := filepath.Base(script)
	if rel, err := filepath.Rel(cwd, script); err == nil && !strings.HasPrefix(rel, "..") {
		// The path as the host sees it - `.euclid/bin/build-dashboard.py` - so
		// the step a report names is the command a human can run.
		name = rel
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	start := time.Now()
	cmd := exec.Command("python3", script)
	cmd.Dir = cwd
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	step := IndexStep{Step: strings.TrimSuffix(filepath.Base(script), ".py"), Script: name}
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

var ensureState struct {
	mu      sync.Mutex
	running bool
	again   bool
	last    map[string]time.Time
}

// BrainNeedsReindex reports whether any journal file or register file in root
// is newer than root/dashboard/index.html, or if the index/catalog is missing.
func BrainNeedsReindex(root string) bool {
	if !IsBrainRoot(root) {
		return false
	}
	idxPath := filepath.Join(root, "dashboard", "index.html")
	idxStat, err := os.Stat(idxPath)
	if err != nil {
		return true // dashboard missing or not built
	}
	if !isFile(filepath.Join(root, "index", "catalog.jsonl")) && !isFile(filepath.Join(root, "index", "build-catalog.py.ran")) {
		return true // catalog missing
	}
	idxMtime := idxStat.ModTime()

	// 1. Check journal/
	jdir := filepath.Join(root, "journal")
	if entries, err := os.ReadDir(jdir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".md") {
				if fi, err := e.Info(); err == nil && fi.ModTime().After(idxMtime) {
					return true
				}
			}
		}
	}

	// 2. Check register files in root
	regFiles := []string{
		"BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md",
		"SOUL.md", "VISION.md", "MAP.md",
	}
	for _, name := range regFiles {
		if fi, err := os.Stat(filepath.Join(root, name)); err == nil {
			if fi.ModTime().After(idxMtime) {
				return true
			}
		}
	}

	// 3. Check memory/
	memDir := filepath.Join(root, "memory")
	if entries, err := os.ReadDir(memDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				if fi, err := e.Info(); err == nil && fi.ModTime().After(idxMtime) {
					return true
				}
			}
		}
	}

	// 4. Check developers/
	devDir := filepath.Join(root, "developers")
	if devs, err := os.ReadDir(devDir); err == nil {
		for _, d := range devs {
			if !d.IsDir() {
				continue
			}
			devRoot := filepath.Join(devDir, d.Name())
			if entries, err := os.ReadDir(filepath.Join(devRoot, "journal")); err == nil {
				for _, e := range entries {
					if !e.IsDir() && (strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), ".md")) {
						if fi, err := e.Info(); err == nil && fi.ModTime().After(idxMtime) {
							return true
						}
					}
				}
			}
			for _, name := range regFiles {
				if fi, err := os.Stat(filepath.Join(devRoot, name)); err == nil {
					if fi.ModTime().After(idxMtime) {
						return true
					}
				}
			}
			if entries, err := os.ReadDir(filepath.Join(devRoot, "memory")); err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
						if fi, err := e.Info(); err == nil && fi.ModTime().After(idxMtime) {
							return true
						}
					}
				}
			}
		}
	}

	return false
}

// EnsureBrains is the launch-time step: the main brain exists, the folder's
// repo brain exists (scaffolded on first launch - VISION/MAP/BRAIN are then
// bootstrapped by the caller, which needs the brain running), and both
// indexes are fresh. A second launch coalesces rather than starting a second
// full ensure.
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

	ensureState.mu.Lock()
	if ensureState.running {
		ensureState.again = true
		ensureState.mu.Unlock()
		say("euclid: ensure already running (coalesced)")
		return rep
	}
	ensureState.running = true
	if ensureState.last == nil {
		ensureState.last = map[string]time.Time{}
	}
	ensureState.mu.Unlock()
	defer func() {
		ensureState.mu.Lock()
		ensureState.running = false
		ensureState.mu.Unlock()
	}()

	main := MainBrainPath()
	if main != "" {
		if created, err := Scaffold(main, "main"); err != nil {
			say("euclid main brain: %v", err)
		} else if len(created) > 0 {
			say("euclid main brain: created %s", main)
		}
		if isDir(main) {
			ensureState.mu.Lock()
			lastMain := ensureState.last[main]
			ensureState.mu.Unlock()
			if time.Since(lastMain) < 2*time.Minute && !BrainNeedsReindex(main) {
				say("euclid main brain: fresh (no changes)")
			} else {
				say("euclid main brain: %s", describeIndex(Reindex(main, 0)))
				ensureState.mu.Lock()
				ensureState.last[main] = time.Now()
				ensureState.mu.Unlock()
			}
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
	ensureState.mu.Lock()
	lastShared := ensureState.last[shared]
	ensureState.mu.Unlock()
	if time.Since(lastShared) < 2*time.Minute && !BrainNeedsReindex(shared) {
		say("euclid local brain: fresh (no changes)")
	} else {
		say("euclid local brain: %s", describeIndex(Reindex(shared, 0)))
		ensureState.mu.Lock()
		ensureState.last[shared] = time.Now()
		ensureState.mu.Unlock()
	}
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
// 2026-09-20, the zorvex dashboard a day behind its journal). The rebuild is
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
		res := ReindexFast(root, 2*time.Minute)
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
