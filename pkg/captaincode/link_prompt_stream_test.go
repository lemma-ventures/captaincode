package captaincode

import (
	"context"
	"errors"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinkStreamPromptReceipts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		refuse bool
		want   string
	}{
		{name: "accepted", want: "prompt-one:accepted:"},
		{name: "refused", refuse: true, want: "prompt-one:refused:folder_not_linked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newStreamFixture(t)
			signed := pres.Signed{Text: "inspect", Envelope: pres.Envelope{V: 1, Machine: fx.machine, Folder: "oak", TextSHA256: pres.HashText("inspect"), IssuedAt: time.Now().Unix() - 1, ExpiresAt: time.Now().Unix() + 60, Nonce: pres.Encode(make([]byte, 32))}, Assertion: pres.Assertion{CredentialID: "credential"}}
			prompt, err := NewLinkPrompt("stream-one", "prompt-one", signed)
			require.NoError(t, err)
			fx.hub.prompt = &prompt
			received := make(chan struct{}, 1)
			opts := fx.options()
			opts.Prompt = func(got LinkPrompt) error {
				assert.Equal(t, prompt.PromptID, got.PromptID)
				assert.Equal(t, signed.Text, got.Text)
				received <- struct{}{}
				if tc.refuse {
					return pres.Err("folder_not_linked")
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct {
				report LinkStreamReport
				err    error
			}, 1)
			go func() {
				report, err := LinkStreamLoop(ctx, opts)
				done <- struct {
					report LinkStreamReport
					err    error
				}{report, err}
			}()
			select {
			case <-received:
			case <-ctx.Done():
				t.Fatal("prompt not delivered")
			}
			require.Eventually(t, func() bool {
				fx.hub.mu.Lock()
				defer fx.hub.mu.Unlock()
				return len(fx.hub.promptAck) == 1 && fx.hub.promptAck[0] == tc.want
			}, 3*time.Second, 10*time.Millisecond)
			cancel()
			got := <-done
			if !errors.Is(got.err, context.Canceled) {
				t.Fatal(got.err)
			}
			if tc.refuse {
				assert.Equal(t, 1, got.report.PromptRefusals)
				assert.Zero(t, got.report.Prompts)
			} else {
				assert.Equal(t, 1, got.report.Prompts)
				assert.Zero(t, got.report.PromptRefusals)
			}
		})
	}
}
