package captaincode

// The shared brain, without merge conflicts (2026-09-16).
//
// Every note a worker records, every failure captain diagnoses and every
// poor verdict the director hands down lands in the developer's LOCAL write
// brain (`.euclid/developers/<handle>/`, gitignored). Nothing reached the
// shared repo brain (`.euclid/BRAIN.md`, `WISDOM.md`, `memory/`): it was
// "PR-gated" with no gate, so a fresh clone or a teammate read the state of
// the day it was scaffolded.
//
// The shared layer is designed so that git can never conflict on it:
//
//   - promotion at the source: the same note is ALSO written as ONE FILE of
//     its own under `.euclid/notes/` (timestamp, handle, kind, short hash in
//     the name). Two developers, or one developer on two branches, never
//     touch the same file, so pull requests carry notes as plain additions;
//     the PR diff is the review gate.
//   - synthesis after the merge: a CI job on the default branch folds the
//     pending notes into the shared registers (BRAIN.md, WISDOM.md by a
//     model; the memory ledgers verbatim) and deletes the folded files.
//     Only that job writes the registers and ledgers, only on main, so a
//     branch never carries a change to them and merging cannot conflict.
//     Regenerating on the PR branch instead would put two branches' versions
//     of BRAIN.md in front of each other - the very conflict to avoid.
//   - degradation: with no model reachable the fold still moves the notes
//     into the ledgers, so nothing is lost and the queue never grows.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SharedNotesDir is where promoted notes wait for the fold, under the repo brain.
const SharedNotesDir = "notes"

// SharedBrainTracked reports whether repo's `.euclid` travels in git - the
// condition for promoting notes. A brain the root .gitignore excludes (this
// repository's own, while Euclid is not the default install) stays local.
func SharedBrainTracked(repo string) bool {
	if repo == "" || !isDir(filepath.Join(repo, ".euclid")) {
		return false
	}
	trackedMu.Lock()
	if v, ok := trackedCache[repo]; ok && time.Since(v.at) < time.Minute {
		trackedMu.Unlock()
		return v.tracked
	}
	trackedMu.Unlock()
	// exit 0: ignored; 1: not ignored; anything else: not a git repo, treat as local.
	cmd := exec.Command("git", "-C", repo, "check-ignore", "-q", ".euclid/"+SharedNotesDir+"/x.md")
	err := cmd.Run()
	var ee *exec.ExitError
	tracked := errors.As(err, &ee) && ee.ExitCode() == 1
	trackedMu.Lock()
	trackedCache[repo] = trackedEntry{tracked: tracked, at: time.Now()}
	trackedMu.Unlock()
	return tracked
}

type trackedEntry struct {
	tracked bool
	at      time.Time
}

var (
	trackedMu    sync.Mutex
	trackedCache = map[string]trackedEntry{}
)

// resetSharedTrackedCache is for tests that flip a repository's ignore rules.
func resetSharedTrackedCache() {
	trackedMu.Lock()
	trackedCache = map[string]trackedEntry{}
	trackedMu.Unlock()
}

// SharedNote is one promoted note: the file under .euclid/notes and what it says.
type SharedNote struct {
	Path   string
	At     time.Time
	Handle string
	By     string // worker | captain | director
	Kind   string // decision | question | memory | failure
	Text   string
}

