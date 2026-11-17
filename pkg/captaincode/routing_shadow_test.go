package captaincode

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shadowRow is one attempt row for a test task: the run it was, the leg it
// ran on, and what the task eventually settled at. Chosen is what the
// director recorded, carried on every row of the task the way the exporter
// writes it. Domain is carried on every row too, as the journal does.
func shadowRow(task string, leg Leg, attempt int, at time.Time, outcome string, label AcceptanceStatus, domain Domain) DatasetRow {
	r := DatasetRow{
		TaskID:     task,
		PromptSHA:  "sha-" + task,
		Attempt:    attempt,
		At:         at,
		Leg:        leg,
		Chosen:     leg,
		RunOutcome: outcome,
		Domain:     domain,
	}
	if label != "" {
		r.Label = label
		r.Settled = true
	}
	return r
}

// pinShadowPriors fixes the two legs this file compares by hand, so a host
// that carries its own priors.json cannot move the expected order.
func pinShadowPriors(t *testing.T) {
	t.Helper()
	savedQuality, savedDomain := qualityPriors, domainPriors
	qualityPriors = map[Leg]float64{LegClaude: 9.5, LegGrok: 7.0}
	domainPriors = map[Leg]map[Domain]float64{}
	t.Cleanup(func() { qualityPriors, domainPriors = savedQuality, savedDomain })
}

// shadowTrain builds the training slice used by several tests: in the code
// domain grok settles often and claude rarely, so the learned policy takes
// grok while the hand priors, which put claude on top, take claude.
func shadowTrain() []DatasetRow {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var rows []DatasetRow
	for i := 0; i < 4; i++ {
		rows = append(rows, shadowRow("g-ok-"+string(rune('a'+i)), LegGrok, 1, at.Add(time.Duration(i)*time.Hour), "ok", AcceptanceAccepted, Domain("code")))
		rows = append(rows, shadowRow("c-bad-"+string(rune('a'+i)), LegClaude, 1, at.Add(time.Duration(i+4)*time.Hour), "ok", AcceptanceRejected, Domain("code")))
	}
	return rows
}

func TestShadowRouterHoldsOutUnseenLegs(t *testing.T) {
	pinShadowPriors(t)
	router := NewShadowRouter(shadowTrain())
	legs := router.Legs()
	require.NotEmpty(t, legs)
	assert.ElementsMatch(t, []Leg{LegGrok, LegClaude}, legs)

	tasks, legCount, domains := router.Trained()
	assert.Equal(t, 8, tasks)
	assert.Equal(t, 2, legCount)
	assert.Equal(t, 1, domains)

	// A held-out task that ran a leg the training slice never saw: both
	// trained policies stay inside the legs they know.
	rows := []DatasetRow{
		shadowRow("held", "outside-leg", 1, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), "ok", AcceptanceAccepted, Domain("code")),
	}
	prior, _ := router.Predict(ShadowPolicyPrior, rows)
	learned, fellBack := router.Predict(ShadowPolicyLearned, rows)
	assert.Contains(t, legs, prior, "the prior policy must stay inside the training legs")
	assert.Contains(t, legs, learned, "the learned policy must stay inside the training legs")
	assert.False(t, fellBack, "the code domain has trained legs, so nothing falls back")
	// The director is not a model: its own recorded choice may name it.
	director, _ := router.Predict(ShadowPolicyDirector, rows)
	assert.Equal(t, Leg("outside-leg"), director)
}

func TestShadowLearnedPicksTheBetterSettledLeg(t *testing.T) {
	pinShadowPriors(t)
	router := NewShadowRouter(shadowTrain())
	rows := []DatasetRow{
		shadowRow("t", LegClaude, 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "ok", AcceptanceAccepted, Domain("code")),
	}
	learned, fellBack := router.Predict(ShadowPolicyLearned, rows)
	assert.Equal(t, LegGrok, learned)
	assert.False(t, fellBack)

	// A domain the training slice never settled in falls back to the priors.
	other := []DatasetRow{
		shadowRow("t", LegClaude, 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "ok", AcceptanceAccepted, Domain("research")),
	}
	pick, fellBack := router.Predict(ShadowPolicyLearned, other)
	assert.True(t, fellBack, "an untrained domain falls back")
	prior, _ := router.Predict(ShadowPolicyPrior, other)
	assert.Equal(t, prior, pick, "the fallback answer is the prior answer")
}

