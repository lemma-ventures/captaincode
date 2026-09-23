package captaincode

// Vetted skills at bootstrap (ROADMAP M3.9). A worker that rediscovers the
// same procedure every run pays frontier tokens for something already
// written down. Agent Skills are that written-down form, and every runtime
// captain drives already reads them: a directory holding a `SKILL.md` with
// two required frontmatter fields, `name` and `description`.
//
// Captain injects NO prompt text. Each runtime already does progressive
// disclosure - it loads every skill's name and description at startup and
// the body only once it decides to activate one. So captain's job is not to
// write a skill into the prompt; it is to decide which skills EXIST in the
// worktree for this task. Captain stocks the shelf, the worker's own runtime
// picks the book.
//
// One real tree does nearly all of it: `.agents/skills/<name>` is read by
// codex, gemini (documented alias), opencode and cursor, and a
// `.claude/skills/<name>` symlink into it covers claude, which reads only its
// own directory. Two writes, five runtimes, no per-runtime copy to keep in
// step.
//
// Three properties carry the weight, and each is a refusal:
//
//   - `scripts/` is QUARANTINED. That directory is arbitrary code and it is
//     where a poisoned skill would keep its payload; it is staged only when
//     the skill is allowlisted by name at sync time.
//   - the cap is not a nicety. Every stocked skill costs its name and
//     description in every worker's startup context, and codex truncates that
//     listing at roughly 8,000 characters - past which it silently shortens
//     descriptions, degrading selection for every skill at once. An
//     unfiltered catalog is worse than none.
//   - the shelf dies with the worktree. Nothing appears in the user's
//     repository: what is staged is removed, and while it is there an
//     exclude line naming exactly those paths - in the exclude file git
//     actually reads, which for a linked worktree is the repository's -
//     keeps it out of `git status`, out of a worker's `git add -A`, and out
//     of an isolated worker's integration diff.
//
// One skill is stocked for every task rather than by selection:
// security-audit (AlwaysSkills), because the changes a security reviewer
// reads - a form, a dependency, a shell call - rarely say "security".
//
// Opt-in by construction, like Euclid: with nothing synced there is no
// catalog, no directory, no listing, and a run is byte-for-byte what it is
// today.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SkillsVersion is stamped on the lock.
const SkillsVersion = 1

// SkillsDirEnv overrides where the synced catalog lives (tests, and a user
// who keeps captain state on another volume).
const SkillsDirEnv = "CAPTAIN_SKILLS_DIR"

// SkillCapEnv overrides how many skills may be stocked for one task.
const SkillCapEnv = "CAPTAIN_SKILLS_CAP"

// SkillsAlwaysEnv names the skills stocked for EVERY task, whatever the task
// says - comma-separated, replacing the default. "0", "off" or "none" stocks
// nothing unconditionally; selection by the task's words is unchanged
// either way.
const SkillsAlwaysEnv = "CAPTAIN_SKILLS_ALWAYS"

// SecuritySkill is the always-on default. Security is not a topic a task
// has to name to need: "add a login form" and "wire up this SDK" never say
// the word, and they are exactly the changes a security reviewer reads.
const SecuritySkill = "security-audit"

// SecuritySkillSource is the catalog SecuritySkill is synced from. One name
// is one skill and the first sync wins, so `captain doctor` names the
// publisher of whatever holds the name rather than assume it.
const SecuritySkillSource = "cloudflare/security-audit-skill"

// skillCapDefault is the hard cap on a shelf. See the file comment: this is
// a context budget, not a preference.
const skillCapDefault = 8

// skillListingBudget bounds the total name+description bytes a shelf costs
// in a worker's startup context, well under codex's ~8,000-character
// truncation point so the listing is never silently shortened.
const skillListingBudget = 6000

// Size caps applied at parse time. A skill that needs more than this is not
// a procedure, it is a program, and it belongs in the repository being
// worked on rather than on every worker's shelf.
const (
	maxSkillMDBytes = 64 << 10 // one SKILL.md
	maxSkillBytes   = 2 << 20  // one skill directory, everything included
	maxSkillFiles   = 64       // files in one skill directory
	maxDescription  = 1024     // frontmatter description
	minDescription  = 12       // shorter than this cannot select anything
)

