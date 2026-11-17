package captaincode

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func BenchmarkLinkDecisionDigest(b *testing.B) {
	card := testCard("dq-261117-0a1b2c")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := LinkDecisionDigest(card); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLinkDecisionDigestCoversWholeCard(t *testing.T) {
	card := testCard("dq-261117-0a1b2c")
	digest, err := LinkDecisionDigest(card)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest: %q, %v", digest, err)
	}
	for _, change := range []func(*LinkDecisionCard){
		func(c *LinkDecisionCard) { c.Title += "changed" },
		func(c *LinkDecisionCard) { c.Question += "changed" },
		func(c *LinkDecisionCard) { c.Options[0].Label += "changed" },
		func(c *LinkDecisionCard) { c.PostedAt++ },
	} {
		changed := card
		changed.Options = append([]LinkDecisionOption(nil), card.Options...)
		change(&changed)
		got, err := LinkDecisionDigest(changed)
		if err != nil || got == digest {
			t.Fatalf("a card change kept its digest: %q, %v", got, err)
		}
	}
}

func TestLinkDecisionDigestCompatibilityVector(t *testing.T) {
	card := testCard("dq-261117-0a1b2c")
	card.PostedAt = 1800000000
	digest, err := LinkDecisionDigest(card)
	if err != nil {
		t.Fatal(err)
	}
	const want = "40cc07ccabbe589359c51935326da023181849fcf3b80e5246ad90ca313c2939"
	if digest != want {
		t.Fatalf("digest = %s, want %s", digest, want)
	}
}

func TestLinkDecisionAnswerValidation(t *testing.T) {
	now := time.Now()
	valid := LinkDecisionAnswer{StreamID: "stream-one", AnswerID: "answer-one", DecisionID: "dq-261117-0a1b2c", CardSHA256: strings.Repeat("a", 64), Option: "a", ExpiresAt: now.Add(time.Minute).Unix()}
	if err := linkDecisionAnswerValid(valid, now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*LinkDecisionAnswer){
		func(a *LinkDecisionAnswer) { a.AnswerID = "bad/id" },
		func(a *LinkDecisionAnswer) { a.DecisionID = "other" },
		func(a *LinkDecisionAnswer) { a.CardSHA256 = strings.Repeat("z", 64) },
		func(a *LinkDecisionAnswer) { a.Option = "not offered!" },
		func(a *LinkDecisionAnswer) { a.Note = strings.Repeat("n", 1001) },
		func(a *LinkDecisionAnswer) { a.ExpiresAt = now.Add(-time.Second).Unix() },
		func(a *LinkDecisionAnswer) { a.ExpiresAt = now.Add(time.Hour).Unix() },
	} {
		answer := valid
		change(&answer)
		if err := linkDecisionAnswerValid(answer, now); err == nil {
			t.Fatalf("invalid answer accepted: %+v", answer)
		}
	}
}

func TestLinkStreamAppliesAndAcknowledgesDecisionAnswer(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.closeAfterPings = 2
	fx.hub.answer = &LinkDecisionAnswer{AnswerID: "answer-one", DecisionID: "dq-261117-0a1b2c", CardSHA256: strings.Repeat("a", 64), Option: "b", Note: "chosen on the web", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	var mu sync.Mutex
	var applied []LinkDecisionAnswer
	opts := fx.options()
	opts.Answer = func(answer LinkDecisionAnswer) error {
		mu.Lock()
		applied = append(applied, answer)
		mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		report LinkStreamReport
		err    error
	}, 1)
	go func() {
		report, err := LinkStreamLoop(ctx, opts)
		result <- struct {
			report LinkStreamReport
			err    error
		}{report, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		fx.hub.mu.Lock()
		receipts := len(fx.hub.receipts)
		fx.hub.mu.Unlock()
		if receipts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer was not acknowledged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	got := <-result
	if got.err != context.Canceled {
		t.Fatalf("stream: %v", got.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) == 0 || applied[0].DecisionID != fx.hub.answer.DecisionID || applied[0].StreamID == "" {
		t.Fatalf("answers: %+v", applied)
	}
	if got.report.Answers == 0 {
		t.Fatalf("answer was not reported: %+v", got.report)
	}
}

func TestLinkStreamDoesNotReceiptRefusedAnswer(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.closeAfterPings = 2
	fx.hub.answer = &LinkDecisionAnswer{AnswerID: "answer-one", DecisionID: "dq-261117-0a1b2c", CardSHA256: strings.Repeat("a", 64), Option: "b", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	opts := fx.options()
	opts.Answer = func(LinkDecisionAnswer) error { return context.DeadlineExceeded }
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	report, err := LinkStreamLoop(ctx, opts)
	if err != context.DeadlineExceeded {
		t.Fatalf("stream: %v", err)
	}
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	if len(fx.hub.receipts) != 0 || report.AnswerErrors == 0 {
		t.Fatalf("refused answer was acknowledged: %+v, %+v", fx.hub.receipts, report)
	}
}