func TestShadowLearnedRanksEligibleOverThinLegs(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var rows []DatasetRow
	// ready: three settled tasks, all accepted.
	for i := 0; i < 3; i++ {
		rows = append(rows, shadowRow("ready-"+string(rune('a'+i)), LegGrok, 1, at, "ok", AcceptanceAccepted, Domain("code")))
	}
	// thin: two settled tasks, both accepted - a perfect rate on two runs
	// must not outrank the leg the slice judged properly.
	for i := 0; i < 2; i++ {
		rows = append(rows, shadowRow("thin-"+string(rune('a'+i)), LegClaude, 1, at, "ok", AcceptanceAccepted, Domain("code")))
	}
	router := NewShadowRouter(rows)
	rank := router.Rank(ShadowPolicyLearned, "code")
	require.Len(t, rank, 2)
	assert.Equal(t, LegGrok, rank[0], "an eligible leg outranks a leg settled too rarely")
	assert.Equal(t, LegClaude, rank[1])
}

func TestShadowCoveragePositionsAndVerdicts(t *testing.T) {
	pinShadowPriors(t)
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	held := []DatasetRow{
		// the learned pick ran first and the task was accepted.
		shadowRow("h1", LegGrok, 1, at, "ok", AcceptanceAccepted, Domain("code")),
		// the learned pick ran only as a repair, and the task was rejected.
		shadowRow("h2", LegClaude, 1, at.Add(time.Hour), "fail", AcceptanceRejected, Domain("code")),
		shadowRow("h2", LegGrok, 2, at.Add(2*time.Hour), "ok", AcceptanceRejected, Domain("code")),
		// the learned pick never ran on this task.
		shadowRow("h3", LegClaude, 1, at.Add(3*time.Hour), "ok", AcceptanceAccepted, Domain("code")),
	}
	report := EvaluateShadow(shadowTrain(), held, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	require.Len(t, report.HeldOut.Runs, 3)

	learned := report.HeldOut.Runs[2]
	require.Equal(t, ShadowPolicyLearned, learned.Policy)
	assert.Equal(t, 3, learned.Tasks)
	assert.Equal(t, 1, learned.Agreement, "h1 is the only pick the director recorded too")
	assert.Equal(t, 2, learned.Covered, "grok ran on h1 and h2; h3's pick never ran")
	assert.Equal(t, 1, learned.First)
	assert.Equal(t, 1, learned.Later)
	assert.Equal(t, 1, learned.Uncovered)
	assert.Equal(t, 1, learned.Labeled, "only a first attempt carries a verdict for the shadow")
	assert.Equal(t, 1, learned.Accepted)
	assert.InDelta(t, 1.0, learned.Acceptance, 0.0001)

	director := report.HeldOut.Runs[0]
	assert.Equal(t, 3, director.Agreement, "the director agrees with itself on every task it had a leg for")
	assert.Equal(t, 3, director.First)
	assert.Equal(t, 3, director.Labeled)
	assert.Equal(t, 2, director.Accepted, "h2 is rejected")
}

func TestShadowHeadToHeadUsesEachOwnOutcome(t *testing.T) {
	pinShadowPriors(t)
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	held := []DatasetRow{
		// director claude failed, learned grok repaired it and finished.
		shadowRow("h2", LegClaude, 1, at, "fail", AcceptanceAccepted, Domain("code")),
		shadowRow("h2", LegGrok, 2, at.Add(time.Hour), "ok", AcceptanceAccepted, Domain("code")),
		// the learned pick never ran, so it cannot be compared on this task.
		shadowRow("h3", LegClaude, 1, at.Add(2*time.Hour), "ok", AcceptanceAccepted, Domain("code")),
		// the two agreed here, so it adds nothing to a difference.
		shadowRow("h1", LegGrok, 1, at.Add(3*time.Hour), "ok", AcceptanceAccepted, Domain("code")),
	}
	report := EvaluateShadow(shadowTrain(), held, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	var learned, prior ShadowCompare
	for _, c := range report.HeldOut.Compares {
		switch c.Policy {
		case ShadowPolicyLearned:
			learned = c
		case ShadowPolicyPrior:
			prior = c
		}
	}
	require.Equal(t, 1, learned.Tasks)
	assert.Equal(t, 1, learned.PolicyOk)
	assert.Equal(t, 0, learned.DirectorOk)
	assert.Zero(t, prior.Tasks, "the prior policy picked the director's leg on every task where both ran")

	learnedRun := report.HeldOut.Runs[2]
	assert.Equal(t, 1, learnedRun.Rank.Judged, "one observed preference on the held-out slice")
	assert.Equal(t, 1, learnedRun.Rank.Concordant, "grok first is the order the observed run supports")
	assert.InDelta(t, 1.0, learnedRun.Rank.Accuracy, 0.0001)
	directorRun := report.HeldOut.Runs[0]
	assert.Zero(t, directorRun.Rank.Judged, "the director records a leg, not an order")
	assert.Equal(t, "-", directorRun.Rank.String())
}

func TestShadowCostIsThePickOwnAttempts(t *testing.T) {
	pinShadowPriors(t)
	train := shadowTrain()
	// give grok's accepted tasks a measured cost, so the training slice
	// carries a cost figure for the leg the learned policy picks.
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		train[i*2].Cost = 0.5
		train[i*2].CostStatus = UsageMeasured
	}
	held := []DatasetRow{{
		TaskID: "h1", PromptSHA: "sha-h1", Leg: LegGrok, Chosen: LegGrok, Attempt: 1,
		At: at.Add(48 * time.Hour), RunOutcome: "ok", Domain: Domain("code"),
		Label: AcceptanceAccepted, Settled: true, Cost: 0.25, CostStatus: UsageMeasured,
	}}
	report := EvaluateShadow(train, held, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	learned := report.HeldOut.Runs[2]
	require.Equal(t, 1, learned.Labeled)
	require.Equal(t, 1, learned.Accepted)
	assert.True(t, learned.HasCost)
	assert.InDelta(t, 0.25, learned.Cost, 0.0001)
	assert.InDelta(t, 0.25, learned.CostPer, 0.0001)
}

func TestShadowClaimNeedsItsMinimum(t *testing.T) {
	under := ShadowSliceEval{Compares: []ShadowCompare{{Policy: ShadowPolicyLearned, Tasks: ShadowClaimMin - 1, PolicyOk: 8, DirectorOk: 2}}}
	claim := shadowClaim(under)
	assert.Contains(t, claim, "no claim")
	assert.Contains(t, claim, strconv.Itoa(ShadowClaimMin-1))

	over := ShadowSliceEval{Compares: []ShadowCompare{
		{Policy: ShadowPolicyPrior, Tasks: ShadowClaimMin, PolicyOk: 4, DirectorOk: 4},
		{Policy: ShadowPolicyLearned, Tasks: ShadowClaimMin + 5, PolicyOk: 9, DirectorOk: 6},
	}}
	claim = shadowClaim(over)
	assert.Contains(t, claim, "prior: 4 ok against the director's 4 on 10 tasks - level")
	assert.Contains(t, claim, "learned: 9 ok against the director's 6 on 15 tasks - ahead")
	assert.Contains(t, claim, "no significance test")
}

func TestShadowPredictWithoutTraining(t *testing.T) {
	pinShadowPriors(t)
	router := NewShadowRouter(nil)
	require.Empty(t, router.Legs())
	rows := []DatasetRow{
		shadowRow("t", LegGrok, 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "ok", AcceptanceAccepted, Domain("code")),
	}
	for _, policy := range []string{ShadowPolicyPrior, ShadowPolicyLearned} {
		leg, _ := router.Predict(policy, rows)
		assert.Equal(t, Leg(""), leg, "%s has no trained legs to pick", policy)
	}
	leg, _ := router.Predict(ShadowPolicyDirector, rows)
	assert.Equal(t, LegGrok, leg, "the director's own record needs no training")
}

func BenchmarkEvaluateShadow(b *testing.B) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var train, held []DatasetRow
	legs := []Leg{LegGrok, LegClaude}
	for i := 0; i < 500; i++ {
		leg := legs[i%len(legs)]
		label := AcceptanceAccepted
		if i%4 == 0 {
			label = AcceptanceRejected
		}
		train = append(train, shadowRow("t-"+strconv.Itoa(i), leg, 1, at.Add(time.Duration(i)*time.Minute), "ok", label, Domain("code")))
	}
	for i := 0; i < 300; i++ {
		leg := legs[i%len(legs)]
		one := shadowRow("h-"+strconv.Itoa(i), leg, 1, at.Add(72*time.Hour+time.Duration(i)*time.Minute), "ok", AcceptanceAccepted, Domain("code"))
		trainLeg := legs[(i+1)%len(legs)]
		if i%3 == 0 {
			held = append(held,
				shadowRow("h-"+strconv.Itoa(i), trainLeg, 1, at.Add(72*time.Hour+time.Duration(i)*time.Minute), "fail", AcceptanceAccepted, Domain("code")),
				shadowRow("h-"+strconv.Itoa(i), leg, 2, at.Add(73*time.Hour+time.Duration(i)*time.Minute), "ok", AcceptanceAccepted, Domain("code")))
			continue
		}
		held = append(held, one)
	}
	cut := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluateShadow(train, held, cut)
	}
}
