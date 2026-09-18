package captaincode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The Euclid dashboard's Performance tab and the recall / composition /
// orientation benchmarks read HOST-AUTHORED gold sets from
// .euclid/probes/*.json. A fresh brain has none, so the tab is a row of
// "not captured" notices. Captain authors them the way it fills VISION/MAP/
// BRAIN: one model call over the repository's own docs and the freshly
// filled registers - then VERIFIES every probe (the needle must occur in the
// register or document it points at; the paths must exist) and keeps only
// what verifies. A probe set that cannot be checked is not written.

// ProbePrompt asks for the three gold sets in one JSON reply.
func ProbePrompt(brain EuclidBrain, repo string) (string, []string) {
	var sb strings.Builder
	sb.WriteString(distillMarker + " probes\n")
	sb.WriteString("You are writing benchmark probes for the Euclid memory of the repository " + filepath.Base(repo) + ", from its OWN documentation and registers below. Every probe must be verifiable against those texts: quote needles VERBATIM (a distinctive phrase, id or number that appears in the file named), never invent.\n")
	sb.WriteString("Produce: (1) orientation: 8-12 facts an engineer must know on day one, each as [label, needle, authority_file] where the needle appears verbatim in one of the registers (VISION.md, MAP.md, BRAIN.md, WISDOM.md, SOUL.md under .euclid/) and authority_file is the doc (repo-relative path) that states it, or null. (2) recall: 8-15 [question, needle] pairs where the question paraphrases a document and the needle is a distinctive substring of THAT document (do not put the needle in the question). (3) composition: 3-6 [label, query, [paths...]] enumeration probes: a query whose complete answer is a SET of documents (all ADRs, every plan under docs/x, …), with every member's repo-relative path.\n")
	sb.WriteString("Respond with JSON only: {\"orientation\": [[\"label\",\"needle\",\"authority_or_null\"]], \"recall\": [[\"question\",\"needle\"]], \"composition\": [[\"label\",\"query\",[\"path\"]]]}\n\n")
	sb.WriteString("### Registers\n")
	for _, n := range []string{"VISION.md", "MAP.md", "BRAIN.md", "WISDOM.md", "SOUL.md"} {
		if b, err := os.ReadFile(filepath.Join(brain.Root, n)); err == nil {
			fmt.Fprintf(&sb, "#### .euclid/%s\n%s\n\n", n, clipText(string(b), 4000))
		}
	}
	docs := bootstrapDocs(repo)
	used := 0
	var read []string
	sb.WriteString("### Documents\n")
	for _, p := range docs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repo, p)
		text := string(b)
		if used+len(text) > bootstrapDocBudget {
			text = text[:max(0, bootstrapDocBudget-used)]
		}
		if text == "" {
			break
		}
		fmt.Fprintf(&sb, "#### %s\n%s\n\n", rel, text)
		used += len(text)
		read = append(read, p)
	}
	return sb.String(), read
}

// ProbeSets is the parsed, verified reply.
type ProbeSets struct {
	Orientation [][3]any
	Recall      [][2]string
	Composition []compositionProbe
	Dropped     int
}

type compositionProbe struct {
	Label string
	Query string
	Paths []string
}

