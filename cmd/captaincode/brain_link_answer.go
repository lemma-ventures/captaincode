package main

import (
	"fmt"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// linkDecisionAnswer applies an owner answer only when it names the exact
// card this machine offered. A retry after a lost receipt reconstructs the
// open card from the matching answered register entry, so idempotence does
// not weaken the digest check.
func (b *brain) linkDecisionAnswer(answer captaincode.LinkDecisionAnswer) error {
	cards, err := b.linkDecisionCards()
	if err != nil {
		return err
	}
	for _, card := range cards {
		if card.DecisionID != answer.DecisionID {
			continue
		}
		if card.Status == captaincode.DecisionAnswered && card.Answer == answer.Option {
			card.Status = captaincode.DecisionOpen
			card.Answer = ""
		}
		if card.Status != captaincode.DecisionOpen {
			return fmt.Errorf("decision is no longer open")
		}
		digest, err := captaincode.LinkDecisionDigest(card)
		if err != nil {
			return err
		}
		if digest != answer.CardSHA256 {
			return fmt.Errorf("decision answer names another card revision")
		}
		before, err := captaincode.GetDecision(answer.DecisionID)
		if err != nil {
			return err
		}
		answered, err := captaincode.AnswerDecision(answer.DecisionID, answer.Option, answer.Note, "", "hub")
		if err == nil && before.Status == captaincode.DecisionOpen {
			logDecision(answered, captaincode.DecisionAnswered)
		}
		return err
	}
	return fmt.Errorf("decision is not offered by a linked folder")
}
