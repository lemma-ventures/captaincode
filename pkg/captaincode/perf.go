package captaincode

// The performance index: one number per model, from an independent benchmark
// feed, that ranks the roster the same way everywhere - the sidebar's
// Frontier and Models sections, the /frontier failover chain, and the
// "a newer version exists" flags.
//
// Three sources, in order of freshness: the live feed (when an Artificial
// Analysis key is configured; the brain fetches it in the background and
// caches it), the cache from a previous fetch, and the snapshot compiled into
// the binary (perf_default.json). Without a key the operator gets the snapshot
// - a ranking that is never blank, just older. The number shown is "perf";
// how the benchmark composes it is the feed's business, not the sidebar's.

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed perf_default.json
var perfDefaultJSON []byte

type perfFile struct {
	Source       string `json:"source"`
	SnapshotDate string `json:"snapshot_date"`
	FetchedAt    string `json:"fetched_at,omitempty"`
	Models       []struct {
		Name         string  `json:"name"`
		Slug         string  `json:"slug"`
		Creator      string  `json:"creator"`
		Released     string  `json:"released"`
		Intelligence float64 `json:"intelligence"`
		Coding       float64 `json:"coding"`
		Math         float64 `json:"math"`
		PriceIn      float64 `json:"price_in"`
		PriceOut     float64 `json:"price_out"`
		TPS          float64 `json:"tps"`
	} `json:"models"`
}

func (f perfFile) rows() []AAModel {
	out := make([]AAModel, 0, len(f.Models))
	for _, m := range f.Models {
		out = append(out, AAModel{Name: m.Name, Slug: m.Slug, Creator: m.Creator, Released: m.Released,
			IntelligenceIndex: m.Intelligence, CodingIndex: m.Coding, MathIndex: m.Math,
			PriceIn: m.PriceIn, PriceOut: m.PriceOut, TokensPerSecond: m.TPS})
	}
	return out
}

func perfFileOf(models []AAModel, source string, at time.Time) perfFile {
	var f perfFile
	f.Source, f.FetchedAt = source, at.UTC().Format(time.RFC3339)
	for _, m := range models {
		f.Models = append(f.Models, struct {
			Name         string  `json:"name"`
			Slug         string  `json:"slug"`
			Creator      string  `json:"creator"`
			Released     string  `json:"released"`
			Intelligence float64 `json:"intelligence"`
			Coding       float64 `json:"coding"`
			Math         float64 `json:"math"`
			PriceIn      float64 `json:"price_in"`
			PriceOut     float64 `json:"price_out"`
			TPS          float64 `json:"tps"`
		}{m.Name, m.Slug, m.Creator, m.Released, m.IntelligenceIndex, m.CodingIndex, m.MathIndex, m.PriceIn, m.PriceOut, m.TokensPerSecond})
	}
	return f
}

var perf struct {
	mu     sync.RWMutex
	models []AAModel
	source string // "live", "cache", "snapshot"
	asOf   string // date the ranking reflects
}

func init() {
	var f perfFile
	if err := json.Unmarshal(perfDefaultJSON, &f); err == nil {
		perf.models, perf.source, perf.asOf = f.rows(), "snapshot", f.SnapshotDate
	}
}

// PerfCachePath is where a live fetch is kept between brain starts.
func PerfCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "perf.json")
}

// LoadPerfCache overlays the cached feed from a previous live fetch, if any.
// The cache wins over the compiled snapshot whatever its age: it is at least
// as new as the binary's snapshot in practice, and a stale-but-live ranking
// beats a stale-and-compiled one.
func LoadPerfCache() bool {
	data, err := os.ReadFile(PerfCachePath())
	if err != nil {
		return false
	}
	var f perfFile
	if err := json.Unmarshal(data, &f); err != nil || len(f.Models) == 0 {
		return false
	}
	perf.mu.Lock()
	defer perf.mu.Unlock()
	perf.models, perf.source = f.rows(), "cache"
	if t, err := time.Parse(time.RFC3339, f.FetchedAt); err == nil {
		perf.asOf = t.Format("2006-01-02")
	}
	return true
}

// SetPerfModels installs a freshly fetched feed and caches it on disk.
func SetPerfModels(models []AAModel) error {
	if len(models) == 0 {
		return nil
	}
	now := time.Now()
	perf.mu.Lock()
	perf.models, perf.source, perf.asOf = models, "live", now.Format("2006-01-02")
	perf.mu.Unlock()
	data, err := json.Marshal(perfFileOf(models, "live", now))
	if err != nil {
		return err
	}
	p := PerfCachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// PerfModels returns the active feed and where it came from.
func PerfModels() (models []AAModel, source, asOf string) {
	perf.mu.RLock()
	defer perf.mu.RUnlock()
	return perf.models, perf.source, perf.asOf
}

// PerfRow is a leg's benchmark row: the model it runs, scored.
type PerfRow struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	Perf    float64 `json:"perf"`   // the ranking number (intelligence index)
	Coding  float64 `json:"coding"` // the coding index, when scored
	Created string  `json:"released,omitempty"`
}

func rowOf(m AAModel) PerfRow {
	return PerfRow{Slug: m.Slug, Name: m.Name, Perf: m.IntelligenceIndex, Coding: m.CodingIndex, Created: m.Released}
}

