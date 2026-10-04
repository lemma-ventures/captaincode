package captaincode

// Curating the private-names list (2026-10-04). A list the user has to write
// by hand is a list that misses the next project. Captain sees every folder
// its TUIs work in, so it proposes the names that identify that work:
//
//   - the repository's own name, added on the spot when GitHub says the
//     repository is private (or it has no remote) and the name is not an
//     ordinary word - the user is told it was added;
//   - its package name and the distinctive words of its README title,
//     proposed for the user to add or dismiss;
//   - nothing from a repository GitHub says is public: its name is public.
//
// Everything stays on this machine: no model sees a candidate.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// NameCandidate is one name captain could add to the list.
type NameCandidate struct {
	Name   string `json:"name"`
	Source string `json:"source"` // where it came from: the repository, its package, its README title
	Repo   string `json:"repo"`   // the repository it identifies
	Auto   bool   `json:"auto"`   // added without asking: a private repository's own non-word name
}

// RepoVisibility is "public", "private", "local" (no remote) or "unknown"
// (a remote GitHub could not answer for). Tests replace it.
var RepoVisibility = repoVisibility

// CurateCandidates proposes the names that identify the repository holding
// dir, minus those already listed or dismissed.
func CurateCandidates(dir string) []NameCandidate {
	root := repoRoot(dir)
	if root == "" {
		return nil
	}
	vis := RepoVisibility(root)
	if vis == "public" {
		return nil // a public repository's names are already public
	}
	known := knownNames()
	base := filepath.Base(root)
	var out []NameCandidate
	add := func(name, source string, auto bool) {
		name = strings.TrimSpace(name)
		if len(name) < 3 || known[strings.ToLower(name)] {
			return
		}
		known[strings.ToLower(name)] = true
		out = append(out, NameCandidate{Name: name, Source: source, Repo: base, Auto: auto})
	}
	add(base, "repository", (vis == "private" || vis == "local") && !isWord(base))
	for _, p := range packageNames(root) {
		if !isWord(p) {
			add(p, "package name", false)
		}
	}
	for _, w := range titleWords(root) {
		add(w, "README title", false)
	}
	return out
}

// knownNames is every name already listed or dismissed, lowercased.
func knownNames() map[string]bool {
	out := map[string]bool{"captaincode": true}
	for _, n := range PrivateNames() {
		out[strings.ToLower(n)] = true
	}
	for _, n := range readLines(PrivateNamesDismissedPath()) {
		out[strings.ToLower(n)] = true
	}
	for k := range NameRecords() {
		out[k] = true // already proposed: asked once, never nagged
	}
	return out
}

// PrivateNamesDismissedPath holds names the user said are not private, so
// they are never proposed again.
func PrivateNamesDismissedPath() string { return PrivateNamesPath() + ".dismissed" }

// NameEntry is what captain knows about a name: the project it identifies,
// where it was found, and how it got its status. The list file stays the
// only thing the checks read; this record lets the dashboard show the names
// per project for the user to review.
type NameEntry struct {
	Name   string    `json:"name"`
	Repo   string    `json:"repo,omitempty"`   // the project it identifies; empty when the user typed it
	Source string    `json:"source,omitempty"` // repository, package name, README title, typed by the user
	How    string    `json:"how"`              // auto, user, suggested, dismissed
	At     time.Time `json:"at"`
}

// Statuses a name can have.
const (
	NameAuto      = "auto"      // added by captain, and the user told
	NameUser      = "user"      // added by the user (typed, or a suggestion accepted)
	NameSuggested = "suggested" // proposed, waiting for the user
	NameDismissed = "dismissed" // the user said it is not private
)

// PrivateNamesMetaPath is the per-name record the dashboard reads.
func PrivateNamesMetaPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "private-names.json")
}

