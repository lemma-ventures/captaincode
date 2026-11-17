package captaincode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
)

const (
	linkPromptTextLimit      = 16 * 1024
	linkPromptEnvelopeLimit  = 4 * 1024
	linkPromptAssertionLimit = 16 * 1024
)

type LinkPrompt struct {
	StreamID     string `json:"stream_id"`
	PromptID     string `json:"prompt_id"`
	EnvelopeB64  string `json:"envelope_b64"`
	AssertionB64 string `json:"assertion_b64"`
	Text         string `json:"text"`
}

func NewLinkPrompt(streamID, promptID string, signed pres.Signed) (LinkPrompt, error) {
	envelope, err := json.Marshal(signed.Envelope)
	if err != nil {
		return LinkPrompt{}, err
	}
	assertion, err := json.Marshal(signed.Assertion)
	if err != nil {
		return LinkPrompt{}, err
	}
	prompt := LinkPrompt{StreamID: streamID, PromptID: promptID, EnvelopeB64: pres.Encode(envelope), AssertionB64: pres.Encode(assertion), Text: signed.Text}
	if _, err := prompt.Signed(); err != nil {
		return LinkPrompt{}, err
	}
	if pres.HashText(signed.Text) != signed.Envelope.TextSHA256 {
		return LinkPrompt{}, fmt.Errorf("prompt text does not match its signed digest")
	}
	return prompt, nil
}

func (p LinkPrompt) Signed() (pres.Signed, error) {
	var signed pres.Signed
	if !linkIdentifier.MatchString(p.StreamID) || !linkIdentifier.MatchString(p.PromptID) {
		return signed, fmt.Errorf("invalid prompt stream or id")
	}
	if p.Text == "" || len(p.Text) > linkPromptTextLimit {
		return signed, fmt.Errorf("prompt text is empty or too large")
	}
	envelope, err := pres.Decode(p.EnvelopeB64)
	if err != nil || len(envelope) > linkPromptEnvelopeLimit {
		return signed, fmt.Errorf("invalid prompt envelope encoding or size")
	}
	assertion, err := pres.Decode(p.AssertionB64)
	if err != nil || len(assertion) > linkPromptAssertionLimit {
		return signed, fmt.Errorf("invalid prompt assertion encoding or size")
	}
	if err := linkPromptJSON(envelope, &signed.Envelope); err != nil {
		return pres.Signed{}, err
	}
	if err := linkPromptJSON(assertion, &signed.Assertion); err != nil {
		return pres.Signed{}, err
	}
	if signed.Envelope.Prepare {
		return pres.Signed{}, fmt.Errorf("a linked prompt cannot prepare a project")
	}
	signed.Text = p.Text
	return signed, nil
}

func LinkPromptReceiptCode(err error) string {
	if err == nil {
		return ""
	}
	code := pres.Code(err)
	if !linkIdentifier.MatchString(code) || len(code) > 64 {
		return "local_refusal"
	}
	return code
}

func linkPromptJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing prompt JSON")
	}
	return nil
}
