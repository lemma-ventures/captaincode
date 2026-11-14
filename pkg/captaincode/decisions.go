package captaincode

// The decision register: questions a worker cannot settle and the user
// must. On 2026-11-07 the user found them by reading worker logs ("D10
// still blocks a cloud run") and per-repo tables in four different formats,
// and some lived only in uncommitted files on one Mac. A worker now posts
// each one here with its options, its recommendation, a short context and
// the log it came from; a cockpit lists them and the user answers.
//
// Answering is signing the record prompt: the exact text that asks a worker
// to write the answer into the repository's own decision file. The brain's
// passkey check and signed queue then carry it, with no new trust path.
// The prompt names only an option the poster offered, so a relay that shows
// one text and gets another signed can at most pick a different one of
// those options.
//
// One JSON file per decision under ~/.captaincode/decisions, mode 0600. Each
// post, answer and withdrawal also appends to the repository's committed
// decision log (decisions_log.go).

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DecisionsDirEnv overrides the register folder (tests).
const DecisionsDirEnv = "CAPTAIN_DECISIONS_DIR"

const (
	DecisionOpen      = "open"
	DecisionAnswered  = "answered"
	DecisionWithdrawn = "withdrawn"
)

type DecisionOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type DecisionPoster struct {
	Leg   string `json:"leg,omitempty"`
	Model string `json:"model,omitempty"`
	// Log is the worker log file name under ~/.captaincode/runs.
	Log     string `json:"log,omitempty"`
	Session string `json:"session,omitempty"`
}

type DecisionAnswer struct {
	Option       string    `json:"option"`
	Note         string    `json:"note,omitempty"`
	PromptSHA256 string    `json:"prompt_sha256,omitempty"`
	Via          string    `json:"via,omitempty"`
	At           time.Time `json:"at"`
}

type OwnerDecision struct {
	ID string `json:"id"`
	// Ref is the repository's own id for it (OD-52, D10), when it has one.
	Ref string `json:"ref,omitempty"`
	// Repo is the main checkout. Checkout is the linked worktree the
	// question was posted from, when it was not Repo, and Branch is the
	// branch it was posted on (decisions_log.go).
	Repo     string           `json:"repo"`
	Checkout string           `json:"checkout,omitempty"`
	Branch   string           `json:"branch,omitempty"`
	Title    string           `json:"title"`
	Question string           `json:"question"`
	Context  string           `json:"context,omitempty"`
	Options  []DecisionOption `json:"options"`
	// Recommend is the poster's option id and Why its one-line reason.
	Recommend string `json:"recommend,omitempty"`
	Why       string `json:"why,omitempty"`
	// Record names the file the answer belongs in, and GitHub an issue or
	// pull request (owner/repo#n) that gets a note.
	Record       string          `json:"record,omitempty"`
	GitHub       string          `json:"github,omitempty"`
	NeededBefore string          `json:"needed_before,omitempty"`
	PostedBy     DecisionPoster  `json:"posted_by"`
	PostedAt     time.Time       `json:"posted_at"`
	Status       string          `json:"status"`
	Answer       *DecisionAnswer `json:"answer,omitempty"`
	Withdrawn    string          `json:"withdrawn,omitempty"`
}

var decisionOptionID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
var decisionIDRe = regexp.MustCompile(`^dq-[0-9]{6}-[0-9a-f]{6}$`)

func DecisionsDir() string {
	if d := os.Getenv(DecisionsDirEnv); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "decisions")
}

func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (d OwnerDecision) option(id string) (DecisionOption, bool) {
	for _, o := range d.Options {
		if o.ID == id {
			return o, true
		}
	}
	return DecisionOption{}, false
}

