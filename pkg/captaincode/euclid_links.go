package captaincode

// Euclid links (MM38 E3): repos that are semantically related or that this
// one depends on, declared as data and resolved to local checkouts, so a
// session's read set reaches the brains of the repos it actually builds on -
// read-only, ranked below the local brains.
//
//	<repo>/.euclid/euclid.yml     links: { depends_on: [...], related: [...] }
//	~/.euclid/links.yml           repos: [...]  links: [{from, to, kind}]
//
// A target is an absolute path, a repo name (a sibling checkout of the current
// repo or a child of CAPTAIN_WORKSPACE_ROOT), or a URL whose last segment is
// the repo name. Proposals come from dependency manifests (go.mod,
// package.json, Cargo.toml path deps) and from journal entries that touched
// paths in another checkout; the developer confirms them.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Link is one directed relation between repos.
type Link struct {
	From string `json:"from,omitempty"` // repo root (main-brain links only)
	To   string `json:"to"`             // target as declared
	Kind string `json:"kind"`           // depends_on | related
}

// Link weights rank foreign hits below local ones.
const (
	weightDependsOn = 0.8
	weightRelated   = 0.5
	weightOtherDev  = 0.3
)

func linkWeight(kind string) float64 {
	if kind == "depends_on" {
		return weightDependsOn
	}
	return weightRelated
}

// parseLinksBlock reads the `links:` mapping out of a YAML document with a
// deliberately small parser: `depends_on:`/`related:` keys followed by `- x`
// items or an inline `[a, b]` list. Anything else in the file is ignored.
func parseLinksBlock(text string) []Link {
	var out []Link
	inLinks, kind := false, ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		t := strings.TrimSpace(line)
		if indent == 0 {
			inLinks = t == "links:"
			kind = ""
			continue
		}
		if !inLinks {
			continue
		}
		if strings.HasPrefix(t, "depends_on:") || strings.HasPrefix(t, "related:") {
			kind = strings.TrimSuffix(strings.SplitN(t, ":", 2)[0], ":")
			rest := strings.TrimSpace(strings.SplitN(t, ":", 2)[1])
			if strings.HasPrefix(rest, "[") { // inline list
				rest = strings.Trim(rest, "[]")
				for _, item := range strings.Split(rest, ",") {
					if v := strings.Trim(strings.TrimSpace(item), `"'`); v != "" {
						out = append(out, Link{To: v, Kind: kind})
					}
				}
			}
			continue
		}
		if strings.HasPrefix(t, "- ") && kind != "" {
			if v := strings.Trim(strings.TrimSpace(t[2:]), `"'`); v != "" {
				out = append(out, Link{To: v, Kind: kind})
			}
		}
	}
	return out
}

// RepoLinks reads the links declared in a repo brain.
func RepoLinks(repoRoot string) []Link {
	if repoRoot == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, ".euclid", "euclid.yml"))
	if err != nil {
		return nil
	}
	return parseLinksBlock(string(b))
}

// mainLinks is the main brain's links.yml, JSON-shaped for simplicity:
// {"repos": [...], "links": [{"from": "...", "to": "...", "kind": "..."}]}
// (YAML is a superset, and the scaffold writes valid JSON).
type mainLinks struct {
	Repos []string `json:"repos"`
	Links []Link   `json:"links"`
}

func mainLinksPath() string {
	if mb := MainBrainPath(); mb != "" {
		return filepath.Join(mb, "links.yml")
	}
	return ""
}

func readMainLinks() mainLinks {
	var ml mainLinks
	p := mainLinksPath()
	if p == "" {
		return ml
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ml
	}
	if json.Unmarshal(b, &ml) != nil {
		// Scaffolded YAML placeholder ("repos: []\nlinks: []") or hand-edited: tolerate.
		return mainLinks{}
	}
	return ml
}

