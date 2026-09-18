package main

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live 2026-07-30: "Have claude, grok and codex review these examples" was
// planned as cursor+grok+glm. Two independent causes, one per test below.

// Cause 1: Rungs excludes the director, so with claude directing the menu
// simply did not CONTAIN claude - the director could not obey even when told to.
func TestNamedLegsEnterTheDirectorsMenuIncludingTheDirector(t *testing.T) {
	b := teamBrain()
	captaincode.SetDirector(captaincode.LegClaude)
	t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })

	var menu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		menu = open
		return captaincode.Plan{Class: captaincode.ClassMedium, Rationale: "as named", Workers: []captaincode.Worker{
			{Leg: captaincode.LegClaude, Brief: "review"},
			{Leg: captaincode.LegGrok, Brief: "review"},
			{Leg: captaincode.LegCodex, Brief: "review"},
		}}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "Have claude, grok and codex review these examples"}))
	require.Equal(t, 200, rec.Code, rec.Body.String())

	for _, want := range []captaincode.Leg{captaincode.LegClaude, captaincode.LegGrok, captaincode.LegCodex} {
		assert.True(t, legInList(want, menu), "the director's menu must contain the named leg %s (menu: %v)", want, menu)
	}
	assert.Contains(t, rec.Body.String(), "team", "three named reviewers is a fan-out")
}

// A benched leg the user named stays in the menu: an explicit request outranks a
// cooldown, and the wrapper still reroutes if the provider is really down.
func TestNamedLegSurvivesACooldown(t *testing.T) {
	b := teamBrain()
	b.ledger.Cooldown(captaincode.LegCodex, 30*60*1e9) // 30m
	var menu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		menu = open
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "ask grok and codex to review this"}))
	require.Equal(t, 200, rec.Code)
	assert.True(t, legInList(captaincode.LegCodex, menu), "a named leg is offered even while benched")
}

// CAPTAIN_LEGS stays a hard boundary: naming a leg cannot smuggle in a model
// the fork has no provider for.
func TestNamedLegsRespectTheAllowlist(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGrok: true, captaincode.LegFree: true}
	var menu []captaincode.Leg
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		menu = open
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: "x"}}}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "have grok and codex review this"}))
	require.Equal(t, 200, rec.Code)
	assert.False(t, legInList(captaincode.LegCodex, menu), "CAPTAIN_LEGS is not negotiable")
}

// Cause 2: the fork's route budget expired (director took 57.7s vs 45s), so it
// called the wrapper with a single leg and the paid-for team plan was dropped.
func TestCachedTeamPlanIsRecoveredWhenTheRouteBudgetExpired(t *testing.T) {
	b := teamBrain()
	task := "Have claude, grok and codex review these examples"
	b.storeTeamPlan(task, captaincode.Plan{Class: captaincode.ClassMedium, Rationale: "as named",
		Workers: []captaincode.Worker{
			{Leg: captaincode.LegClaude, Brief: "review A"},
			{Leg: captaincode.LegGrok, Brief: "review B"},
			{Leg: captaincode.LegCodex, Brief: "review C"},
		}})
	var mu sync.Mutex
	var ran []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock() // parallel stage: unsynchronized appends lose entries
		ran = append(ran, string(leg))
		mu.Unlock()
		return leg, captaincode.Result{Text: "review by " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		return captaincode.MultiAssessment{Synthesis: "THREE-REVIEWS"}, nil
	}

	// The fork fell back to a single leg: model=grok, not team.
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, task))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"claude", "grok", "codex"}, ran,
		"all three named reviewers must run, not just the fallback leg")
	assert.Contains(t, answerOf(t, rec), "THREE-REVIEWS")
}

func TestNoCachedPlanMeansOrdinarySoloRun(t *testing.T) {
	b := teamBrain()
	var ran []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = append(ran, string(leg))
		return leg, captaincode.Result{Text: "solo", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, "just answer this question"))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, []string{"claude"}, ran, "no plan cached ⇒ one leg, as before")
}