func (d OwnerDecision) validate() error {
	switch {
	case strings.TrimSpace(d.Repo) == "":
		return errors.New("decision: repo is required")
	case strings.TrimSpace(d.Question) == "":
		return errors.New("decision: question is required")
	case len(d.Options) < 2:
		return errors.New("decision: at least two options are required")
	}
	seen := map[string]bool{}
	for _, o := range d.Options {
		if !decisionOptionID.MatchString(o.ID) {
			return fmt.Errorf("decision: option id %q must be short lowercase letters, digits or dashes", o.ID)
		}
		if seen[o.ID] {
			return fmt.Errorf("decision: option id %q is used twice", o.ID)
		}
		if strings.TrimSpace(o.Label) == "" {
			return fmt.Errorf("decision: option %q has no label", o.ID)
		}
		seen[o.ID] = true
	}
	if d.Recommend != "" && !seen[d.Recommend] {
		return fmt.Errorf("decision: recommended option %q is not one of the options", d.Recommend)
	}
	return nil
}

func maskDecision(d *OwnerDecision) {
	m := func(s string) string { out, _ := MaskSecrets(s, "[secret]"); return out }
	d.Title, d.Question, d.Context, d.Why = m(d.Title), m(d.Question), m(d.Context), m(d.Why)
	for i := range d.Options {
		d.Options[i].Label, d.Options[i].Detail = m(d.Options[i].Label), m(d.Options[i].Detail)
	}
}

func writeDecision(d OwnerDecision) error {
	dir := DecisionsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dq-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, d.ID+".json"))
}

// PostDecision validates d, masks secrets in its text and stores it open.
func PostDecision(d OwnerDecision) (OwnerDecision, error) {
	if err := d.validate(); err != nil {
		return OwnerDecision{}, err
	}
	maskDecision(&d)
	var b [3]byte
	_, _ = rand.Read(b[:])
	d.PostedAt = time.Now().UTC()
	d.ID = "dq-" + d.PostedAt.Format("060102") + "-" + hex.EncodeToString(b[:])
	d.Status, d.Answer, d.Withdrawn = DecisionOpen, nil, ""
	return d, writeDecision(d)
}

func GetDecision(id string) (OwnerDecision, error) {
	if !decisionIDRe.MatchString(id) {
		return OwnerDecision{}, fmt.Errorf("decision: %q is not a decision id", id)
	}
	data, err := os.ReadFile(filepath.Join(DecisionsDir(), id+".json"))
	if err != nil {
		return OwnerDecision{}, fmt.Errorf("decision %s: %w", id, err)
	}
	var d OwnerDecision
	if err := json.Unmarshal(data, &d); err != nil {
		return OwnerDecision{}, fmt.Errorf("decision %s: %w", id, err)
	}
	return d, nil
}

// ListDecisions returns the register, oldest first; status "" lists all.
func ListDecisions(status string) ([]OwnerDecision, error) {
	entries, err := os.ReadDir(DecisionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []OwnerDecision
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !decisionIDRe.MatchString(id) {
			continue
		}
		d, err := GetDecision(id)
		if err != nil {
			continue
		}
		if status == "" || d.Status == status {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PostedAt.Before(out[j].PostedAt) })
	return out, nil
}

