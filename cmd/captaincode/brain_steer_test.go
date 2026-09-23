package main

import (
	"net/http/httptest"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptainMoreSetsTheMixAndPrintsTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := teamBrain()
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		t.Errorf("a worker ran for a mix command: %s", prompt)
		return leg, captaincode.Result{}, nil
	}

	rec := httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain more oss 20%"))
	require.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "24%")
	assert.Contains(t, rec.Body.String(), "oss")
	assert.True(t, b.ledger.Steer.Set)
	assert.InDelta(t, 24, b.ledger.Steer.OSS, 0.05)

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain oss=20% deterministic=10% frontier=30% quality=20% cheap=10% fast=10%"))
	assert.Contains(t, rec.Body.String(), "deterministic")
	assert.InDelta(t, 30, b.ledger.Steer.Frontier, 0.05)
	assert.InDelta(t, 10, b.ledger.Steer.Deterministic, 0.05)
	assert.InDelta(t, 100, b.ledger.Steer.Frontier+b.ledger.Steer.Quality+b.ledger.Steer.Cheap+b.ledger.Steer.Fast+b.ledger.Steer.OSS+b.ledger.Steer.Deterministic, 0.2)

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain targets"))
	assert.Contains(t, rec.Body.String(), "Routing mix")
	assert.Contains(t, rec.Body.String(), "30%")

	rec = httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain mix reset"))
	assert.False(t, b.ledger.Steer.Set)
	assert.Contains(t, rec.Body.String(), "do not steer")
}

func TestCaptainMoreOfANonAxisIsStillATask(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	handled := b.handleCaptainHelp(rec, oaiChatReq{}, "/captain more about the router")
	assert.False(t, handled)
}

func TestUnsetMixPrintsTheDefaultAndDoesNotSteer(t *testing.T) {
	b := teamBrain()
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, captainReq("/captain targets"))
	body := rec.Body.String()
	assert.Contains(t, body, "20%")
	assert.Contains(t, body, "deterministic")
	assert.Contains(t, body, "do not steer")
	assert.False(t, b.ledger.Steer.Set)
}