// ParseProbes parses the model's reply and verifies every probe against the
// brain and the repository, dropping what does not hold.
func ParseProbes(text string, brain EuclidBrain, repo string) (ProbeSets, error) {
	t := strings.TrimSpace(text)
	if i := strings.Index(t, "{"); i > 0 {
		t = t[i:]
	}
	if j := strings.LastIndex(t, "}"); j >= 0 {
		t = t[:j+1]
	}
	var raw struct {
		Orientation [][]any `json:"orientation"`
		Recall      [][]any `json:"recall"`
		Composition [][]any `json:"composition"`
	}
	if err := json.Unmarshal([]byte(t), &raw); err != nil {
		return ProbeSets{}, fmt.Errorf("probes reply is not the expected JSON: %w", err)
	}
	str := func(v any) string {
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	registers := ""
	for _, n := range []string{"VISION.md", "MAP.md", "BRAIN.md", "WISDOM.md", "SOUL.md"} {
		if b, err := os.ReadFile(filepath.Join(brain.Root, n)); err == nil {
			registers += string(b) + "\n"
		}
	}
	var out ProbeSets
	inRepo := func(rel string) bool {
		if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			return false
		}
		return isFile(filepath.Join(repo, rel))
	}
	for _, p := range raw.Orientation {
		if len(p) < 2 {
			out.Dropped++
			continue
		}
		label, needle := str(p[0]), str(p[1])
		var authority any
		if len(p) > 2 {
			if a := str(p[2]); a != "" && inRepo(a) {
				authority = a
			}
		}
		if label == "" || len(needle) < 4 || !strings.Contains(registers, needle) {
			out.Dropped++
			continue
		}
		out.Orientation = append(out.Orientation, [3]any{label, needle, authority})
	}
	for _, p := range raw.Recall {
		if len(p) < 2 {
			out.Dropped++
			continue
		}
		q, needle := str(p[0]), str(p[1])
		if q == "" || len(needle) < 4 || strings.Contains(strings.ToLower(q), strings.ToLower(needle)) || !corpusContains(repo, needle) {
			out.Dropped++
			continue
		}
		out.Recall = append(out.Recall, [2]string{q, needle})
	}
	for _, p := range raw.Composition {
		if len(p) < 3 {
			out.Dropped++
			continue
		}
		label, query := str(p[0]), str(p[1])
		members, _ := p[2].([]any)
		var paths []string
		for _, m := range members {
			if rel := str(m); inRepo(rel) {
				paths = append(paths, rel)
			}
		}
		if label == "" || query == "" || len(paths) < 2 {
			out.Dropped++
			continue
		}
		out.Composition = append(out.Composition, compositionProbe{Label: label, Query: query, Paths: paths})
	}
	return out, nil
}

// corpusContains says whether a needle occurs in one of the repository's
// bootstrap documents (the texts the probes were authored from).
func corpusContains(repo, needle string) bool {
	for _, p := range bootstrapDocs(repo) {
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

// WriteProbes writes the verified gold sets into <brain>/probes/ in the
// benchmarks' shapes. Returns the files written.
func WriteProbes(brain EuclidBrain, ps ProbeSets) ([]string, error) {
	dir := filepath.Join(brain.Root, "probes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	write := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			return err
		}
		written = append(written, p)
		return nil
	}
	if len(ps.Orientation) >= 3 {
		warm := []string{}
		for _, n := range []string{"SOUL.md", "VISION.md", "WISDOM.md", "BRAIN.md", "memory/FAILURES.md", "memory/open-questions.md", "MAP.md"} {
			if isFile(filepath.Join(brain.Root, n)) {
				warm = append(warm, ".euclid/"+n)
			}
		}
		cold := []string{}
		for _, rel := range []string{"README.md", "AGENTS.md", "CLAUDE.md"} {
			if isFile(filepath.Join(filepath.Dir(brain.Root), rel)) {
				cold = append(cold, rel)
			}
		}
		if err := write("orientation.json", map[string]any{
			"warm": warm, "cold_static": cold, "cold_recent_journals": 5,
			"probes": ps.Orientation, "read_order_paths": warm, "nav_pointers": []any{},
		}); err != nil {
			return written, err
		}
	}
	if len(ps.Recall) >= 3 {
		if err := write("recall.json", map[string]any{"probes": ps.Recall}); err != nil {
			return written, err
		}
	}
	if len(ps.Composition) >= 1 {
		rows := make([][]any, 0, len(ps.Composition))
		for _, c := range ps.Composition {
			rows = append(rows, []any{c.Label, c.Query, c.Paths})
		}
		if err := write("composition.json", map[string]any{"probes": rows}); err != nil {
			return written, err
		}
	}
	return written, nil
}
