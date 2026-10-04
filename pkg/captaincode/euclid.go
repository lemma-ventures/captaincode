package captaincode

// Euclid brains (MM38, phases E1–E2): Captain reads and writes Euclid
// memory - the git-native registers `~/src/euclid` serves - so a worker no
// longer starts every turn from the replayed conversation alone.
//
// Brains and homes:
//
//	~/.euclid/                       the developer's MAIN brain (never committed)
//	<repo>/.euclid/                  the REPO brain (local; scaffolded by init --repo)
//	<repo>/.euclid/developers/<h>/   this developer's subtree (local, same tree)
//
// A session composes a READ SET (own subtree → repo shared → main brain) and
// exactly ONE write brain (own subtree when the repo has a brain, else the
// main brain). Everything Captain learns from a run lands in the write brain
// and nowhere else; foreign brains are read, never modified.
//
// Opt-in by construction: without `captain euclid init` no `~/.euclid` exists,
// so no orientation is injected and nothing is journaled - a run in a repo
// without a brain is unchanged. CAPTAIN_EUCLID=0 disables everything. The
// whole `.euclid/` tree is gitignored while Euclid is not the default install.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EuclidBrain is one memory root in a session's read set.
type EuclidBrain struct {
	Root     string  // directory holding the registers
	Kind     string  // main | repo | developer | linked | developer-other
	Writable bool    // the session's single write brain
	Label    string  // for provenance in prompts ("repo:ash", "me", "main")
	Weight   float64 // retrieval rank: 1 for own brains, link weight for linked repos, lowest for other developers
}

// euclidEnabled: CAPTAIN_EUCLID=0 turns the whole layer off.
func euclidEnabled() bool { return os.Getenv("CAPTAIN_EUCLID") != "0" }

// MainBrainPath is ~/.euclid (EUCLID_HOME overrides).
func MainBrainPath() string {
	if v := strings.TrimSpace(os.Getenv("EUCLID_HOME")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".euclid")
}

var handleRe = regexp.MustCompile(`[^a-z0-9]+`)

// DeveloperHandle identifies this developer's subtree: EUCLID_HANDLE, else
// the `handle` file in the main brain, else the local part of git's
// user.email slugified, else "me".
func DeveloperHandle() string {
	if v := strings.TrimSpace(os.Getenv("EUCLID_HANDLE")); v != "" {
		return slugHandle(v)
	}
	if mb := MainBrainPath(); mb != "" {
		if b, err := os.ReadFile(filepath.Join(mb, "handle")); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return slugHandle(v)
			}
		}
	}
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		email := strings.TrimSpace(string(out))
		if i := strings.IndexByte(email, '@'); i > 0 {
			email = email[:i]
		}
		if email != "" {
			return slugHandle(email)
		}
	}
	return "me"
}

func slugHandle(s string) string {
	s = strings.Trim(handleRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "me"
	}
	return s
}

// RepoRoot finds the git toplevel of dir, else walks up for a .euclid dir,
// else "".
func RepoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	if out, err := cmd.Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top
		}
	}
	main := MainBrainPath()
	for d := dir; ; d = filepath.Dir(d) {
		// ~/.euclid is the MAIN brain, not the brain of whatever folder sits
		// under $HOME; walking up must not turn $HOME into a repo root.
		if p := filepath.Join(d, ".euclid"); p != main {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return d
			}
		}
		// A .git that git itself would not vouch for (git absent, a bare
		// marker) still names the root.
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// ReadSet composes the brains a session in cwd may read, in precedence
// order, with exactly one marked writable. Empty when Euclid is off or no
// brain exists anywhere.
func ReadSet(cwd string) []EuclidBrain { return ReadSetWith(cwd, nil) }

// ReadSetWith is ReadSet plus the repo brains of other repositories the
// task named (reporefs.go): read-only, after the session's own brains and
// before the linked ones - the user pointed at them, a link only implies.
func ReadSetWith(cwd string, also []string) []EuclidBrain {
	if !euclidEnabled() {
		return nil
	}
	set := readSet(cwd)
	if len(also) == 0 {
		return set
	}
	seen := map[string]bool{}
	for _, b := range set {
		seen[b.Root] = true
	}
	var named []EuclidBrain
	for _, root := range also {
		shared := filepath.Join(root, ".euclid")
		if seen[shared] || !isDir(shared) {
			continue
		}
		seen[shared] = true
		named = append(named, EuclidBrain{Root: shared, Kind: "named", Label: "repo:" + filepath.Base(root), Weight: 1})
	}
	if len(named) == 0 {
		return set
	}
	// Own brains, the named ones, then the rest (linked).
	var own, rest []EuclidBrain
	for _, b := range set {
		if b.Kind == "linked" {
			rest = append(rest, b)
		} else {
			own = append(own, b)
		}
	}
	return append(append(own, named...), rest...)
}

