package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Naming a session must stay cheap. It was not: a title request carries the
// user's whole prompt, so it went through the full worker path - on 2026-09-11
// one stalled on the free leg after 4 minutes and was REROUTED to ds-flash,
// spending a second leg on a two-word answer while the user watched their
// /frontier turn apparently run on "captain-free".

func TestIsTitlePrompt(t *testing.T) {
	assert.True(t, IsTitlePrompt("You are a title generator. Name this conversation."))
	assert.True(t, IsTitlePrompt("[system]\nYou are a title generator\n[user]\nfix the flaky test"))
	assert.False(t, IsTitlePrompt("write a title for the blog post about routing"),
		"a user asking for a title is real work")
	assert.False(t, IsTitlePrompt(""))
}

func TestTitleBudgetIsShortRegardlessOfTheWorkerBudget(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_TIMEOUT", "25m")
	base, ceil := titleBudget()
	assert.LessOrEqual(t, base, 2*time.Minute, "a title that takes minutes is a bug, not a long task")
	assert.Less(t, ceil, workerTimeout(), "and it must never inherit the full worker ceiling")
	assert.Greater(t, ceil, base)
}
