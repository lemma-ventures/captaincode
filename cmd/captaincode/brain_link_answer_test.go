package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestLinkDecisionAnswerBindsExactCardAndRetries(t *testing.T) {
	b := linkStreamFixture(t)
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := captaincode.LinkAdd(repo, captaincode.LinkModeActive); err != nil {
		t.Fatal(err)
	}
	linkStreamWriteDecision(t, linkStreamDecision("dq-261117-0a1b2c", repo))
	cards, err := b.linkDecisionCards()
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %v, %d", err, len(cards))
	}
	digest, err := captaincode.LinkDecisionDigest(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	answer := captaincode.LinkDecisionAnswer{
		AnswerID: "answer-one", DecisionID: cards[0].DecisionID,
		CardSHA256: digest, Option: "b", Note: "use the second provider",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	if err := b.linkDecisionAnswer(answer); err != nil {
		t.Fatal(err)
	}
	got, err := captaincode.GetDecision(answer.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != captaincode.DecisionAnswered || got.Answer == nil || got.Answer.Option != "b" || got.Answer.Via != "hub" {
		t.Fatalf("answer did not reach the register: %+v", got)
	}
	if err := b.linkDecisionAnswer(answer); err != nil {
		t.Fatalf("an identical retry was not idempotent: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(repo, ".captaincode", "decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), " answered:") != 1 {
		t.Fatalf("a retry duplicated the repository decision log:\n%s", log)
	}
	answer.Option = "a"
	if err := b.linkDecisionAnswer(answer); err == nil {
		t.Fatal("a different option reused an answered card")
	}
}

func TestLinkDecisionAnswerRefusesOtherCardAndUnlinkedDecision(t *testing.T) {
	b := linkStreamFixture(t)
	repo, other := t.TempDir(), t.TempDir()
	if _, err := captaincode.LinkAdd(repo, captaincode.LinkModeActive); err != nil {
		t.Fatal(err)
	}
	linkStreamWriteDecision(t, linkStreamDecision("dq-261117-0a1b2c", repo))
	linkStreamWriteDecision(t, linkStreamDecision("dq-261117-0d1e2f", other))
	cards, err := b.linkDecisionCards()
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %v, %d", err, len(cards))
	}
	digest, _ := captaincode.LinkDecisionDigest(cards[0])
	answer := captaincode.LinkDecisionAnswer{DecisionID: cards[0].DecisionID, CardSHA256: strings.Repeat("0", 64), Option: "a"}
	if err := b.linkDecisionAnswer(answer); err == nil {
		t.Fatal("a different card digest was accepted")
	}
	answer.CardSHA256 = digest
	answer.DecisionID = "dq-261117-0d1e2f"
	if err := b.linkDecisionAnswer(answer); err == nil {
		t.Fatal("an unlinked folder's decision was answered")
	}
	got, err := captaincode.GetDecision(cards[0].DecisionID)
	if err != nil || got.Status != captaincode.DecisionOpen {
		t.Fatalf("a refused answer changed the register: %+v, %v", got, err)
	}
}