// A plan older than teamPlanTTL must not be resurrected by a later prompt that
// happens to repeat the same text.
func TestStaleTeamPlanIsNotRecovered(t *testing.T) {
	b := teamBrain()
	task := "have claude and grok review this"
	b.storeTeamPlan(task, captaincode.Plan{Workers: []captaincode.Worker{
		{Leg: captaincode.LegClaude, Brief: "a"}, {Leg: captaincode.LegGrok, Brief: "b"}}})
	b.tmu.Lock()
	b.teamPlanAt[task] = time.Now().Add(-2 * teamPlanTTL)
	b.tmu.Unlock()
	assert.False(t, b.hasTeamPlan(task), "an expired plan is not executable")

	var ran []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		ran = append(ran, string(leg))
		return leg, captaincode.Result{Text: "solo", DurationMs: 5}, nil
	}
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, wfReq(false, task))
	require.Equal(t, 200, rec.Code)
	assert.Equal(t, []string{"claude"}, ran, "stale plan ⇒ ordinary solo run")
}

// "Have grok, codex and then claude review X" must run grok+codex first and
// claude AFTER them, with claude seeing their output - /team ran all three at
// once and the director claimed it had sequenced them (live 2026-07-30).
func TestNamedSequenceRunsAsAWorkflow(t *testing.T) {
	b := teamBrain()
	task := "Canonical is attestation-continuous-v1-1_simple.md. Have grok, codex and then claude Review the research paper to ensure we address all problems with sound solutions."

	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		t.Fatal("a named sequence needs no director plan - legs and order are given")
		return captaincode.Plan{}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": task}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "workflow", "the route says it is running a workflow")

	var mu sync.Mutex
	var order []string
	var claudePrompt string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		order = append(order, string(leg))
		if leg == captaincode.LegClaude {
			claudePrompt = prompt
		}
		mu.Unlock()
		return leg, captaincode.Result{Text: "review by " + string(leg), DurationMs: 5}, nil
	}
	b.assessMultiFn = func(task string, o map[string]captaincode.WorkerOutput, objective string) (captaincode.MultiAssessment, error) {
		require.Len(t, o, 1, "only the terminal stage (claude) is reviewed")
		return captaincode.MultiAssessment{Synthesis: "ONE-AGGREGATE"}, nil
	}

	rec2 := httptest.NewRecorder()
	b.chatCompletions(rec2, wfReq(false, task))
	require.Equal(t, 200, rec2.Code, rec2.Body.String())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 3)
	assert.ElementsMatch(t, []string{"grok", "codex"}, order[:2], "stage 1 runs grok and codex in parallel")
	assert.Equal(t, "claude", order[2], "claude runs LAST, not alongside them")
	assert.Contains(t, claudePrompt, "review by grok", "claude sees grok's output")
	assert.Contains(t, claudePrompt, "review by codex", "and codex's")
	assert.Contains(t, answerOf(t, rec2), "ONE-AGGREGATE")
}

// Without a sequence cue the behaviour is unchanged: one parallel team stage
// planned by the director.
func TestNamedParallelStillGoesToTeam(t *testing.T) {
	b := teamBrain()
	planned := false
	b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
		planned = true
		return captaincode.Plan{Class: captaincode.ClassMedium, Rationale: "as named", Workers: []captaincode.Worker{
			{Leg: captaincode.LegClaude, Brief: "review"}, {Leg: captaincode.LegGrok, Brief: "review"}}}, nil
	}
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "have claude and grok review this"}))
	require.Equal(t, 200, rec.Code)
	assert.True(t, planned, "a flat fan-out is still the director's plan")
}

