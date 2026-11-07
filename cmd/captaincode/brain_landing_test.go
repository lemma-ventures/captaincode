package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Workers left 91 merged worktrees and 183 merged branches in one repository
// (2026-11-07); each worker is told to clean up what it landed.
func TestLandingContractTellsWorkersToCleanUp(t *testing.T) {
	c := landingContract()
	assert.Contains(t, c, "git worktree remove")
	assert.Contains(t, c, "git branch -d")
	assert.Contains(t, c, "captain.sweep.branches", "a repository that keeps its branches is respected")
	assert.Contains(t, c, "git worktree lock", "a cited checkout has a way to stay")
	assert.Contains(t, c, "uncommitted", "never at the cost of unlanded work")
}

func TestLandingContractCanBeTurnedOff(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_LANDING", "0")
	assert.Empty(t, landingContract())
}

// "D10 still blocks a cloud run" sat in a worker log; the user found it by
// reading the log (2026-11-07). A worker posts such a question instead.
func TestDecisionContractTellsWorkersToPost(t *testing.T) {
	c := decisionContract()
	assert.Contains(t, c, "captain decision post")
	assert.Contains(t, c, "--recommend")
	assert.Contains(t, c, "only the user", "routine choices stay with the worker")
	t.Setenv("CAPTAIN_WORKER_DECISIONS", "0")
	assert.Empty(t, decisionContract())
}