// ShareNote promotes a note to the repository's shared brain as a file of
// its own. It is a no-op (ok=false) when the shared brain is not tracked.
func ShareNote(cwd, kind, text, by string) (string, bool) {
	repo := RepoRoot(cwd)
	if !SharedBrainTracked(repo) {
		return "", false
	}
	text = Scrub(strings.TrimSpace(text))
	if text == "" || noteFiles[strings.ToLower(strings.TrimSpace(kind))] == "" {
		return "", false
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	now := time.Now().UTC()
	handle := DeveloperHandle()
	sum := sha256.Sum256([]byte(now.Format(time.RFC3339Nano) + handle + text))
	name := fmt.Sprintf("%s-%s-%s-%s.md", now.Format("20060102T150405Z"), handle, kind, hex.EncodeToString(sum[:3]))
	dir := filepath.Join(repo, ".euclid", SharedNotesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\nat: %s\nhandle: %s\nby: %s\nkind: %s\n---\n%s\n", now.Format(time.RFC3339), handle, by, kind, text)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		return "", false
	}
	return p, true
}

// PendingNotes lists the notes waiting under a shared brain, oldest first.
func PendingNotes(shared string) ([]SharedNote, error) {
	dir := filepath.Join(shared, SharedNotesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SharedNote
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n := parseSharedNote(string(b))
		n.Path = p
		if n.Text == "" {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func parseSharedNote(s string) SharedNote {
	var n SharedNote
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---\n") {
		n.Text = s
		return n
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		n.Text = s
		return n
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "at":
			n.At, _ = time.Parse(time.RFC3339, v)
		case "handle":
			n.Handle = v
		case "by":
			n.By = v
		case "kind":
			n.Kind = v
		}
	}
	n.Text = strings.TrimSpace(strings.TrimPrefix(rest[end+4:], "\n"))
	return n
}

// LedgerLine is how a promoted note reads once folded into a shared ledger.
func (n SharedNote) LedgerLine() string {
	who := n.Handle
	if n.By != "" && n.By != "worker" {
		who = n.By + " · " + n.Handle
	}
	day := n.At.Format("2006-01-02")
	if n.At.IsZero() {
		day = time.Now().Format("2006-01-02")
	}
	return "- " + day + " " + strings.ReplaceAll(n.Text, "\n", " ") + " _(" + who + ")_"
}

// FoldPrompt asks a model to fold the pending notes into the shared
// registers. Only BRAIN.md and WISDOM.md are edited by the model: the
// ledgers receive the notes verbatim (Fold does that deterministically).
func FoldPrompt(shared string, notes []SharedNote) string {
	var sb strings.Builder
	sb.WriteString(distillMarker + " You are Euclid's distiller for the SHARED brain at " + shared + " (the repository's, read by every developer and every worker).\n")
	sb.WriteString("Below are notes developers and captain promoted since the last fold: decisions, memories, failures, open questions. Fold what changes the picture into the registers: BRAIN.md is a snapshot of what is now true (replace stale sections, keep it dense); WISDOM.md holds lessons that held. The notes themselves are appended to the memory ledgers separately - do not repeat them there.\n")
	sb.WriteString("Rules: only what the notes support; no speculation; no secrets; few, dense lines; never rewrite SOUL, VISION or MAP. Nothing worth changing → an empty edits list.\n")
	sb.WriteString("Respond with JSON only: {\"summary\": \"one paragraph\", \"edits\": [{\"file\": \"BRAIN.md|WISDOM.md\", \"mode\": \"append|replace_section\", \"anchor\": \"## heading (replace_section only)\", \"text\": \"markdown\", \"why\": \"one line\"}]}\n\n")
	for _, name := range []string{"BRAIN.md", "WISDOM.md"} {
		if b, err := os.ReadFile(filepath.Join(shared, name)); err == nil {
			sb.WriteString("=== current " + name + " ===\n" + clipText(string(b), 6000) + "\n\n")
		}
	}
	fmt.Fprintf(&sb, "=== notes (%d) ===\n", len(notes))
	for _, n := range notes {
		fmt.Fprintf(&sb, "[%s %s %s/%s] %s\n", n.At.Format("2006-01-02"), n.Kind, n.By, n.Handle, strings.ReplaceAll(n.Text, "\n", " "))
	}
	return sb.String()
}

var foldEditFiles = map[string]bool{"BRAIN.md": true, "WISDOM.md": true}

// ParseFold reads the model's answer; edits outside BRAIN/WISDOM are dropped.
func ParseFold(text string) (Distillation, error) { return parseEdits(text, foldEditFiles) }

// FoldResult is what one fold did to the shared brain.
type FoldResult struct {
	Notes   int
	Ledgers []string // ledger files appended
	Edits   []string // register files the model edited
	Removed []string // note files folded away
	Summary string
	ByModel bool
}

// Fold applies register edits (nil when no model answered), appends every
// note to its ledger, and removes the note files. The shared brain is
// written directly: this runs on the default branch, after the merge.
func Fold(shared string, notes []SharedNote, d *Distillation) (FoldResult, error) {
	var r FoldResult
	r.Notes = len(notes)
	if len(notes) == 0 {
		return r, nil
	}
	brain := EuclidBrain{Root: shared, Kind: "repo", Writable: true, Label: "repo"}
	if d != nil {
		touched, err := applyEditsTo(brain, d.Edits, foldEditFiles)
		if err != nil {
			return r, err
		}
		r.Edits, r.Summary, r.ByModel = touched, d.Summary, true
	}
	byFile := map[string][]string{}
	for _, n := range notes {
		rel := noteFiles[n.Kind]
		if rel == "" {
			rel = noteFiles["memory"]
		}
		byFile[rel] = append(byFile[rel], n.LedgerLine())
	}
	files := make([]string, 0, len(byFile))
	for rel := range byFile {
		files = append(files, rel)
	}
	sort.Strings(files)
	for _, rel := range files {
		p := filepath.Join(shared, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return r, err
		}
		cur, _ := os.ReadFile(p)
		text := dropPlaceholders(string(cur))
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += strings.Join(byFile[rel], "\n") + "\n"
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			return r, err
		}
		r.Ledgers = append(r.Ledgers, p)
	}
	for _, n := range notes {
		if err := os.Remove(n.Path); err == nil {
			r.Removed = append(r.Removed, n.Path)
		}
	}
	return r, nil
}

// ChatAPI is one plain chat completion against an OpenAI-compatible endpoint
// - what the fold uses where no captain leg exists (a CI runner).
// CAPTAIN_DISTILL_API_URL (default OpenRouter), CAPTAIN_DISTILL_API_KEY (else
// OPENROUTER_API_KEY), CAPTAIN_DISTILL_MODEL (default deepseek/deepseek-v4.1-flash).
func ChatAPI(ctx context.Context, prompt string) (string, error) {
	key := strings.TrimSpace(os.Getenv("CAPTAIN_DISTILL_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	}
	if key == "" {
		return "", errors.New("no CAPTAIN_DISTILL_API_KEY / OPENROUTER_API_KEY")
	}
	url := strings.TrimSpace(os.Getenv("CAPTAIN_DISTILL_API_URL"))
	if url == "" {
		url = "https://openrouter.ai/api/v1/chat/completions"
	}
	model := strings.TrimSpace(os.Getenv("CAPTAIN_DISTILL_MODEL"))
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}
	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0.2,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d: %s", model, resp.StatusCode, truncateStr(string(raw), 300))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: no choices in the answer", model)
	}
	return out.Choices[0].Message.Content, nil
}

// ChatAPIAvailable reports whether ChatAPI has a key to use.
func ChatAPIAvailable() bool {
	return os.Getenv("CAPTAIN_DISTILL_API_KEY") != "" || os.Getenv("OPENROUTER_API_KEY") != ""
}
