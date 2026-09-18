package captaincode

// The Agentic Determinism Index as a routing signal (2026-09-17).
//
// ADI (lemma-ventures/agentic-determinism-index) asks hosted model APIs one
// question: send the same request N times, how identical are the answers?
// Its leaderboard.json names every measured SERVING TUPLE - provider, model,
// and the pin that reproduces it (OpenRouter provider.order) - with a green
// flag: the latest scored appearance was fully byte-exact. Determinism is a
// property of the deployment, not the weights, so the tuple is the unit.
//
// `/deterministic <task>` picks the leg whose serving tuple is green, and
// pins the request to that tuple through the egress proxy (provider_prefs,
// temperature 0) so the run uses the stack that was measured, not whatever
// OpenRouter routes to this minute.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ADITuple is one row of the feed.
type ADITuple struct {
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	Label         string         `json:"label"`
	Display       string         `json:"display"`
	Rank          int            `json:"rank"`
	Medal         string         `json:"medal"`
	Score         float64        `json:"score"`
	MeanModeShare float64        `json:"mean_mode_share"`
	RunsSeen      int            `json:"runs_seen"`
	Deterministic int            `json:"deterministic_runs"`
	Streak        int            `json:"streak"`
	Green         bool           `json:"green"`
	AsOf          string         `json:"score_as_of"`
	ProviderPrefs map[string]any `json:"provider_prefs"`
	BaseURL       string         `json:"base_url"`
}

// ADIFeed is leaderboard.json.
type ADIFeed struct {
	FeedVersion int        `json:"feed_version"`
	GeneratedAt string     `json:"generated_at"`
	RunStamp    string     `json:"run_stamp"`
	NRuns       int        `json:"n_runs"`
	Tuples      []ADITuple `json:"tuples"`
	FetchedAt   time.Time  `json:"fetched_at"`
}

const adiDefaultURL = "https://lemma-ventures.github.io/agentic-determinism-index/leaderboard.json"

// ADIRefreshEvery: the watch scores at most hourly; six hours keeps the feed
// fresh without hammering Pages.
const ADIRefreshEvery = 6 * time.Hour

var (
	adiMu   sync.RWMutex
	adiFeed *ADIFeed
)

func adiEnabled() bool { return os.Getenv("CAPTAIN_ADI") != "0" }

// ADIURL is the feed's location: CAPTAIN_ADI_URL (a URL or a local path,
// e.g. a checkout's website/leaderboard.json), else the published feed.
func ADIURL() string {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_ADI_URL")); v != "" {
		return v
	}
	return adiDefaultURL
}

// ADICachePath keeps the last fetch between brain starts.
func ADICachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "adi.json")
}

// LoadADICache overlays the cached feed, if any. Reports whether one loaded.
func LoadADICache() bool {
	b, err := os.ReadFile(ADICachePath())
	if err != nil {
		return false
	}
	var f ADIFeed
	if json.Unmarshal(b, &f) != nil || len(f.Tuples) == 0 {
		return false
	}
	adiMu.Lock()
	adiFeed = &f
	adiMu.Unlock()
	return true
}

// FetchADI reads the feed from ADIURL and caches it.
func FetchADI() (*ADIFeed, error) {
	if !adiEnabled() {
		return nil, errors.New("CAPTAIN_ADI=0")
	}
	src := ADIURL()
	var raw []byte
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(src)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", src, resp.StatusCode)
		}
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		if raw, err = os.ReadFile(src); err != nil {
			return nil, err
		}
	}
	var f ADIFeed
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	if len(f.Tuples) == 0 {
		return nil, fmt.Errorf("%s: no tuples", src)
	}
	f.FetchedAt = time.Now()
	adiMu.Lock()
	adiFeed = &f
	adiMu.Unlock()
	if b, err := json.MarshalIndent(f, "", " "); err == nil {
		_ = os.MkdirAll(filepath.Dir(ADICachePath()), 0o755)
		_ = os.WriteFile(ADICachePath(), b, 0o644)
	}
	return &f, nil
}