// DecisionRecordPrompt is the text the user signs to answer d with option.
func DecisionRecordPrompt(d OwnerDecision, option, note string) (string, error) {
	o, ok := d.option(option)
	if !ok {
		return "", fmt.Errorf("decision %s: %q is not one of its options", d.ID, option)
	}
	name := d.ID
	if d.Ref != "" {
		name = d.Ref + " (" + d.ID + ")"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Record the user's decision %s: %s\n\n", name, d.Title)
	fmt.Fprintf(&b, "Question: %s\n", d.Question)
	fmt.Fprintf(&b, "The user chose option %s: %s", o.ID, o.Label)
	if o.Detail != "" {
		fmt.Fprintf(&b, " - %s", o.Detail)
	}
	b.WriteString("\n")
	if d.Recommend != "" {
		if d.Recommend == o.ID {
			b.WriteString("This is the option the posting agent recommended.\n")
		} else {
			fmt.Fprintf(&b, "The posting agent recommended option %s; the user chose otherwise.\n", d.Recommend)
		}
	}
	if note = strings.TrimSpace(note); note != "" {
		fmt.Fprintf(&b, "The user's note: %s\n", note)
	}
	b.WriteString("\nWrite this answer into the repository's own decision record")
	if d.Record != "" {
		fmt.Fprintf(&b, " (%s)", d.Record)
	}
	b.WriteString(", in the format and wording that file already uses, and mark the question closed wherever it is listed as open.")
	if d.Branch != "" {
		fmt.Fprintf(&b, " The question was asked on branch %s: make the change on that branch (in its worktree, if it has one).", d.Branch)
	}
	b.WriteString(decisionCommitStep)
	if d.GitHub != "" {
		fmt.Fprintf(&b, " Then leave a short comment on %s that names the decision and the chosen option.", d.GitHub)
	}
	// No "run captain decision answered" here (7 Nov): an older captain reads
	// an unknown word as a task, so a worker would start a run instead. The
	// cockpit marks the item answered when the signature is accepted.
	return b.String(), nil
}

// decisionCommitStep asks the record worker to commit the answer with the
// repository's decision log, which `captain decision answered` has already
// appended to (decisions_log.go).
const decisionCommitStep = " Captain adds the answer to " + DecisionLogRel + ", the repository's decision log. Commit the record together with that log if it changed, by path, and change nothing else."

// DecisionChoice is one answer in a bundle.
type DecisionChoice struct {
	ID     string `json:"id"`
	Option string `json:"option"`
	Note   string `json:"note,omitempty"`
}

// DecisionBundlePrompt is one record prompt for several open decisions of the
// same repository, so the user signs them with one passkey approval. A
// signature binds one folder, so a bundle never spans repositories.
func DecisionBundlePrompt(choices []DecisionChoice) (string, error) {
	if len(choices) == 0 {
		return "", errors.New("decision bundle: nothing chosen")
	}
	var repo string
	seen := map[string]bool{}
	type item struct {
		d OwnerDecision
		o DecisionOption
		n string
	}
	var items []item
	for _, c := range choices {
		if seen[c.ID] {
			return "", fmt.Errorf("decision bundle: %s appears twice", c.ID)
		}
		seen[c.ID] = true
		d, err := GetDecision(c.ID)
		if err != nil {
			return "", err
		}
		if d.Status != DecisionOpen {
			return "", fmt.Errorf("decision %s is %s", d.ID, d.Status)
		}
		o, ok := d.option(c.Option)
		if !ok {
			return "", fmt.Errorf("decision %s: %q is not one of its options", d.ID, c.Option)
		}
		if repo == "" {
			repo = d.Repo
		} else if d.Repo != repo {
			return "", errors.New("decision bundle: all decisions must belong to one repository")
		}
		items = append(items, item{d, o, strings.TrimSpace(c.Note)})
	}
	var b strings.Builder
	noun := "decisions"
	if len(items) == 1 {
		noun = "decision"
	}
	fmt.Fprintf(&b, "Record the user's %d %s for %s.\n", len(items), noun, filepath.Base(repo))
	records, notes := []string{}, []string{}
	branches := false
	for i, it := range items {
		name := it.d.ID
		if it.d.Ref != "" {
			name = it.d.Ref + " (" + it.d.ID + ")"
		}
		fmt.Fprintf(&b, "\n%d. %s: %s\n   Question: %s\n   The user chose option %s: %s", i+1, name, it.d.Title, it.d.Question, it.o.ID, it.o.Label)
		if it.o.Detail != "" {
			fmt.Fprintf(&b, " - %s", it.o.Detail)
		}
		b.WriteString("\n")
		if it.d.Recommend != "" && it.d.Recommend != it.o.ID {
			fmt.Fprintf(&b, "   The posting agent recommended option %s; the user chose otherwise.\n", it.d.Recommend)
		}
		if it.n != "" {
			fmt.Fprintf(&b, "   The user's note: %s\n", it.n)
		}
		if it.d.Branch != "" {
			fmt.Fprintf(&b, "   Asked on branch %s.\n", it.d.Branch)
			branches = true
		}
		if it.d.Record != "" && !containsString(records, it.d.Record) {
			records = append(records, it.d.Record)
		}
		if it.d.GitHub != "" && !containsString(notes, it.d.GitHub) {
			notes = append(notes, it.d.GitHub)
		}
	}
	b.WriteString("\nWrite each answer into the repository's own decision record")
	if len(records) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(records, "; "))
	}
	b.WriteString(", in the format and wording that file already uses, and mark each question closed wherever it is listed as open.")
	if branches {
		b.WriteString(" Make each change on the branch its question was asked on (in that branch's worktree, if it has one).")
	}
	b.WriteString(decisionCommitStep)
	if len(notes) > 0 {
		fmt.Fprintf(&b, " Then leave a short comment on %s that names the decisions and the chosen options.", strings.Join(notes, ", "))
	}
	return b.String(), nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func updateDecision(id string, f func(*OwnerDecision) error) (OwnerDecision, error) {
	d, err := GetDecision(id)
	if err != nil {
		return OwnerDecision{}, err
	}
	if err := f(&d); err != nil {
		return OwnerDecision{}, err
	}
	return d, writeDecision(d)
}

