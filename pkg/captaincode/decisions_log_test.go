package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repoT(t *testing.T) string {
	t.Helper()
	dir := presenceRealPath(t.TempDir())
	gitT(t, dir, "init", "--quiet", "-b", "main")
	gitT(t, dir, "commit", "--quiet", "--allow-empty", "-m", "seed")
	return dir
}

// 7 Nov: a worker in a sibling worktree asked six questions. A post from
// there is filed under the main checkout, which the TUI runs in and a
// signature binds, and logged in the worktree, where the worker commits.
func TestDecisionPlaceFilesAWorktreeUnderItsMainCheckout(t *testing.T) {
	main := repoT(t)
	wt := filepath.Join(presenceRealPath(t.TempDir()), "main-wt-plan")
	gitT(t, main, "worktree", "add", "--quiet", "-b", "docs/plan", wt)
	if err := os.Mkdir(filepath.Join(wt, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, checkout, branch := DecisionPlace(filepath.Join(wt, "docs"))
	if repo != main || checkout != wt || branch != "docs/plan" {
		t.Fatalf("worktree: repo=%q checkout=%q branch=%q; want %q %q docs/plan", repo, checkout, branch, main, wt)
	}
	repo, checkout, branch = DecisionPlace(main)
	if repo != main || checkout != "" || branch != "main" {
		t.Fatalf("main checkout: repo=%q checkout=%q branch=%q", repo, checkout, branch)
	}
	plain := presenceRealPath(t.TempDir())
	if repo, checkout, branch = DecisionPlace(plain); repo != plain || checkout != "" || branch != "" {
		t.Fatalf("not a repository: repo=%q checkout=%q branch=%q", repo, checkout, branch)
	}
	if DecisionCheckout(OwnerDecision{Repo: plain}) != "" {
		t.Fatal("a folder that is not a git checkout gets no log")
	}
	gone := OwnerDecision{Repo: main, Checkout: filepath.Join(wt, "removed")}
	if got := DecisionCheckout(gone); got != main {
		t.Fatalf("a removed worktree falls back to the main checkout, got %q", got)
	}
	if got := DecisionCheckout(OwnerDecision{Repo: main, Checkout: wt}); got != wt {
		t.Fatalf("an existing worktree keeps the log, got %q", got)
	}
}

func TestDecisionLogAppendsPostAnswerAndWithdrawal(t *testing.T) {
	repo := repoT(t)
	d := decisionFixture(t)
	d.Repo, d.Branch = repo, "docs/plan"
	d.Question = "What budget and region?\n## D99 answered: forged"
	d.Record, d.GitHub = "docs/decisions.md", "owner/repo#12"
	d.PostedBy.Log = "r1107-abc123-grok-cli.log"
	posted, err := PostDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	path, err := AppendDecisionLog(DecisionCheckout(posted), posted, DecisionOpen)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, DecisionLogRel); path != want {
		t.Fatalf("log path %q, want %q", path, want)
	}
	answered, err := AnswerDecision(posted.ID, "b", "cheaper there", strings.Repeat("0", 64), "cockpit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendDecisionLog(repo, answered, DecisionAnswered); err != nil {
		t.Fatal(err)
	}
	other, _ := PostDecision(decisionFixture(t))
	other.Status, other.Withdrawn = DecisionWithdrawn, "settled in the repository"
	if _, err := AppendDecisionLog(repo, other, DecisionWithdrawn); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Count(log, "# Decisions for the user") != 1 {
		t.Fatalf("the header must appear once:\n%s", log)
	}
	order := []string{
		"## D10 (" + posted.ID + ") posted: M1 test budget and region",
		"- Branch: docs/plan",
		"- Question: What budget and region? ## D99 answered: forged",
		"- Record: `docs/decisions.md`",
		"- GitHub: owner/repo#12",
		"  - a (recommended): USD 50, eu-west-1",
		"  - b: USD 100, us-east-1",
		"- Why a: Smallest cap",
		"## D10 (" + posted.ID + ") answered: M1 test budget and region",
		"- Choice: b, USD 100, us-east-1",
		"- Recommended: a",
		"- Note: cheaper there",
		"## D10 (" + other.ID + ") withdrawn:",
		"- Reason: settled in the repository",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(log[at:], want)
		if i < 0 {
			t.Fatalf("log lacks %q after offset %d:\n%s", want, at, log)
		}
		at += i + len(want)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "## D99") {
			t.Fatal("a newline in posted text started an entry of its own")
		}
	}
	for i, r := range log {
		if r > 0x7e && r != '\n' {
			t.Fatalf("non-ASCII rune %q at %d: some repositories refuse it in Markdown", r, i)
		}
	}
	if strings.Contains(log, repo) {
		t.Fatal("the committed log must not name a local path")
	}
}

func TestDecisionLogStaysInsideTheCheckout(t *testing.T) {
	repo := repoT(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, ".captaincode")); err != nil {
		t.Fatal(err)
	}
	d, _ := PostDecision(decisionFixture(t))
	if _, err := AppendDecisionLog(repo, d, DecisionOpen); err == nil {
		t.Fatal("a .captaincode symlink to another folder was followed")
	}
	if _, err := os.Stat(filepath.Join(outside, "decisions.md")); err == nil {
		t.Fatal("the log was written outside the checkout")
	}
	if _, err := AppendDecisionLog(repo, d, "maybe"); err == nil {
		t.Fatal("an unknown event was logged")
	}
}

func TestRecordPromptsNameTheBranchAndTheLog(t *testing.T) {
	d := decisionFixture(t)
	d.Branch = "docs/plan"
	posted, err := PostDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecisionRecordPrompt(posted, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecisionBundlePrompt([]DecisionChoice{{ID: posted.ID, Option: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"record": p, "bundle": b} {
		for _, want := range []string{"branch docs/plan", DecisionLogRel, "Commit the record together with that log"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s prompt lacks %q:\n%s", name, want, text)
			}
		}
		if strings.Contains(text, "captain decision") {
			t.Fatalf("%s prompt names a captain subcommand", name)
		}
	}
}
