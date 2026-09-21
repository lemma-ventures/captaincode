package captaincode

// A second decision backend, beside the first.
//
// CAPTAIN_SYSTEMONE_URL already lets any System One-shaped endpoint BE the
// decision leg (systemone.go): one backend in place of another, which is the
// right shape when there is no key and the wrong one for what turned up.
// laya-mlx is an open MLX re-implementation of the same typed-decision shape
// - choice, score and noul, the same answer fields, the same {model, answers,
// usage} response - that answers a short question in 7-14ms on Apple silicon
// for nothing, and holds 512 tokens (1024 on two of its checkpoints). The
// vendor is slower, costs money per input token, holds 32k and runs wherever
// there is a key. Neither is the other's replacement: one is better at triage
// on this machine, the other is the only one that can be sent what the action
// gate sends, or reached from Linux.
//
// So the sidecar gets a variable of its own, CAPTAIN_SYSTEMONE_OPEN_URL, and
// runs BESIDE the primary client instead of replacing it. Two rules make that
// safe, and they are the whole point of this file.
//
// A bar is not transferable. jev's confidence bar was read off jev's rows.
// Another model's 0.85 means whatever that model's calibration makes it mean,
// so an open backend decides NOTHING by default: it is asked the same
// questions beside the backend captain acts on, its answers land on rows of
// their own stamped with its host, and that is all - until an operator names
// a bar it earned, CAPTAIN_SYSTEMONE_OPEN_FOR=triage=0.85. The number IS the
// promotion. There is no way to promote a backend without stating the bar you
// read off its own calibration, and `captain jev shadow` refuses to print one
// until the rows are unmixed and numerous enough to carry it.
//
// A truncated state is not a small state. laya's sequence builder CUTS a
// state that overruns max_len and answers anyway, with no error, so a
// 512-token backend handed the action gate's state would come back confident
// about the first two thirds of a command. That is worse than no answer. The
// sidecar refuses an oversized state outright (sidecars/laya/serve.py), and
// the routing below declines to send one in the first place: a call whose
// state does not fit the open context goes to the primary, and the two
// capabilities whose states are largest and whose misses are worst - the
// action gate and the supervisor - are not promotable at all.

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// SystemOneOpenURLEnv names a System One-shaped endpoint that runs beside
	// the primary one. Keyless by construction: it is a loopback sidecar, and
	// a credential travelling to localhost buys nothing.
	SystemOneOpenURLEnv = "CAPTAIN_SYSTEMONE_OPEN_URL"
	// SystemOneOpenModelEnv is the model id the sidecar is asked for. The
	// sidecar answers with what actually served, which is the figure that
	// reaches the record.
	SystemOneOpenModelEnv = "CAPTAIN_SYSTEMONE_OPEN_MODEL"
	// SystemOneOpenContextEnv is the sidecar's context ceiling in tokens -
	// instructions, options and state together. Default SystemOneOpenContext.
	SystemOneOpenContextEnv = "CAPTAIN_SYSTEMONE_OPEN_CONTEXT"
	// SystemOneOpenForEnv promotes the sidecar, one capability at a time, each
	// with the bar read off its OWN shadow rows: "triage=0.85,route=0.9".
	// Unset, the sidecar answers in the shadow and decides nothing.
	SystemOneOpenForEnv = "CAPTAIN_SYSTEMONE_OPEN_FOR"
	// SystemOneOpenTimeoutEnv bounds one sidecar call.
	SystemOneOpenTimeoutEnv = "CAPTAIN_SYSTEMONE_OPEN_TIMEOUT"
)

const (
	// SystemOneOpenContext is the ceiling assumed for a sidecar that does not
	// say otherwise: laya's smallest published checkpoint holds 512 tokens
	// (`convaiinnovations/laya`, ModernBERT-large); its multilingual and
	// typed-decisions checkpoints hold 1024. Raise it with
	// CAPTAIN_SYSTEMONE_OPEN_CONTEXT when the sidecar loads one of those.
	SystemOneOpenContext = 512
	// SystemOneQuestionHead is what the question itself costs before any state
	// reaches the model: laya reserves head_max_len = 192 tokens for the
	// instructions and the rendered options. The vendor documents no
	// equivalent, so charging the same allowance there is simply conservative.
	SystemOneQuestionHead = 192
	// SystemOneOpenTimeout bounds one sidecar call. A local answer is 7-14ms;
	// two seconds is not a latency budget, it is the point past which the
	// thing is wedged and triage should stop waiting for it.
	SystemOneOpenTimeout = 2 * time.Second
	// SystemOneOpenModel is what the sidecar is asked for when nothing pins
	// it. It is a request field, not a claim: the answer names what served.
	SystemOneOpenModel = "laya-mlx"
	// systemOneCharsPerToken is a deliberately pessimistic ratio. Prose runs
	// 3.5-4 characters per token and paths, commands and JSON run worse, so 3
	// over-counts on purpose: the failure being avoided is a silent
	// truncation, and the cost of erring this way is one call going to the
	// backend that was going to answer it anyway.
	systemOneCharsPerToken = 3
)