func readSet(cwd string) []EuclidBrain {
	var set []EuclidBrain
	handle := DeveloperHandle()
	if repo := RepoRoot(cwd); repo != "" {
		shared := filepath.Join(repo, ".euclid")
		if isDir(shared) {
			name := filepath.Base(repo)
			dev := filepath.Join(shared, "developers", handle)
			if isDir(dev) {
				set = append(set, EuclidBrain{Root: dev, Kind: "developer", Writable: true, Label: "me@" + name})
			}
			set = append(set, EuclidBrain{Root: shared, Kind: "repo", Label: "repo:" + name})
		}
	}
	if mb := MainBrainPath(); mb != "" && isDir(mb) {
		set = append(set, EuclidBrain{Root: mb, Kind: "main", Label: "main"})
	}
	for i := range set {
		set[i].Weight = 1
	}
	// Linked repos' shared brains (E3): read-only, ranked by link weight.
	if repo := RepoRoot(cwd); repo != "" {
		set = append(set, LinkedBrains(repo)...)
	}
	// One write brain: the developer subtree if present, else the main brain.
	hasWritable := false
	for _, b := range set {
		if b.Writable {
			hasWritable = true
		}
	}
	if !hasWritable {
		for i := range set {
			if set[i].Kind == "main" {
				set[i].Writable = true
				break
			}
		}
	}
	return set
}

// WriteBrain returns the session's single write brain (ok=false when none).
func WriteBrain(cwd string) (EuclidBrain, bool) {
	for _, b := range ReadSet(cwd) {
		if b.Writable {
			return b, true
		}
	}
	return EuclidBrain{}, false
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ---------------------------------------------------------------- orientation

// orientationBudget caps the injected block: ~2.5k chars (~600 tokens) is
// under 5% of a compacted worker prompt. CAPTAIN_EUCLID_ORIENTATION_CHARS
// overrides.
func orientationBudget() int {
	if v := os.Getenv("CAPTAIN_EUCLID_ORIENTATION_CHARS"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 200 {
			return n
		}
	}
	return 2500
}

func registerThesis(text string, limit int) string {
	body := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(text, "")
	body = regexp.MustCompile(`(?s)^\s*---\n.*?\n---\n`).ReplaceAllString(body, "")
	var blocks []string
	for _, block := range regexp.MustCompile(`\n\s*\n`).Split(body, -1) {
		if strings.Contains(block, "snapshot, not a log") || strings.Contains(block, "Merge overlapping") || strings.Contains(block, "Judgment proven") || strings.Contains(block, "Transferable lessons proven") || strings.Contains(block, "Heuristics that earned") {
			continue
		}
		blocks = append(blocks, block)
	}
	body = strings.Join(blocks, "\n\n")
	lines := strings.Split(body, "\n")
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "## ") {
			start = i + 1
			break
		}
	}
	var out []string
	for _, l := range lines[start:] {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ">") || strings.HasSuffix(t, ":") || strings.HasPrefix(t, "- (") {
			continue
		}
		if strings.Contains(t, "snapshot, not a log") || strings.Contains(t, "Merge overlapping") || strings.HasPrefix(t, "Judgment proven") || strings.HasPrefix(t, "Transferable lessons proven") || strings.HasPrefix(t, "Heuristics that earned") || strings.HasPrefix(t, "lines on every update") || strings.HasPrefix(t, "statements; retire") {
			continue
		}
		out = append(out, t)
	}
	return clipText(strings.Join(strings.Fields(strings.Join(out, " ")), " "), limit)
}

func clipText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit]) + "…"
}

type orientationCache struct {
	mu   sync.Mutex
	key  string
	at   time.Time
	text string
}

var orientCache orientationCache

// Orientation renders the `<euclid>` block for cwd: the write brain's BRAIN
// snapshot first (it is the freshest "you are here"), the repo's MAP, then
// the other brains' BRAIN theses, within the budget. Empty when no brain.
// Cached for 30s per cwd - it is rendered into every worker prompt.
func Orientation(cwd string) string { return OrientationWith(cwd, nil) }

// OrientationWith is Orientation over ReadSetWith: the named repos' brains
// are in the block too.
func OrientationWith(cwd string, also []string) string {
	if !euclidEnabled() {
		return ""
	}
	if memoryMCPEnabled() {
		return memoryOrientation(ReadSetWith(cwd, also), orientationBudget())
	}
	key := cwd + "\x00" + strings.Join(also, "\x00")
	orientCache.mu.Lock()
	defer orientCache.mu.Unlock()
	if orientCache.key == key && time.Since(orientCache.at) < 30*time.Second {
		return orientCache.text
	}
	text := renderOrientation(ReadSetWith(cwd, also), orientationBudget())
	orientCache.key, orientCache.at, orientCache.text = key, time.Now(), text
	return text
}

