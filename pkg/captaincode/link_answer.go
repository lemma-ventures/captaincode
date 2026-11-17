package captaincode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// LinkDecisionAnswer is one owner answer on its way down. CardSHA256 binds it
// to the exact card the owner saw; the brain recomputes that digest from its
// own register before it changes any local state.
type LinkDecisionAnswer struct {
	StreamID   string `json:"stream_id"`
	AnswerID   string `json:"answer_id"`
	DecisionID string `json:"decision_id"`
	CardSHA256 string `json:"card_sha256"`
	Option     string `json:"option"`
	Note       string `json:"note,omitempty"`
	ExpiresAt  int64  `json:"expires_at"`
}

// LinkDecisionDigest identifies the complete wire card, not only its question
// or decision id. Both sides use the same JSON field order and omit rules.
func LinkDecisionDigest(card LinkDecisionCard) (string, error) {
	raw, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func linkDecisionAnswerValid(answer LinkDecisionAnswer, now time.Time) error {
	if !linkIdentifier.MatchString(answer.StreamID) || !linkIdentifier.MatchString(answer.AnswerID) {
		return fmt.Errorf("a decision answer has an invalid stream or answer id")
	}
	if !decisionIDRe.MatchString(answer.DecisionID) || !decisionOptionID.MatchString(answer.Option) {
		return fmt.Errorf("a decision answer has an invalid decision or option id")
	}
	if len(answer.CardSHA256) != 64 {
		return fmt.Errorf("a decision answer has an invalid card digest")
	}
	if _, err := hex.DecodeString(answer.CardSHA256); err != nil {
		return fmt.Errorf("a decision answer has an invalid card digest")
	}
	if len(answer.Note) > 1000 || answer.ExpiresAt <= now.Unix() || answer.ExpiresAt > now.Add(10*time.Minute).Unix() {
		return fmt.Errorf("a decision answer has an invalid note or expiry")
	}
	return nil
}
