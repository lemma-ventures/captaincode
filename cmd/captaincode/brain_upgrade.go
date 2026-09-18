package main

// Retargeting a leg to the newer model the ranking flagged (⇡ in the
// sidebar). Not automatic: a pin change moves cost, context and behaviour,
// so it is one deliberate click (the ⇡ in the sidebar → POST
// /v1/roster/upgrade) or one command (`captain upgrade --models --apply`).
// Both do the same thing: resolve the benchmark slug to the provider's model
// id (models.dev, the catalogue opencode itself reads), write the leg's new
// pin to the registry overlay (~/.captaincode/legs.json) and reload the
// registry, so the next worker on that leg runs the new model. The compiled
// default is untouched; `captain legs remove <leg>` or editing the overlay
// puts it back.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const modelsDevURL = "https://models.dev/api.json"

// modelsDevCatalogue is provider id → model ids, cached a day.
func modelsDevCatalogue() (map[string][]string, error) {
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".captaincode", "modelsdev.json")
	var raw map[string]struct {
		Models map[string]any `json:"models"`
	}
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &raw) == nil {
			return flattenCatalogue(raw), nil
		}
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(modelsDevURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models.dev: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(cache), 0o755)
	_ = os.WriteFile(cache, b, 0o644)
	return flattenCatalogue(raw), nil
}

func flattenCatalogue(raw map[string]struct {
	Models map[string]any `json:"models"`
}) map[string][]string {
	out := map[string][]string{}
	for p, v := range raw {
		for id := range v.Models {
			out[p] = append(out[p], id)
		}
	}
	return out
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// resolveProviderModel finds the provider's id for a benchmark slug:
// "gemini-3-8-flash" ↔ "google/gemini-3.8-flash" once both are reduced to
// letters and digits. The shortest id that matches is the base variant.
func resolveProviderModel(catalogue map[string][]string, provider, slug string) (string, bool) {
	want := nonAlnum.ReplaceAllString(strings.ToLower(slug), "")
	best := ""
	for _, id := range catalogue[provider] {
		base := id
		if i := strings.LastIndex(id, "/"); i >= 0 {
			base = id[i+1:]
		}
		got := nonAlnum.ReplaceAllString(strings.ToLower(base), "")
		if got == want || strings.HasPrefix(got, want) {
			if best == "" || len(id) < len(best) {
				best = id
			}
		}
	}
	return best, best != ""
}

// upgradeLeg retargets one leg to the model the ranking flagged. dry=true
// only reports what would change.
func upgradeLeg(leg captaincode.Leg, dry bool) (string, error) {
	spec, ok := captaincode.Spec(leg)
	if !ok {
		return "", fmt.Errorf("unknown leg %q", leg)
	}
	up, ok := captaincode.UpgradeFor(leg)
	if !ok {
		return "", fmt.Errorf("%s is current (no newer model in its family outscores %s)", leg, spec.Model)
	}
	if spec.Transport != captaincode.TransportOpencode || spec.Provider == "" {
		return "", fmt.Errorf("%s runs through %s, not a model pin - upgrade the CLI (`captain upgrade`) or set its model flag", leg, spec.Transport)
	}
	cat, err := modelsDevCatalogue()
	if err != nil {
		return "", fmt.Errorf("model catalogue: %w", err)
	}
	id, ok := resolveProviderModel(cat, spec.Provider, up.Slug)
	if !ok {
		return "", fmt.Errorf("%s (%s) is not served by %s yet - the ranking is ahead of the provider", up.Name, up.Slug, spec.Provider)
	}
	line := fmt.Sprintf("%s: %s/%s → %s/%s (index %.0f → %.0f)", leg, spec.Provider, spec.Model, spec.Provider, id, func() float64 {
		if p, ok := captaincode.PerfFor(leg); ok {
			return p.Perf
		}
		return 0
	}(), up.Perf)
	if dry {
		return line + "  (dry run)", nil
	}
	if err := captaincode.AddLeg("", captaincode.LegSpec{ID: leg, Model: id, AA: up.Slug}); err != nil {
		return "", err
	}
	fmt.Printf("captain brain: leg %s retargeted → %s/%s (overlay %s)\n", leg, spec.Provider, id, captaincode.RegistryOverlayPath())
	return line, nil
}

// POST /v1/roster/upgrade {"leg":"gemini","dry":false}
func (b *brain) rosterUpgradeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Leg string `json:"leg"`
		Dry bool   `json:"dry"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	line, err := upgradeLeg(captaincode.Leg(req.Leg), req.Dry)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"result": line})
}

// upgradeModels is `captain upgrade --models [--apply]`: every flagged leg.
func upgradeModels(apply bool) {
	n := 0
	for _, l := range captaincode.AllLegs {
		if _, ok := captaincode.UpgradeFor(l); !ok {
			continue
		}
		n++
		line, err := upgradeLeg(l, !apply)
		if err != nil {
			fmt.Printf("  %s: %v\n", l, err)
			continue
		}
		fmt.Println("  " + line)
	}
	if n == 0 {
		fmt.Println("  every model pin is current against the ranking")
	} else if !apply {
		fmt.Println("\ndry run - `captain upgrade --models --apply` writes the new pins to " + captaincode.RegistryOverlayPath() + " (a running brain reloads the registry; restart it to be sure)")
	}
}
