package captaincode

// Quota telemetry (ROADMAP M2.3). The ledger already records cooldowns - when
// a leg was benched and when it reopens - and the accounting schema records
// what each call spent. What neither answers is "how much quota does this leg
// have left, and how do we know?" A subscription leg that hit its window an
// hour ago and resets at 02:50 has zero remaining; a paid-per-token leg has no
// window at all; a leg whose adapter never reports quota is unknown, not zero.
//
// The three-valued pattern from M2.2 capabilities and M1.2 usage status applies
// here too: measured (the adapter or rate-limit message said it), inferred (the
// cooldown window implies it), and unknown (nothing reported). A quota figure
// that is unknown must never be presented as "0 remaining" - that is the same
// silence-as-zero bug M1.2 exists to prevent.
//
// Quota is observed, not polled. A rate-limit message is the one event every
// adapter produces that carries real quota state: the window closed, and here
// is when it reopens. Proactive polling (asking the adapter for remaining
// allowance before dispatch) is M2.4's resource controller; M2.3 captures what
// the system already sees and makes it readable.

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuotaStatus is how well a quota figure is known. Every observation states
// one; there is no implicit default - the same contract UsageStatus carries.
type QuotaStatus string

const (
	QuotaMeasured QuotaStatus = "measured" // the adapter or rate-limit message reported it
	QuotaInferred QuotaStatus = "inferred" // the cooldown window implies it
	QuotaUnknown  QuotaStatus = "unknown"  // nothing was reported and nothing can be derived
)

// Quota is one leg's observed subscription/quota state. It is NOT a real-time
// meter: it is the last thing the system learned, stamped with when it learned
// it. A stale observation is explicitly aged, not silently current.
type Quota struct {
	Leg        Leg         `json:"leg"`
	Source     string      `json:"source"`              // "rate-limit", "adapter", "overlay" - where the observation came from
	Account    string      `json:"account,omitempty"`   // account/tier identifier when the message named one
	ObservedAt time.Time   `json:"observed_at"`         // when this observation was made
	ResetAt    time.Time   `json:"reset_at,omitempty"`  // when the window reopens (zero when unknown)
	Remaining  int         `json:"remaining,omitempty"` // remaining allowance when reported (0 = exhausted or unknown)
	Limit      int         `json:"limit,omitempty"`     // total allowance when reported
	Status     QuotaStatus `json:"status"`              // how well the remaining figure is known
	Message    string      `json:"message,omitempty"`   // the provider's own words (truncated)
}

// QuotaVersion is stamped on every observation. Readers reject what they do
// not understand, the same contract AccountingVersion and EvalSuiteVersion
// carry.
const QuotaVersion = 1

// Exhausted reports whether this observation says the leg has no remaining
// quota. A measured 0 is exhausted; an inferred leg inside its cooldown window
// is exhausted; an unknown status is NOT exhausted (it might have quota, we
// just don't know - treating it as exhausted would starve a healthy leg).
func (q Quota) Exhausted() bool {
	switch q.Status {
	case QuotaMeasured:
		return q.Remaining <= 0
	case QuotaInferred:
		return q.Remaining <= 0
	}
	return false
}

// Stale reports whether this observation is older than maxQuotaAge. A stale
// observation is still readable (it is what the system last learned) but a
// report marks it so the reader does not treat it as current.
func (q Quota) Stale(now time.Time) bool {
	return q.ObservedAt.Before(now.Add(-maxQuotaAge))
}

// maxQuotaAge is how long a quota observation is considered current before it
// is marked stale. Subscription windows range from minutes (free models) to
// hours (Claude 5h, Codex 5h+weekly); 6h covers the longest without holding a
// stale short-window observation as current.
const maxQuotaAge = 6 * time.Hour

// QuotaFromHeaders derives a quota observation from HTTP response headers.
// Adapters that report remaining allowance proactively — xAI's
// `x-ratelimit-remaining-requests`, Anthropic's `anthropic-ratelimit-*-remaining`,
// or the standard `x-ratelimit-*` family — carry quota state BEFORE the window
// closes, which is the difference between steering around a leg that is about
// to hit its limit and discovering it after (ROADMAP M2.3 remaining).
//
// The header family is messy: some providers use `x-ratelimit-remaining-requests`,
// others `x-ratelimit-remaining`, others `retry-after`. This function checks
// the known variants and returns ok=false when none carry a usable figure.
// A reset time from `x-ratelimit-reset` or `retry-after` (seconds) is parsed
// when present. The status is measured: the adapter reported it.
func QuotaFromHeaders(leg Leg, headers http.Header, now time.Time) (Quota, bool) {
	if headers == nil {
		return Quota{}, false
	}
	remaining, limit, resetAt, ok := parseRatelimitHeaders(headers, now)
	if !ok {
		return Quota{}, false
	}
	q := Quota{
		Leg:        leg,
		Source:     "adapter-headers",
		ObservedAt: now,
		Remaining:  remaining,
		Limit:      limit,
		Status:     QuotaMeasured,
	}
	if !resetAt.IsZero() {
		q.ResetAt = resetAt
	}
	return q, true
}

