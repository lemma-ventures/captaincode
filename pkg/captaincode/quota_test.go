package captaincode

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaFromRateLimit(t *testing.T) {
	now := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	rl := &RateLimitError{
		Leg:     LegClaude,
		Msg:     "You've hit your session limit · resets 2:50am (Europe/Zurich)",
		ResetAt: reset,
		Tier:    "Fable",
	}
	q := QuotaFromRateLimit(rl, now)
	assert.Equal(t, LegClaude, q.Leg)
	assert.Equal(t, "rate-limit", q.Source)
	assert.Equal(t, QuotaMeasured, q.Status)
	assert.Equal(t, 0, q.Remaining)
	assert.Equal(t, reset, q.ResetAt)
	assert.Equal(t, "Fable", q.Account)
	assert.Contains(t, q.Message, "session limit")
}

func TestQuotaFromRateLimitNoReset(t *testing.T) {
	now := time.Now()
	rl := &RateLimitError{Leg: LegGrok, Msg: "429 too many requests"}
	q := QuotaFromRateLimit(rl, now)
	assert.Equal(t, QuotaMeasured, q.Status)
	assert.True(t, q.ResetAt.IsZero())
	assert.True(t, q.Exhausted())
}

func TestQuotaFromCooldown(t *testing.T) {
	now := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	until := now.Add(2 * time.Hour)

	q := QuotaFromCooldown(LegClaude, until, now)
	assert.Equal(t, QuotaInferred, q.Status)
	assert.Equal(t, 0, q.Remaining)
	assert.Equal(t, until, q.ResetAt)
	assert.True(t, q.Exhausted())

	// An expired cooldown means unknown, not "has quota"
	q2 := QuotaFromCooldown(LegGrok, now.Add(-time.Hour), now)
	assert.Equal(t, QuotaUnknown, q2.Status)
	assert.False(t, q2.Exhausted())
}

func TestQuotaExhaustedUnknownIsNotExhausted(t *testing.T) {
	q := Quota{Status: QuotaUnknown, Remaining: 0}
	assert.False(t, q.Exhausted(), "unknown remaining must not read as exhausted")
}

func TestQuotaExhaustedMeasured(t *testing.T) {
	assert.True(t, Quota{Status: QuotaMeasured, Remaining: 0}.Exhausted())
	assert.False(t, Quota{Status: QuotaMeasured, Remaining: 100}.Exhausted())
}

func TestQuotaStale(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	fresh := Quota{ObservedAt: now.Add(-time.Hour)}
	stale := Quota{ObservedAt: now.Add(-7 * time.Hour)}
	assert.False(t, fresh.Stale(now))
	assert.True(t, stale.Stale(now))
}

func TestRecordQuotaReplacesOlder(t *testing.T) {
	l := &Ledger{Quotas: map[Leg]Quota{}}
	old := Quota{Leg: LegClaude, Status: QuotaInferred, ObservedAt: time.Now().Add(-time.Hour)}
	l.RecordQuota(old)
	require.Equal(t, QuotaInferred, l.QuotaFor(LegClaude).Status)

	newer := Quota{Leg: LegClaude, Status: QuotaMeasured, ObservedAt: time.Now()}
	l.RecordQuota(newer)
	assert.Equal(t, QuotaMeasured, l.QuotaFor(LegClaude).Status)
}

func TestRecordQuotaKeepsMeasuredOverInferred(t *testing.T) {
	l := &Ledger{Quotas: map[Leg]Quota{}}
	measured := Quota{Leg: LegGrok, Status: QuotaMeasured, ObservedAt: time.Unix(1000, 0)}
	l.RecordQuota(measured)
	inferred := Quota{Leg: LegGrok, Status: QuotaInferred, ObservedAt: time.Unix(1000, 0)}
	l.RecordQuota(inferred)
	assert.Equal(t, QuotaMeasured, l.QuotaFor(LegGrok).Status, "measured wins at equal timestamp")
}

func TestRecordQuotaDoesNotDowngrade(t *testing.T) {
	l := &Ledger{Quotas: map[Leg]Quota{}}
	measured := Quota{Leg: LegCodex, Status: QuotaMeasured, ObservedAt: time.Now()}
	l.RecordQuota(measured)
	inferred := Quota{Leg: LegCodex, Status: QuotaInferred, ObservedAt: time.Now().Add(time.Hour)}
	l.RecordQuota(inferred)
	assert.Equal(t, QuotaMeasured, l.QuotaFor(LegCodex).Status, "a newer inferred observation must not downgrade a measured one")
}