func renderOrientation(set []EuclidBrain, budget int) string {
	if len(set) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n<euclid>\nThis project carries Euclid memory (git-native registers). Consult it before re-deriving where things live or what was decided; the write brain's journal records every prior run.\n")
	used := sb.Len()
	add := func(s string) bool {
		if used+len(s) > budget {
			return false
		}
		sb.WriteString(s)
		used += len(s)
		return true
	}
	read := func(root, name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return ""
		}
		return string(b)
	}
	// 1. BRAIN snapshots, write brain first.
	for _, b := range set {
		if t := registerThesis(read(b.Root, "BRAIN.md"), 600); t != "" {
			if !add(fmt.Sprintf("<brain source=%q>%s</brain>\n", b.Label, t)) {
				break
			}
		}
	}
	// 3. WISDOM theses (lessons), shortest first so several fit.
	for _, b := range set {
		if t := registerThesis(read(b.Root, "WISDOM.md"), 400); t != "" {
			if !add(fmt.Sprintf("<wisdom source=%q>%s</wisdom>\n", b.Label, t)) {
				break
			}
		}

	}
	// 4. Accepted lessons from learning store, if present.
	for _, b := range set {
		if raw := read(b.Root, "learning/state.json"); raw != "" {
			var state struct {
				Lessons map[string]struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Text   string `json:"text"`
				} `json:"lessons"`
			}
			if err := json.Unmarshal([]byte(raw), &state); err == nil && len(state.Lessons) > 0 {
				var ids []string
				for id := range state.Lessons {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					l := state.Lessons[id]
					if l.Status == "accepted" && strings.TrimSpace(l.Text) != "" {
						line := fmt.Sprintf("<memory source=%q>%s</memory>\n", html.EscapeString(b.Label+":lesson:"+l.ID), html.EscapeString(l.Text))
						if !add(line) {
							break
						}
					}
				}
			}
		}
	}
	// 2. The repo MAP (shared brain), table rows only, clipped.
	for _, b := range set {
		if b.Kind != "repo" {
			continue
		}
		if m := read(b.Root, "MAP.md"); m != "" {
			var rows []string
			for _, line := range strings.Split(m, "\n") {
				if strings.HasPrefix(line, "|") && !strings.HasPrefix(line, "|--") && !strings.Contains(line, "| area |") {
					rows = append(rows, strings.TrimSpace(line))
				}
			}
			if len(rows) > 0 {
				block := "<map source=\"" + b.Label + "\">\n" + strings.Join(rows, "\n") + "\n</map>\n"
				if used+len(block) > budget {
					block = clipText(block, budget-used-12) + "\n</map>\n"
				}
				add(block)
			}
		}
	}

	sb.WriteString("</euclid>\n")
	return sb.String()
}

// ------------------------------------------------------------------- journal

// JournalEntry is one run, as the write brain's journal records it.
type JournalEntry struct {
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"` // worker | review | distill
	Task       string    `json:"task"`
	Leg        string    `json:"leg,omitempty"`
	Outcome    string    `json:"outcome"` // ok | partial | failed
	DurationMs int64     `json:"duration_ms,omitempty"`
	Repo       string    `json:"repo,omitempty"` // repo root the run worked in
	Files      []string  `json:"files,omitempty"`
	Log        string    `json:"log,omitempty"`
	Error      string    `json:"error,omitempty"`
	Chars      int       `json:"chars,omitempty"`
	Tokens     int       `json:"tokens,omitempty"`   // the leg's reported token total (0 = unknown)
	CostUSD    float64   `json:"cost_usd,omitempty"` // when the leg reports a price
	Summary    string    `json:"summary,omitempty"`  // the deliverable's opening, for the markdown entry
	Score      float64   `json:"score,omitempty"`    // a review's quality 0-10 (kind=review)
	Verdict    string    `json:"verdict,omitempty"`  // a review's verdict: good | acceptable | poor
	Reviewer   string    `json:"reviewer,omitempty"` // the director leg that graded (kind=review)
}

// Scrub replaces secret-shaped substrings before a line is committed to a
// brain that may be pushed - the redaction engine's patterns (redact.go),
// flat replacement, nothing vaulted.
func Scrub(s string) string { out, _ := MaskSecrets(s, "<redacted>"); return out }

// JournalPath is the day file inside a brain.
func JournalPath(brain EuclidBrain, at time.Time) string {
	return filepath.Join(brain.Root, "journal", "activity-"+at.Format("2006-01-02")+".jsonl")
}

// JournalRun appends one entry to cwd's write brain. No brain → no-op, nil.
func JournalRun(cwd string, e JournalEntry) (string, error) {
	b, ok := WriteBrain(cwd)
	if !ok {
		return "", nil
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Repo == "" {
		e.Repo = RepoRoot(cwd)
	}
	e.Task = Scrub(clipText(strings.Join(strings.Fields(e.Task), " "), 300))
	e.Error = Scrub(clipText(e.Error, 200))
	sort.Strings(e.Files)
	if memoryMCPEnabled() {
		return recordMemoryEvent(b, memoryEntry(e, entryKeys([]JournalEntry{e})[0], "journal"))
	}
	path := JournalPath(b, e.At)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		return "", err
	}
	writeJournalEntryMarkdown(b, e)
	ScheduleReindex(b)
	return path, nil
}

