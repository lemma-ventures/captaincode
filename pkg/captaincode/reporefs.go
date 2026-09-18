package captaincode

// Which repository a prompt is about. A TUI open in DLM that is asked to
// "fix the launcher in captaincode" or "update the lemma website" is not
// talking about DLM: the worker should run in the repository it names and
// that repository's brain should be the one read and written - a journal
// page about captaincode in DLM's brain is noise there and lost here
// (2026-09-13). Default: the folder the TUI is open in. One other repo
// named: the workspace moves there. Several: the workspace stays, the
// named brains are read alongside it.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// workspaceRoot is where the developer's repositories live
// (CAPTAIN_WORKSPACE_ROOT, default ~/Gits).
func workspaceRoot() string {
	if root := strings.TrimSpace(os.Getenv("CAPTAIN_WORKSPACE_ROOT")); root != "" {
		return root
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Gits")
	}
	return ""
}

var knownReposCache struct {
	mu    sync.Mutex
	root  string
	at    time.Time
	repos []string
}

// KnownRepos lists the repositories a prompt may name: every git repo or
// brain-bearing folder one or two levels under the workspace root
// (Compliance/lemma-ventures-website is a repo inside a folder), plus the
// main brain's links.yml repos. Cached a minute; the set changes when a
// repo is cloned, not per turn.
func KnownRepos() []string {
	root := workspaceRoot()
	knownReposCache.mu.Lock()
	defer knownReposCache.mu.Unlock()
	if knownReposCache.root == root && time.Since(knownReposCache.at) < time.Minute {
		return knownReposCache.repos
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] || !isRepoDir(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if root != "" {
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
					continue
				}
				p := filepath.Join(root, e.Name())
				add(p)
				if sub, err := os.ReadDir(p); err == nil {
					for _, s := range sub {
						if s.IsDir() && !strings.HasPrefix(s.Name(), ".") && s.Name() != "node_modules" {
							add(filepath.Join(p, s.Name()))
						}
					}
				}
			}
		}
	}
	for _, r := range readMainLinks().Repos {
		if p := ResolveLinkTarget(r, ""); p != "" {
			add(p)
		}
	}
	knownReposCache.root, knownReposCache.at, knownReposCache.repos = root, time.Now(), out
	return out
}

func isRepoDir(p string) bool {
	return isDir(p) && (isDir(filepath.Join(p, ".git")) || isDir(filepath.Join(p, ".euclid")) || fileExists(filepath.Join(p, ".git")))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// resetKnownReposForTest drops the cache.
func ResetKnownReposForTest() {
	knownReposCache.mu.Lock()
	knownReposCache.at = time.Time{}
	knownReposCache.mu.Unlock()
}

// A path in prose: ~/x, /Users/x, /home/x - up to whitespace or a quote.
var pathTokenRe = regexp.MustCompile("(?:~|/Users|/home|/srv|/opt|/var|/private)(?:/[^\\s\"'`<>|)\\]},;:]+)+")

var repoCueRe = `(?i)(?:repo(?:sitory)?|project|codebase|folder|website|site|app)`

// RepoRefs returns the repositories the task names other than the one cwd
// is in, most specific first. A path always counts (it is looked up to
// its repository root). A bare name counts when it is a known repo's
// folder name as a whole word AND is not also a word: Compliance, Relay,
// brand, strategy, lemma, euclid, arc are folders here and words in any
// DLM prompt ("prove agentic compliance" moved the worker to ~/Gits/
// Compliance in the first cut, 2026-09-15) - those need a cue ("the
// euclid repo", ~/Gits/euclid); captaincode, DLM, HerdG, buzz-finance do
// not. A hyphenated name also matches by its parts in order ("lemma
// website" is lemma-ventures-website, which then outranks the bare
// "lemma" repo).
func RepoRefs(task, cwd string) []string {
	if os.Getenv("CAPTAIN_REPO_REFS") == "0" || strings.TrimSpace(task) == "" {
		return nil
	}
	home := RepoRoot(cwd)
	if home == "" {
		home = filepath.Clean(cwd)
	}
	seen := map[string]bool{}
	type hit struct {
		root   string
		tokens int // how many name tokens matched (a path counts as all of them)
	}
	var hits []hit
	add := func(root string, tokens int) {
		root = filepath.Clean(root)
		if root == "" || root == "." || root == home || seen[root] {
			return
		}
		seen[root] = true
		hits = append(hits, hit{root, tokens})
	}
	// Paths.
	for _, tok := range pathTokenRe.FindAllString(task, -1) {
		p := tok
		if strings.HasPrefix(p, "~") {
			if h, err := os.UserHomeDir(); err == nil {
				p = h + p[1:]
			}
		}
		p = strings.TrimRight(p, "./")
		// The longest existing prefix, then its repository.
		for p != "" && !isDir(p) {
			p = filepath.Dir(p)
			if p == "/" || p == "." {
				p = ""
			}
		}
		if p == "" {
			continue
		}
		if root := RepoRoot(p); root != "" && root != MainBrainPath() {
			add(root, 1<<10)
		}
	}
	// Names.
	lower := " " + strings.ToLower(collapseName(task)) + " "
	for _, root := range KnownRepos() {
		name := filepath.Base(root)
		parts := nameTokens(name)
		if len(parts) == 0 {
			continue
		}
		// Whole name as a phrase ("lemma ventures website", "lemma-ventures-website").
		if strings.Contains(lower, " "+strings.Join(parts, " ")+" ") {
			switch {
			case nearCue(task, name):
				// "the arc repo", "~/Gits/arc": said to be a repository.
			case len(parts) > 1:
				// buzz-finance, 22-arcana: nobody writes that in prose.
			case isWord(name) || len(name) < 4:
				continue // a word in prose (compliance, relay, brand, arc)
			case strings.IndexFunc(name, unicode.IsUpper) >= 0 && !strings.Contains(task, name):
				continue // DLM, HerdG: as spelt, not as a lowercase token
			}
			add(root, len(parts))
			continue
		}
		// Its parts in order, the first present, at most one word skipped
		// between two ("lemma website" is lemma-ventures-website).
		if len(parts) >= 3 && partsInOrder(lower, parts) {
			add(root, len(parts)-1)
		}
	}
	// A repo whose whole name is one part of a better match is that match
	// ("lemma" inside "lemma website").
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].tokens > hits[j].tokens })
	var out []string
	for i, h := range hits {
		name := strings.ToLower(filepath.Base(h.root))
		shadowed := false
		for j := 0; j < i; j++ {
			for _, p := range nameTokens(filepath.Base(hits[j].root)) {
				if p == name && hits[j].tokens >= 2 {
					shadowed = true
				}
			}
		}
		if !shadowed {
			out = append(out, h.root)
		}
	}
	return out
}

