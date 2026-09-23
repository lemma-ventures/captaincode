package main

// The roster: what the sidebar's Frontier and Models sections are built from.
// GET /v1/roster returns every active leg with its perf index, whether a
// newer model in its family outscores it, and which section it belongs to -
// plus the local agent CLIs with their installed and latest versions. The
// sidebar polls it slowly (it changes daily, not per turn) and merges it with
// the live state from /v1/workers and the scorecards from /v1/stats.
//
// The perf feed refreshes in the background when an Artificial Analysis key
// is configured (aaKey: env or aa.env), every six hours, and is cached across
// restarts; without a key the ranking is the cache, then the snapshot
// compiled into the binary. Each refresh says what changed - a model new to
// the feed, a leg whose row moved, a leg newly flagged ⇡ - in the log and in
// every open TUI's Last Runs, so a release is heard the day it lands and
// retargeting stays the operator's one click (brain_upgrade.go).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	// The feed lists a model within a day of its release (Grok 4.7: released
	// 2026-09-21, listed 09-22). Four reads a day is nothing against the
	// key's 1000, and a daily read learned of it a day late.
	perfRefreshEvery = 6 * time.Hour
	// After a failed fetch, and while no key is configured: a key dropped
	// into aa.env is found within the hour, no restart.
	perfRetryEvery = time.Hour
	cliCheckEvery  = 6 * time.Hour
)

// perfRefreshInterval is CAPTAIN_PERF_REFRESH (a duration: "1h", "12h";
// a minute at least) or the default.
func perfRefreshInterval() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_PERF_REFRESH")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
			return d
		}
	}
	return perfRefreshEvery
}

type rosterLeg struct {
	Leg        string               `json:"leg"`
	Label      string               `json:"label"` // what the sidebar prints: the leg with its route visible (codex-cli, codex-openai, glm-orouter)
	Model      string               `json:"model"`
	Display    string               `json:"display"`
	Frontier   bool                 `json:"frontier"`
	Perf       float64              `json:"perf,omitempty"`
	Coding     float64              `json:"coding,omitempty"`
	Slug       string               `json:"slug,omitempty"`
	Upgrade    *captaincode.PerfRow `json:"upgrade,omitempty"`
	Sub        bool                 `json:"subscription,omitempty"`
	PriceIn    float64              `json:"price_in,omitempty"`
	PriceOut   float64              `json:"price_out,omitempty"`
	OpenWeight bool                 `json:"open_weights,omitempty"`
}

type cliStatus struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Latest    string `json:"latest,omitempty"`
	Outdated  bool   `json:"outdated"`
	Legs      string `json:"legs"` // the legs that run through this binary
}

// cliTools are the local agent binaries the legs run through, with the npm
// package that publishes each one (the version check reads the registry -
// no auth, one small GET). cursor-agent has no public feed: installed only.
var cliTools = []struct{ name, pkg, legs string }{
	{"claude", "@anthropic-ai/claude-code", "claude, frontier"},
	{"codex", "@openai/codex", "codex-cli"},
	{"opencode", "opencode-ai", "grok, grok-max, glm, kimi, …"},
	{"cursor-agent", "", "cursor"},
}

type rosterState struct {
	mu   sync.RWMutex
	cli  []cliStatus
	seen time.Time

	key       func() string                                   // stubbed in tests; nil = aaKey
	fetch     func(key string) ([]captaincode.AAModel, error) // stubbed in tests; nil = the live feed
	keyWarned bool                                            // the missing-key line is said once per brain
}

func (rs *rosterState) aaKey() string {
	if rs.key != nil {
		return rs.key()
	}
	return aaKey()
}

func (rs *rosterState) fetchFeed(key string) ([]captaincode.AAModel, error) {
	if rs.fetch != nil {
		return rs.fetch(key)
	}
	return fetchAAModels(aaModelsURL, key)
}

// startRosterRefresh runs the perf feed and CLI version checks in the
// background: once now, then on their own cadences. Never blocks startup.
func (b *brain) startRosterRefresh() {
	if captaincode.LoadPerfCache() {
		_, _, asOf := captaincode.PerfModels()
		fmt.Printf("captain brain: perf ranking from cache (%s)\n", asOf)
	}
	go func() {
		for {
			time.Sleep(b.refreshPerf())
		}
	}()
	go func() {
		for {
			b.refreshCLI()
			time.Sleep(cliCheckEvery)
		}
	}()
}