// parseRatelimitHeaders checks the known header variants for remaining
// allowance, limit, and reset time. Returns ok=false when no remaining figure
// is present.
func parseRatelimitHeaders(h http.Header, now time.Time) (remaining, limit int, resetAt time.Time, ok bool) {
	// Remaining: try x-ratelimit-remaining-requests, x-ratelimit-remaining,
	// anthropic-ratelimit-requests-remaining, retry-after (seconds → 0 remaining).
	for _, key := range []string{
		"X-Ratelimit-Remaining-Requests",
		"X-Ratelimit-Remaining",
		"Anthropic-Ratelimit-Requests-Remaining",
		"X-RateLimit-Remaining",
	} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				remaining = n
				ok = true
				break
			}
		}
	}
	// retry-after in seconds means the window is already closed.
	if !ok {
		if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				remaining = 0
				resetAt = now.Add(time.Duration(secs) * time.Second)
				ok = true
			}
		}
	}
	if !ok {
		return 0, 0, time.Time{}, false
	}
	// Limit: try x-ratelimit-limit-requests, x-ratelimit-limit.
	for _, key := range []string{
		"X-Ratelimit-Limit-Requests",
		"X-Ratelimit-Limit",
		"Anthropic-Ratelimit-Requests-Limit",
		"X-RateLimit-Limit",
	} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
				break
			}
		}
	}
	// Reset: try x-ratelimit-reset (unix timestamp or seconds), retry-after.
	if resetAt.IsZero() {
		for _, key := range []string{"X-Ratelimit-Reset", "X-RateLimit-Reset"} {
			if v := strings.TrimSpace(h.Get(key)); v != "" {
				if t, parsed := parseResetHeader(v, now); parsed {
					resetAt = t
					break
				}
			}
		}
	}
	return remaining, limit, resetAt, ok
}

