package captaincode

// Rate limits with the provider's own words and reset time.
//
// Live 2026-09-10 00:47: a 45-minute frontier run ended with "You've hit your
// session limit · resets 2:50am (Europe/Zurich)". Three things went wrong at
// once: the runner returned an EMPTY result (the streamed narrative of 45
// minutes of work was discarded), the ledger recorded the bare word "rate
// limited", and the leg was benched a flat 30 minutes - until 01:17 - while
// the window actually reopened at 02:50. RateLimitError fixes the second and
// third; WorthKeeping and the runners' partial returns fix the first.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RateLimitError is a rate limit with context: which leg, what the provider
// said, and when it says the window reopens (zero when unknown). It unwraps
// to ErrRateLimited so every existing errors.Is check keeps working.
type RateLimitError struct {
	Leg     Leg
	Msg     string
	ResetAt time.Time
	// Tier is set when the limit is one model tier's, not the account's:
	// "You've reached your Fable limit" closes Fable, and the CLI's other
	// models still answer. The claude leg was benched for 30 minutes at
	// every /frontier attempt on that message, so ordinary claude runs said
	// "not available" all evening while `claude -p` worked fine (2026-09-12).
	Tier string
}

// tierLimitRe matches a limit that names one model tier. The account-wide
// windows say "usage", "session", "daily", "weekly", "rate".
var tierLimitRe = regexp.MustCompile(`(?i)reached your ([A-Za-z][A-Za-z0-9.-]*) limit`)

// ModelTierLimit reports the model tier a rate-limit message names, "" when
// the limit is the account's own window.
func ModelTierLimit(msg string) string {
	m := tierLimitRe.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	switch strings.ToLower(m[1]) {
	case "usage", "session", "daily", "weekly", "rate", "monthly", "token", "request":
		return ""
	}
	return m[1]
}

func (e *RateLimitError) Error() string {
	s := "rate limited"
	if m := strings.TrimSpace(e.Msg); m != "" {
		s += ": " + truncateStr(strings.Join(strings.Fields(m), " "), 200)
	}
	if !e.ResetAt.IsZero() {
		s += fmt.Sprintf(" (resets %s)", e.ResetAt.Format("15:04"))
	}
	return s
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// NewRateLimitError builds a RateLimitError with an explicit reset time.
func NewRateLimitError(leg Leg, msg string, resetAt time.Time) error {
	return &RateLimitError{Leg: leg, Msg: msg, ResetAt: resetAt}
}

// rateLimitedAt classifies a provider message as a rate limit, parsing the
// reset time out of it when the message carries one.
func rateLimitedAt(leg Leg, msg string, now time.Time) error {
	reset, _ := ParseResetAt(msg, now)
	return &RateLimitError{Leg: leg, Msg: msg, ResetAt: reset, Tier: ModelTierLimit(msg)}
}

func rateLimited(leg Leg, msg string) error { return rateLimitedAt(leg, msg, time.Now()) }

var (
	// "resets 2:50am (Europe/Zurich)", "resets at 1:49pm", "Try again at 14:00",
	// "resets Sep 14 at 5:59am (Europe/Zurich)".
	resetClockRe = regexp.MustCompile(`(?i)(?:resets?|try again|retry|available)\s+(?:at\s+)?(?:([A-Z][a-z]{2})\s+(\d{1,2})\s+at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b(?:\s*\(([^)]+)\))?`)
	// "retry in 2h 15m", "in 45 minutes", "in 3 hours".
	resetInRe = regexp.MustCompile(`(?i)\bin\s+(?:(\d+)\s*h(?:ours?)?)?\s*(?:(\d+)\s*m(?:in(?:utes?)?)?)?\b`)
)

// ParseResetAt extracts the reset instant a rate-limit message announces.
// A bare time of day is the NEXT occurrence after now, in the message's
// zone when it names one (Claude Code prints "(Europe/Zurich)"), else in
// now's zone. ok=false when the message carries no usable time.
func ParseResetAt(msg string, now time.Time) (time.Time, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return time.Time{}, false
	}
	if m := resetClockRe.FindStringSubmatch(msg); m != nil {
		loc := now.Location()
		if m[6] != "" {
			if l, err := time.LoadLocation(strings.TrimSpace(m[6])); err == nil {
				loc = l
			}
		}
		hour, _ := strconv.Atoi(m[3])
		minute := 0
		if m[4] != "" {
			minute, _ = strconv.Atoi(m[4])
		}
		ampm := strings.ToLower(m[5])
		if ampm == "" && m[4] == "" {
			return time.Time{}, false // a bare number ("resets 2") is not a time
		}
		if ampm == "pm" && hour < 12 {
			hour += 12
		}
		if ampm == "am" && hour == 12 {
			hour = 0
		}
		if hour > 23 || minute > 59 {
			return time.Time{}, false
		}
		n := now.In(loc)
		if m[1] != "" && m[2] != "" {
			if mon, err := time.Parse("Jan", m[1]); err == nil {
				day, _ := strconv.Atoi(m[2])
				year := n.Year()
				at := time.Date(year, mon.Month(), day, hour, minute, 0, 0, loc)
				if at.Before(n.Add(-24 * time.Hour)) { // a date already behind us → next year
					at = time.Date(year+1, mon.Month(), day, hour, minute, 0, 0, loc)
				}
				return at, true
			}
		}
		at := time.Date(n.Year(), n.Month(), n.Day(), hour, minute, 0, 0, loc)
		if !at.After(n) {
			at = at.Add(24 * time.Hour)
		}
		return at, true
	}
	if m := resetInRe.FindStringSubmatch(msg); m != nil && (m[1] != "" || m[2] != "") {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		d := time.Duration(h)*time.Hour + time.Duration(mm)*time.Minute
		if d <= 0 {
			return time.Time{}, false
		}
		return now.Add(d), true
	}
	return time.Time{}, false
}

