package main

import (
	"errors"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
)

func TestBtwRefusalIncludesTheTransportReason(t *testing.T) {
	b := teamBrain()
	s := captaincode.NewSteer(t.TempDir())
	s.Began(captaincode.LegCodexCLI)
	defer s.Ended(captaincode.LegCodexCLI)
	detach := s.Attach(captaincode.LegCodexCLI, func(string) error {
		return errors.New("codex app-server refused request: turn is not steerable")
	})
	defer detach()
	took, deaf, msg := b.btwDeliverTurns(s.Dir, "keep the helper", []*captaincode.Steer{s}, false)
	assert.Empty(t, took)
	assert.Equal(t, []captaincode.Leg{captaincode.LegCodexCLI}, deaf)
	assert.Contains(t, msg, "next turn")
	assert.Contains(t, msg, "turn is not steerable")
}
