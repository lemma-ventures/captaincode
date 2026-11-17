package captaincode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	linkSummaryDoneLimit  = 4000
	linkSummaryFieldLimit = 2000
)

type LinkTurnSummary struct {
	PromptID    string   `json:"prompt_id"`
	Status      string   `json:"status"`
	Done        string   `json:"done"`
	Learned     string   `json:"learned,omitempty"`
	Left        string   `json:"left,omitempty"`
	Limitations string   `json:"limitations,omitempty"`
	Decisions   []string `json:"decisions,omitempty"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	CostStatus  string   `json:"cost_status"`
	CompletedAt int64    `json:"completed_at"`
}

func linkSummaryClip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	if cut < 1 {
		return "…"
	}
	text = text[:cut]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return strings.TrimSpace(text) + "…"
}

func linkSummaryText(text string, limit int) string {
	masked, _ := MaskSecrets(text, "[secret]")
	for _, rule := range identityRules() {
		masked = strings.ReplaceAll(masked, rule.real, rule.fake)
	}
	return linkSummaryClip(masked, limit)
}

func NewLinkTurnSummary(promptID, result string, completedAt time.Time) LinkTurnSummary {
	summary := LinkTurnSummary{
		PromptID: promptID, Status: "completed", Done: linkSummaryText(result, linkSummaryDoneLimit),
		CostStatus: "unknown", CompletedAt: completedAt.Unix(),
	}
	if summary.Done == "" {
		summary.Done = "The read-only turn completed without a text result."
	}
	if strings.HasPrefix(result, "failed:") {
		summary.Status = "failed"
		summary.Done = "The read-only turn did not complete."
		summary.Limitations = linkSummaryText(strings.TrimSpace(strings.TrimPrefix(result, "failed:")), linkSummaryFieldLimit)
	}
	return summary
}

func LinkTurnSummaryValid(s LinkTurnSummary) error {
	if !linkIdentifier.MatchString(s.PromptID) || (s.Status != "completed" && s.Status != "failed") {
		return fmt.Errorf("a turn summary has an invalid prompt id or status")
	}
	if s.Done == "" || len(s.Done) > linkSummaryDoneLimit || len(s.Learned) > linkSummaryFieldLimit || len(s.Left) > linkSummaryFieldLimit || len(s.Limitations) > linkSummaryFieldLimit || s.CompletedAt <= 0 {
		return fmt.Errorf("a turn summary is outside the text or time bounds")
	}
	if len(s.Decisions) > 16 {
		return fmt.Errorf("a turn summary names too many decisions")
	}
	seen := map[string]bool{}
	for _, id := range s.Decisions {
		if !decisionIDRe.MatchString(id) || seen[id] {
			return fmt.Errorf("a turn summary names an invalid or repeated decision")
		}
		seen[id] = true
	}
	switch s.CostStatus {
	case "unknown":
		if s.CostUSD != nil {
			return fmt.Errorf("an unknown turn cost cannot carry an amount")
		}
	case "estimated", "measured":
		if s.CostUSD == nil || math.IsNaN(*s.CostUSD) || math.IsInf(*s.CostUSD, 0) || *s.CostUSD < 0 || *s.CostUSD > 1_000_000 {
			return fmt.Errorf("a turn summary carries an invalid cost")
		}
	default:
		return fmt.Errorf("a turn summary carries an invalid cost status")
	}
	return nil
}

func linkStreamPostSummary(ctx context.Context, client *http.Client, opts LinkStreamOptions, pub ed25519.PublicKey, summary LinkTurnSummary) error {
	posted, err := LinkSign(opts.Key, opts.MachineID, linkBrainDirection, "turn.summary", summary, time.Now())
	if err != nil {
		return linkRefuse("the link signing key is missing: %v", err)
	}
	raw, err := json.Marshal(posted)
	if err != nil || len(raw) > linkMaxBody {
		return linkRefuse("the turn summary this machine builds is too large")
	}
	reqCtx, cancel := context.WithTimeout(ctx, linkPostLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, linkEndpoint(opts.Hub.URL, "/messages"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("the turn summary request could not be built: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the turn summary could not be delivered: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
	if err != nil || len(body) > linkMaxBody {
		return fmt.Errorf("the hub's turn acknowledgement could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		return linkStreamStatus(pub, opts.MachineID, resp.StatusCode, body)
	}
	var reply LinkEnvelope
	if LinkDecodeEnvelope(body, &reply) != nil || LinkVerify(pub, reply, linkHubDirection, time.Now()) != nil || reply.MachineID != opts.MachineID || reply.Type != "turn.accepted" {
		return linkRefuse("the hub's turn acknowledgement cannot be verified")
	}
	var ack struct {
		MessageID string `json:"message_id"`
		PromptID  string `json:"prompt_id"`
	}
	if LinkDecodePayload(reply, &ack) != nil || ack.MessageID != posted.ID || ack.PromptID != summary.PromptID {
		return linkRefuse("the hub acknowledged a different turn summary")
	}
	return nil
}