// refreshPerf fetches the feed once and returns how long to wait before the
// next attempt: the refresh interval after a success, an hour after a failure
// or while no key is configured. Silence was the failure mode before
// 2026-09-22: a brain started without a key slept a day between checks and
// never said its ranking was five days old.
func (b *brain) refreshPerf() time.Duration {
	key := b.roster.aaKey()
	if key == "" {
		if !b.roster.keyWarned {
			b.roster.keyWarned = true
			_, src, asOf := captaincode.PerfModels()
			fmt.Printf("captain brain: perf feed: no Artificial Analysis key (CAPTAIN_AA_API_KEY, or API_KEY=… in ~/.config/captain/aa.env) - the ranking stays the %s from %s; checking for a key hourly\n", src, asOf)
		}
		return perfRetryEvery
	}
	models, err := b.roster.fetchFeed(key)
	if err != nil {
		fmt.Printf("captain brain: perf feed: %v (keeping the %s ranking; retrying in %s)\n", err, perfSourceLabel(), perfRetryEvery)
		return perfRetryEvery
	}
	prev, _, _ := captaincode.PerfModels()
	news := captaincode.PerfDiff(prev, models)
	if err := captaincode.SetPerfModels(models); err != nil {
		fmt.Printf("captain brain: perf cache: %v\n", err)
	}
	fmt.Printf("captain brain: perf ranking refreshed from the live feed (%d models; next in %s)\n", len(models), perfRefreshInterval())
	b.announcePerfNews(news)
	return perfRefreshInterval()
}

// announcePerfNews says what a refresh changed, in the log and in every open
// TUI's Last Runs (a machine-wide activity item), so the operator learns a
// model landed without watching the ranking. It only speaks: retargeting a
// pin stays a click on ⇡ or `captain upgrade --models --apply`.
func (b *brain) announcePerfNews(n captaincode.PerfNews) {
	for _, line := range perfNewsLines(n) {
		fmt.Println("captain brain: perf feed: " + line)
		b.pushActivity(activity{Kind: "feed", Leg: "ranking", Text: line})
	}
}

// perfNewsLines renders the news: arrivals on one line (four names, then a
// count), then each moved row, then each new ⇡ with what to do about it.
func perfNewsLines(n captaincode.PerfNews) []string {
	var out []string
	if len(n.Arrivals) > 0 {
		var names []string
		for i, a := range n.Arrivals {
			if i == 4 {
				break
			}
			names = append(names, fmt.Sprintf("%s (%.0f)", a.Slug, a.Perf))
		}
		line := "new on the ranking: " + strings.Join(names, ", ")
		if extra := len(n.Arrivals) - len(names); extra > 0 {
			line += fmt.Sprintf(" +%d more", extra)
		}
		out = append(out, line)
	}
	for _, m := range n.Moved {
		if m.From.Slug == "" {
			out = append(out, fmt.Sprintf("%s is now listed: %s (%.0f)", m.Leg, m.To.Slug, m.To.Perf))
			continue
		}
		out = append(out, fmt.Sprintf("%s now reads %s (%.0f), was %s (%.0f)", m.Leg, m.To.Slug, m.To.Perf, m.From.Slug, m.From.Perf))
	}
	for _, f := range n.Flagged {
		out = append(out, fmt.Sprintf("⇡ %s: %s (%.0f) outscores %s (%.0f) - click ⇡ in the sidebar or `captain upgrade --models`", f.Leg, f.Upgrade.Slug, f.Upgrade.Perf, f.Current.Slug, f.Current.Perf))
	}
	return out
}

func perfSourceLabel() string {
	_, src, _ := captaincode.PerfModels()
	return src
}

// latestNpmVersion reads a package's latest tag from the npm registry.
func latestNpmVersion(pkg string) string {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get("https://registry.npmjs.org/" + pkg + "/latest")
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	return v.Version
}

// versionLess compares dotted versions numerically (2.1.9 < 2.1.10).
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func (b *brain) refreshCLI() {
	var out []cliStatus
	for _, t := range cliTools {
		if _, err := exec.LookPath(t.name); err != nil {
			continue
		}
		st := cliStatus{Name: t.name, Installed: toolVersion(t.name), Legs: t.legs}
		if t.pkg != "" {
			st.Latest = latestNpmVersion(t.pkg)
			st.Outdated = st.Latest != "" && st.Installed != "?" && versionLess(st.Installed, st.Latest)
		}
		out = append(out, st)
	}
	b.roster.mu.Lock()
	b.roster.cli, b.roster.seen = out, time.Now()
	b.roster.mu.Unlock()
	// The same pass answers "can this leg run?": a login that came back or a
	// key that was just added should reopen the leg without a brain restart.
	captaincode.RefreshReadiness()
	ready := captaincode.LegReadiness()
	b.mu.Lock()
	b.legReady = ready
	b.mu.Unlock()
}