// partsInOrder: the name's parts appear in the text in order, the first
// one present, with at most one other word between two matched parts and
// at most one part missing.
func partsInOrder(lower string, parts []string) bool {
	words := strings.Fields(lower)
	if len(words) == 0 {
		return false
	}
	pi, matched, gap := 0, 0, 0
	started := false
	for _, w := range words {
		if pi >= len(parts) {
			break
		}
		switch {
		case w == parts[pi]:
			pi++
			matched++
			started = true
			gap = 0
		case started && pi+1 < len(parts) && w == parts[pi+1] && matched >= 1:
			pi += 2 // one part skipped
			matched++
			gap = 0
		case started:
			gap++
			if gap > 1 {
				return false
			}
		}
	}
	return matched >= 2 && matched >= len(parts)-1 && strings.Contains(" "+lower+" ", " "+parts[0]+" ")
}

var dictOnce sync.Once
var dictWords map[string]bool

// isWord: the name is an ordinary word (the system dictionary, lowercase
// match). Without a dictionary every plain lowercase name counts as one -
// the safe side: a cue is then needed, a worker is never moved by prose.
func isWord(name string) bool {
	dictOnce.Do(func() {
		for _, p := range []string{"/usr/share/dict/words", "/usr/dict/words"} {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			dictWords = map[string]bool{}
			for _, w := range strings.Split(string(b), "\n") {
				if w != "" {
					dictWords[strings.ToLower(w)] = true
				}
			}
			return
		}
	})
	lower := strings.ToLower(name)
	if dictWords == nil {
		return lower == name // no dictionary: a plain lowercase name is prose-shaped
	}
	return dictWords[lower]
}

// nameTokens splits a folder name on -, _ and . into lowercase parts.
func nameTokens(name string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// collapseName makes a prompt comparable to a folder name: separators
// become spaces, punctuation goes.
func collapseName(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// nearCue: the name sits next to a word that says it is a repository.
func nearCue(task, name string) bool {
	re := regexp.MustCompile(`(?i)(?:` + repoCueRe + `\s+["'` + "`" + `]?` + regexp.QuoteMeta(name) + `\b|\b` + regexp.QuoteMeta(name) + `["'` + "`" + `]?\s+` + repoCueRe + `|~/` + regexp.QuoteMeta(name) + `\b)`)
	return re.MatchString(task)
}
