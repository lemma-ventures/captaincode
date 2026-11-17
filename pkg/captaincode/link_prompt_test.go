package captaincode

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinkPromptRoundTripAndRefusals(t *testing.T) {
	now := time.Now().Unix()
	signed := pres.Signed{Text: "inspect the repository", Envelope: pres.Envelope{V: 1, Machine: "machine-one", Folder: "repo", TextSHA256: pres.HashText("inspect the repository"), IssuedAt: now - 1, ExpiresAt: now + 60, Nonce: pres.Encode(make([]byte, 32))}, Assertion: pres.Assertion{CredentialID: "credential", AuthenticatorData: "auth", ClientDataJSON: "client", Signature: "signature"}}
	prompt, err := NewLinkPrompt("stream-one", "prompt-one", signed)
	require.NoError(t, err)
	decoded, err := prompt.Signed()
	require.NoError(t, err)
	assert.Equal(t, signed, decoded)
	assert.Equal(t, "inspect the repository", prompt.Text)

	bad := prompt
	bad.EnvelopeB64 = base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"machine":"machine-one","folder":"repo","text_sha256":"` + signed.Envelope.TextSHA256 + `","issued_at":1,"expires_at":2,"nonce":"` + signed.Envelope.Nonce + `","extra":true}`))
	_, err = bad.Signed()
	assert.Error(t, err)
	bad = prompt
	bad.Text = "changed"
	decoded, err = bad.Signed()
	require.NoError(t, err)
	assert.NotEqual(t, decoded.Envelope.TextSHA256, pres.HashText(decoded.Text))
	bad = prompt
	bad.EnvelopeB64 = base64.RawURLEncoding.EncodeToString([]byte(`not-json`))
	_, err = bad.Signed()
	assert.Error(t, err)
}

func BenchmarkLinkPromptSigned(b *testing.B) {
	signed := pres.Signed{Text: "inspect", Envelope: pres.Envelope{V: 1, Machine: "machine-one", Folder: "repo", TextSHA256: pres.HashText("inspect"), IssuedAt: 1, ExpiresAt: 2, Nonce: pres.Encode(make([]byte, 32))}, Assertion: pres.Assertion{CredentialID: "credential", AuthenticatorData: "auth", ClientDataJSON: "client", Signature: "signature"}}
	prompt, err := NewLinkPrompt("stream-one", "prompt-one", signed)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prompt.Signed(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLinkPromptRejectsPrepareAndOversize(t *testing.T) {
	signed := pres.Signed{Text: "inspect", Envelope: pres.Envelope{V: 1, Machine: "machine-one", Folder: "project", TextSHA256: pres.HashText("inspect"), IssuedAt: 1, ExpiresAt: 2, Nonce: pres.Encode(make([]byte, 32)), Prepare: true}}
	_, err := NewLinkPrompt("stream-one", "prompt-one", signed)
	assert.Error(t, err)
	raw, _ := json.Marshal(pres.Envelope{V: 1, Machine: "machine-one", Folder: "repo", TextSHA256: pres.HashText("inspect"), IssuedAt: 1, ExpiresAt: 2, Nonce: pres.Encode(make([]byte, 32))})
	assertion, _ := json.Marshal(pres.Assertion{})
	prompt := LinkPrompt{StreamID: "stream-one", PromptID: "prompt-one", EnvelopeB64: base64.RawURLEncoding.EncodeToString(raw), AssertionB64: base64.RawURLEncoding.EncodeToString(assertion), Text: string(make([]byte, linkPromptTextLimit+1))}
	_, err = prompt.Signed()
	assert.Error(t, err)
}
