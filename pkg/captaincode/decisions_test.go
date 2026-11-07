package captaincode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decisionFixture(t *testing.T) OwnerDecision {
	t.Helper()
	t.Setenv(DecisionsDirEnv, t.TempDir())
	return OwnerDecision{
		Ref: "D10", Repo: "/tmp/repo", Title: "M1 test budget and region",
		Question: "What budget and region does the M1 cloud test run use?",
		Context:  "The loop runs tests in the local sandbox until this is set.",
		Options: []DecisionOption{
			{ID: "a", Label: "USD 50, eu-west-1"},
			{ID: "b", Label: "USD 100, us-east-1"},
			{ID: "c", Label: "No cloud run yet"},
		},
		Recommend: "a", Why: "Smallest cap that covers one full M1 pass.",
		PostedBy: DecisionPoster{Leg: "grok-cli", Model: "grok-4.7"},
	}
}

func TestPostDecisionStoresAnOpenItem(t *testing.T) {
	d := decisionFixture(t)
	got, err := PostDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ID, "dq-") || got.Status != DecisionOpen || got.PostedAt.IsZero() {
		t.Fatalf("bad stored decision: %+v", got)
	}
	list, err := ListDecisions(DecisionOpen)
	if err != nil || len(list) != 1 || list[0].ID != got.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	st, _ := os.Stat(filepath.Join(os.Getenv(DecisionsDirEnv), got.ID+".json"))
	if st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("register file must be private: %v", st)
	}
}

func TestPostDecisionRejectsMalformedItems(t *testing.T) {
	cases := map[string]func(*OwnerDecision){
		"one option":           func(d *OwnerDecision) { d.Options = d.Options[:1] },
		"duplicate option ids": func(d *OwnerDecision) { d.Options[1].ID = "a" },
		"recommend not listed": func(d *OwnerDecision) { d.Recommend = "z" },
		"no question":          func(d *OwnerDecision) { d.Question = "" },
		"no repo":              func(d *OwnerDecision) { d.Repo = "" },
		"bad option id":        func(d *OwnerDecision) { d.Options[0].ID = "a b" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := decisionFixture(t)
			mutate(&d)
			if _, err := PostDecision(d); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A posted context is read on a phone through a relay; a key pasted into
// it by a worker must not travel.
func TestPostDecisionMasksSecrets(t *testing.T) {
	d := decisionFixture(t)
	key := "sk-ant-" + "api03-" + strings.Repeat("A", 40)
	d.Context = "the worker set ANTHROPIC_API_KEY=" + key
	got, err := PostDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Context, key) {
		t.Fatalf("secret kept: %q", got.Context)
	}
}

// The record prompt is what the user signs. It names only an option the
// agent offered, so a relay that swaps the text can at most pick another of
// those options, and the same inputs always give the same bytes.
func TestDecisionRecordPrompt(t *testing.T) {
	d, err := PostDecision(decisionFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p1, err := DecisionRecordPrompt(d, "a", "start in eu")
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := DecisionRecordPrompt(d, "a", "start in eu")
	if p1 != p2 {
		t.Fatal("record prompt is not deterministic")
	}
	for _, want := range []string{d.ID, "D10", "option a", "USD 50, eu-west-1", "start in eu", "captain decision answered " + d.ID} {
		if !strings.Contains(p1, want) {
			t.Fatalf("record prompt lacks %q:\n%s", want, p1)
		}
	}
	if _, err := DecisionRecordPrompt(d, "z", ""); err == nil {
		t.Fatal("an option the agent did not offer was accepted")
	}
}

func TestAnswerDecision(t *testing.T) {
	d, _ := PostDecision(decisionFixture(t))
	prompt, _ := DecisionRecordPrompt(d, "b", "")
	got, err := AnswerDecision(d.ID, "b", "", SHA256Hex(prompt), "galaxy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != DecisionAnswered || got.Answer == nil || got.Answer.Option != "b" || got.Answer.PromptSHA256 != SHA256Hex(prompt) {
		t.Fatalf("bad answer: %+v", got)
	}
	if open, _ := ListDecisions(DecisionOpen); len(open) != 0 {
		t.Fatalf("answered item still open: %+v", open)
	}
	if _, err := AnswerDecision(d.ID, "a", "", "", "galaxy"); err == nil {
		t.Fatal("a second answer overwrote the first")
	}
	if _, err := AnswerDecision("dq-missing", "a", "", "", "galaxy"); err == nil {
		t.Fatal("answered an unknown decision")
	}
}

func TestAnswerRejectsAnUnofferedOption(t *testing.T) {
	d, _ := PostDecision(decisionFixture(t))
	if _, err := AnswerDecision(d.ID, "z", "", "", "galaxy"); err == nil {
		t.Fatal("accepted an option the agent did not offer")
	}
}

func TestWithdrawDecision(t *testing.T) {
	d, _ := PostDecision(decisionFixture(t))
	if _, err := WithdrawDecision(d.ID, "superseded by D12"); err != nil {
		t.Fatal(err)
	}
	if open, _ := ListDecisions(DecisionOpen); len(open) != 0 {
		t.Fatal("withdrawn item still open")
	}
	if all, _ := ListDecisions(""); len(all) != 1 || all[0].Status != DecisionWithdrawn {
		t.Fatalf("withdrawn item lost: %+v", all)
	}
}

// The user opens the worker log that raised the decision to read the
// context; the poster does not know its own log name, so it is found by
// folder and leg among the run logs.
func TestFindRunLog(t *testing.T) {
	dir := t.TempDir()
	write := func(name, cwd string, age time.Duration) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("# captain worker log\nleg: grok-cli\ncwd: "+cwd+"\n---\n"), 0o600)
		ts := time.Now().Add(-age)
		os.Chtimes(p, ts, ts)
	}
	write("r1107-aaaaaa-grok-cli.log", "/repo/x", 3*time.Hour)
	write("r1107-bbbbbb-grok-cli.log", "/repo/x", time.Minute)
	write("r1107-cccccc-grok-cli.log", "/repo/other", 0)
	write("r1107-dddddd-codex-cli.log", "/repo/x", 0)
	if got := FindRunLog(dir, "/repo/x", "grok-cli"); got != "r1107-bbbbbb-grok-cli.log" {
		t.Fatalf("got %q", got)
	}
	if got := FindRunLog(dir, "/repo/none", "grok-cli"); got != "" {
		t.Fatalf("got %q for a folder with no run", got)
	}
}