// AnswerDecision marks an open decision answered with one of its options.
func AnswerDecision(id, option, note, promptSHA, via string) (OwnerDecision, error) {
	return updateDecision(id, func(d *OwnerDecision) error {
		if d.Status != DecisionOpen {
			if d.Status == DecisionAnswered && d.Answer != nil && d.Answer.Option == option {
				return nil
			}
			return fmt.Errorf("decision %s is %s", d.ID, d.Status)
		}
		if _, ok := d.option(option); !ok {
			return fmt.Errorf("decision %s: %q is not one of its options", d.ID, option)
		}
		masked, _ := MaskSecrets(strings.TrimSpace(note), "[secret]")
		d.Status = DecisionAnswered
		d.Answer = &DecisionAnswer{Option: option, Note: masked, PromptSHA256: promptSHA, Via: via, At: time.Now().UTC()}
		return nil
	})
}

func WithdrawDecision(id, reason string) (OwnerDecision, error) {
	return updateDecision(id, func(d *OwnerDecision) error {
		if d.Status != DecisionOpen {
			return fmt.Errorf("decision %s is %s", d.ID, d.Status)
		}
		d.Status, d.Withdrawn = DecisionWithdrawn, strings.TrimSpace(reason)
		return nil
	})
}

// FindRunLog names the newest worker log in runsDir whose header says it ran
// in dir with leg, or "" when there is none. A worker that posts a decision
// does not know its own log's name.
func FindRunLog(runsDir, dir, leg string) string {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return ""
	}
	type cand struct {
		name string
		mod  time.Time
	}
	var cands []cand
	suffix := "-" + strings.ReplaceAll(leg, "/", "_") + ".log"
	for _, e := range entries {
		if leg != "" && !strings.HasSuffix(e.Name(), suffix) || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if info, err := e.Info(); err == nil {
			cands = append(cands, cand{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	want := "cwd: " + filepath.Clean(dir)
	for i, c := range cands {
		if i >= 400 {
			break
		}
		f, err := os.Open(filepath.Join(runsDir, c.name))
		if err != nil {
			continue
		}
		head := make([]byte, 2048)
		n, _ := f.Read(head)
		f.Close()
		for _, line := range strings.Split(string(head[:n]), "\n") {
			if strings.TrimSpace(line) == want {
				return c.name
			}
			if line == "---" {
				break
			}
		}
	}
	return ""
}