// skillNameRe is the spec's name shape: lowercase, digits, single dashes.
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// allowedFrontmatter is the frontmatter captain reads. `allowed-tools` is
// deliberately absent: the spec marks it experimental and support varies, so
// honouring it would be trusting a field nobody implements the same way.
var allowedFrontmatter = map[string]bool{
	"name": true, "description": true, "license": true, "version": true, "metadata": true,
}

// Skill is one skill in the catalog.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`  // "anthropics/skills"
	Commit      string `json:"commit,omitempty"`  // the named commit it was fetched at
	License     string `json:"license,omitempty"` // as recorded by the sync
	// Redistribute is false for source-available skills (Anthropic's four
	// document skills): they may be fetched onto the user's machine at their
	// own request, and may not be vendored into this repository.
	Redistribute bool              `json:"redistribute"`
	Dir          string            `json:"dir,omitempty"` // absolute path in the catalog
	Files        map[string]string `json:"files,omitempty"`
	Bytes        int               `json:"bytes,omitempty"`
	HasScripts   bool              `json:"has_scripts,omitempty"` // the source shipped a scripts/ directory
	Scripts      bool              `json:"scripts,omitempty"`     // …and it was allowlisted, so it is on disk
}

// SkillRef is the pair a runtime actually loads at startup, and the pair the
// director is shown when it grades what the shelf was worth.
type SkillRef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Ref is this skill's startup-context cost, the name and description.
func (s Skill) Ref() SkillRef { return SkillRef{Name: s.Name, Description: s.Description} }

// SkillsHome is the synced catalog: one directory per skill.
func SkillsHome() string {
	if d := strings.TrimSpace(os.Getenv(SkillsDirEnv)); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "skills")
}

// SkillsLockPath pins what was synced: source repository, commit, per-file
// sha256 and license - the same shape as Toolchain() pins, so `captain
// doctor` can report a skill set the way it reports an adapter.
func SkillsLockPath() string { return filepath.Join(SkillsHome(), "skills.lock") }

// SkillCap is how many skills may be stocked for one task.
func SkillCap() int {
	if v := strings.TrimSpace(os.Getenv(SkillCapEnv)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 && n <= 32 {
			return n
		}
	}
	return skillCapDefault
}

