package captaincode

// The repository's decision log: one append-only Markdown file per
// repository, committed with the work. On 2026-11-07 a worker asked the user
// six design questions in its final message only: the register was empty, so
// the cockpit showed nothing, and no committed file listed them. A post, an
// answer and a withdrawal now each append one entry here, so the questions
// and their answers reach every clone and every Mac through git, not only
// this Mac's register.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DecisionLogRel is the log's path inside a checkout.
const DecisionLogRel = ".captaincode/decisions.md"

// decisionLogHeader opens a new log. ASCII only: some repositories refuse
// dashes and other typography in Markdown files.
const decisionLogHeader = `# Decisions for the user

Captain appends to this file. A worker adds an entry when it posts a question
that only the user can answer. Captain adds an entry when the user answers the
question or the worker withdraws it. The answer itself goes in the decision
record that the entry names. Do not edit old entries. After a merge conflict,
keep the entries from both sides.
`

// DecisionPlace resolves the folder a worker posts from. repo is the main
// checkout: the folder a TUI runs in and a passkey signature binds. checkout
// is the linked worktree it was posted from, or "" when that is repo itself.
// branch is the posting checkout's branch.
func DecisionPlace(dir string) (repo, checkout, branch string) {
	root := GitRoot(dir)
	if root == "" {
		return presenceRealPath(dir), "", ""
	}
	root = presenceRealPath(root)
	repo = root
	// A linked worktree shares the main checkout's .git directory; a
	// submodule's common directory is under .git/modules and stays itself.
	if common := presenceGitCommon(root); filepath.Base(common) == ".git" {
		repo = filepath.Dir(common)
	}
	if repo != root {
		checkout = root
	}
	out, _ := runGit(root, "branch", "--show-current")
	return repo, checkout, strings.TrimSpace(string(out))
}

// DecisionCheckout is the checkout that gets d's log entries: the one it was
// posted from while that still exists, else its repository. It is "" when
// neither is a git checkout.
func DecisionCheckout(d OwnerDecision) string {
	for _, dir := range []string{d.Checkout, d.Repo} {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() && GitRoot(dir) != "" {
			return dir
		}
	}
	return ""
}

// AppendDecisionLog appends the entry for d's event (DecisionOpen,
// DecisionAnswered or DecisionWithdrawn) to the log in checkout and returns
// the log's path. The write goes through an os.Root, so a symlink in the
// checkout cannot take it to a file outside.
func AppendDecisionLog(checkout string, d OwnerDecision, event string) (string, error) {
	entry, err := decisionLogEntry(d, event)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir(filepath.Dir(DecisionLogRel), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	f, err := root.OpenFile(DecisionLogRel, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", DecisionLogRel)
	}
	if err != nil {
		f.Close()
		return "", err
	}
	if info.Size() == 0 {
		entry = decisionLogHeader + entry
	}
	if _, err := f.WriteString(entry); err != nil {
		f.Close()
		return "", err
	}
	return filepath.Join(checkout, DecisionLogRel), f.Close()
}

// logLine keeps posted text on one line, so no field can start a heading or
// an entry of its own.
func logLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func logTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func decisionLogEntry(d OwnerDecision, event string) (string, error) {
	name := d.ID
	if d.Ref != "" {
		name = logLine(d.Ref) + " (" + d.ID + ")"
	}
	var b strings.Builder
	item := func(label, value string) {
		if value = logLine(value); value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, value)
		}
	}
	switch event {
	case DecisionOpen:
		fmt.Fprintf(&b, "\n## %s posted: %s\n\n", name, logLine(d.Title))
		by := d.PostedBy.Leg
		if d.PostedBy.Model != "" {
			by += " (" + d.PostedBy.Model + ")"
		}
		if d.PostedBy.Log != "" {
			by += ", worker log " + d.PostedBy.Log
		}
		item("Posted", logTime(d.PostedAt))
		item("Posted by", by)
		item("Branch", d.Branch)
		item("Question", d.Question)
		item("Context", d.Context)
		item("Needed before", d.NeededBefore)
		if d.Record != "" {
			item("Record", "`"+logLine(d.Record)+"`")
		}
		item("GitHub", d.GitHub)
		b.WriteString("- Options:\n")
		for _, o := range d.Options {
			mark := ""
			if o.ID == d.Recommend {
				mark = " (recommended)"
			}
			fmt.Fprintf(&b, "  - %s%s: %s", o.ID, mark, logLine(o.Label))
			if detail := logLine(o.Detail); detail != "" {
				fmt.Fprintf(&b, " - %s", detail)
			}
			b.WriteString("\n")
		}
		if d.Recommend != "" {
			item("Why "+d.Recommend, d.Why)
		}
	case DecisionAnswered:
		if d.Answer == nil {
			return "", fmt.Errorf("decision %s has no answer", d.ID)
		}
		fmt.Fprintf(&b, "\n## %s answered: %s\n\n", name, logLine(d.Title))
		at := logTime(d.Answer.At)
		if d.Answer.Via != "" {
			at += ", in " + d.Answer.Via
		}
		item("Answered", at)
		choice := d.Answer.Option
		if o, ok := d.option(d.Answer.Option); ok {
			choice += ", " + o.Label
		}
		item("Choice", choice)
		if d.Recommend != "" && d.Recommend != d.Answer.Option {
			item("Recommended", d.Recommend)
		}
		item("Note", d.Answer.Note)
	case DecisionWithdrawn:
		fmt.Fprintf(&b, "\n## %s withdrawn: %s\n\n", name, logLine(d.Title))
		item("Withdrawn", logTime(time.Now()))
		item("Reason", d.Withdrawn)
	default:
		return "", fmt.Errorf("decision log: unknown event %q", event)
	}
	return b.String(), nil
}
