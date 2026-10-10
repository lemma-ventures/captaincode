package captaincode

// Hosts and failure causes. A leg is a model behind a login, but the same
// model is served by different hosts, and the host decides its speed and its
// stalls as much as the model does: Step 3.5 Flash went from 31 s to 395 s
// when it moved to Hugging Face, and the NIM host stalled DeepSeek V4.1
// Flash, GLM 5.3 and Kimi K3 the same week (2026-10, "Twelve Weeks of
// Routing"). So the scorecards keep a leg's speed and reliability for the
// host it runs on now, and three stalls in a row on one host bench every leg
// it serves.

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Failure causes, from the recorded error text.
const (
	CauseUserStop   = "user_stop"  // the user interrupted or withdrew the turn
	CauseHarness    = "harness"    // captaincode's own bug
	CauseCredential = "credential" // a dead login, an exhausted quota, an unpaid key
	CauseHost       = "host"       // the host stalled, timed out or was unreachable
	CauseModel      = "model"      // what is left: the model refused, answered empty or erred
)

var causePatterns = []struct {
	cause   string
	needles []string
}{
	{CauseUserStop, []string{"interrupted by the user", "withdrawn before it started", "stopped by the user", "cancelled by the user"}},
	{CauseHarness, []string{
		"exited but did not release its output pipes", "invalid utf-8", "chdir ", "create opencode session",
		"invalid project config", "no such file or directory", "secitemcopymatching", "keychain",
	}},
	{CauseCredential, []string{
		"not logged in", "please run /login", "codex login", "401 unauthorized", "missing bearer",
		"credentials rejected", "authentication required", "agent login", "token is expired",
		"authentication token", "payment required", "usage limit", "hit your usage", "no cookie auth",
		"quota exceeded", "insufficient credits",
	}},
	{CauseHost, []string{
		"stalled", "did not finish within", "went quiet", "timed out", "http 500", "http 502", "http 503",
		"cannot connect", "unable to connect", "getaddrinfo", "connection reset", "unavailable", "bad gateway",
	}},
}

// FailCause sorts a failure by who caused it. An error that matches nothing
// is the model's until proven otherwise.
func FailCause(msg string) string {
	m := strings.ToLower(msg)
	for _, p := range causePatterns {
		for _, n := range p.needles {
			if strings.Contains(m, n) {
				return p.cause
			}
		}
	}
	return CauseModel
}

// countsAgainstModel: only host and model failures say anything about the
// leg's reliability. Our bugs, a user's stop and a dead login do not.
func countsAgainstModel(cause string) bool { return cause == CauseHost || cause == CauseModel }

// knownHosts are the provider prefixes a model id can carry.
var knownHosts = []string{"openrouter", "huggingface", "nim", "xai", "openai", "google", "deepseek", "anthropic", "fireworks", "together", "groq"}

// HostOf is the host that served a run: the provider half of its route
// ("opencode:nim" → nim), the transport for a CLI leg that has one host
// ("claude-cli"), else the provider prefix of the model id. Runs recorded
// before routes were ("" for an opencode leg whose model id names no host)
// read "unknown".
func HostOf(e Event) string {
	if i := strings.IndexByte(e.Route, ':'); i >= 0 {
		return e.Route[i+1:]
	}
	if e.Route != "" {
		return e.Route
	}
	if spec, ok := Spec(e.Leg); ok && spec.Transport != TransportOpencode && spec.Transport != "" {
		return string(spec.Transport)
	}
	if i := strings.IndexByte(e.Model, '/'); i > 0 {
		p := strings.ToLower(e.Model[:i])
		for _, h := range knownHosts {
			if p == h {
				return h
			}
		}
	}
	return "unknown"
}

// HostOfLeg is the host a leg is configured to run on now.
func HostOfLeg(l Leg) string { return HostOf(Event{Leg: l, Route: RouteOf(l)}) }

// hostBenchRun is how many stalls in a row on one host bench it.
const hostBenchRun = 3

// hostBenchFor is how long a benched host's legs cool. CAPTAIN_HOST_BENCH
// (a Go duration, "0" off), default 30 minutes.
func hostBenchFor() time.Duration {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_HOST_BENCH"))
	if v == "0" || v == "off" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 30 * time.Minute
}

// benchHostAfter benches e's host when e is the third host failure in a row
// there, from two legs at least: each leg configured on that host cools.
// Returns the legs it cooled.
func (l *Ledger) benchHostAfter(e Event) []Leg {
	d := hostBenchFor()
	if d == 0 || e.Outcome == "ok" || FailCause(e.Error) != CauseHost {
		return nil
	}
	host := HostOf(e)
	if host == "unknown" || host == string(TransportClaudeCLI) || host == string(TransportCodexCLI) || host == string(TransportCursorCLI) {
		return nil // a CLI's own host is its leg: the leg's cooldown already covers it
	}
	run := 0
	legs := map[Leg]bool{}
	for i := len(l.Events) - 1; i >= 0 && run < hostBenchRun; i-- {
		x := l.Events[i]
		if x.Leg == "" || HostOf(x) != host {
			continue
		}
		if x.Outcome == "ok" || FailCause(x.Error) != CauseHost {
			return nil
		}
		run++
		legs[x.Leg] = true
	}
	// Two legs at least: three stalls of one model on OpenRouter, which
	// serves eight legs, say that model is stalling, not the host.
	if run < hostBenchRun || len(legs) < 2 {
		return nil
	}
	until := time.Now().Add(d)
	var benched []Leg
	for _, leg := range AllLegs {
		if HostOfLeg(leg) != host {
			continue
		}
		if l.Cooldowns == nil {
			l.Cooldowns = map[Leg]time.Time{}
		}
		if l.Cooldowns[leg].Before(until) {
			l.Cooldowns[leg] = until
		}
		benched = append(benched, leg)
	}
	if l.BenchedHosts == nil {
		l.BenchedHosts = map[string]time.Time{}
	}
	l.BenchedHosts[host] = until
	return benched
}

// HostNote says, for `captain why`, that a leg's speed and reliability
// restarted when it moved host. "" when every run was on the current one.
func HostNote(s LegStats) string {
	if s.OtherHostRuns == 0 || s.Host == "" {
		return ""
	}
	at := ""
	if !s.HostSince.IsZero() {
		at = " since " + s.HostSince.Format("Jan 02")
	}
	return fmt.Sprintf("on %s%s; speed and reliability restarted there (%d runs elsewhere set aside)", s.Host, at, s.OtherHostRuns)
}
