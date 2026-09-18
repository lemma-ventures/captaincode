package captaincode

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The engine indexes the roots named in .euclid/euclid.yml with the code
// extensions listed there. The template's values are the lemma corpus's
// (docs + .euclid, Python), so a Rust repo scaffolded from it indexed its
// docs and nothing under arc/src - "Source code: (no matches)" on every
// search (2026-09-13). The corpus is detected from the repository instead.

var codeExts = map[string]bool{
	".rs": true, ".go": true, ".py": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".java": true, ".kt": true, ".swift": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".rb": true, ".sh": true, ".sol": true, ".sql": true, ".proto": true, ".zig": true, ".ex": true,
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "vendor": true, "dist": true, "build": true,
	".next": true, ".venv": true, "venv": true, "__pycache__": true, ".cache": true, "coverage": true,
	".euclid": true, ".idea": true, ".vscode": true,
}

// DetectCorpus walks a repository (bounded) and returns the top-level
// directories holding documentation or code, plus the code extensions seen,
// most frequent first. ".euclid" is always a root.
func DetectCorpus(repo string) (roots []string, exts []string) {
	counts := map[string]int{}
	rootHits := map[string]int{}
	seen := 0
	_ = filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repo, p)
		if d.IsDir() {
			if rel != "." && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > 20000 {
			return filepath.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(rel))
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if top == rel { // a top-level file: README.md and friends are indexed by the engine itself
			return nil
		}
		if codeExts[ext] {
			counts[ext]++
			rootHits[top]++
		} else if ext == ".md" {
			rootHits[top]++
		}
		return nil
	})
	for r, n := range rootHits {
		if n > 0 {
			roots = append(roots, r)
		}
	}
	sort.Strings(roots)
	roots = append(roots, ".euclid")
	for e := range counts {
		exts = append(exts, e)
	}
	sort.Slice(exts, func(i, j int) bool {
		if counts[exts[i]] != counts[exts[j]] {
			return counts[exts[i]] > counts[exts[j]]
		}
		return exts[i] < exts[j]
	})
	if len(exts) > 6 {
		exts = exts[:6]
	}
	return roots, exts
}

var (
	rootsBlockRe   = regexp.MustCompile(`(?m)^roots:\n(?:[ \t]+-[^\n]*\n)*`)
	codeExtsRe     = regexp.MustCompile(`(?m)^code_exts:[^\n]*\n`)
	excludeGlobsRe = regexp.MustCompile(`(?m)^exclude_globs:[^\n]*\n`)
)

// derivedExcludes keeps the brain's DERIVED files out of its own corpus: with
// ".js" among the code extensions the dashboard's body shards were indexed
// as sources, each new build embedded the previous shards inside the next
// (10 MB of shard-in-shard) and the page's document viewer failed to read
// anything (arc, 2026-09-13).
var derivedExcludes = []string{".euclid/dashboard/*", ".euclid/index/*", ".euclid/bin/*", ".euclid/probes/*"}

// ApplyCorpus rewrites the roots and code_exts keys of a euclid.yml text
// from a detected corpus, leaving every other key as it is.
func ApplyCorpus(cfg string, roots, exts []string) string {
	var rb strings.Builder
	rb.WriteString("roots:\n")
	for _, r := range roots {
		fmt.Fprintf(&rb, "  - %s\n", r)
	}
	ce := "code_exts: [" + strings.Join(exts, ", ") + "]\n"
	if rootsBlockRe.MatchString(cfg) {
		cfg = rootsBlockRe.ReplaceAllString(cfg, rb.String())
	} else {
		cfg = rb.String() + "\n" + cfg
	}
	if codeExtsRe.MatchString(cfg) {
		cfg = codeExtsRe.ReplaceAllString(cfg, ce)
	} else {
		cfg = strings.Replace(cfg, rb.String(), rb.String()+"\n"+ce, 1)
	}
	quoted := make([]string, 0, len(derivedExcludes))
	for _, g := range derivedExcludes {
		quoted = append(quoted, `"`+g+`"`)
	}
	eg := "exclude_globs: [" + strings.Join(quoted, ", ") + "]\n"
	if excludeGlobsRe.MatchString(cfg) {
		cfg = excludeGlobsRe.ReplaceAllString(cfg, eg)
	} else {
		cfg = strings.Replace(cfg, ce, ce+eg, 1)
	}
	return cfg
}

// IsTemplateCorpus says whether a euclid.yml still carries the template's
// corpus (the lemma host's: docs + .euclid, Python) - the one case the
// launch ensure rewrites; a corpus the operator edited is left alone.
func IsTemplateCorpus(cfg string) bool {
	m := rootsBlockRe.FindString(cfg)
	if m == "" {
		return true
	}
	roots := []string{}
	for _, l := range strings.Split(m, "\n")[1:] {
		if t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-")); t != "" {
			roots = append(roots, t)
		}
	}
	return strings.Join(roots, ",") == "docs,.euclid"
}

// ConfigureCorpus detects the repository's corpus and writes it into the
// brain's euclid.yml. Returns what it set.
func ConfigureCorpus(repo string) (roots, exts []string, err error) {
	root := filepath.Join(repo, ".euclid")
	p := filepath.Join(root, "euclid.yml")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	roots, exts = DetectCorpus(repo)
	if len(exts) == 0 {
		exts = []string{".py"}
	}
	out := ApplyCorpus(string(b), roots, exts)
	if out == string(b) {
		return roots, exts, nil
	}
	return roots, exts, os.WriteFile(p, []byte(out), 0o644)
}