// SetADIFeed installs a feed directly (tests, the CLI reading a local file).
func SetADIFeed(f *ADIFeed) {
	adiMu.Lock()
	adiFeed = f
	adiMu.Unlock()
}

// ADISnapshot is the feed in hand (nil when none loaded).
func ADISnapshot() *ADIFeed {
	adiMu.RLock()
	defer adiMu.RUnlock()
	return adiFeed
}

// adiProvider maps an opencode provider id to ADI's provider id.
func adiProvider(opencodeProvider string) string {
	switch strings.ToLower(opencodeProvider) {
	case "openrouter":
		return "openrouter"
	case "nim", "nvidia", "nvidia_nim":
		return "nvidia_nim"
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "google", "gemini":
		return "gemini"
	case "huggingface", "hf":
		return "huggingface"
	}
	return ""
}

// ADIFor returns the best-ranked tuple measured for a leg's provider and
// model - green first, then by rank - and whether any was measured. A leg
// whose provider ADI does not probe (the CLIs, xai) is unmeasured.
func ADIFor(l Leg) (ADITuple, bool) {
	spec, ok := Spec(l)
	if !ok {
		return ADITuple{}, false
	}
	return ADIForModel(spec.Provider, spec.Model)
}

// ADIForModel is ADIFor by provider and model id.
func ADIForModel(provider, model string) (ADITuple, bool) {
	f := ADISnapshot()
	if f == nil {
		return ADITuple{}, false
	}
	p := adiProvider(provider)
	if p == "" {
		return ADITuple{}, false
	}
	var rows []ADITuple
	for _, t := range f.Tuples {
		if strings.EqualFold(t.Provider, p) && strings.EqualFold(t.Model, model) {
			rows = append(rows, t)
		}
	}
	if len(rows) == 0 {
		return ADITuple{}, false
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Green != rows[j].Green {
			return rows[i].Green
		}
		return rows[i].Rank < rows[j].Rank
	})
	return rows[0], true
}

// ADIGreenLegs lists the active legs whose serving tuple is green, best
// ADI rank first.
func ADIGreenLegs() []Leg {
	type row struct {
		leg  Leg
		rank int
	}
	var rows []row
	for _, l := range AllLegs {
		if t, ok := ADIFor(l); ok && t.Green {
			rows = append(rows, row{l, t.Rank})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].rank < rows[j].rank })
	out := make([]Leg, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.leg)
	}
	return out
}

// ADIGreenUnregistered lists green tuples no active leg serves - what
// `captain legs add` could bring in.
func ADIGreenUnregistered() []ADITuple {
	f := ADISnapshot()
	if f == nil {
		return nil
	}
	served := map[string]bool{}
	for _, l := range AllLegs {
		if s, ok := Spec(l); ok {
			served[strings.ToLower(adiProvider(s.Provider)+"/"+s.Model)] = true
		}
	}
	var out []ADITuple
	for _, t := range f.Tuples {
		if t.Green && !served[strings.ToLower(t.Provider+"/"+t.Model)] {
			out = append(out, t)
		}
	}
	return out
}

// ADIPinFor is the OpenRouter request pin that reproduces a leg's green
// tuple (provider_prefs), or nil when the leg is not green or not pinned.
func ADIPinFor(l Leg) map[string]any {
	t, ok := ADIFor(l)
	if !ok || !t.Green || len(t.ProviderPrefs) == 0 {
		return nil
	}
	return t.ProviderPrefs
}

// ADIOneLine describes a leg's determinism standing for the feed and the
// director: "green (ADI #1, streak 10, Cerebras via OpenRouter, as of …)".
func ADIOneLine(l Leg) string {
	t, ok := ADIFor(l)
	if !ok {
		return "not measured by ADI"
	}
	state := "red"
	if t.Green {
		state = "green"
	}
	where := t.Label
	if where == "" {
		where = t.Provider
	}
	return fmt.Sprintf("%s (ADI #%d, streak %d, mode share %.2f, %s, as of %s)", state, t.Rank, t.Streak, t.MeanModeShare, where, t.AsOf)
}