// NameRecords reads the record, keyed by lowercased name.
func NameRecords() map[string]NameEntry {
	out := map[string]NameEntry{}
	if b, err := os.ReadFile(PrivateNamesMetaPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeNameRecords(recs map[string]NameEntry) error {
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(PrivateNamesMetaPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(PrivateNamesMetaPath(), b, 0o600)
}

// recordNames upserts entries in the record.
func recordNames(entries []NameEntry) error {
	recs := NameRecords()
	for _, e := range entries {
		if e.At.IsZero() {
			e.At = time.Now()
		}
		if old, ok := recs[strings.ToLower(e.Name)]; ok {
			if e.Repo == "" {
				e.Repo = old.Repo
			}
			if e.Source == "" {
				e.Source = old.Source
			}
		}
		recs[strings.ToLower(e.Name)] = e
	}
	return writeNameRecords(recs)
}

// AddPrivateNames appends entries to the list - with a dated note naming
// the project and how - records them, and returns the names that were new.
func AddPrivateNames(entries []NameEntry) ([]string, error) {
	listMu.Lock()
	defer listMu.Unlock()
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	why := entries[0].How
	if entries[0].Repo != "" {
		why = entries[0].Repo + ": " + why
	}
	added, err := appendNames(PrivateNamesPath(), names, why, PrivateNames())
	if err != nil {
		return nil, err
	}
	return added, recordNames(entries)
}

// DismissPrivateNames records names the user said are not private, so they
// are never proposed again.
func DismissPrivateNames(names []string) ([]string, error) {
	listMu.Lock()
	defer listMu.Unlock()
	added, err := appendNames(PrivateNamesDismissedPath(), names, NameDismissed, readLines(PrivateNamesDismissedPath()))
	if err != nil {
		return nil, err
	}
	var entries []NameEntry
	for _, n := range names {
		entries = append(entries, NameEntry{Name: n, How: NameDismissed})
	}
	return added, recordNames(entries)
}

// SuggestNames records proposals waiting for the user.
func SuggestNames(cands []NameCandidate) error {
	listMu.Lock()
	defer listMu.Unlock()
	var entries []NameEntry
	for _, c := range cands {
		entries = append(entries, NameEntry{Name: c.Name, Repo: c.Repo, Source: c.Source, How: NameSuggested})
	}
	return recordNames(entries)
}

// PendingSuggestions are the proposals the user has not answered, by name.
func PendingSuggestions() []NameEntry {
	var out []NameEntry
	for _, e := range NameRecords() {
		if e.How == NameSuggested {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo+out[i].Name < out[j].Repo+out[j].Name })
	return out
}

// RemovePrivateNames takes names off the list and returns those it removed.
func RemovePrivateNames(names []string) ([]string, error) {
	listMu.Lock()
	defer listMu.Unlock()
	drop := map[string]bool{}
	for _, n := range names {
		drop[strings.ToLower(strings.TrimSpace(n))] = true
	}
	raw, err := os.ReadFile(PrivateNamesPath())
	if err != nil {
		return nil, err
	}
	var keep []string
	var removed []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") && drop[strings.ToLower(t)] {
			removed = append(removed, t)
			continue
		}
		keep = append(keep, line)
	}
	if err := os.WriteFile(PrivateNamesPath(), []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	recs := NameRecords()
	for _, n := range removed {
		delete(recs, strings.ToLower(n))
	}
	return removed, writeNameRecords(recs)
}

var listMu sync.Mutex

// appendNames appends the new names to a list file. Caller holds listMu.
func appendNames(path string, names []string, why string, existing []string) ([]string, error) {
	have := map[string]bool{}
	for _, n := range existing {
		have[strings.ToLower(n)] = true
	}
	var added []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || strings.ContainsAny(n, " \t\n#") || have[strings.ToLower(n)] {
			continue
		}
		have[strings.ToLower(n)] = true
		added = append(added, n)
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fmt.Fprintf(f, "# %s - %s\n%s\n", time.Now().Format("2006-01-02"), why, strings.Join(added, "\n"))
	return added, nil
}

func readLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, t)
		}
	}
	return out
}

func repoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "" // not a repository: nothing it names is about to be pushed
	}
	return strings.TrimSpace(string(out))
}

// packageNames reads the names a repository publishes itself under.
func packageNames(root string) []string {
	var out []string
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if m := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(b); m != nil {
			out = append(out, filepath.Base(string(m[1])))
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var p struct{ Name string }
		if json.Unmarshal(b, &p) == nil && p.Name != "" {
			out = append(out, strings.TrimPrefix(filepath.Base(p.Name), "@"))
		}
	}
	for _, f := range []string{"Cargo.toml", "pyproject.toml"} {
		if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			if m := regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`).FindSubmatch(b); m != nil {
				out = append(out, string(m[1]))
			}
		}
	}
	return out
}

var titleWordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9-]{2,}`)

// titleWords are the README title's words that are not ordinary words: a
// product or paper name ("Quillon: ongoing proofs of …" gives Quillon).
func titleWords(root string) []string {
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		var out []string
		for _, w := range titleWordRe.FindAllString(line, -1) {
			if !isWord(w) && !isWord(strings.TrimRight(w, "s")) {
				out = append(out, w)
			}
		}
		return out
	}
	return nil
}

// ---- repository visibility, cached a week ----

type visEntry struct {
	Vis string    `json:"vis"`
	At  time.Time `json:"at"`
}

var visMu sync.Mutex

func visCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "repo-visibility.json")
}

var githubRemoteRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/.]+?)(\.git)?$`)

func repoVisibility(root string) string {
	visMu.Lock()
	defer visMu.Unlock()
	cache := map[string]visEntry{}
	if b, err := os.ReadFile(visCachePath()); err == nil {
		_ = json.Unmarshal(b, &cache)
	}
	if e, ok := cache[root]; ok && time.Since(e.At) < 7*24*time.Hour {
		return e.Vis
	}
	vis := "unknown"
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	switch {
	case err != nil:
		vis = "local"
	default:
		if m := githubRemoteRe.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
			cmd := exec.Command("gh", "api", "repos/"+m[1]+"/"+m[2], "-q", ".private")
			if b, err := cmd.Output(); err == nil {
				if strings.TrimSpace(string(b)) == "true" {
					vis = "private"
				} else {
					vis = "public"
				}
			}
		}
	}
	cache[root] = visEntry{Vis: vis, At: time.Now()}
	if b, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(visCachePath()), 0o700)
		_ = os.WriteFile(visCachePath(), b, 0o600)
	}
	return vis
}

// SortedNames is names sorted case-insensitively, for display.
func SortedNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
