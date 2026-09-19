package main

// The roster: what the sidebar's Frontier and Models sections are built from.
// GET /v1/roster returns every active leg with its perf index, whether a
// newer model in its family outscores it, and which section it belongs to -
// plus the local agent CLIs with their installed and latest versions. The
// sidebar polls it slowly (it changes daily, not per turn) and merges it with
// the live state from /v1/workers and the scorecards from /v1/stats.
//
// The perf feed refreshes in the background when an Artificial Analysis key
// is configured (aaKey: env or aa.env), daily, and is cached across restarts;
// without a key the ranking is the snapshot compiled into the binary.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const (
	perfRefreshEvery = 24 * time.Hour
	cliCheckEvery    = 6 * time.Hour
)

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
			b.refreshPerf()
			time.Sleep(perfRefreshEvery)
		}
	}()
	go func() {
		for {
			b.refreshCLI()
			time.Sleep(cliCheckEvery)
		}
	}()
}

func (b *brain) refreshPerf() {
	key := aaKey()
	if key == "" {
		return
	}
	models, err := fetchAAModels(aaModelsURL, key)
	if err != nil {
		fmt.Printf("captain brain: perf feed: %v (keeping the %s ranking)\n", err, perfSourceLabel())
		return
	}
	if err := captaincode.SetPerfModels(models); err != nil {
		fmt.Printf("captain brain: perf cache: %v\n", err)
	}
	fmt.Printf("captain brain: perf ranking refreshed from the live feed (%d models)\n", len(models))
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
	_, src, asOf := captaincode.PerfModels()
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
		"perf_source": src, "perf_as_of": asOf,
		"legs": legs, "cli": cli,
	})
}
