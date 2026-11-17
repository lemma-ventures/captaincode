package captaincode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func linkStreamPostPromptReceipt(ctx context.Context, client *http.Client, opts LinkStreamOptions, pub ed25519.PublicKey, prompt LinkPrompt, acceptErr error) error {
	status := "accepted"
	code := LinkPromptReceiptCode(acceptErr)
	if acceptErr != nil {
		status = "refused"
	}
	payload := struct {
		PromptID string `json:"prompt_id"`
		Status   string `json:"status"`
		Code     string `json:"code,omitempty"`
	}{PromptID: prompt.PromptID, Status: status, Code: code}
	posted, err := LinkSign(opts.Key, opts.MachineID, linkBrainDirection, "prompt.receipt", payload, time.Now())
	if err != nil {
		return linkRefuse("the link signing key is missing: %v", err)
	}
	raw, err := json.Marshal(posted)
	if err != nil || len(raw) > linkMaxBody {
		return linkRefuse("the prompt receipt this machine builds is too large")
	}
	reqCtx, cancel := context.WithTimeout(ctx, linkPostLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, linkEndpoint(opts.Hub.URL, "/messages"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("the prompt receipt request could not be built: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the prompt receipt could not be delivered: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
	if err != nil {
		return fmt.Errorf("the hub's prompt acknowledgement could not be read: %w", err)
	}
	if len(body) > linkMaxBody {
		return linkRefuse("the hub's prompt acknowledgement is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return linkStreamStatus(pub, opts.MachineID, resp.StatusCode, body)
	}
	var reply LinkEnvelope
	if err := LinkDecodeEnvelope(body, &reply); err != nil {
		return linkRefuse("the hub's prompt acknowledgement is not a link envelope")
	}
	if err := LinkVerify(pub, reply, linkHubDirection, time.Now()); err != nil {
		return linkRefuse("the hub's prompt acknowledgement cannot be verified")
	}
	if reply.MachineID != opts.MachineID || reply.Type != "prompt.acknowledged" {
		return linkRefuse("the hub answered a prompt receipt with another machine or message type")
	}
	var ack struct {
		PromptID string `json:"prompt_id"`
		Status   string `json:"status"`
	}
	if err := LinkDecodePayload(reply, &ack); err != nil {
		return linkRefuse("the hub's prompt acknowledgement cannot be read")
	}
	if ack.PromptID != prompt.PromptID || ack.Status != status {
		return linkRefuse("the hub acknowledged a different prompt receipt")
	}
	return nil
}