// PerfFor is the benchmark row of the model a leg runs. ok=false when the
// feed does not list it (cursor's Composer, the rotating free model).
func PerfFor(leg Leg) (PerfRow, bool) {
	models, _, _ := PerfModels()
	m, ok := MatchAA(models, leg)
	if !ok {
		return PerfRow{}, false
	}
	return rowOf(m), true
}

// perfOrPrior ranks a leg by its perf index, falling back to its prior on
// the same 0-100 scale when the feed does not list it (prior 8.0 → 80, so an
// unlisted leg lands about where its hand-written estimate says).
func perfOrPrior(leg Leg) float64 {
	if r, ok := PerfFor(leg); ok && r.Perf > 0 {
		return r.Perf
	}
	return QualityPrior(leg) * 10 * 0.55 // priors top out at 9.5 ≈ the index's ~53 ceiling
}

// RankByPerf orders legs by perf index, highest first; ties keep ladder order.
func RankByPerf(legs []Leg) []Leg {
	out := append([]Leg{}, legs...)
	sort.SliceStable(out, func(i, j int) bool { return perfOrPrior(out[i]) > perfOrPrior(out[j]) })
	return out
}

// FrontierLegs is the roster's frontier section: the frontier-class legs
// (claude included - /frontier IS claude at full effort), ranked by perf.
func FrontierLegs() []Leg {
	var out []Leg
	for _, l := range AllLegs {
		if IsFrontierClass(l) || l == LegClaude {
			out = append(out, l)
		}
	}
	return RankByPerf(out)
}

// FrontierLegsFor is the frontier section narrowed to the legs that can
// actually serve the task (M2.2). The initial frontier worker is selected
// from eligible candidates rather than from the perf ranking alone: a
// frontier leg that cannot see the screenshot the task is about is the wrong
// first attempt however well it scores. If no frontier leg qualifies, the
// unfiltered section is returned - a capability floor narrows the choice, it
// never leaves /frontier with nothing to run.
func FrontierLegsFor(r Requirements) []Leg {
	all := FrontierLegs()
	if capable := FilterCapable(all, r); len(capable) > 0 {
		return capable
	}
	return all
}

// FrontierChain is the /frontier failover order: every frontier leg by perf,
// then every other leg by perf - so when the frontier tier is closed the
// work lands on the next most capable model that is open, never on whatever
// the cheap ladder had at hand. The failed leg is left out.
func FrontierChain(failed Leg) []Leg { return FrontierChainFor(failed, Requirements{}) }

// FrontierChainFor is FrontierChain with a capability floor: legs that can
// serve the task come first (frontier tier, then the rest), and the ones that
// cannot follow rather than being dropped, because a chain that ran out of
// links is worse than a last attempt by a leg that may answer partially.
func FrontierChainFor(failed Leg, r Requirements) []Leg {
	seen := map[Leg]bool{failed: true, LegFrontier: true}
	var out []Leg
	add := func(ls []Leg) {
		for _, l := range ls {
			if !seen[l] && ServesTasks(l) { // a decision leg cannot be a last attempt
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	add(FilterCapable(FrontierLegs(), r))
	add(FilterCapable(RankByPerf(AllLegs), r))
	add(FrontierLegs())
	add(RankByPerf(AllLegs))
	return out
}

// ── upgrades ─────────────────────────────────────────────────────────────────

var (
	effortSuffix = regexp.MustCompile(`-(xhigh|high|medium|low|minimal|max|thinking|reasoning|non-reasoning|preview|fallback|contributor|exp)$`)
	versionBits  = regexp.MustCompile(`[0-9]+`)
	dashRuns     = regexp.MustCompile(`-+`)
)

// perfFamily reduces a slug to its family: effort suffixes and version
// numbers stripped, so claude-fable-5-1 and claude-fable-5 are one family,
// glm-5-3-flash and glm-5-3 are two.
func perfFamily(slug string) string {
	s := strings.ToLower(slug)
	for {
		t := effortSuffix.ReplaceAllString(s, "")
		if t == s {
			break
		}
		s = t
	}
	s = versionBits.ReplaceAllString(s, "")
	s = dashRuns.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func isEffortVariant(slug string) bool { return effortSuffix.MatchString(strings.ToLower(slug)) }

// UpgradeFor reports a newer model in the same family that outscores the one
// a leg runs: released later (when both dates are known), a base variant, at
// least a point better. ok=false when the leg is current or unlisted.
func UpgradeFor(leg Leg) (PerfRow, bool) {
	models, _, _ := PerfModels()
	cur, ok := MatchAA(models, leg)
	if !ok {
		return PerfRow{}, false
	}
	fam := perfFamily(cur.Slug)
	var best AAModel
	for _, m := range models {
		if m.Slug == cur.Slug || isEffortVariant(m.Slug) || perfFamily(m.Slug) != fam {
			continue
		}
		if cur.Creator != "" && m.Creator != "" && !strings.EqualFold(cur.Creator, m.Creator) {
			continue
		}
		if m.IntelligenceIndex < cur.IntelligenceIndex+1 {
			continue
		}
		if cur.Released != "" && m.Released != "" && m.Released <= cur.Released {
			continue
		}
		if m.IntelligenceIndex > best.IntelligenceIndex {
			best = m
		}
	}
	if best.Slug == "" {
		return PerfRow{}, false
	}
	return rowOf(best), true
}