// writeJournalEntryMarkdown is the same run as one Euclid journal page:
// <brain>/journal/<date>_<hhmm>Z_<slug>.md with the canonical
// `**Tokens-Spent**:` field. The dashboard's Tokens & cost tab reads those
// pages and the catalog indexes them (the journal root), so past runs are
// searchable by what they did - the jsonl is captain's own ledger, invisible
// to both. Distills and titles are bookkeeping and get no page.
func writeJournalEntryMarkdown(b EuclidBrain, e JournalEntry) {
	if e.Kind != "worker" {
		return
	}
	slug := strings.ToLower(strings.Join(strings.Fields(clipText(e.Task, 48)), "-"))
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, slug)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "run"
	}
	at := e.At.UTC()
	name := fmt.Sprintf("%s_%sZ_%s-%s.md", at.Format("2006-01-02"), at.Format("1504"), e.Leg, slug)
	tokens := "unknown"
	if e.Tokens > 0 {
		tokens = strconv.Itoa(e.Tokens)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ntype: journal\ntitle: %q\nsummary: %q\ndate: %s\ntags: [captain, %s, %s]\n---\n", clipText(e.Task, 100), clipText(e.Task, 200), at.Format("2006-01-02"), e.Leg, e.Outcome)
	fmt.Fprintf(&sb, "# %s\n\n", clipText(e.Task, 100))
	fmt.Fprintf(&sb, "**Tokens-Spent**: %s\n", tokens)
	if e.CostUSD > 0 {
		fmt.Fprintf(&sb, "**Cost-USD**: %.4f\n", e.CostUSD)
	}
	fmt.Fprintf(&sb, "**Leg**: %s · **Outcome**: %s · **Duration**: %s\n\n", e.Leg, e.Outcome, (time.Duration(e.DurationMs) * time.Millisecond).Round(time.Second))
	fmt.Fprintf(&sb, "## Task\n\n%s\n\n", e.Task)
	if e.Summary != "" {
		fmt.Fprintf(&sb, "## Did\n\n%s\n\n", Scrub(e.Summary))
	}
	if len(e.Files) > 0 {
		sb.WriteString("## Files\n\n")
		for _, f := range e.Files {
			fmt.Fprintf(&sb, "- `%s`\n", f)
		}
		sb.WriteString("\n")
	}
	if e.Error != "" {
		fmt.Fprintf(&sb, "## Error\n\n%s\n\n", e.Error)
	}
	if e.Log != "" {
		fmt.Fprintf(&sb, "Log: `%s`\n", e.Log)
	}
	_ = os.WriteFile(filepath.Join(b.Root, "journal", name), []byte(sb.String()), 0o644)
}

// FilesFromWorkerLog extracts the paths a worker touched from its status
// lines ("[12s] ⚙ edit a.go, b.go", "⚙ Read pkg/x.go", "⚙ Write …").
var logStatusRe = regexp.MustCompile(`(?m)^\[[^\]]*\]\s+⚙\s+(\S+)\s+(.*)$`)

