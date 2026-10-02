package captaincode

// OpenShell sandbox profiles from the registry. A sandbox worker may only
// reach a model through Shield, and Shield can only promise zero data
// retention where the request itself forces it: OpenRouter's provider.zdr
// pin against an endpoint on OpenRouter's public ZDR list. So the catalog is
// every API-key leg's model (its tiers included) on every ZDR endpoint that
// takes tool calls. Subscription logins, Hugging Face and NIM give no
// per-request ZDR control and are left out; a NIM leg's model is still listed
// when OpenRouter serves the same model id on a ZDR endpoint.
//
// The catalog only names a model and a route; the pilot (profiles.py) fixes
// the host, key and policy, so a catalog entry cannot point Shield anywhere
// but OpenRouter. Listing is not selection: task.py refuses a profile until a
// fixture run passed all 18 pilot checks on it (captain openshell qualify).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OpenRouterZDRURL is OpenRouter's public list of zero-data-retention endpoints.
const OpenRouterZDRURL = "https://openrouter.ai/api/v1/endpoints/zdr"

const (
	openShellCatalogFile   = "catalog.json"
	openShellCatalogLimit  = 16 << 20
	openShellOutputCap     = 16384 // profiles.py OPENROUTER["output"]
	openShellMinContext    = 32768 // a worker's prompt, tools and file reads
	openShellCeilingMargin = 1.25  // list price headroom for a strict cap
)

var (
	openShellRoute      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(/[a-z0-9][a-z0-9.-]*){0,3}$`)
	openShellModelID    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._:-]*$`)
	openShellServedBy   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()&-]{0,63}$`)
	openShellNameUnsafe = regexp.MustCompile(`[^a-z0-9]+`)
)

// openShellWorkerParams are what the pilot's opencode worker sends.
var openShellWorkerParams = []string{"tools", "max_tokens", "temperature"}

// OpenRouterEndpoint is one row of OpenRouter's ZDR endpoint list.
type OpenRouterEndpoint struct {
	ModelID             string   `json:"model_id"`
	ProviderName        string   `json:"provider_name"`
	Tag                 string   `json:"tag"`
	ContextLength       int      `json:"context_length"`
	MaxCompletionTokens *int     `json:"max_completion_tokens"`
	SupportedParameters []string `json:"supported_parameters"`
	Status              int      `json:"status"`
	Pricing             struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// OpenShellCatalogProfile is one generated sandbox profile: a registry model
// pinned to one OpenRouter ZDR endpoint.
type OpenShellCatalogProfile struct {
	Leg      Leg        `json:"leg"`
	Tier     Tier       `json:"tier,omitempty"`
	Model    string     `json:"model"`
	Route    string     `json:"route"`
	ServedBy string     `json:"served_by"`
	Output   int        `json:"output"`
	Context  int        `json:"context"`
	Ceiling  [2]float64 `json:"ceiling"` // USD per million prompt, completion tokens
}

// OpenShellCatalog is examples/openshell-pilot/catalog.json.
type OpenShellCatalog struct {
	Schema    int                                `json:"schema"`
	Source    string                             `json:"source"`
	Generated string                             `json:"generated"`
	Profiles  map[string]OpenShellCatalogProfile `json:"profiles"`
	Skipped   []string                           `json:"skipped,omitempty"`
}

// FetchOpenRouterZDR reads OpenRouter's public ZDR list (no key is sent).
func FetchOpenRouterZDR(ctx context.Context, url string) ([]OpenRouterEndpoint, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter zdr list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter zdr list: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, openShellCatalogLimit+1))
	if err != nil {
		return nil, fmt.Errorf("openrouter zdr list: %w", err)
	}
	if len(data) > openShellCatalogLimit {
		return nil, errors.New("openrouter zdr list: over 16 MiB")
	}
	var list struct {
		Data []OpenRouterEndpoint `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("openrouter zdr list: %w", err)
	}
	if len(list.Data) == 0 {
		return nil, errors.New("openrouter zdr list: empty")
	}
	return list.Data, nil
}