func TestQuotaForUnknownLeg(t *testing.T) {
	l := &Ledger{}
	q := l.QuotaFor(LegKimi)
	assert.Equal(t, QuotaUnknown, q.Status)
}

func TestQuotasSortedInLadderOrder(t *testing.T) {
	l := &Ledger{Quotas: map[Leg]Quota{}}
	l.RecordQuota(Quota{Leg: LegClaude, Status: QuotaMeasured, ObservedAt: time.Now()})
	l.RecordQuota(Quota{Leg: LegFree, Status: QuotaMeasured, ObservedAt: time.Now()})
	qs := l.QuotasSorted()
	assert.NotEmpty(t, qs)
	var sawFree, sawClaude bool
	for _, q := range qs {
		if q.Leg == LegFree {
			sawFree = true
		}
		if q.Leg == LegClaude {
			sawClaude = true
		}
	}
	assert.True(t, sawFree)
	assert.True(t, sawClaude)
}

func TestFormatQuotaUnknown(t *testing.T) {
	q := Quota{Leg: LegKimi, Status: QuotaUnknown}
	s := FormatQuota(q, time.Now())
	assert.Contains(t, s, "kimi")
	assert.Contains(t, s, "unknown")
}

func TestFormatQuotaMeasuredExhausted(t *testing.T) {
	now := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	q := Quota{
		Leg:        LegClaude,
		Source:     "rate-limit",
		Status:     QuotaMeasured,
		Remaining:  0,
		ResetAt:    now.Add(2 * time.Hour),
		Account:    "Fable",
		ObservedAt: now,
	}
	s := FormatQuota(q, now)
	assert.Contains(t, s, "exhausted")
	assert.Contains(t, s, "Fable")
	assert.Contains(t, s, "resets")
	assert.Contains(t, s, "rate-limit")
}

func TestFormatQuotaMeasuredRemaining(t *testing.T) {
	now := time.Now()
	q := Quota{
		Leg:        LegGrok,
		Source:     "adapter",
		Status:     QuotaMeasured,
		Remaining:  42,
		Limit:      100,
		ObservedAt: now,
	}
	s := FormatQuota(q, now)
	assert.Contains(t, s, "42 remaining")
	assert.Contains(t, s, "/ 100")
}

func TestFormatQuotaStale(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	q := Quota{
		Leg:        LegGrok,
		Source:     "rate-limit",
		Status:     QuotaMeasured,
		Remaining:  0,
		ObservedAt: now.Add(-7 * time.Hour),
	}
	s := FormatQuota(q, now)
	assert.Contains(t, s, "stale")
}

func TestQuotaSummary(t *testing.T) {
	now := time.Now()
	l := &Ledger{
		Quotas:    map[Leg]Quota{},
		Cooldowns: map[Leg]time.Time{},
	}
	l.RecordQuota(Quota{Leg: LegGrok, Status: QuotaMeasured, Remaining: 50, ObservedAt: now})
	l.RecordQuota(Quota{Leg: LegClaude, Status: QuotaMeasured, Remaining: 0, ObservedAt: now})
	l.Cooldowns[LegCodex] = now.Add(time.Hour)
	s := QuotaSummary(l, now)
	assert.Contains(t, s, "available")
	assert.Contains(t, s, "exhausted")
	assert.Contains(t, s, "unknown")
}

func TestSortQuotasByReset(t *testing.T) {
	now := time.Now()
	qs := []Quota{
		{Leg: LegClaude, ResetAt: now.Add(3 * time.Hour)},
		{Leg: LegGrok, ResetAt: now.Add(time.Hour)},
		{Leg: LegFree},
		{Leg: LegCodex, ResetAt: now.Add(2 * time.Hour)},
	}
	sorted := SortQuotasByReset(qs)
	assert.Equal(t, LegGrok, sorted[0].Leg)
	assert.Equal(t, LegCodex, sorted[1].Leg)
	assert.Equal(t, LegClaude, sorted[2].Leg)
	assert.Equal(t, LegFree, sorted[3].Leg) // no reset sorts last
}

// ── M2.3 proactive quota polling: QuotaFromHeaders tests ──