// SystemOneOpenPromotable are the capabilities an open sidecar may be
// promoted to decide. Triage and route ask closed-set questions over a task's
// head: small states, and a miss costs a rung on one turn, which the reroute
// and escalation paths already correct. The three that are missing are
// missing on purpose. `keep` decides what compaction drops, and what is
// dropped cannot be got back. `gate` and `supervise` send the largest states
// captain produces - a command with its working directory and the worker's
// assignment, a worker's whole recent action list - which is both where a
// 512-token backend truncates first and where being wrong means a destructive
// command screened on two thirds of its text.
//
// Route is on this list ahead of its use: captain's shape and leg points are
// answered by a frontier director in prose and nothing acts on a System One
// answer to them yet (shadow.go). Promoting a backend for route today records
// which backend WOULD answer them and changes no behaviour - the reading that
// earns the promotion is the same either way.
var SystemOneOpenPromotable = []string{CapTriage, CapRoute}

// SystemOneOpen builds the sidecar client, or nil when none is configured. It
// is keyless, pinned to its own model and context, and given a short timeout
// of its own: the primary's 20s belongs to a call crossing the internet.
func SystemOneOpen() *SystemOneClient {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(SystemOneOpenURLEnv)), "/")
	if base == "" {
		return nil
	}
	model := strings.TrimSpace(os.Getenv(SystemOneOpenModelEnv))
	if model == "" {
		model = SystemOneOpenModel
	}
	return &SystemOneClient{
		BaseURL:       base,
		KeySource:     SystemOneOpenURLEnv,
		Model:         model,
		ContextTokens: envInt(SystemOneOpenContextEnv, SystemOneOpenContext),
		HTTP:          &http.Client{Timeout: envDuration(SystemOneOpenTimeoutEnv, SystemOneOpenTimeout)},
	}
}

// envInt reads a positive integer from the environment, or returns def.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

// envDuration reads a Go duration from the environment, or returns def.
func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && d > 0 {
		return d
	}
	return def
}

// StateTokens is a pessimistic token estimate for a state, used to decide
// which backend a call can go to at all. It is not a billing figure: the
// answer reports what was actually consumed.
func StateTokens(state string) int {
	return (len(state) + systemOneCharsPerToken - 1) / systemOneCharsPerToken
}

// Holds says this backend can read the whole of a state, question head
// included. A client with no declared ceiling holds anything - the vendor
// answers 400 max_tokens_exceeded rather than truncating, which is a refusal
// captain can read, so there is nothing to pre-empt.
func (c *SystemOneClient) Holds(state string) bool {
	if c == nil {
		return false
	}
	if c.ContextTokens <= 0 {
		return true
	}
	return StateTokens(state)+SystemOneQuestionHead <= c.ContextTokens
}

// SystemOneBackends is everything the decision leg can reach: the primary
// client - the vendor's key, or CAPTAIN_SYSTEMONE_URL, whatever
// SystemOneFromEnv builds - and beside it an optional open sidecar with the
// capabilities it has been promoted to decide and the bar it earned for each.
type SystemOneBackends struct {
	Primary *SystemOneClient
	Open    *SystemOneClient
	// Bars: capability → the confidence floor an answer from Open must clear
	// to be acted on, read off Open's OWN calibration. A capability absent
	// here is one Open is not promoted for, whatever it answers.
	Bars map[string]float64
	// Refused: CAPTAIN_SYSTEMONE_OPEN_FOR entries that were not honoured, each
	// with the reason. Named rather than dropped, because a promotion that
	// silently did not happen is how an operator comes to believe a backend
	// was tested.
	Refused []string
}

// SystemOneBackendsFromEnv reads both backends and the sidecar's promotions.
func SystemOneBackendsFromEnv() SystemOneBackends {
	b := SystemOneBackends{Primary: SystemOneFromEnv(), Open: SystemOneOpen()}
	if b.Open == nil {
		return b
	}
	b.Bars, b.Refused = parseOpenPromotions(os.Getenv(SystemOneOpenForEnv))
	return b
}

