package captaincode

// Euclid retrieval over a session's brains (MM38 E3): a stdlib BM25-style
// search across every brain in the read set - own subtree, repo, main,
// linked repos, and other developers' subtrees (search-only) - with each
// hit tagged by its source brain and weighted by that brain's rank. Serves
// the `euclid_search` / `euclid_read_register` / `euclid_recent_runs` MCP
// tools (cmd/captaincode/euclid_mcp.go) so workers can ask the memory on
// demand instead of re-deriving it.

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SearchSet is the read set plus the search-only brains.
func SearchSet(cwd string) []EuclidBrain {
	set := ReadSet(cwd)
	if len(set) == 0 {
		return nil
	}
	for i := range set {
		if set[i].Weight == 0 {
			set[i].Weight = 1
		}
	}
	if repo := RepoRoot(cwd); repo != "" && isDir(filepath.Join(repo, ".euclid")) {
		set = append(set, OtherDeveloperBrains(repo, DeveloperHandle())...)
	}
	return set
}

// Hit is one search result.
type Hit struct {
	Source string  `json:"source"` // brain label (me@repo, repo:x, main, dev:h)
	Kind   string  `json:"kind"`
	File   string  `json:"file"` // path relative to the brain root
	Line   int     `json:"line"`
	Score  float64 `json:"score"`
	Text   string  `json:"text"`
}

type chunk struct {
	brain EuclidBrain
	file  string
	line  int
	text  string
	terms map[string]int
	n     int
}

var tokenRe = regexp.MustCompile(`[a-z0-9][a-z0-9_./-]*`)

func tokens(s string) []string {
	return tokenRe.FindAllString(strings.ToLower(s), -1)
}

// chunksOf splits a brain's registers and journal into paragraph chunks.
func chunksOf(b EuclidBrain) []chunk {
	var out []chunk
	add := func(rel, text string, baseLine int) {
		terms := map[string]int{}
		n := 0
		for _, t := range tokens(text) {
			terms[t]++
			n++
		}
		if n == 0 {
			return
		}
		out = append(out, chunk{brain: b, file: rel, line: baseLine, text: text, terms: terms, n: n})
	}
	files := []string{"BRAIN.md", "WISDOM.md", "SOUL.md", "VISION.md", "MAP.md",
		"memory/MEMORIES.md", "memory/FAILURES.md", "memory/decisions-ledger.md", "memory/open-questions.md"}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(b.Root, rel))
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		start, buf := 0, []string{}
		flush := func(end int) {
			if text := strings.TrimSpace(strings.Join(buf, "\n")); text != "" {
				add(rel, text, start+1)
			}
			buf = buf[:0]
		}
		for i, l := range lines {
			if strings.TrimSpace(l) == "" {
				flush(i)
				start = i + 1
				continue
			}
			if len(buf) == 0 {
				start = i
			}
			buf = append(buf, l)
		}
		flush(len(lines))
	}
	// Journal lines: one chunk each (task + files + outcome).
	entries, _ := ReadJournal(b, time.Time{})
	for i, e := range entries {
		text := "run " + e.At.Format("2006-01-02 15:04") + " [" + e.Leg + " " + e.Outcome + "] " + e.Task
		if len(e.Files) > 0 {
			text += " - files: " + strings.Join(e.Files, ", ")
		}
		add("journal", text, i+1)
	}
	return out
}

// EuclidSearch ranks chunks across the search set by a BM25-style score
// scaled by the brain's weight; returns the top k.
func EuclidSearch(cwd, query string, k int) []Hit {
	q := tokens(query)
	if len(q) == 0 {
		return nil
	}
	set := SearchSet(cwd)
	if len(set) == 0 {
		return nil
	}
	var all []chunk
	for _, b := range set {
		all = append(all, chunksOf(b)...)
	}
	if len(all) == 0 {
		return nil
	}
	df := map[string]int{}
	total := 0
	for _, c := range all {
		total += c.n
		for t := range c.terms {
			df[t]++
		}
	}
	avg := float64(total) / float64(len(all))
	const k1, bb = 1.2, 0.75
	var hits []Hit
	for _, c := range all {
		score := 0.0
		for _, t := range q {
			tf := c.terms[t]
			if tf == 0 {
				// prefix match for identifiers (value.go matches value)
				for term, n := range c.terms {
					if strings.HasPrefix(term, t) && len(t) >= 4 {
						tf += n
					}
				}
			}
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(all))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			score += idf * float64(tf) * (k1 + 1) / (float64(tf) + k1*(1-bb+bb*float64(c.n)/avg))
		}
		if score <= 0 {
			continue
		}
		w := c.brain.Weight
		if w == 0 {
			w = 1
		}
		hits = append(hits, Hit{Source: c.brain.Label, Kind: c.brain.Kind, File: c.file, Line: c.line, Score: score * w, Text: clipText(c.text, 600)})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// ReadRegister returns a register from a brain in the search set, by label
// ("" = the write brain) and name (BRAIN, WISDOM, MAP, MEMORIES, …).
func ReadRegister(cwd, source, name string) (string, string, bool) {
	name = strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(name)), ".MD")
	rel := name + ".md"
	switch name {
	case "MEMORIES", "FAILURES":
		rel = "memory/" + name + ".md"
	case "DECISIONS", "DECISIONS-LEDGER", "LEDGER":
		rel = "memory/decisions-ledger.md"
	case "OPEN-QUESTIONS", "QUESTIONS":
		rel = "memory/open-questions.md"
	}
	for _, b := range SearchSet(cwd) {
		if (source == "" && !b.Writable) || (source != "" && b.Label != source) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(b.Root, rel))
		if err != nil {
			return "", b.Label, false
		}
		return string(data), b.Label, true
	}
	return "", "", false
}

// RecentRuns returns the last n journal entries of the write brain.
func RecentRuns(cwd string, n int) []JournalEntry {
	wb, ok := WriteBrain(cwd)
	if !ok {
		return nil
	}
	entries, _ := ReadJournal(wb, time.Time{})
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries
}