// unreadyLegs names the legs that cannot run, with their reasons, for the one
// startup line that explains why the ladder is shorter than the registry.
func unreadyLegs(ready map[captaincode.Leg]captaincode.Readiness) string {
	var out []string
	for _, s := range captaincode.Registry() {
		if r, ok := ready[s.ID]; ok && !r.OK {
			out = append(out, string(s.ID)+" ("+r.Reason+")")
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// rosterLabel names a leg the way the sidebar prints it: model × route.
// The route is the local agent binary (-cli) or the provider a pin goes
// through (-xai, -orouter, -nim, -openai, -zen), so codex-cli, codex-openai
// and a future codex-orouter read as the same model on three roads. Effort
// is not in the name: it is decided per request (effort.go) and shown on
// the run. Display only - slash commands and CAPTAIN_LEGS keep the bare
// leg id (2026-09-13: "codex-cli" next to "codex" read as one thing at
// two prices).
func rosterLabel(spec captaincode.LegSpec) string {
	id := string(spec.ID)
	switch spec.Transport {
	case captaincode.TransportOpencode:
		return id + "-" + routeSuffix(spec.Provider)
	case captaincode.TransportClaudeCLI, captaincode.TransportCodexCLI, captaincode.TransportCursorCLI:
		return strings.TrimSuffix(id, "-cli") + "-cli"
	}
	return id
}

// routeSuffix is the short name of an opencode provider in a label.
func routeSuffix(provider string) string {
	switch provider {
	case "openrouter":
		return "orouter"
	case "nvidia", "nim":
		return "nim"
	case "opencode":
		return "zen"
	case "":
		return "api"
	}
	return provider
}

// openWeightLegs: models whose weights are published - the sidebar marks
// them so the operator sees which capable models are not tied to a vendor.
var openWeightProviders = map[string]bool{"openrouter": true, "nim": true, "nvidia": true, "opencode": true, "huggingface": true}

func isOpenWeights(spec captaincode.LegSpec) bool {
	if !openWeightProviders[spec.Provider] {
		return false
	}
	m := strings.ToLower(spec.Model)
	for _, closed := range []string{"gpt", "claude", "gemini", "grok", "composer", "x-ai/", "openai/", "anthropic/", "google/"} {
		if strings.Contains(m, closed) {
			return false
		}
	}
	return true
}

// GET /v1/roster
func (b *brain) rosterHTTP(w http.ResponseWriter, r *http.Request) {
	models, src, asOf := captaincode.PerfModels()
	frontier := map[captaincode.Leg]bool{}
	for _, l := range captaincode.FrontierLegs() {
		frontier[l] = true
	}
	var legs []rosterLeg
	for _, l := range captaincode.AllLegs {
		if !captaincode.ServesTasks(l) || (len(b.allowed) > 0 && !b.allowed[l]) {
			continue // the sidebar lists models a turn can go to
		}
		spec, _ := captaincode.Spec(l)
		row := rosterLeg{Leg: string(l), Label: rosterLabel(spec), Model: spec.Model, Display: spec.Display, Frontier: frontier[l],
			Sub: spec.Subscription, PriceIn: spec.PriceIn, PriceOut: spec.PriceOut, OpenWeight: isOpenWeights(spec)}
		if p, ok := captaincode.PerfFor(l); ok {
			row.Perf, row.Coding, row.Slug = p.Perf, p.Coding, p.Slug
		}
		if u, ok := captaincode.UpgradeFor(l); ok {
			u := u
			row.Upgrade = &u
		}
		legs = append(legs, row)
	}
	// Ranked by perf; unlisted legs after the listed ones, by prior.
	ranked := captaincode.RankByPerf(captaincode.AllLegs)
	pos := map[string]int{}
	for i, l := range ranked {
		pos[string(l)] = i
	}
	sort.SliceStable(legs, func(i, j int) bool { return pos[legs[i].Leg] < pos[legs[j].Leg] })

	b.roster.mu.RLock()
	cli := append([]cliStatus{}, b.roster.cli...)
	b.roster.mu.RUnlock()
	writeJSON(w, 200, map[string]any{
		"perf_source": src, "perf_as_of": asOf, "perf_models": len(models),
		"perf_key": b.roster.aaKey() != "", // false: the ranking cannot get fresher than its cache
		"legs":     legs, "cli": cli,
	})
}
