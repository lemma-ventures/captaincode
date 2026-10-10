package captaincode

// The behaviour report: what "Twelve Weeks of Routing" found by hand, kept
// current by the brain. Every CAPTAIN_REPORT_EVERY runs (50), and at once when
// a leg or a host appears that the last report did not have, it writes
// ~/.captaincode/behaviour.json (for the dashboard) and
// ~/.captaincode/reports/behaviour-latest.md (to read): per leg, where it
// runs, how fast and how reliably there, what the judges and the outcomes
// say, the model each tier runs, and the notes that call for a change.
// `captain behaviour` prints it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LegBehaviour is one leg in the report.
type LegBehaviour struct {
	Leg        Leg                 `json:"leg"`
	Host       string              `json:"host"`
	HostSince  time.Time           `json:"host_since,omitempty"`
	InLegs     bool                `json:"in_captain_legs"`
	Open       bool                `json:"open_weights"`
	Runs       int                 `json:"runs"`
	OK         int                 `json:"ok"`
	Fails      int                 `json:"fails"`       // host and model failures on the current host
	NotCounted int                 `json:"not_counted"` // our bugs, logins, user stops
	FailCauses map[string]int      `json:"fail_causes,omitempty"`
	Speed      map[Class]SpeedStat `json:"speed,omitempty"`
	Prior      float64             `json:"prior"`
	JudgeMean  float64             `json:"judge_mean,omitempty"`
	Scored     int                 `json:"scored"`
	Decided    int                 `json:"decided"`
	Rejected   int                 `json:"rejected"`
	Quality    float64             `json:"quality"` // blended: prior, judges, outcomes
	Tiers      map[string]string   `json:"tiers"`   // cheap / speed / quality / frontier → model@effort
}

// Behaviour is the whole report.
type Behaviour struct {
	At      time.Time            `json:"at"`
	Window  string               `json:"window"`
	Events  int                  `json:"events"`
	Reason  string               `json:"reason"`
	Legs    []LegBehaviour       `json:"legs"`
	Benched map[string]time.Time `json:"benched_hosts,omitempty"`
	Notes   []string             `json:"notes"`
}

// reportEvery is how many runs pass between two reports.
func reportEvery() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAPTAIN_REPORT_EVERY"))); err == nil && n >= 0 {
		return n
	}
	return 50
}

// BuildBehaviour reads the ledger's scorecards into a report. inLegs says
// whether CAPTAIN_LEGS lets a leg take turns (nil: every leg).
func BuildBehaviour(l *Ledger, inLegs func(Leg) bool, now time.Time) Behaviour {
	events := l.statsEvents()
	stats := l.Stats()
	b := Behaviour{At: now, Window: statsWindow().String(), Events: len(events)}
	runs := map[Leg]int{}
	for _, e := range events {
		if e.Leg != "" {
			runs[e.Leg]++
		}
	}
	for _, leg := range AllLegs {
		if !ServesTasks(leg) {
			continue
		}
		s := stats[leg]
		lb := LegBehaviour{
			Leg: leg, Host: HostOfLeg(leg), HostSince: s.HostSince, InLegs: inLegs == nil || inLegs(leg),
			Open: OpenWeights(leg), Runs: runs[leg], OK: s.N, Fails: s.Fails, NotCounted: s.HarnessFails,
			FailCauses: s.FailCauses, Speed: s.Speed, Prior: QualityPrior(leg), Scored: s.Scored,
			Decided: s.Decided, Rejected: s.Rejected, Quality: round2(BlendedQuality(leg, s)),
			Tiers: map[string]string{},
		}
		if s.Scored > 0 {
			lb.JudgeMean = round2(s.AvgQuality)
		}
		for tier, e := range map[string]Effort{"cheap": EffortLow, "speed": EffortLow, "quality": EffortHigh, "frontier": EffortMax} {
			lb.Tiers[tier] = ModelIDAt(leg, e) + "@" + string(e)
		}
		b.Legs = append(b.Legs, lb)
	}
	sort.SliceStable(b.Legs, func(i, j int) bool { return b.Legs[i].Quality > b.Legs[j].Quality })
	b.Benched = map[string]time.Time{}
	for h, until := range l.BenchedHosts {
		if until.After(now) {
			b.Benched[h] = until
		}
	}
	b.Notes = behaviourNotes(b, stats)
	return b
}