// Forced /team plans inside the wrapper (teamPlanFor), which built its own menu
// and so bypassed both fixes: the director substituted glm for the claude the
// user named, and a requested order was flattened (live 2026-07-30).
func TestForcedTeamHonorsNamedLegsAndSequence(t *testing.T) {
	t.Run("named legs reach the wrapper-side menu", func(t *testing.T) {
		b := teamBrain()
		captaincode.SetDirector(captaincode.LegClaude)
		t.Cleanup(func() { captaincode.SetDirector(captaincode.LegGrok) })
		var menu []captaincode.Leg
		b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
			menu = open
			return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{
				{Leg: captaincode.LegClaude, Brief: "review"}, {Leg: captaincode.LegGrok, Brief: "review"}}}, nil
		}
		b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
			return leg, captaincode.Result{Text: "out", DurationMs: 5}, nil
		}
		b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
			return captaincode.MultiAssessment{Synthesis: "S"}, nil
		}
		rec := httptest.NewRecorder()
		b.teamChat(rec, oaiChatReq{Model: "team"}, "[user]\nhave claude and grok review the paper\n\n")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.True(t, legInList(captaincode.LegClaude, menu), "claude was named: it must be assignable even though it directs")
	})

	t.Run("a named sequence becomes a workflow", func(t *testing.T) {
		b := teamBrain()
		b.planFn = func(string, captaincode.Class, string, []captaincode.Leg, map[captaincode.Leg]captaincode.LegStats, map[string]captaincode.TeamStat, bool) (captaincode.Plan, error) {
			t.Fatal("a named sequence must not be flattened into a team plan")
			return captaincode.Plan{}, nil
		}
		var mu sync.Mutex
		var order []string
		b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
			mu.Lock()
			order = append(order, string(leg))
			mu.Unlock()
			return leg, captaincode.Result{Text: "out " + string(leg), DurationMs: 5}, nil
		}
		b.assessMultiFn = func(string, map[string]captaincode.WorkerOutput, string) (captaincode.MultiAssessment, error) {
			return captaincode.MultiAssessment{Synthesis: "S"}, nil
		}
		rec := httptest.NewRecorder()
		b.teamChat(rec, oaiChatReq{Model: "team"},
			"[user]\nhave grok, codex and then claude review the paper\n\n")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, order, 3)
		assert.Equal(t, "claude", order[2], "claude runs last, after grok and codex")
	})
}

// An empty answer is a provider failure: the reroute path must accept it, or
// the user gets a turn that ran for 90 seconds and said nothing (2026-07-31).
func TestEmptyAnswerIsRerouteable(t *testing.T) {
	for _, err := range []error{
		captaincode.ErrEmptyOutput,
		fmt.Errorf("grok: %w", captaincode.ErrEmptyOutput),
	} {
		assert.True(t, isReroutable(err), "empty output must reroute: %v", err)
	}
	assert.False(t, isReroutable(fmt.Errorf("some other failure")))
}

// Live 2026-08-07: grok stalled on a site build and the reroute handed the task
// to glm - 2 provider fails in 6 runs, the flakiest leg on the board - because
// the ladder consults quality order but never reliability. And if glm had also
// stalled, the single-hop reroute would have killed the turn.
func TestRerouteSkipsUnreliableLegs(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "0") // exercises the hand-written FastLadder (MM37 value routing has its own tests)
	b := teamBrain()
	// glm: 2 of 6 runs failed (provider faults - the live ratio). minimax: clean.
	for i := 0; i < 4; i++ {
		b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "ok", Task: "x", Duration: 100})
	}
	b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "fail", Task: "x", Error: "worker stalled - no session activity"})
	b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "fail", Task: "x", Error: "worker stalled - no session activity"})
	leg, ok := b.rerouteTarget(captaincode.LegGrok,
		"[user]\nanalyze the study's evidence, survey the related literature on continuous attestation, and position our approach against the strongest three alternatives\n\n")
	require.True(t, ok)
	// Medium/research ladder is [grok, glm, minimax, …] - glm is next by
	// quality but unreliable; minimax is the correct target.
	assert.Equal(t, captaincode.LegMiniMax, leg,
		"a leg failing a third of its runs is not a reroute target while a clean one is open (got %s)", leg)
}

func TestRerouteToleratesUnreliableWhenNothingElseIsOpen(t *testing.T) {
	b := teamBrain()
	b.allowed = map[captaincode.Leg]bool{captaincode.LegGrok: true, captaincode.LegGLM: true}
	b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "fail", Task: "x", Error: "stalled"})
	b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "fail", Task: "x", Error: "stalled"})
	b.ledger.Record(captaincode.Event{Leg: captaincode.LegGLM, Outcome: "ok", Task: "x", Duration: 100})
	leg, ok := b.rerouteTarget(captaincode.LegGrok, "[user]\nanalyze the study's evidence\n\n")
	require.True(t, ok)
	assert.Equal(t, captaincode.LegGLM, leg, "an unreliable leg beats no leg")
}