func TestQuotaFromHeadersRemainingRequests(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("X-Ratelimit-Remaining-Requests", "39")
	h.Set("X-Ratelimit-Limit-Requests", "40")
	q, ok := QuotaFromHeaders(LegGrok, h, now)
	require.True(t, ok)
	assert.Equal(t, QuotaMeasured, q.Status)
	assert.Equal(t, 39, q.Remaining)
	assert.Equal(t, 40, q.Limit)
	assert.Equal(t, "adapter-headers", q.Source)
	assert.False(t, q.Exhausted())
}

func TestQuotaFromHeadersExhausted(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("X-Ratelimit-Remaining-Requests", "0")
	h.Set("X-Ratelimit-Limit-Requests", "40")
	h.Set("X-Ratelimit-Reset", "3600")
	q, ok := QuotaFromHeaders(LegGrok, h, now)
	require.True(t, ok)
	assert.Equal(t, 0, q.Remaining)
	assert.True(t, q.Exhausted())
	assert.False(t, q.ResetAt.IsZero(), "reset time should be parsed from seconds")
}

func TestQuotaFromHeadersRetryAfterSeconds(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Retry-After", "120")
	q, ok := QuotaFromHeaders(LegCodex, h, now)
	require.True(t, ok)
	assert.Equal(t, 0, q.Remaining)
	assert.True(t, q.Exhausted())
	assert.True(t, q.ResetAt.After(now))
}

func TestQuotaFromHeadersNoHeaders(t *testing.T) {
	h := http.Header{}
	_, ok := QuotaFromHeaders(LegClaude, h, time.Now())
	assert.False(t, ok, "empty headers should return ok=false")
}

func TestQuotaFromHeadersNil(t *testing.T) {
	_, ok := QuotaFromHeaders(LegClaude, nil, time.Now())
	assert.False(t, ok, "nil headers should return ok=false")
}

func TestQuotaFromHeadersAlternateNames(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "15")
	h.Set("X-RateLimit-Limit", "100")
	q, ok := QuotaFromHeaders(LegGLM, h, now)
	require.True(t, ok)
	assert.Equal(t, 15, q.Remaining)
	assert.Equal(t, 100, q.Limit)
}

func TestQuotaFromHeadersAnthropicFormat(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Requests-Remaining", "50")
	h.Set("Anthropic-Ratelimit-Requests-Limit", "100")
	q, ok := QuotaFromHeaders(LegClaude, h, now)
	require.True(t, ok)
	assert.Equal(t, 50, q.Remaining)
	assert.Equal(t, 100, q.Limit)
}

func TestQuotaLow(t *testing.T) {
	now := time.Now()
	// 3 remaining out of 100 → below 10% threshold
	low := Quota{Status: QuotaMeasured, Remaining: 3, Limit: 100, ObservedAt: now}
	assert.True(t, QuotaLow(low, 10, now))

	// 15 remaining out of 100 → above 10% threshold
	healthy := Quota{Status: QuotaMeasured, Remaining: 15, Limit: 100, ObservedAt: now}
	assert.False(t, QuotaLow(healthy, 10, now))

	// Unknown status → not low (we don't know)
	unknown := Quota{Status: QuotaUnknown, Remaining: 0, ObservedAt: now}
	assert.False(t, QuotaLow(unknown, 10, now))

	// Stale → not low (the figure may have reset)
	stale := Quota{Status: QuotaMeasured, Remaining: 1, Limit: 100, ObservedAt: now.Add(-7 * time.Hour)}
	assert.False(t, QuotaLow(stale, 10, now))
}

func TestQuotaLowNoLimit(t *testing.T) {
	now := time.Now()
	// No limit known, threshold is absolute (5)
	low := Quota{Status: QuotaMeasured, Remaining: 3, ObservedAt: now}
	assert.True(t, QuotaLow(low, 5, now))

	healthy := Quota{Status: QuotaMeasured, Remaining: 10, ObservedAt: now}
	assert.False(t, QuotaLow(healthy, 5, now))
}

func TestParseResetHeaderUnixSeconds(t *testing.T) {
	now := time.Now()
	// "3600" → 3600 seconds from now
	t1, ok := parseResetHeader("3600", now)
	require.True(t, ok)
	assert.WithinDuration(t, now.Add(time.Hour), t1, time.Second)
}

func TestParseResetHeaderEpoch(t *testing.T) {
	// A large number is treated as epoch seconds
	t2, ok := parseResetHeader("1735689600", time.Now())
	require.True(t, ok)
	assert.Equal(t, int64(1735689600), t2.Unix())
}