// behaviourNotes are the findings that call for a change.
func behaviourNotes(b Behaviour, stats map[Leg]LegStats) []string {
	var notes []string
	// The fastest leg on trivial work, when CAPTAIN_LEGS leaves it out.
	var fastest *LegBehaviour
	var fastMs int64
	for i := range b.Legs {
		lb := &b.Legs[i]
		if IsAgentCLI(lb.Leg) {
			continue
		}
		if st, ok := SpeedOf(stats[lb.Leg], ClassTrivial); ok && (fastest == nil || st.MedianMs < fastMs) {
			fastest, fastMs = lb, st.MedianMs
		}
	}
	if fastest != nil && !fastest.InLegs {
		notes = append(notes, fmt.Sprintf("%s is the fastest leg measured on trivial work (%s) and CAPTAIN_LEGS leaves it out: /speed cannot use it.", fastest.Leg, fmtMs(fastMs)))
	}
	for _, lb := range b.Legs {
		total := lb.OK + lb.Fails
		if lb.Fails >= 3 && lb.Fails*3 >= total {
			notes = append(notes, fmt.Sprintf("%s failed %d of %d runs on %s%s.", lb.Leg, lb.Fails, total, lb.Host, causeList(lb.FailCauses)))
		}
		if s := stats[lb.Leg]; s.MovedFrom != "" {
			since := "; no run there yet"
			if !s.HostSince.IsZero() {
				since = " on " + s.HostSince.Format("Jan 02")
			}
			notes = append(notes, fmt.Sprintf("%s moved from %s to %s%s: its speed and reliability start over there.", lb.Leg, s.MovedFrom, lb.Host, since))
		}
		if lb.Scored >= refitMinScored && lb.JudgeMean > 0 && abs(lb.JudgeMean-lb.Prior) >= 1 {
			notes = append(notes, fmt.Sprintf("%s: prior %.1f, judges %.2f over %d scores - the weekly refit is moving it.", lb.Leg, lb.Prior, lb.JudgeMean, lb.Scored))
		}
		if lb.Decided >= outcomeMinDecided && lb.Rejected*4 >= lb.Decided {
			notes = append(notes, fmt.Sprintf("%s had %d of %d informative outcomes rejected.", lb.Leg, lb.Rejected, lb.Decided))
		}
	}
	hosts := make([]string, 0, len(b.Benched))
	for h := range b.Benched {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		notes = append(notes, fmt.Sprintf("host %s is benched until %s after repeated stalls.", h, b.Benched[h].Format("15:04")))
	}
	return notes
}

func causeList(c map[string]int) string {
	if len(c) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return c[keys[i]] > c[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(k, "_", " "), c[k]))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// BehaviourPath is ~/.captaincode/behaviour.json.
func BehaviourPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "behaviour.json")
}

// LoadBehaviour reads the last report (ok false when there is none).
func LoadBehaviour(path string) (Behaviour, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Behaviour{}, false
	}
	var b Behaviour
	return b, json.Unmarshal(raw, &b) == nil
}

// BehaviourDue says whether a new report is due against the last one, and
// why: enough runs since, or a leg or host it did not have.
func BehaviourDue(prev Behaviour, havePrev bool, cur Behaviour) (bool, string) {
	if !havePrev {
		return true, "first report"
	}
	known := map[string]bool{}
	for _, lb := range prev.Legs {
		known[string(lb.Leg)+"@"+lb.Host] = true
	}
	for _, lb := range cur.Legs {
		if !known[string(lb.Leg)+"@"+lb.Host] {
			return true, fmt.Sprintf("new leg or host: %s on %s", lb.Leg, lb.Host)
		}
	}
	if every := reportEvery(); every > 0 && cur.Events-prev.Events >= every {
		return true, fmt.Sprintf("%d runs since the last report", cur.Events-prev.Events)
	}
	return false, ""
}

// WriteBehaviour writes the report beside path (behaviour.json) and its
// markdown into reports/behaviour-latest.md.
func WriteBehaviour(path string, b Behaviour) error {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	reports := filepath.Join(filepath.Dir(path), "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(reports, "behaviour-latest.md"), []byte(b.Markdown()), 0o600)
}

// Markdown renders the report.
func (b Behaviour) Markdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Model behaviour\n\n%s · %d runs over the last %s · %s\n\n", b.At.Format("2006-01-02 15:04"), b.Events, b.Window, b.Reason)
	if len(b.Notes) > 0 {
		sb.WriteString("## Needs attention\n\n")
		for _, n := range b.Notes {
			sb.WriteString("- " + n + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("## Legs\n\n| Leg | On | Host | Runs | OK | Fails | Trivial | Medium | High | Prior | Judges (n) | Rejected | Quality |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, lb := range b.Legs {
		on := "yes"
		if !lb.InLegs {
			on = "no"
		}
		judges := "–"
		if lb.JudgeMean > 0 {
			judges = fmt.Sprintf("%.2f (%d)", lb.JudgeMean, lb.Scored)
		}
		rej := "–"
		if lb.Decided > 0 {
			rej = fmt.Sprintf("%d/%d", lb.Rejected, lb.Decided)
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %d | %d | %d | %s | %s | %s | %.1f | %s | %s | %.2f |\n",
			lb.Leg, on, lb.Host, lb.Runs, lb.OK, lb.Fails, speedCell(lb.Speed, ClassTrivial), speedCell(lb.Speed, ClassMedium), speedCell(lb.Speed, ClassHigh),
			lb.Prior, judges, rej, lb.Quality)
	}
	sb.WriteString("\n## Tiers\n\n| Leg | Cheap | Speed | Quality | Frontier |\n|---|---|---|---|---|\n")
	for _, lb := range b.Legs {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n", lb.Leg, lb.Tiers["cheap"], lb.Tiers["speed"], lb.Tiers["quality"], lb.Tiers["frontier"])
	}
	sb.WriteString("\nSpeed is the median of the leg's ok runs of that class on its current host. Quality blends the prior, the cross-vendor judges and the rejected outcomes. `/frontier` and `/quality` set the effort by the task's class.\n")
	return sb.String()
}

func speedCell(s map[Class]SpeedStat, c Class) string {
	st, ok := s[c]
	if !ok || st.N == 0 {
		return "–"
	}
	return fmt.Sprintf("%s (%d)", fmtMs(st.MedianMs), st.N)
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