func writeMainLinks(ml mainLinks) error {
	p := mainLinksPath()
	if p == "" {
		return fmt.Errorf("no main brain")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ml, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

// ResolveLinkTarget maps a declared target to a local checkout with a brain,
// else "". Candidates: the path itself, a sibling of repoRoot, a child of
// CAPTAIN_WORKSPACE_ROOT, the URL's last segment under both.
func ResolveLinkTarget(target, repoRoot string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	name := target
	if i := strings.LastIndex(strings.TrimSuffix(target, "/"), "/"); i >= 0 {
		name = strings.TrimSuffix(target, "/")[i+1:]
	}
	name = strings.TrimSuffix(name, ".git")
	var candidates []string
	if filepath.IsAbs(target) {
		candidates = append(candidates, target)
	}
	if repoRoot != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(repoRoot), name))
	}
	if ws := os.Getenv("CAPTAIN_WORKSPACE_ROOT"); ws != "" {
		candidates = append(candidates, filepath.Join(ws, name))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Gits", name))
	}
	for _, c := range candidates {
		if isDir(filepath.Join(c, ".euclid")) {
			return c
		}
	}
	return ""
}

// LinkedBrains resolves a repo's links (repo brain + main brain entries whose
// From is this repo) to read-only brains, dependencies first.
func LinkedBrains(repoRoot string) []EuclidBrain {
	links := RepoLinks(repoRoot)
	for _, l := range readMainLinks().Links {
		if l.From == "" || l.From == repoRoot || filepath.Base(l.From) == filepath.Base(repoRoot) {
			links = append(links, l)
		}
	}
	sort.SliceStable(links, func(i, j int) bool { return linkWeight(links[i].Kind) > linkWeight(links[j].Kind) })
	seen := map[string]bool{}
	var out []EuclidBrain
	for _, l := range links {
		root := ResolveLinkTarget(l.To, repoRoot)
		if root == "" || root == repoRoot || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, EuclidBrain{Root: filepath.Join(root, ".euclid"), Kind: "linked", Label: "repo:" + filepath.Base(root), Weight: linkWeight(l.Kind)})
	}
	return out
}

// OtherDeveloperBrains lists the other developers' subtrees in a repo brain
// (E4): search-only, lowest weight, never warm-loaded into orientation.
func OtherDeveloperBrains(repoRoot, handle string) []EuclidBrain {
	if repoRoot == "" {
		return nil
	}
	dir := filepath.Join(repoRoot, ".euclid", "developers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []EuclidBrain
	for _, e := range entries {
		if !e.IsDir() || e.Name() == handle {
			continue
		}
		out = append(out, EuclidBrain{Root: filepath.Join(dir, e.Name()), Kind: "developer-other", Label: "dev:" + e.Name(), Weight: weightOtherDev})
	}
	return out
}

// AddLink declares a link. With a repo brain it is written into the repo's
// euclid.yml (committed with the brain); otherwise into the main brain's
// links.yml with From = the repo. kind is depends_on or related.
func AddLink(repoRoot, target, kind string) (string, error) {
	if kind != "depends_on" && kind != "related" {
		return "", fmt.Errorf("kind must be depends_on or related")
	}
	if repoRoot != "" && isDir(filepath.Join(repoRoot, ".euclid")) {
		p := filepath.Join(repoRoot, ".euclid", "euclid.yml")
		b, _ := os.ReadFile(p)
		text := string(b)
		for _, l := range parseLinksBlock(text) {
			if l.To == target && l.Kind == kind {
				return p, nil
			}
		}
		text = addLinkToYAML(text, kind, target)
		return p, os.WriteFile(p, []byte(text), 0o644)
	}
	ml := readMainLinks()
	for _, l := range ml.Links {
		if l.From == repoRoot && l.To == target && l.Kind == kind {
			return mainLinksPath(), nil
		}
	}
	ml.Links = append(ml.Links, Link{From: repoRoot, To: target, Kind: kind})
	if repoRoot != "" {
		found := false
		for _, r := range ml.Repos {
			if r == repoRoot {
				found = true
			}
		}
		if !found {
			ml.Repos = append(ml.Repos, repoRoot)
		}
	}
	return mainLinksPath(), writeMainLinks(ml)
}

// addLinkToYAML inserts `- target` under links.<kind>, creating the block or
// key when absent and converting an inline `[]` list to block form.
func addLinkToYAML(text, kind, target string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		lines = nil
	}
	linksAt := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "links:" && !strings.HasPrefix(l, " ") {
			linksAt = i
		}
	}
	if linksAt < 0 {
		lines = append(lines, "", "links:", "  "+kind+":", "    - "+target)
		return strings.Join(lines, "\n") + "\n"
	}
	// find the kind key inside the links block
	end := len(lines)
	for i := linksAt + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(lines[i], " ") {
			end = i
			break
		}
	}
	for i := linksAt + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, kind+":") {
			rest := strings.TrimSpace(strings.TrimPrefix(t, kind+":"))
			if strings.HasPrefix(rest, "[") { // inline → block
				items := parseLinksBlock("links:\n  " + t)
				repl := []string{"  " + kind + ":"}
				for _, it := range items {
					repl = append(repl, "    - "+it.To)
				}
				repl = append(repl, "    - "+target)
				lines = append(lines[:i], append(repl, lines[i+1:]...)...)
				return strings.Join(lines, "\n") + "\n"
			}
			// block form: insert after the last existing item
			j := i + 1
			for j < end && strings.HasPrefix(strings.TrimSpace(lines[j]), "- ") {
				j++
			}
			lines = append(lines[:j], append([]string{"    - " + target}, lines[j:]...)...)
			return strings.Join(lines, "\n") + "\n"
		}
	}
	ins := []string{"  " + kind + ":", "    - " + target}
	lines = append(lines[:end], append(ins, lines[end:]...)...)
	return strings.Join(lines, "\n") + "\n"
}