// BuildOpenShellCatalog derives the sandbox profiles from the given legs and
// ZDR endpoints. Skipped says, per leg or model, why nothing was generated.
func BuildOpenShellCatalog(legs []LegSpec, endpoints []OpenRouterEndpoint, generated time.Time) OpenShellCatalog {
	c := OpenShellCatalog{Schema: 1, Source: OpenRouterZDRURL, Generated: generated.UTC().Format(time.RFC3339),
		Profiles: map[string]OpenShellCatalogProfile{}}
	byModel := map[string][]OpenRouterEndpoint{}
	for _, e := range endpoints {
		byModel[e.ModelID] = append(byModel[e.ModelID], e)
	}
	for _, s := range legs {
		switch {
		case s.Disabled || s.Transport == TransportSystemOne || s.Transport == TransportOpencodeShell:
			continue
		case s.Transport != TransportOpencode || s.Subscription:
			c.Skipped = append(c.Skipped, fmt.Sprintf("%s: subscription login, not an API key", s.ID))
			continue
		case s.Provider != "openrouter" && s.Provider != "nim":
			c.Skipped = append(c.Skipped, fmt.Sprintf("%s: provider %s has no per-request ZDR control", s.ID, s.Provider))
			continue
		}
		models := []struct {
			tier  Tier
			model string
		}{{"", s.Model}}
		for _, t := range []Tier{TierCheap, TierFrontier} {
			if m := s.Tiers[t]; m != "" && m != s.Model {
				models = append(models, struct {
					tier  Tier
					model string
				}{t, m})
			}
		}
		for _, m := range models {
			added := 0
			for _, p := range openShellEndpointProfiles(s, m.tier, m.model, byModel[m.model]) {
				name := openShellProfileName(s.ID, m.tier, p.Route)
				if !openShellProfile.MatchString(name) {
					c.Skipped = append(c.Skipped, fmt.Sprintf("%s on %s: profile name %q is not usable", m.model, p.Route, name))
					continue
				}
				if _, taken := c.Profiles[name]; taken {
					c.Skipped = append(c.Skipped, fmt.Sprintf("%s on %s: profile name %s is taken", m.model, p.Route, name))
					continue
				}
				c.Profiles[name] = p
				added++
			}
			if added == 0 {
				c.Skipped = append(c.Skipped, fmt.Sprintf("%s: no OpenRouter ZDR endpoint for %s takes tool calls", s.ID, m.model))
			}
		}
	}
	return c
}

// openShellEndpointProfiles turns one model's ZDR endpoints into profiles,
// one per route. OpenRouter lists a dated variant of a model under the same
// route; the route's profile takes the higher price and the smaller limits,
// so it holds whichever variant serves.
func openShellEndpointProfiles(s LegSpec, tier Tier, model string, endpoints []OpenRouterEndpoint) []OpenShellCatalogProfile {
	byRoute := map[string]OpenShellCatalogProfile{}
	var routes []string
	for _, e := range endpoints {
		if e.Status != 0 || !openShellRoute.MatchString(e.Tag) || !openShellModelID.MatchString(e.ModelID) ||
			!openShellServedBy.MatchString(e.ProviderName) || !hasAll(e.SupportedParameters, openShellWorkerParams) {
			continue
		}
		in, okIn := openShellPerMillion(e.Pricing.Prompt)
		out, okOut := openShellPerMillion(e.Pricing.Completion)
		if !okIn || !okOut {
			continue
		}
		output := openShellOutputCap
		if e.MaxCompletionTokens != nil && *e.MaxCompletionTokens > 0 && *e.MaxCompletionTokens < output {
			output = *e.MaxCompletionTokens
		}
		window := e.ContextLength
		if s.Ctx > 0 && s.Ctx < window {
			window = s.Ctx
		}
		if window < openShellMinContext {
			continue
		}
		p := OpenShellCatalogProfile{Leg: s.ID, Tier: tier, Model: model, Route: e.Tag, ServedBy: e.ProviderName,
			Output: output, Context: window, Ceiling: [2]float64{openShellCeiling(in), openShellCeiling(out)}}
		prev, seen := byRoute[e.Tag]
		if !seen {
			routes = append(routes, e.Tag)
		} else {
			if prev.ServedBy != p.ServedBy {
				continue
			}
			p.Output, p.Context = min(p.Output, prev.Output), min(p.Context, prev.Context)
			p.Ceiling = [2]float64{math.Max(p.Ceiling[0], prev.Ceiling[0]), math.Max(p.Ceiling[1], prev.Ceiling[1])}
		}
		byRoute[e.Tag] = p
	}
	sort.Strings(routes)
	profiles := make([]OpenShellCatalogProfile, 0, len(routes))
	for _, r := range routes {
		profiles = append(profiles, byRoute[r])
	}
	return profiles
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}

// openShellPerMillion parses OpenRouter's USD-per-token string into USD per
// million tokens; a price Shield's cap cannot hold is refused.
func openShellPerMillion(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	v *= 1e6
	return v, v < openShellMaxCostUSD
}

// openShellCeiling is the strict-cap price: list price plus a quarter,
// rounded up to the cent.
func openShellCeiling(perMillion float64) float64 {
	return math.Ceil(math.Round(perMillion*openShellCeilingMargin*1e6)/1e4) / 100
}

func openShellProfileName(leg Leg, tier Tier, route string) string {
	name := string(leg)
	if tier != "" {
		name += "-" + string(tier)
	}
	return strings.Trim(openShellNameUnsafe.ReplaceAllString(name+"-"+route, "-"), "-")
}