// parseOpenPromotions reads "triage=0.85,route=0.9" into bars, and says what
// it would not honour. A capability with no number, a number outside (0,1],
// an unknown capability and a capability that is not promotable are all
// refusals rather than defaults: every one of them is someone believing a
// promotion happened.
func parseOpenPromotions(spec string) (map[string]float64, []string) {
	var refused []string
	bars := map[string]float64{}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, raw, ok := strings.Cut(entry, "=")
		name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
		switch {
		case !ok || raw == "":
			refused = append(refused, fmt.Sprintf("%s: no bar - promote it as %s=<confidence> with the floor you read off `captain jev shadow` for THIS backend", entry, name))
			continue
		case !strIn(name, ConformCaps):
			refused = append(refused, fmt.Sprintf("%s: no such capability - one of %s", entry, strings.Join(ConformCaps, ", ")))
			continue
		case !strIn(name, SystemOneOpenPromotable):
			refused = append(refused, fmt.Sprintf("%s: %s stays on the primary backend - only %s are promotable", entry, name, strings.Join(SystemOneOpenPromotable, " and ")))
			continue
		}
		bar, err := strconv.ParseFloat(raw, 64)
		if err != nil || bar <= 0 || bar > 1 {
			refused = append(refused, fmt.Sprintf("%s: %q is not a confidence in (0,1]", entry, raw))
			continue
		}
		bars[name] = bar
	}
	if len(bars) == 0 {
		bars = nil
	}
	sort.Strings(refused)
	return bars, refused
}

// strIn says s is one of the set.
func strIn(s string, set []string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// Promoted is the bar the sidecar earned at a capability, and whether it was
// promoted there at all.
func (b SystemOneBackends) Promoted(capability string) (float64, bool) {
	if b.Open == nil {
		return 0, false
	}
	bar, ok := b.Bars[capability]
	return bar, ok
}

// Decider names the client whose answer at this capability may be ACTED ON,
// the confidence bar it must clear (0: the caller's own default, which is the
// primary's tuned bar), and a line for the log saying which and why.
//
// The sidecar decides only where it has been promoted and only when the state
// fits what it can read. Everything else is the primary's - including the
// case where there is no primary, where the answer is simply not available
// and the caller does what it did before a decision leg existed.
func (b SystemOneBackends) Decider(capability, state string) (*SystemOneClient, float64, string) {
	bar, ok := b.Promoted(capability)
	switch {
	case !ok:
	case !b.Open.Holds(state):
		// The sidecar is promoted here and this particular state is too big
		// for it. Not a failure: the primary holds 32k and was always the
		// backend for a state this size.
		return b.Primary, 0, fmt.Sprintf("%s: state ~%d tokens overruns %s (%d) - the primary answers this one",
			capability, StateTokens(state), b.Open.Backend(), b.Open.ContextTokens)
	default:
		return b.Open, bar, fmt.Sprintf("%s: %s at bar %.2f (%s)", capability, b.Open.Backend(), bar, SystemOneOpenForEnv)
	}
	if b.Primary == nil {
		return nil, 0, ""
	}
	return b.Primary, 0, ""
}

// Shadowing names the open client when it is configured and is NOT the one
// deciding this capability - which is where its rows have to come from. Nil
// when there is no sidecar, when the sidecar is the decider (its answer is
// already on the record as the decision's own), or when this state would not
// fit it: an answer read off a truncated state is not a datum, and a
// calibration built from a hundred of them would promote a backend on the
// strength of questions it never saw.
func (b SystemOneBackends) Shadowing(capability, state string) *SystemOneClient {
	if b.Open == nil || !b.Open.Holds(state) {
		return nil
	}
	if c, _, _ := b.Decider(capability, state); c == b.Open {
		return nil
	}
	return b.Open
}

// Describe is the startup line: what answers, what is only watched, and what
// was refused. One line per fact, because every one of them changes what a
// later `captain jev shadow` means.
func (b SystemOneBackends) Describe() []string {
	var out []string
	switch {
	case b.Primary != nil && b.Primary.Keyless():
		out = append(out, fmt.Sprintf("decision leg: %s (%s, keyless)", b.Primary.Backend(), b.Primary.model()))
	case b.Primary != nil:
		out = append(out, fmt.Sprintf("decision leg: %s (%s, key from %s)", b.Primary.Backend(), b.Primary.model(), b.Primary.KeySource))
	default:
		out = append(out, "decision leg: none - triage falls back to the heuristics and the free-leg classify")
	}
	if b.Open == nil {
		return append(out, b.Refused...)
	}
	promoted := make([]string, 0, len(b.Bars))
	for _, capability := range ConformCaps {
		if bar, ok := b.Bars[capability]; ok {
			promoted = append(promoted, fmt.Sprintf("%s ≥%.2f", capability, bar))
		}
	}
	line := fmt.Sprintf("open sidecar: %s (%s, holds %d tokens)", b.Open.Backend(), b.Open.model(), b.Open.ContextTokens)
	if len(promoted) == 0 {
		line += " - SHADOW ONLY: it answers beside captain's own choices and decides nothing. Read `captain jev shadow --backend " +
			b.Open.Backend() + "`, then promote it with " + SystemOneOpenForEnv + "=triage=<its own bar>"
	} else {
		line += " - decides " + strings.Join(promoted, ", ")
	}
	out = append(out, line)
	for _, r := range b.Refused {
		out = append(out, SystemOneOpenForEnv+" refused "+r)
	}
	return out
}