// ------------------------------------------------------------- proposals

// LinkProposal is a suggested link with its evidence.
type LinkProposal struct {
	Target string `json:"target"` // resolved local repo root
	Kind   string `json:"kind"`
	Why    string `json:"why"`
}

var (
	goRequireRe   = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([a-zA-Z0-9.\-]+/[^\s]+)\s+v[0-9]`)
	cargoPathRe   = regexp.MustCompile(`(?m)path\s*=\s*"([^"]+)"`)
	proposalLimit = 12
)

// ProposeLinks scans manifests and the write brain's journal for repos this
// one appears to depend on or work alongside. Only targets that resolve to a
// local checkout WITH a brain are proposed; links already declared are
// skipped.
func ProposeLinks(repoRoot string, journal []JournalEntry) []LinkProposal {
	if repoRoot == "" {
		return nil
	}
	declared := map[string]bool{}
	for _, b := range LinkedBrains(repoRoot) {
		declared[filepath.Dir(b.Root)] = true
	}
	seen := map[string]bool{}
	var out []LinkProposal
	add := func(target, kind, why string) {
		root := ResolveLinkTarget(target, repoRoot)
		if root == "" || root == repoRoot || declared[root] || seen[root] || len(out) >= proposalLimit {
			return
		}
		seen[root] = true
		out = append(out, LinkProposal{Target: root, Kind: kind, Why: why})
	}
	// go.mod requires whose last segment is a sibling checkout.
	if b, err := os.ReadFile(filepath.Join(repoRoot, "go.mod")); err == nil {
		for _, m := range goRequireRe.FindAllStringSubmatch(string(b), -1) {
			add(m[1], "depends_on", "go.mod requires "+m[1])
		}
	}
	// package.json dependencies (names or file: paths).
	if b, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
				for name, spec := range deps {
					if strings.HasPrefix(spec, "file:") {
						add(filepath.Join(repoRoot, strings.TrimPrefix(spec, "file:")), "depends_on", "package.json "+name+" → "+spec)
					} else {
						add(name, "depends_on", "package.json depends on "+name)
					}
				}
			}
		}
	}
	// Cargo.toml path dependencies.
	if b, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.toml")); err == nil {
		for _, m := range cargoPathRe.FindAllStringSubmatch(string(b), -1) {
			add(filepath.Join(repoRoot, m[1]), "depends_on", "Cargo.toml path = "+m[1])
		}
	}
	// Journal: runs in this repo that touched files under another checkout.
	counts := map[string]int{}
	for _, e := range journal {
		for _, f := range e.Files {
			if !filepath.IsAbs(f) || strings.HasPrefix(f, repoRoot+string(filepath.Separator)) {
				continue
			}
			for d := filepath.Dir(f); d != "/" && d != "."; d = filepath.Dir(d) {
				if isDir(filepath.Join(d, ".euclid")) {
					counts[d]++
					break
				}
			}
		}
	}
	roots := make([]string, 0, len(counts))
	for r := range counts {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(i, j int) bool { return counts[roots[i]] > counts[roots[j]] })
	for _, r := range roots {
		add(r, "related", fmt.Sprintf("%d journaled runs touched files there", counts[r]))
	}
	return out
}

// scanLines is a small helper for tests and tools.
func scanLines(text string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