// OpenShellRegistryCatalog builds the catalog from the active registry.
func OpenShellRegistryCatalog(endpoints []OpenRouterEndpoint, generated time.Time) OpenShellCatalog {
	legs := make([]LegSpec, 0, len(AllLegs))
	for _, l := range AllLegs {
		if s, ok := Spec(l); ok {
			legs = append(legs, s)
		}
	}
	return BuildOpenShellCatalog(legs, endpoints, generated)
}

// WriteOpenShellCatalog replaces dir/catalog.json atomically.
func WriteOpenShellCatalog(dir string, c OpenShellCatalog) (string, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, openShellCatalogFile)
	tmp, err := os.CreateTemp(dir, ".catalog-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return "", err
	}
	return path, syncOpenShellDir(dir)
}

// openShellCatalogPriced reports whether the pilot's catalog names a profile.
// Every catalog profile carries a ceiling, so it can run under a strict cap.
func openShellCatalogPriced(pilot, name string) bool {
	if pilot == "" {
		return false
	}
	data, err := readOpenShellFile(filepath.Join(pilot, openShellCatalogFile), openShellCatalogLimit)
	if err != nil {
		return false
	}
	var c OpenShellCatalog
	if json.Unmarshal(data, &c) != nil || c.Schema != 1 {
		return false
	}
	_, ok := c.Profiles[name]
	return ok
}

// openShellQualifyBudget bounds one fixture run: sandbox create, the worker's
// 10-minute deadline and one repair, cancellation and the restart-recovery
// process.
const openShellQualifyBudget = 45 * time.Minute

// openShellQualifyRuns is how many fixture runs a qualification takes. All
// of them must pass; profiles.py enforces the same count when it records.
const openShellQualifyRuns = 3

// OpenShellQualifyRun is one fixture run of a qualification.
type OpenShellQualifyRun struct {
	State  string
	Report *OpenShellReport
	Err    error
}

// Qualify runs the pilot's fixture (pilot.py, the 18 checks) on one profile
// openShellQualifyRuns times, each in a fresh state with the one repair a
// real task gets. Every run is made, so a failure shows as a pass count, not
// as the first miss. Only when all of them pass does profiles.py record the
// profile in qualified.json; task.py accepts it from then on. The pilot's
// output goes to out.
func (r *OpenShellRunner) Qualify(ctx context.Context, profile string, out io.Writer) ([]OpenShellQualifyRun, error) {
	if !openShellProfile.MatchString(profile) {
		return nil, fmt.Errorf("openshell: profile %q is not a profile name", profile)
	}
	var runs []OpenShellQualifyRun
	passed := 0
	for i := 1; i <= openShellQualifyRuns && ctx.Err() == nil; i++ {
		fmt.Fprintf(out, "openshell: qualify %s run %d/%d\n", profile, i, openShellQualifyRuns)
		state, report, err := r.qualifyRun(ctx, profile, out)
		runs = append(runs, OpenShellQualifyRun{State: state, Report: report, Err: err})
		if err == nil && report != nil && report.Verdict == "pass" {
			passed++
		}
	}
	if passed != openShellQualifyRuns {
		return runs, fmt.Errorf("openshell: %s passed %d of %d fixture runs; not qualified", profile, passed, openShellQualifyRuns)
	}
	args := []string{"-B", filepath.Join(r.Pilot, "profiles.py"), "record", profile}
	for _, run := range runs {
		args = append(args, filepath.Join(run.State, "report.json"))
	}
	cmd := exec.CommandContext(ctx, filepath.Join(runs[0].State, "venv", "bin", "python"), args...)
	cmd.Dir = r.Pilot
	cmd.Env = openShellEnv()
	cmd.Stdout, cmd.Stderr = io.Discard, out
	if err := cmd.Run(); err != nil {
		return runs, fmt.Errorf("openshell: record %s: %w", profile, err)
	}
	return runs, nil
}

// qualifyRun is one fixture run of Qualify, in a fresh state.
func (r *OpenShellRunner) qualifyRun(ctx context.Context, profile string, out io.Writer) (string, *OpenShellReport, error) {
	state, err := r.newState()
	if err != nil {
		return "", nil, err
	}
	c, cancel := context.WithTimeout(ctx, openShellQualifyBudget)
	defer cancel()
	cmd := exec.CommandContext(c, filepath.Join(state, "venv", "bin", "python"), "-B", filepath.Join(r.Pilot, "pilot.py"),
		"--state", state, "--runtime", r.Runtime, "--profile", profile, "--qualify")
	cmd.Env = openShellEnv()
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = openShellWaitDelay
	runErr := cmd.Run()
	report, err := readOpenShellReport(filepath.Join(state, "report.json"))
	switch {
	case err != nil:
		return state, nil, fmt.Errorf("pilot left no readable report (%v): %w", runErr, err)
	case c.Err() != nil:
		return state, report, fmt.Errorf("pilot stopped (%v): %s", c.Err(), report.Error)
	case runErr != nil:
		return state, report, fmt.Errorf("pilot verdict %s: %v %s", report.Verdict, runErr, report.Error)
	}
	return state, report, nil
}