// WorthKeeping decides whether a failed run's text is a partial deliverable
// (deliver it marked partial, do NOT reroute - a fresh leg would start over)
// or a false start (reroute). A time-capped run always kept its text; a
// rate-limited run keeps it once it has been working for at least a minute.
// Outages keep nothing: what came before a crash is whatever the stream
// happened to hold. A rate-limited run's text must also be MORE than its
// running commentary: 17 minutes of "build passed, running clippy" is not a
// deliverable, and keeping it cost the user the answer another leg would
// have produced (2026-09-17, a frontier run capped by the monthly spend
// limit was neither salvaged nor rerouted).
func WorthKeeping(res Result, err error) bool {
	if strings.TrimSpace(res.Text) == "" {
		return false
	}
	switch {
	case errors.Is(err, ErrWorkerTimeout), errors.Is(err, ErrInterrupted):
		return true
	case errors.Is(err, ErrRateLimited):
		return res.DurationMs >= 60_000 && len(strings.TrimSpace(res.Text)) >= worthKeepingChars
	}
	return false
}

// worthKeepingChars: below this a rate-limited run's text is narration, not
// a partial answer worth showing instead of a reroute.
const worthKeepingChars = 1200

// spendLimitRe: the CLI's extra-usage cap ("You've hit your monthly spend
// limit"). It is not an account window: the subscription's own models keep
// answering (verified 2026-09-17: opus, sonnet and haiku said ok while fable
// was refused), so a frontier run that hits it is the frontier tier's
// problem, never a reason to bench the claude leg.
var spendLimitRe = regexp.MustCompile(`(?i)\bspend limit\b`)

// rateLimitedFrontier classifies a limit hit by a run at frontier settings:
// a spend limit with no tier named is worn by the frontier tier.
func rateLimitedFrontier(msg string) error {
	err := rateLimited(LegClaude, msg)
	var rl *RateLimitError
	if errors.As(err, &rl) && rl.Tier == "" && spendLimitRe.MatchString(msg) {
		rl.Tier = "frontier"
	}
	return err
}