// The reroute chains: first target fails → try a second, excluding both.
func TestRerouteChainsToASecondTarget(t *testing.T) {
	t.Setenv("CAPTAIN_VALUE_ROUTING", "0") // exercises the hand-written FastLadder (MM37 value routing has its own tests)
	b := teamBrain()
	var tried []string
	var mu sync.Mutex
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		tried = append(tried, string(leg))
		mu.Unlock()
		switch leg {
		case captaincode.LegGrok, captaincode.LegMiniMax:
			return leg, captaincode.Result{}, captaincode.ErrWorkerStalled
		}
		return leg, captaincode.Result{Text: "answer from " + string(leg), DurationMs: 5}, nil
	}
	ranLeg, res, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegGrok,
		"[user]\ncondense the abstract in my writing style, keep the flow of the argument intact\n\n", nil, nil, "")
	require.NoError(t, err, "a second hop must rescue the turn: tried %v", tried)
	assert.NotEqual(t, captaincode.LegGrok, ranLeg)
	assert.NotEqual(t, captaincode.LegMiniMax, ranLeg)
	assert.Contains(t, res.Text, "answer from")
	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, len(tried), 3, "grok, minimax, then a third leg")
}

// A reroute chain must not stack full worker timeouts: three 15m caps in a row
// made one stage crawl 45 minutes (2026-08-07). No NEW hop starts once the
// chain has already consumed a full worker budget.
func TestRerouteChainRespectsTheTimeBudget(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_TIMEOUT", "300ms")
	b := teamBrain()
	var mu sync.Mutex
	var tried []string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		mu.Lock()
		tried = append(tried, string(leg))
		mu.Unlock()
		time.Sleep(350 * time.Millisecond) // each attempt exhausts the budget
		return leg, captaincode.Result{}, captaincode.ErrWorkerStalled
	}
	_, _, err := b.runWorkerRerouted(defaultWorkspace(), captaincode.LegGrok,
		"[user]\ncondense the abstract in my writing style and keep the flow intact\n\n", nil, nil, "")
	require.Error(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, len(tried), 2, "no new hop after the budget is spent (tried %v)", tried)
}

// A stated preference asks for ONE worker of a kind - /quality the best,
// /speed the fastest, /save the cheapest - never a team; that is /team's
// word. Live 2026-09-16: "/quality address this issue <pasted issue that
// mentions grok and claude>" ran as a team of the two. The director is asked
// with fan-out off; legs the user actually names still bind.
func TestAPreferenceNeverFansOutUnlessLegsAreNamed(t *testing.T) {
	t.Setenv("CAPTAIN_TEAM", "1")
	t.Setenv("CAPTAIN_TRIAGE", "0") // every turn reaches the director
	b := teamBrain()
	var fanOut []bool
	b.planFn = func(task string, class captaincode.Class, prefer string, open []captaincode.Leg, stats map[captaincode.Leg]captaincode.LegStats, teams map[string]captaincode.TeamStat, allowFanOut bool) (captaincode.Plan, error) {
		fanOut = append(fanOut, allowFanOut)
		return captaincode.Plan{Class: captaincode.ClassMedium, Workers: []captaincode.Worker{{Leg: captaincode.LegGrok, Brief: task}}}, nil
	}
	pasted := "address this issue  When i run the doctor, the brain is starting, but the director is listed as Grok, i dont have Grok installed. from what claude has just told me, becuase grok is listed and i dont have it, captaincode will do manager planning everytime"
	rec := httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": pasted, "prefer": "quality"}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"team"`)

	rec = httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "have claude and grok review the doctor output", "prefer": "quality"}))
	require.Equal(t, 200, rec.Code)

	rec = httptest.NewRecorder()
	b.route(rec, jsonReq("/v1/route", map[string]any{"task": "redesign the routing layer across the brain and the plugin", "prefer": ""}))
	require.Equal(t, 200, rec.Code)

	require.Len(t, fanOut, 3, "every turn reached the director")
	assert.False(t, fanOut[0], "/quality + pasted mentions: one worker")
	assert.True(t, fanOut[1], "/quality + legs the user named: they bind")
	assert.True(t, fanOut[2], "no preference: the director may fan out")
}