// parseResetHeader parses a reset header value: a Unix timestamp (seconds or
// epoch), a duration in seconds, or an HTTP-date.
func parseResetHeader(v string, now time.Time) (time.Time, bool) {
	// Unix timestamp (seconds since epoch).
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n > 1e9 {
			return time.Unix(n, 0), true // epoch seconds
		}
		if n > 0 {
			return now.Add(time.Duration(n) * time.Second), true // seconds until reset
		}
	}
	// HTTP-date (RFC 7231).
	if t, err := http.ParseTime(v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// QuotaLow reports whether a measured quota observation is below a threshold.
// This is the proactive steering signal: a leg with 3 remaining requests out
// of 100 is not exhausted but is about to be, and the routing gate can
// deprioritize it. Unknown observations return false (we do not know, so we
// do not steer). Stale observations return false (the figure may have reset).
func QuotaLow(q Quota, threshold int, now time.Time) bool {
	if q.Status != QuotaMeasured {
		return false
	}
	if q.Stale(now) {
		return false
	}
	if q.Limit > 0 {
		// Percentage-based: below threshold% of the limit.
		return q.Remaining*100 < threshold*q.Limit
	}
	// Absolute: below the threshold count.
	return q.Remaining < threshold
}

// QuotaFromRateLimit derives a quota observation from a RateLimitError. A
// rate-limit message is the one event that carries real quota state: the
// window closed, the provider said when it reopens, and sometimes which tier
// or account hit it. The remaining allowance is 0 - the leg just hit its
// limit - and the status is measured, because the provider told us.
func QuotaFromRateLimit(rl *RateLimitError, now time.Time) Quota {
	q := Quota{
		Leg:        rl.Leg,
		Source:     "rate-limit",
		ObservedAt: now,
		Remaining:  0,
		Status:     QuotaMeasured,
		Message:    truncateStr(strings.Join(strings.Fields(rl.Msg), " "), 200),
	}
	if !rl.ResetAt.IsZero() {
		q.ResetAt = rl.ResetAt
	}
	if rl.Tier != "" {
		q.Account = rl.Tier
	}
	return q
}

// QuotaFromCooldown infers quota state from a cooldown entry. A leg that is
// cooling down has no remaining quota until its cooldown expires; a leg with
// no cooldown entry has unknown quota (we never polled it). The status is
// inferred rather than measured: the cooldown was set from a rate-limit message
// or a provider-down heuristic, not from the adapter reporting remaining
// allowance.
func QuotaFromCooldown(leg Leg, until time.Time, now time.Time) Quota {
	if now.After(until) || until.IsZero() {
		return Quota{Leg: leg, Source: "cooldown", ObservedAt: now, Status: QuotaUnknown}
	}
	q := Quota{
		Leg:        leg,
		Source:     "cooldown",
		ObservedAt: now,
		ResetAt:    until,
		Remaining:  0,
		Status:     QuotaInferred,
	}
	return q
}

// RecordQuota stores or updates a leg's quota observation. A newer observation
// (by ObservedAt) replaces an older one; an equal-timestamp measured
// observation replaces an inferred one, because the adapter's own report is
// better than our derivation. The ledger is the single store so `captain quota`
// and the routing gate read the same state.
func (l *Ledger) RecordQuota(q Quota) {
	if q.ObservedAt.IsZero() {
		q.ObservedAt = time.Now()
	}
	old, exists := l.Quotas[q.Leg]
	if !exists || quotaRank(q) > quotaRank(old) ||
		(quotaRank(q) == quotaRank(old) && q.ObservedAt.After(old.ObservedAt)) {
		l.Quotas[q.Leg] = q
	}
}

// QuotaFor returns the current observation for a leg, or a zero Quota with
// unknown status when none has been recorded.
func (l *Ledger) QuotaFor(leg Leg) Quota {
	if q, ok := l.Quotas[leg]; ok {
		return q
	}
	return Quota{Leg: leg, Status: QuotaUnknown}
}

// QuotasSorted returns every leg's quota observation in leg-ladder order, so
// `captain quota` reads in the same order the router walks.
func (l *Ledger) QuotasSorted() []Quota {
	out := make([]Quota, 0, len(AllLegs))
	for _, leg := range AllLegs {
		out = append(out, l.QuotaFor(leg))
	}
	return out
}

func quotaRank(q Quota) int {
	switch q.Status {
	case QuotaMeasured:
		return 2
	case QuotaInferred:
		return 1
	}
	return 0
}

// FormatQuota renders one leg's quota observation for `captain quota`. The
// format mirrors the accounting coverage line: the status is named, a stale
// observation is marked, and an unknown remaining is never printed as "0 left".
func FormatQuota(q Quota, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %-10s ", q.Leg)
	if q.Status == QuotaUnknown && q.Source == "" {
		b.WriteString("unknown (no quota observation)")
		return b.String()
	}
	switch q.Status {
	case QuotaMeasured:
		if q.Exhausted() {
			b.WriteString("exhausted")
		} else if q.Remaining > 0 {
			fmt.Fprintf(&b, "%d remaining", q.Remaining)
			if q.Limit > 0 {
				fmt.Fprintf(&b, " / %d", q.Limit)
			}
		} else {
			b.WriteString("measured (details unknown)")
		}
	case QuotaInferred:
		b.WriteString("inferred exhausted")
	default:
		b.WriteString("unknown")
	}
	if q.Account != "" {
		fmt.Fprintf(&b, " [%s]", q.Account)
	}
	if !q.ResetAt.IsZero() {
		if now.Before(q.ResetAt) {
			fmt.Fprintf(&b, ", resets %s", q.ResetAt.Local().Format("15:04"))
		} else {
			fmt.Fprintf(&b, ", reset %s", q.ResetAt.Local().Format("15:04"))
		}
	}
	if q.Source != "" {
		fmt.Fprintf(&b, " (%s", q.Source)
		if q.Stale(now) {
			fmt.Fprintf(&b, ", stale")
		}
		b.WriteString(")")
	}
	return b.String()
}

// SortedQuotaLines renders every leg's quota in ladder order for display.
func SortedQuotaLines(l *Ledger, now time.Time) []string {
	qs := l.QuotasSorted()
	lines := make([]string, 0, len(qs))
	for _, q := range qs {
		lines = append(lines, FormatQuota(q, now))
	}
	return lines
}

// QuotaSummary returns a one-line count of how many legs are exhausted, have
// remaining, or are unknown - the headline `captain quota` prints above the
// per-leg detail.
func QuotaSummary(l *Ledger, now time.Time) string {
	var exhausted, available, unknown int
	for _, leg := range AllLegs {
		q := l.QuotaFor(leg)
		if until, ok := l.Cooldowns[leg]; ok && now.Before(until) {
			exhausted++
			continue
		}
		switch q.Status {
		case QuotaMeasured:
			if q.Exhausted() {
				exhausted++
			} else {
				available++
			}
		default:
			unknown++
		}
	}
	return fmt.Sprintf("%d available, %d exhausted, %d unknown", available, exhausted, unknown)
}

// SortQuotasByReset sorts quota observations by reset time (earliest first),
// for the "when does quota return" view. Legs with no reset time sort last.
func SortQuotasByReset(qs []Quota) []Quota {
	out := make([]Quota, len(qs))
	copy(out, qs)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].ResetAt, out[j].ResetAt
		if ri.IsZero() && !rj.IsZero() {
			return false
		}
		if !ri.IsZero() && rj.IsZero() {
			return true
		}
		return ri.Before(rj)
	})
	return out
}