// AlwaysSkills is the always-on list, in the order it is stocked. A name
// the catalog does not hold is simply not stocked: always-on is a place on
// the shelf, not a fetch.
func AlwaysSkills() []string {
	v := strings.TrimSpace(os.Getenv(SkillsAlwaysEnv))
	switch strings.ToLower(v) {
	case "":
		return []string{SecuritySkill}
	case "0", "off", "none", "false":
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range strings.Split(v, ",") {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// AlwaysStocked reports whether a skill will be on every worker's shelf: on
// the always-on list, synced, and the shelf not capped to nothing.
func AlwaysStocked(name string) bool {
	if SkillCap() <= 0 {
		return false
	}
	listed := false
	for _, n := range AlwaysSkills() {
		listed = listed || n == name
	}
	if !listed {
		return false
	}
	for _, s := range Catalog() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// ── the lock ────────────────────────────────────────────────────────────────

// SkillLock is the pinned record of one sync.
type SkillLock struct {
	Version   int          `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
	Sources   []LockSource `json:"sources,omitempty"`
	Skills    []Skill      `json:"skills,omitempty"`
}

// LockSource is one catalog the sync read, at the commit it read.
type LockSource struct {
	Repo    string    `json:"repo"`
	Commit  string    `json:"commit"`
	License string    `json:"license,omitempty"`
	At      time.Time `json:"at"`
}

// ReadSkillLock reads the lock. A missing lock is not an error: nothing
// synced is the default state and means the feature is simply off.
func ReadSkillLock() (SkillLock, error) {
	raw, err := os.ReadFile(SkillsLockPath())
	if err != nil {
		if os.IsNotExist(err) {
			return SkillLock{Version: SkillsVersion}, nil
		}
		return SkillLock{}, err
	}
	var l SkillLock
	if err := json.Unmarshal(raw, &l); err != nil {
		return SkillLock{}, fmt.Errorf("skills: lock is unreadable (%w) - re-run `captain skills sync`", err)
	}
	for i := range l.Skills {
		if l.Skills[i].Dir == "" {
			l.Skills[i].Dir = filepath.Join(SkillsHome(), l.Skills[i].Name)
		}
	}
	return l, nil
}

// WriteSkillLock replaces the lock atomically.
func WriteSkillLock(l SkillLock) error {
	l.Version = SkillsVersion
	if l.UpdatedAt.IsZero() {
		l.UpdatedAt = time.Now()
	}
	sort.Slice(l.Skills, func(i, j int) bool { return l.Skills[i].Name < l.Skills[j].Name })
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SkillsHome(), 0o700); err != nil {
		return err
	}
	tmp := SkillsLockPath() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, SkillsLockPath())
}

// Catalog is what is synced and still on disk, in name order. A skill in the
// lock whose directory has gone is dropped rather than offered: stocking a
// directory that is not there gives the worker a broken shelf.
func Catalog() []Skill {
	l, err := ReadSkillLock()
	if err != nil {
		return nil
	}
	out := make([]Skill, 0, len(l.Skills))
	for _, s := range l.Skills {
		if _, err := os.Stat(filepath.Join(s.Dir, "SKILL.md")); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// SkillDrift is one file whose content no longer matches the lock.
type SkillDrift struct {
	Skill  string
	File   string
	Reason string // "missing" | "changed"
}

// VerifySkills recomputes every locked hash. A skill set captain cannot
// attest to is worse than none, so `captain doctor` reports drift rather
// than assuming the shelf is what was vetted.
func VerifySkills() []SkillDrift {
	var out []SkillDrift
	for _, s := range Catalog() {
		for rel, want := range s.Files {
			p := filepath.Join(s.Dir, rel)
			got, err := fileSHA256(p)
			if err != nil {
				out = append(out, SkillDrift{Skill: s.Name, File: rel, Reason: "missing"})
				continue
			}
			if got != want {
				out = append(out, SkillDrift{Skill: s.Name, File: rel, Reason: "changed"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Skill != out[j].Skill {
			return out[i].Skill < out[j].Skill
		}
		return out[i].File < out[j].File
	})
	return out
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── parsing and vetting ─────────────────────────────────────────────────────

// ParseSkill reads and vets one skill directory. Vetting is at SYNC time,
// never at spawn: a skill that reaches the catalog has already been read,
// measured and hashed, so stocking a worker is a file copy and no more.
func ParseSkill(dir string) (Skill, error) {
	md := filepath.Join(dir, "SKILL.md")
	st, err := os.Stat(md)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: %s has no SKILL.md", filepath.Base(dir))
	}
	if st.Size() > maxSkillMDBytes {
		return Skill{}, fmt.Errorf("skills: %s SKILL.md is %d bytes, over the %d cap", filepath.Base(dir), st.Size(), maxSkillMDBytes)
	}
	raw, err := os.ReadFile(md)
	if err != nil {
		return Skill{}, err
	}
	fm, err := parseFrontmatter(string(raw))
	if err != nil {
		return Skill{}, fmt.Errorf("skills: %s: %w", filepath.Base(dir), err)
	}
	s := Skill{
		Name:        strings.TrimSpace(fm["name"]),
		Description: strings.TrimSpace(fm["description"]),
		// A frontmatter license is prose as often as it is an identifier
		// ("Complete terms in LICENSE.txt"), so it is read as a HINT and only
		// kept when it resolves to a license captain can name. An unrecorded
		// license is a question the user can answer; a sentence in the
		// license column is one they will not think to ask.
		License: licenseID(fm["license"]),
		Dir:     dir,
		Files:   map[string]string{},
	}
	if !skillNameRe.MatchString(s.Name) {
		return Skill{}, fmt.Errorf("skills: %s: name %q is not a skill name (lowercase words joined by single dashes)", filepath.Base(dir), s.Name)
	}
	if n := len(s.Description); n < minDescription || n > maxDescription {
		return Skill{}, fmt.Errorf("skills: %s: description is %d characters, outside %d-%d - it is the only thing selection reads",
			s.Name, n, minDescription, maxDescription)
	}
	walked := 0
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skills: %s: %s is a symlink; a vetted skill is files, not links out of the tree", s.Name, info.Name())
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		walked++
		if walked > maxSkillFiles {
			return fmt.Errorf("skills: %s has more than %d files", s.Name, maxSkillFiles)
		}
		s.Bytes += int(info.Size())
		if s.Bytes > maxSkillBytes {
			return fmt.Errorf("skills: %s is over the %d-byte cap", s.Name, maxSkillBytes)
		}
		if strings.HasPrefix(filepath.ToSlash(rel), "scripts/") {
			s.HasScripts = true
		}
		sum, herr := fileSHA256(p)
		if herr != nil {
			return herr
		}
		s.Files[filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return Skill{}, err
	}
	return s, nil
}

// parseFrontmatter reads the leading `---` block. Fields outside the spec's
// set are REFUSED rather than ignored: an unknown field is either a newer
// spec captain has not read or a field the skill expects to be honoured, and
// silently dropping it is how a skill comes to mean something other than
// what it says.
func parseFrontmatter(md string) (map[string]string, error) {
	sc := bufio.NewScanner(strings.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), maxSkillMDBytes)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return nil, fmt.Errorf("no YAML frontmatter (a skill starts with a --- block holding name and description)")
	}
	out, key, nested := map[string]string{}, "", false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			if out["name"] == "" || out["description"] == "" {
				return nil, fmt.Errorf("frontmatter needs both name and description")
			}
			return out, nil
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A continuation line (indented, or a folded block's body) belongs to
		// the key above it - descriptions routinely wrap.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if nested {
				continue
			}
			if key != "" {
				out[key] = strings.TrimSpace(out[key] + " " + strings.TrimSpace(line))
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("frontmatter line %q is not key: value", truncateStr(line, 60))
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if !allowedFrontmatter[k] {
			return nil, fmt.Errorf("frontmatter field %q is outside the spec's set (%s)", k, strings.Join(sortedKeys(allowedFrontmatter), ", "))
		}
		key = k
		v = strings.TrimSpace(v)
		nested = v == "" || v == "|" || v == ">"
		if v == "|" || v == ">" {
			v, nested = "", false // a folded scalar: the indented body continues it
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return nil, fmt.Errorf("frontmatter block is not closed")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── selection ───────────────────────────────────────────────────────────────

// SkillPick is one selected skill and why it was picked, so `captain why`
// and the report can say what the shelf was built from.
type SkillPick struct {
	Skill  Skill
	Score  float64
	Why    string
	Always bool // stocked by the always-on list, not by the task's words
}

// SelectSkills chooses the shelf for one task. Selection happens POST PROMPT,
// at spawn: the class and domain triage has already answered, and it is
// matched against each skill's `description`, which is the field the standard
// designs for exactly this.
//
// The scorer is lexical on purpose. A model call to decide which procedures a
// task might use would cost more than the procedures save, and would run on
// the hot path of every spawn.
//
// The always-on skills (AlwaysSkills) go on first, whatever the task says,
// and pay into the same two budgets as every other book.
func SelectSkills(catalog []Skill, task string, class Class, domain Domain, cap int) []SkillPick {
	if cap <= 0 || len(catalog) == 0 {
		return nil
	}
	// Two budgets, and the tighter one wins: the count cap, and the bytes the
	// listing costs in every worker's startup context.
	out, budget := make([]SkillPick, 0, cap), 0
	fits := func(s Skill) bool {
		cost := len(s.Name) + len(s.Description)
		if len(out) >= cap || budget+cost > skillListingBudget {
			return false
		}
		budget += cost
		return true
	}
	always := map[string]bool{}
	for _, name := range AlwaysSkills() {
		for _, s := range catalog {
			if s.Name == name && !always[name] && fits(s) {
				always[name] = true
				out = append(out, SkillPick{Skill: s, Always: true, Why: "always stocked (" + SkillsAlwaysEnv + ")"})
			}
		}
	}
	var picks []SkillPick
	if terms := skillTerms(task); len(terms) > 0 {
		for _, s := range catalog {
			if always[s.Name] {
				continue
			}
			score, hits := skillScore(s, terms, class, domain)
			if score <= 0 {
				continue
			}
			picks = append(picks, SkillPick{Skill: s, Score: score, Why: strings.Join(hits, ", ")})
		}
	}
	sort.Slice(picks, func(i, j int) bool {
		if picks[i].Score != picks[j].Score {
			return picks[i].Score > picks[j].Score
		}
		return picks[i].Skill.Name < picks[j].Skill.Name
	})
	for _, p := range picks {
		if !fits(p.Skill) {
			break
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// skillScore matches one skill's own words against the task's. A name hit
// outweighs a description hit: a task that says "pdf" and a skill called
// "pdf" is the case the standard's description field was written for.
func skillScore(s Skill, terms map[string]int, class Class, domain Domain) (float64, []string) {
	nameTerms := skillTerms(strings.ReplaceAll(s.Name, "-", " "))
	descTerms := skillTerms(s.Description)
	score, hits, nameHits, descHits := 0.0, []string{}, 0, 0
	for t := range nameTerms {
		if terms[t] > 0 {
			score += 3
			nameHits++
			hits = append(hits, t)
		}
	}
	for t := range descTerms {
		if terms[t] > 0 && nameTerms[t] == 0 {
			score += 1
			descHits++
			hits = append(hits, t)
		}
	}
	// One word in common with a description is not a match, it is a
	// coincidence - live, "fix a typo in the readme" drew the spreadsheet
	// skill because its description happens to contain "fix". A skill earns
	// shelf space by its NAME appearing in the task, or by at least two
	// distinct words of its description doing so.
	if nameHits == 0 && descHits < 2 {
		return 0, nil
	}
	// The triage's answers are a mild prior, never a selector on their own:
	// a skill nothing in the task matched does not reach here at all.
	if domain != "" && descTerms[string(domain)] > 0 {
		score += 0.5
	}
	if class == ClassHigh {
		score += 0.25 // a hard task is the one worth spending shelf space on
	}
	sort.Strings(hits)
	return score, hits
}

// skillStop are words that match everything and therefore select nothing.
var skillStop = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "to": true, "in": true,
	"for": true, "with": true, "this": true, "that": true, "it": true, "is": true, "are": true,
	"use": true, "used": true, "using": true, "when": true, "user": true, "users": true, "any": true,
	"from": true, "into": true, "on": true, "at": true, "by": true, "be": true, "as": true,
	"skill": true, "skills": true, "file": true, "files": true, "make": true, "new": true, "also": true,
	"do": true, "does": true, "not": true, "you": true, "your": true, "can": true, "want": true,
	"wants": true, "asks": true, "ask": true, "please": true, "task": true, "work": true, "code": true,
}

var skillWordRe = regexp.MustCompile(`[a-z0-9]+`)

// skillTerms lowercases, splits and drops the words that match everything.
// Terms shorter than three characters are dropped with them: "go" and "id"
// select a catalog, not a skill.
func skillTerms(s string) map[string]int {
	out := map[string]int{}
	for _, w := range skillWordRe.FindAllString(strings.ToLower(s), -1) {
		if len(w) < 3 || skillStop[w] {
			continue
		}
		out[w]++
		out[singularize(w)]++
	}
	return out
}

func singularize(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ses") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

// ── staging ─────────────────────────────────────────────────────────────────

// excludeMarker heads the block captain adds to a repository's exclude file,
// so captain takes back exactly its own lines and never a line the user
// wrote.
const excludeMarker = "# captain: staged skills (M3.9) - removed when the shelf is"

// stagedMarker is dropped into every skill captain stages. A copy left behind
// by a brain that died mid-turn carries it, so the next shelf knows that copy
// for captain's own; a user's skill of the same name never does, so it is
// never touched.
const stagedMarker = ".captain-staged"

// Shelf is what was staged into one worker's directory. Remove takes it back;
// a Shelf that is never removed leaves the directory as the exclude line
// describes it, which is why the callers defer Remove and the worktree paths
// get it for free when the worktree goes.
type Shelf struct {
	Dir     string
	Stocked []SkillPick

	held []string // the skills this shelf holds in its directory's yard
}

// yard is every live shelf in one directory. A directory often holds several
// at once - a serialized stage's workers, a second session in the same
// repository, the next stage staged while the last one's shelf is still up -
// and with an always-on skill that is the ordinary case, not the rare one.
// So staging is counted: a skill a live shelf already staged is SHARED, not
// mistaken for the user's own, and only the last shelf holding it takes it
// back. Without the count the first turn to finish would pull the skill, and
// its exclude line, out from under a worker still running - whose `git add
// -A` would then commit captain's shelf into the user's history.
type yard struct {
	holders int             // live shelves in this directory
	skills  map[string]int  // staged skill → live shelves holding it
	links   map[string]bool // …and whether its .claude symlink is captain's
	dirs    []string        // parent directories captain created, shallowest first
	exclude string          // the exclude file git reads here; "" outside a repository
	prefix  string          // this directory's path from the repository root
}

var (
	yardMu sync.Mutex
	yards  = map[string]*yard{}
	// excludeLines is every live exclude line, per exclude file. A
	// repository's linked worktrees share ONE exclude file - git has no
	// per-worktree one - so captain's block there is the union of every live
	// shelf's lines, rewritten as shelves come and go.
	excludeLines = map[string]map[string]int{}
)

// Refs is what the shelf costs in a worker's startup context, and what the
// director is shown when it grades the shelf.
func (s *Shelf) Refs() []SkillRef {
	if s == nil {
		return nil
	}
	out := make([]SkillRef, 0, len(s.Stocked))
	for _, p := range s.Stocked {
		out = append(out, p.Skill.Ref())
	}
	return out
}

// Names is the stocked skill names in shelf order.
func (s *Shelf) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Stocked))
	for _, p := range s.Stocked {
		out = append(out, p.Skill.Name)
	}
	return out
}

// StageSkills places the shelf in one worker's directory: the real tree at
// `.agents/skills/<name>` that codex, gemini, opencode and cursor read, and a
// `.claude/skills/<name>` symlink into it for claude, which reads only its
// own directory.
//
// Staging is best-effort per skill: one skill that cannot be copied is
// skipped and reported, because a shelf missing one book is still a shelf,
// while failing the spawn over it would make an optional feature able to
// break a turn.
func StageSkills(dir string, picks []SkillPick) (*Shelf, error) {
	if len(picks) == 0 {
		return nil, nil
	}
	yardMu.Lock()
	defer yardMu.Unlock()
	key := yardKey(dir)
	agents := filepath.Join(dir, ".agents", "skills")
	claude := filepath.Join(dir, ".claude", "skills")
	y := yards[key]
	fresh := y == nil
	if fresh {
		y = &yard{skills: map[string]int{}, links: map[string]bool{}}
		for _, base := range []string{agents, claude} {
			made, err := mkdirTrack(base)
			if err != nil {
				y.removeDirs()
				return nil, fmt.Errorf("skills: stage %s: %w", base, err)
			}
			y.dirs = append(y.dirs, made...)
		}
		y.exclude, y.prefix = gitExcludeFile(dir)
	}
	sh := &Shelf{Dir: dir}
	for _, p := range picks {
		if y.skills[p.Skill.Name] > 0 {
			y.skills[p.Skill.Name]++ // a live shelf staged it: share, never re-copy under a running worker
		} else if !y.stage(p.Skill, agents, claude) {
			continue
		}
		sh.held = append(sh.held, p.Skill.Name)
		sh.Stocked = append(sh.Stocked, p)
	}
	if len(sh.Stocked) == 0 {
		if fresh {
			y.removeDirs()
		}
		return nil, nil
	}
	y.holders++
	yards[key] = y
	writeExclude(y.exclude)
	return sh, nil
}

// stage copies one skill into the directory. A directory already at that
// name is the user's own and wins - unless it carries captain's marker, in
// which case it is a shelf a dead brain never took down, refreshed here and
// counted as this yard's.
func (y *yard) stage(s Skill, agents, claude string) bool {
	dst := filepath.Join(agents, s.Name)
	if _, err := os.Lstat(dst); err == nil {
		if _, err := os.Stat(filepath.Join(dst, stagedMarker)); err != nil {
			return false // the user's own skill of that name wins; captain never overwrites it
		}
		os.RemoveAll(dst)
	}
	if err := copySkill(s, dst); err != nil {
		os.RemoveAll(dst)
		return false
	}
	os.WriteFile(filepath.Join(dst, stagedMarker), []byte("staged by captain for one worker turn; removed when the turn ends\n"), 0o644)
	link := filepath.Join(claude, s.Name)
	rel, err := filepath.Rel(claude, dst)
	if err != nil {
		rel = dst
	}
	if t, err := os.Readlink(link); err == nil && t == rel {
		y.links[s.Name] = true // captain's own, left behind with the copy above
	} else if _, err := os.Lstat(link); err != nil {
		y.links[s.Name] = os.Symlink(rel, link) == nil
	}
	y.skills[s.Name] = 1
	y.countLines(s.Name, 1)
	return true
}

// Remove gives this shelf's skills back to the yard. A skill another live
// shelf still holds stays; the last holder takes it out, then the
// directories captain created, deepest first. A created directory is removed
// only when it is EMPTY - a user file that landed in `.agents/skills` while
// the worker ran is not captain's to delete.
func (s *Shelf) Remove() {
	if s == nil || len(s.held) == 0 {
		return
	}
	yardMu.Lock()
	defer yardMu.Unlock()
	key := yardKey(s.Dir)
	if y := yards[key]; y != nil {
		for _, name := range s.held {
			if y.skills[name]--; y.skills[name] > 0 {
				continue
			}
			y.countLines(name, -1)
			os.RemoveAll(filepath.Join(s.Dir, ".agents", "skills", name))
			if y.links[name] {
				os.Remove(filepath.Join(s.Dir, ".claude", "skills", name))
			}
			delete(y.skills, name)
			delete(y.links, name)
		}
		if y.holders--; y.holders <= 0 {
			y.removeDirs()
			delete(yards, key)
		}
		writeExclude(y.exclude)
	}
	s.held, s.Stocked = nil, nil
}

func yardKey(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

// removeDirs takes back the directories captain created, deepest first.
func (y *yard) removeDirs() {
	for i := len(y.dirs) - 1; i >= 0; i-- {
		os.Remove(y.dirs[i]) // fails harmlessly when something else is in there
	}
	y.dirs = nil
}

// shelfLines are the exclude lines one staged skill needs, anchored at the
// repository root: the tree, and the claude symlink when it is captain's.
// Exactly those paths and no wider - a whole-directory line would also hide
// the skill a user asked a worker to WRITE, and a worker's `git add -A` would
// then leave it out of the commit it reports.
func (y *yard) shelfLines(name string) []string {
	out := []string{"/" + y.prefix + ".agents/skills/" + name}
	if y.links[name] {
		out = append(out, "/"+y.prefix+".claude/skills/"+name)
	}
	return out
}

func (y *yard) countLines(name string, delta int) {
	if y.exclude == "" {
		return
	}
	m := excludeLines[y.exclude]
	if m == nil {
		m = map[string]int{}
		excludeLines[y.exclude] = m
	}
	for _, l := range y.shelfLines(name) {
		if m[l] += delta; m[l] <= 0 {
			delete(m, l)
		}
	}
}

// gitExcludeFile is the exclude file git reads for dir, and dir's path from
// the repository root. Both matter. A linked worktree has no exclude file of
// its own - git reads the repository's shared one - so a write to the
// worktree's `.git/info/exclude` (a `.git` FILE there) never happened, and
// an isolated worker's shelf showed as new files in its integration diff:
// every worker the same new files, which integration reads as a conflict.
// And a pattern is anchored at the root, so a shelf staged in a subdirectory
// needs the subdirectory in its line. Outside a repository there is nothing
// to exclude.
func gitExcludeFile(dir string) (file, prefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-common-dir", "--show-prefix")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	common := strings.TrimSpace(lines[0])
	if common == "" {
		return "", ""
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if len(lines) > 1 {
		prefix = lines[1]
	}
	// A directory name is matched as itself, not as a glob.
	return filepath.Join(common, "info", "exclude"), strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(prefix)
}

// writeExclude rewrites captain's block in one exclude file to the lines live
// shelves hold, and takes the block out when none do. The write goes through
// a rename: git reads this file on every status, and a worker's git must
// never see half of it.
func writeExclude(file string) {
	if file == "" {
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	body := stripExcludeBlock(string(raw))
	live := make([]string, 0, len(excludeLines[file]))
	for l := range excludeLines[file] {
		live = append(live, l)
	}
	if len(live) == 0 {
		delete(excludeLines, file)
	} else {
		sort.Strings(live)
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += excludeMarker + "\n" + strings.Join(live, "\n") + "\n"
	}
	if body == string(raw) {
		return
	}
	if os.MkdirAll(filepath.Dir(file), 0o755) != nil {
		return
	}
	tmp := file + ".captain.tmp"
	if os.WriteFile(tmp, []byte(body), 0o644) != nil || os.Rename(tmp, file) != nil {
		os.Remove(tmp)
	}
}

// stripExcludeBlock takes captain's block out of an exclude file's text -
// this format, and the two whole-directory lines the first one wrote - and
// leaves every other line as it was.
func stripExcludeBlock(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, excludeMarker) {
			in = true
			continue
		}
		if in && isShelfLine(l) {
			continue
		}
		in = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func isShelfLine(l string) bool {
	if l == ".agents/skills/" || l == ".claude/skills/" {
		return true
	}
	return strings.HasPrefix(l, "/") && (strings.Contains(l, ".agents/skills/") || strings.Contains(l, ".claude/skills/"))
}

// mkdirTrack creates dir and returns the directories it actually made, so
// Remove takes back only what did not exist before.
func mkdirTrack(dir string) ([]string, error) {
	var made []string
	var missing []string
	p := dir
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		made = append(made, missing[i])
	}
	return made, nil
}

// copySkill copies one vetted skill into the worker's shelf. `scripts/` is
// dropped unless the sync allowlisted it: that directory is arbitrary code,
// and it is where a poisoned skill would keep its payload.
func copySkill(s Skill, dst string) error {
	return filepath.Walk(s.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(s.Dir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		slash := filepath.ToSlash(rel)
		if !s.Scripts && (slash == "scripts" || strings.HasPrefix(slash, "scripts/")) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		mode := os.FileMode(0o644)
		if s.Scripts && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(target, raw, mode)
	})
}

// ── reporting ───────────────────────────────────────────────────────────────

// FormatCatalog renders the shelf for `captain skills`.
func FormatCatalog(cat []Skill) string {
	if len(cat) == 0 {
		return "no skills synced - `captain skills sync` stages a pinned set; with none, a run is byte-for-byte what it is today\n"
	}
	// The source column fits the longest source: a fixed width pushed every
	// column after cloudflare/security-audit-skill out of line.
	srcW, sourceAvailable := len("SOURCE"), false
	for _, s := range cat {
		srcW = max(srcW, len(s.Source))
		sourceAvailable = sourceAvailable || !s.Redistribute
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-24s %-*s %-18s %-9s %s\n", "SKILL", srcW, "SOURCE", "LICENSE", "SCRIPTS", "DESCRIPTION")
	for _, s := range cat {
		scripts := "-"
		if s.HasScripts && s.Scripts {
			scripts = "allowed"
		} else if s.HasScripts {
			scripts = "quarantined"
		}
		lic := s.License
		if lic == "" {
			lic = "unrecorded"
		}
		if !s.Redistribute {
			lic += "*"
		}
		fmt.Fprintf(&sb, "%-24s %-*s %-18s %-9s %s\n",
			s.Name, srcW, s.Source, lic, scripts, truncateStr(oneLine(s.Description), 60))
	}
	fmt.Fprintf(&sb, "\n%d skills · cap %d per task\n", len(cat), SkillCap())
	if sourceAvailable {
		sb.WriteString("* source-available, not open source: fetched at your request, never vendored into captain.\n")
	}
	return sb.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// UnstageSkills removes a shelf from a directory without a Shelf handle: the
// hand-staged case, and the residue a crashed brain left behind. It removes
// what captain staged and nothing else: a copy carrying captain's marker, and
// the claude symlink to that copy, which the first pass leaves dangling. A
// name alone proves nothing - security-audit is on every shelf, and
// Cloudflare's own instructions install it into the repository, so a
// same-named skill the user vendored is the likely case, not the odd one;
// nor does a link's shape, since a user who shares `.agents/skills` with
// Claude Code writes exactly the link captain does. It returns how many
// skills it took out.
func UnstageSkills(dir string) int {
	known := map[string]bool{}
	for _, s := range Catalog() {
		known[s.Name] = true
	}
	agents := filepath.Join(dir, ".agents", "skills")
	claude := filepath.Join(dir, ".claude", "skills")
	removed := map[string]bool{}
	left := 0
	if entries, err := os.ReadDir(agents); err == nil {
		for _, e := range entries {
			dst := filepath.Join(agents, e.Name())
			if _, err := os.Stat(filepath.Join(dst, stagedMarker)); !known[e.Name()] || err != nil || os.RemoveAll(dst) != nil {
				left++
				continue
			}
			removed[e.Name()] = true
		}
		if left == 0 {
			os.Remove(agents)
			os.Remove(filepath.Dir(agents)) // fails harmlessly when anything else is in there
		}
	}
	left = 0
	if entries, err := os.ReadDir(claude); err == nil {
		for _, e := range entries {
			link := filepath.Join(claude, e.Name())
			rel, _ := filepath.Rel(claude, filepath.Join(agents, e.Name()))
			t, err := os.Readlink(link)
			_, resolves := os.Stat(link)
			if !known[e.Name()] || err != nil || t != rel || resolves == nil || os.Remove(link) != nil {
				left++
				continue
			}
			removed[e.Name()] = true
		}
		if left == 0 {
			os.Remove(claude)
			os.Remove(filepath.Dir(claude))
		}
	}
	// The block is rewritten to what live shelves in THIS process hold: from
	// the CLI that is nothing, so captain's lines go and the user's stay.
	if file, _ := gitExcludeFile(dir); file != "" {
		yardMu.Lock()
		writeExclude(file)
		yardMu.Unlock()
	}
	return len(removed)
}