func FilesFromWorkerLog(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range logStatusRe.FindAllStringSubmatch(string(data), -1) {
		tool, detail := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		switch tool {
		case "read", "edit", "write", "multiedit", "notebookedit", "file_change":
		default:
			continue
		}
		for _, p := range strings.Split(detail, ",") {
			p = strings.TrimSpace(strings.TrimSuffix(p, "…"))
			if p == "" || strings.ContainsAny(p, " \t") || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ReadJournal returns entries in a brain since a time (all when zero).
func ReadJournal(brain EuclidBrain, since time.Time) ([]JournalEntry, error) {
	dir := filepath.Join(brain.Root, "journal")
	names, err := filepath.Glob(filepath.Join(dir, "activity-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []JournalEntry
	for _, n := range names {
		data, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var e JournalEntry
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			if !since.IsZero() && !e.At.After(since) {
				continue
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ scaffold

func templateDir() string {
	if v := os.Getenv("EUCLID_TEMPLATE_DIR"); v != "" && isDir(v) {
		return v
	}
	return ""
}

var builtinRegisters = map[string]string{
	"SOUL.md":                    "# SOUL - charter\n\nWhat this brain is for and how it is allowed to change. Doctrine, not narrative.\n",
	"VISION.md":                  "# VISION - where this is going\n\nThe strategy this work serves. Mirror the sources; do not invent.\n",
	"BRAIN.md":                   "# BRAIN - live world-model\n\nA \"you are here\" snapshot, not a log. Prune stale lines on every update.\n\n## Current state\n\n- Active front:\n- Last landed:\n- Next gate:\n",
	"WISDOM.md":                  "# WISDOM - lessons that held\n\nHeuristics that earned their place. Merge overlapping ones; drop the ones that stopped being true.\n",
	"INTUITION.md":               "# INTUITION - hunches not yet proven\n\nOne line each, tagged [open]. A hunch that held moves to WISDOM. A hunch that failed moves to FAILURES.\n",
	"AFFECT.md":                  "# AFFECT - stance and calibration\n\n## Stance\n\nHow sure to be, and when to stop.\n",
	"MAP.md":                     "# MAP - navigation surface\n\nWhere things live, for a cold-started agent.\n\n| area | path | what lives there |\n|------|------|------------------|\n",
	"memory/MEMORIES.md":         "# MEMORIES - append-only\n",
	"memory/FAILURES.md":         "# FAILURES - episodes folded into guardrails\n",
	"memory/decisions-ledger.md": "# Decisions ledger - append-only\n",
	"memory/open-questions.md":   "# Open questions\n",
}

// Scaffold creates a brain at root (main, repo shared, or a developer
// subtree) from Euclid's template when available. Existing files are never
// overwritten. Returns the files it created.
func Scaffold(root, kind string) ([]string, error) {
	if root == "" {
		return nil, errors.New("no brain path")
	}
	var created []string
	write := func(rel, content string) error {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
		created = append(created, p)
		return nil
	}
	names := []string{"SOUL.md", "VISION.md", "BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md", "MAP.md",
		"memory/MEMORIES.md", "memory/FAILURES.md", "memory/decisions-ledger.md", "memory/open-questions.md"}
	if kind == "developer" { // a subtree carries the personal registers only
		names = []string{"BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md", "memory/MEMORIES.md", "memory/FAILURES.md"}
	} else if kind == "repo" {
		// The index and the dashboard are DERIVED from the corpus (Euclid
		// NADR-0017): rebuilt at every launch, never committed - a data.json
		// in a PR is a merge conflict waiting to happen.
		if err := write(".gitignore", "# derived by the Euclid engine at every captain launch - never commit\nindex/\ndashboard/\n# each developer's own brain (journal, notes, personal distill) is local;\n# what is worth sharing is promoted as a note file (notes/) and folded on main\ndevelopers/\n"); err != nil {
			return created, err
		}
		// Where promoted notes wait for the fold on main (euclid_share.go).
		if err := write(filepath.Join(SharedNotesDir, ".gitkeep"), ""); err != nil {
			return created, err
		}
	}
	tpl := templateDir()
	for _, n := range names {
		content := builtinRegisters[n]
		if tpl != "" {
			if b, err := os.ReadFile(filepath.Join(tpl, n)); err == nil {
				content = string(b)
			}
		}
		// A fresh repo brain inherits DOCTRINE from the main brain: SOUL (how the
		// assistant works) and WISDOM (lessons that transfer) are not project
		// facts, so starting every repo from the blank template throws away what
		// the developer already learned. VISION, BRAIN and MAP are per project
		// and stay templates until bootstrapped from the repo's own docs.
		if kind == "repo" && (n == "SOUL.md" || n == "WISDOM.md") {
			if inherited := inheritedRegister(n); inherited != "" {
				content = inherited
			}
		}
		if err := write(n, content); err != nil {
			return created, err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "journal"), 0o755); err != nil {
		return created, err
	}
	if kind == "repo" {
		cfg := "# Euclid - repo brain configuration (Captain Code)\nkind: repo\n# Cross-repo links (E3): depends_on / related, as repo URLs or local names.\nlinks:\n  depends_on: []\n  related: []\n"
		if tpl != "" {
			if b, err := os.ReadFile(filepath.Join(tpl, "euclid.yml")); err == nil {
				cfg = string(b) + "\n# Captain Code additions\nlinks:\n  depends_on: []\n  related: []\n"
			}
		}
		// The template's corpus is another host's: index THIS repository.
		if roots, exts := DetectCorpus(filepath.Dir(root)); len(roots) > 1 || len(exts) > 0 {
			if len(exts) == 0 {
				exts = []string{".py"}
			}
			cfg = ApplyCorpus(cfg, roots, exts)
		}
		if err := write("euclid.yml", cfg); err != nil {
			return created, err
		}
		if err := write("developers/README.md", "One subtree per developer (`developers/<handle>/`). Each developer commits only their own; Captain writes a session's learnings there.\n"); err != nil {
			return created, err
		}
	}
	if kind == "main" {
		if err := write("links.yml", "# Repos this developer works in and cross-repo links (E3).\nrepos: []\nlinks: []\n"); err != nil {
			return created, err
		}
	}
	return created, nil
}

// ------------------------------------------------------------------- distill

// Register edits proposed by a distillation.
type RegisterEdit struct {
	File   string `json:"file"`   // BRAIN.md | WISDOM.md | memory/MEMORIES.md | memory/FAILURES.md | memory/decisions-ledger.md
	Mode   string `json:"mode"`   // append | replace_section
	Anchor string `json:"anchor"` // for replace_section: the "## …" heading whose body is replaced
	Text   string `json:"text"`
	Why    string `json:"why"`
}

// Distillation is the model's proposal plus the journal window it covers.
type Distillation struct {
	Brain   EuclidBrain    `json:"brain"`
	Since   time.Time      `json:"since"`
	Entries int            `json:"entries"`
	Summary string         `json:"summary"`
	Edits   []RegisterEdit `json:"edits"`
}

var allowedEditFiles = map[string]bool{
	"BRAIN.md": true, "WISDOM.md": true, "INTUITION.md": true, "AFFECT.md": true,
	"memory/MEMORIES.md": true, "memory/FAILURES.md": true, "memory/decisions-ledger.md": true,
}

// crystallizeFiles is the main brain's persona, minus MEMORIES. The echo
// already appended the episode; crystallize turns those episodes into the
// registers the explorer was leaving blank.
var crystallizeFiles = map[string]bool{
	"BRAIN.md": true, "WISDOM.md": true, "INTUITION.md": true, "AFFECT.md": true,
	"memory/FAILURES.md": true, "memory/open-questions.md": true,
}

// inheritedRegister returns the main brain's copy of a doctrine register when
// it has moved past the template, else "".
func inheritedRegister(name string) string {
	mb := MainBrainPath()
	if mb == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(mb, name))
	if err != nil {
		return ""
	}
	content := string(b)
	if strings.TrimSpace(content) == strings.TrimSpace(builtinRegisters[name]) || registerThesis(content, 600) == "" {
		return ""
	}
	return content
}

// DistillPrompt builds the distiller's prompt: the brain's current BRAIN and
// WISDOM, the journal window, and a strict JSON contract. The marker in the
// first line lets the brain keep this call out of the scorecards and the
// journal.
const distillMarker = "[euclid distill]"

func DistillPrompt(brain EuclidBrain, entries []JournalEntry) string {
	var sb strings.Builder
	sb.WriteString(distillMarker + " You are Euclid's distiller for the brain at " + brain.Root + " (" + brain.Kind + ").\n")
	sb.WriteString("Turn the run journal below into durable memory: what is now true (BRAIN, a snapshot - replace stale lines), lessons that held (WISDOM), hunches not yet proven (INTUITION, one line tagged [open]), stance and calibration (AFFECT, replace the Stance section), facts worth recalling later (memory/MEMORIES.md, append-only), incidents folded into guardrails (memory/FAILURES.md), decisions (memory/decisions-ledger.md).\n")
	sb.WriteString("Rules: only what the journal supports; no speculation; no secrets; prefer few, dense lines; never rewrite SOUL or VISION. Do not leave INTUITION or AFFECT untouched when the journal supports a hunch or a change in stance.\n")
	sb.WriteString("Respond with JSON only: {\"summary\": \"one paragraph\", \"edits\": [{\"file\": \"BRAIN.md|WISDOM.md|INTUITION.md|AFFECT.md|memory/MEMORIES.md|memory/FAILURES.md|memory/decisions-ledger.md\", \"mode\": \"append|replace_section\", \"anchor\": \"## heading (replace_section only)\", \"text\": \"markdown\", \"why\": \"one line\"}]}\n\n")
	for _, name := range []string{"BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md"} {
		if b, err := os.ReadFile(filepath.Join(brain.Root, name)); err == nil {
			sb.WriteString("=== current " + name + " ===\n" + clipText(string(b), 6000) + "\n\n")
		}
	}
	sb.WriteString(fmt.Sprintf("=== journal (%d runs) ===\n", len(entries)))
	for _, e := range entries {
		line, _ := json.Marshal(e)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// IsDistillRequest reports whether a prompt is a distillation call.
func IsDistillRequest(prompt string) bool { return strings.Contains(prompt, distillMarker) }

// ParseDistillation reads the model's JSON (tolerating fences and prose
// around it) and drops edits outside the allowed files.
func ParseDistillation(text string) (Distillation, error) {
	return parseEdits(text, allowedEditFiles)
}

// ParseBootstrap parses a bootstrap reply: same shape, but the strategy
// registers (VISION, MAP, BRAIN) are the allowed targets.
func ParseBootstrap(text string) (Distillation, error) {
	return parseEdits(text, bootstrapFiles)
}

func parseEdits(text string, allowed map[string]bool) (Distillation, error) {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "{"); i > 0 {
		t = t[i:]
	}
	if j := strings.LastIndex(t, "}"); j >= 0 {
		t = t[:j+1]
	}
	var d Distillation
	if err := json.Unmarshal([]byte(t), &d); err != nil {
		return Distillation{}, fmt.Errorf("distillation is not JSON: %w", err)
	}
	kept := d.Edits[:0]
	for _, e := range d.Edits {
		if !allowed[e.File] || strings.TrimSpace(e.Text) == "" {
			continue
		}
		if e.Mode != "replace_section" {
			e.Mode = "append"
		}
		e.Text = Scrub(e.Text)
		kept = append(kept, e)
	}
	d.Edits = kept
	return d, nil
}

// ApplyEdits writes edits into a brain. Only the session's WRITE brain may be
// edited; the shared repo brain is written by the fold on main (euclid_share.go).
func ApplyEdits(brain EuclidBrain, edits []RegisterEdit) ([]string, error) {
	return applyEditsTo(brain, edits, allowedEditFiles)
}

// applyEditsTo is ApplyEdits with the allowed-file set as a parameter, so
// bootstrap can target the strategy registers distillation must not touch.
func applyEditsTo(brain EuclidBrain, edits []RegisterEdit, allowed map[string]bool) ([]string, error) {
	if !brain.Writable {
		return nil, fmt.Errorf("brain %s is read-only for this session", brain.Label)
	}
	var names []string
	for n := range allowed {
		names = append(names, n)
	}
	if err := RecoverBrainEdits(brain); err != nil {
		return nil, err
	}
	base, err := snapshotBrain(brain.Root, names)
	if err != nil {
		return nil, err
	}
	writes, err := prepareBrainEdits(brain, edits, allowed, base)
	if err != nil {
		return nil, err
	}
	if err := commitBrainFiles(brain.Root, base, writes); err != nil {
		return nil, err
	}
	var touched []string
	for _, e := range edits {
		if allowed[e.File] {
			touched = append(touched, filepath.Join(brain.Root, e.File))
		}
	}
	return touched, nil
}

// replaceSection swaps the body under a markdown heading (up to the next
// heading of the same or higher level); a missing heading is appended.
// dropPlaceholders removes template placeholder bullets ("- (add the first
// heuristic…)", "- [ ] (first open question)") once real content is appended.
func dropPlaceholders(doc string) string {
	lines := strings.Split(doc, "\n")
	out := lines[:0]
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if (strings.HasPrefix(s, "- (") && strings.HasSuffix(s, ")")) || strings.HasPrefix(s, "- [ ] (") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func replaceSection(doc, anchor, body string) string {
	anchor = strings.TrimSpace(anchor)
	// The model often opens its replacement with the heading it is replacing;
	// the heading stays where it is, so strip a leading copy from the body.
	if b := strings.TrimSpace(body); anchor != "" && strings.HasPrefix(b, anchor) {
		body = strings.TrimSpace(strings.TrimPrefix(b, anchor))
	}
	if anchor == "" || !strings.HasPrefix(anchor, "#") {
		return strings.TrimRight(doc, "\n") + "\n\n" + strings.TrimSpace(body) + "\n"
	}
	level := len(anchor) - len(strings.TrimLeft(anchor, "#"))
	lines := strings.Split(doc, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == anchor {
			start = i
			break
		}
	}
	if start < 0 {
		return strings.TrimRight(doc, "\n") + "\n\n" + anchor + "\n\n" + strings.TrimSpace(body) + "\n"
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		t := lines[i]
		if strings.HasPrefix(t, "#") {
			if lvl := len(t) - len(strings.TrimLeft(t, "#")); lvl <= level {
				end = i
				break
			}
		}
	}
	out := append([]string{}, lines[:start+1]...)
	out = append(out, "", strings.TrimSpace(body), "")
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// RenderPatch renders edits as a reviewable markdown patch for a PR-gated
// brain (never applied by Captain).
func RenderPatch(brain EuclidBrain, d Distillation) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Euclid distillation for %s (%s) - %d runs since %s\n\n%s\n", brain.Label, brain.Root, d.Entries, d.Since.Format("2006-01-02 15:04"), strings.TrimSpace(d.Summary))
	for _, e := range d.Edits {
		fmt.Fprintf(&sb, "\n## %s · %s", e.File, e.Mode)
		if e.Anchor != "" {
			fmt.Fprintf(&sb, " · %s", e.Anchor)
		}
		fmt.Fprintf(&sb, "\n_%s_\n\n```markdown\n%s\n```\n", strings.TrimSpace(e.Why), strings.TrimSpace(e.Text))
	}
	return sb.String()
}

// LastDistilledAt / MarkDistilled persist the journal cursor in the brain.
func LastDistilledAt(brain EuclidBrain) time.Time {
	b, err := os.ReadFile(filepath.Join(brain.Root, "journal", ".distilled"))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

func MarkDistilled(brain EuclidBrain, at time.Time) error {
	p := filepath.Join(brain.Root, "journal", ".distilled")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(at.Format(time.RFC3339Nano)+"\n"), 0o644)
}

// EnsurePersonaFiles creates INTUITION and AFFECT when a brain never had
// them. Existing files are left as they are.
func EnsurePersonaFiles(root string) {
	for _, name := range []string{"INTUITION.md", "AFFECT.md"} {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		_ = os.WriteFile(p, []byte(builtinRegisters[name]), 0o644)
	}
}

// ParseCrystallize reads a crystallize reply. MEMORIES edits are dropped:
// the echo already recorded the episode.
func ParseCrystallize(text string) (Distillation, error) {
	return parseEdits(text, crystallizeFiles)
}

// ApplyCrystallize writes a crystallize reply into the main brain.
func ApplyCrystallize(brain EuclidBrain, edits []RegisterEdit) ([]string, error) {
	return applyEditsTo(brain, edits, crystallizeFiles)
}

// MemorySections splits MEMORIES on dated ## headings.
func MemorySections(text string) []JournalEntry {
	var out []JournalEntry
	var cur *JournalEntry
	flush := func() {
		if cur == nil {
			return
		}
		cur.Summary = strings.TrimSpace(cur.Summary)
		if cur.Summary != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "## ") {
			flush()
			at := time.Time{}
			rest := strings.TrimSpace(strings.TrimPrefix(ln, "## "))
			if len(rest) >= 10 {
				if t, err := time.Parse("2006-01-02", rest[:10]); err == nil {
					at = t
				}
			}
			cur = &JournalEntry{At: at, Kind: "memory", Task: rest, Summary: ln}
			continue
		}
		if cur != nil {
			cur.Summary += "\n" + ln
		}
	}
	flush()
	return out
}

// DatedSections is MEMORIES headings that start with a day. The echo writes
// those; an undated heading is a template, not a new run.
func DatedSections(text string) []JournalEntry {
	var out []JournalEntry
	for _, e := range MemorySections(text) {
		if !e.At.IsZero() {
			out = append(out, e)
		}
	}
	return out
}

// SectionsSince counts dated memory sections appended after the last
// crystallize. already is how many dated sections that pass consumed.
func SectionsSince(text string, already int) int {
	n := len(DatedSections(text))
	if already < 0 || n < already {
		return n
	}
	return n - already
}

// CrystallizeEntries is the window a crystallize call reads: dated sections
// after the ones already consumed, capped at limit, oldest first within that cap.
func CrystallizeEntries(text string, already, limit int) []JournalEntry {
	if limit <= 0 {
		limit = 12
	}
	all := DatedSections(text)
	if already < 0 || already > len(all) {
		already = 0
	}
	kept := all[already:]
	if len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

const crystallizeMarker = "[euclid crystallize]"

// CrystallizePrompt asks the model to fold cross-repo memory lines into the
// persona registers the per-repo distill does not touch.
func CrystallizePrompt(brain EuclidBrain, entries []JournalEntry) string {
	var sb strings.Builder
	sb.WriteString(distillMarker + " " + crystallizeMarker + " You are Euclid's crystallizer for the main brain at " + brain.Root + ".\n")
	sb.WriteString("MEMORIES below were appended from other repositories. Fold them into the persona. Replace BRAIN's current state. Merge lessons into WISDOM. Append unproven hunches to INTUITION, one line tagged [open]. Replace AFFECT's Stance section when the runs change how sure to be. Fold incidents into FAILURES. Append open questions. Do not edit MEMORIES, SOUL or VISION.\n")
	sb.WriteString("Respond with JSON only: {\"summary\": \"one paragraph\", \"edits\": [{\"file\": \"BRAIN.md|WISDOM.md|INTUITION.md|AFFECT.md|memory/FAILURES.md|memory/open-questions.md\", \"mode\": \"append|replace_section\", \"anchor\": \"## heading (replace_section only)\", \"text\": \"markdown\", \"why\": \"one line\"}]}\n\n")
	for _, name := range []string{"BRAIN.md", "WISDOM.md", "INTUITION.md", "AFFECT.md"} {
		if b, err := os.ReadFile(filepath.Join(brain.Root, name)); err == nil {
			sb.WriteString("=== current " + name + " ===\n" + clipText(string(b), 4000) + "\n\n")
		}
	}
	sb.WriteString(fmt.Sprintf("=== new memories (%d) ===\n", len(entries)))
	for _, e := range entries {
		sb.WriteString(e.Summary)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// LastCrystallizedCount is how many dated MEMORIES sections the last
// crystallize had already seen. A missing cursor means none.
func LastCrystallizedCount(brain EuclidBrain) int {
	b, err := os.ReadFile(filepath.Join(brain.Root, "journal", ".crystallized"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// MarkCrystallized records how many dated sections have been folded.
func MarkCrystallized(brain EuclidBrain, n int) error {
	p := filepath.Join(brain.Root, "journal", ".crystallized")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strconv.Itoa(n)+"\n"), 0o644)
}
