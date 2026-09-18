// `captain priors` - show the active quality priors and where each came from.
// `captain priors sync [--apply]` - fetch the Artificial Analysis coding index
// and propose refreshed priors; --apply writes ~/.captaincode/priors.json
// (human-confirmed: nothing rewrites routing behavior silently).
//
// Key: CAPTAIN_AA_API_KEY (or AA_API_KEY) - free at artificialanalysis.ai
// (Insights account → API key; rate limit 1000 req/day, we make 1 request).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const aaModelsURL = "https://artificialanalysis.ai/api/v2/data/llms/models"

// aaResponse mirrors the fields we need from the AA data API.
type aaResponse struct {
	Data []struct {
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		Released string `json:"release_date"`
		Creator  struct {
			Name string `json:"name"`
		} `json:"model_creator"`
		Evaluations struct {
			CodingIndex       float64 `json:"artificial_analysis_coding_index"`
			IntelligenceIndex float64 `json:"artificial_analysis_intelligence_index"`
			MathIndex         float64 `json:"artificial_analysis_math_index"`
		} `json:"evaluations"`
		Pricing struct {
			In  float64 `json:"price_1m_input_tokens"`
			Out float64 `json:"price_1m_output_tokens"`
		} `json:"pricing"`
		TPS float64 `json:"median_output_tokens_per_second"`
	} `json:"data"`
}

func fetchAAModels(url, key string) ([]captaincode.AAModel, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artificial analysis API: HTTP %d (bad/missing key? set CAPTAIN_AA_API_KEY)", resp.StatusCode)
	}
	var r aaResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode AA response: %w", err)
	}
	out := make([]captaincode.AAModel, 0, len(r.Data))
	for _, d := range r.Data {
		out = append(out, captaincode.AAModel{Name: d.Name, Slug: d.Slug, CodingIndex: d.Evaluations.CodingIndex,
			IntelligenceIndex: d.Evaluations.IntelligenceIndex, MathIndex: d.Evaluations.MathIndex,
			PriceIn: d.Pricing.In, PriceOut: d.Pricing.Out, TokensPerSecond: d.TPS, Creator: d.Creator.Name, Released: d.Released})
	}
	return out, nil
}

func cmdPriors(args []string) {
	if len(args) > 0 && args[0] == "sync" {
		apply := len(args) > 1 && args[1] == "--apply"
		cmdPriorsSync(apply)
		return
	}
	// show active priors
	fmt.Println("active quality priors (compiled defaults + ~/.captaincode/priors.json overrides):")
	legs := append([]captaincode.Leg(nil), captaincode.AllLegs...)
	sort.Slice(legs, func(i, j int) bool { return captaincode.QualityPrior(legs[i]) > captaincode.QualityPrior(legs[j]) })
	for _, l := range legs {
		fmt.Printf("  %-8s %.1f\n", l, captaincode.QualityPrior(l))
	}
	fmt.Println("\nrefresh from the Artificial Analysis coding index: captain priors sync [--apply]")
}

func cmdPriorsSync(apply bool) {
	key := aaKey()
	if key == "" {
		fmt.Println("no API key: set CAPTAIN_AA_API_KEY, or put API_KEY=… in aa.env next to the captain source or in ~/.config/captain/ (free key: artificialanalysis.ai → Insights account → API keys)")
		os.Exit(2)
	}
	models, err := fetchAAModels(aaModelsURL, key)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("fetched %d models from Artificial Analysis\n", len(models))
	proposed, err := captaincode.ProposeDomainPriors(models)
	if err != nil {
		fatal(err)
	}
	fmt.Println("\nleg        current → all / code / prose   (source model: coding idx, intelligence idx, $/M in)")
	for _, l := range captaincode.AllLegs {
		cur := captaincode.QualityPrior(l)
		if dp, ok := proposed[l]; ok {
			m, _ := captaincode.MatchAA(models, l)
			marker := ""
			if dp["all"] != cur {
				marker = "  *"
			}
			fmt.Printf("  %-10s %.1f → %.1f / %.1f / %.1f%s   (%s: %.0f, %.0f, $%.2f)\n", l, cur, dp["all"], dp["code"], dp["editorial"], marker, m.Name, m.CodingIndex, m.IntelligenceIndex, m.PriceIn)
		} else {
			fmt.Printf("  %-10s %.1f (no benchmark match - unchanged)\n", l, cur)
		}
	}
	if !apply {
		fmt.Println("\ndry run - re-run with `captain priors sync --apply` to write ~/.captaincode/priors.json")
		return
	}
	if err := captaincode.SaveDomainPriorOverrides(captaincode.PriorOverridesPath(), proposed); err != nil {
		fatal(err)
	}
	fmt.Printf("\napplied → %s (restart `captain brain` to pick it up)\n", captaincode.PriorOverridesPath())
}

// aaKey finds the Artificial Analysis key: env first, then an aa.env file
// (`API_KEY=…` or `CAPTAIN_AA_API_KEY=…`) in ~/.config/captain/, the captain
// source dir (CAPTAIN_SRC, gitignored there) or the current directory. The
// key is never printed.
func aaKey() string {
	for _, v := range []string{"CAPTAIN_AA_API_KEY", "AA_API_KEY"} {
		if k := strings.TrimSpace(os.Getenv(v)); k != "" {
			return k
		}
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "captain", "aa.env"))
	}
	if src := os.Getenv("CAPTAIN_SRC"); src != "" {
		candidates = append(candidates, filepath.Join(src, "aa.env"))
	}
	candidates = append(candidates, "aa.env")
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			for _, prefix := range []string{"CAPTAIN_AA_API_KEY=", "AA_API_KEY=", "API_KEY="} {
				if strings.HasPrefix(line, prefix) {
					if k := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "\"'"); k != "" {
						return k
					}
				}
			}
		}
	}
	return ""
}
