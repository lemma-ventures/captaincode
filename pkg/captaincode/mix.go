package captaincode

// Routing mix (2026-09-23): a standing target for how unprefixed turns are
// shared across six axes. `/captain more oss 20%` raises the oss share by
// 20% relative; repeating more or less compounds; an assignment
// (`oss=20% frontier=30% …`) sets the balance directly. Until the user
// sets one, the mix is the default and does not steer.
//
// An explicit /quality, /speed, /save, /frontier, /oss or /deterministic
// on a turn still wins. CAPTAIN_STEER=0 keeps a saved mix from moving
// the ranking.

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

const steerStep = 5.0

const steerGain = 0.8

const steerWindow = 40

const steerWarmup = 20

var steerAxes = []string{"frontier", "quality", "cheap", "fast", "oss", "deterministic"}

// SteerMix is the desired share of unprefixed routes, in percent. Set is
// false until the user steers; a zero mix then means the default, and the
// ranking is left alone.
type SteerMix struct {
	Frontier      float64 `json:"frontier,omitempty"`
	Quality       float64 `json:"quality,omitempty"`
	Cheap         float64 `json:"cheap,omitempty"`
	Fast          float64 `json:"fast,omitempty"`
	OSS           float64 `json:"oss,omitempty"`
	Deterministic float64 `json:"deterministic,omitempty"`
	Set           bool    `json:"set,omitempty"`
}

// DefaultSteer is the mix when nothing has been set: five axes at 20%,
// deterministic at 0. It is a baseline, not a bias.
func DefaultSteer() SteerMix {
	return SteerMix{Frontier: 20, Quality: 20, Cheap: 20, Fast: 20, OSS: 20}
}

// Resolved is the mix in force: the default when unset, otherwise the
// saved percents (a corrupt all-zero mix falls back to the default).
func (m SteerMix) Resolved() SteerMix {
	if !m.Set {
		return DefaultSteer()
	}
	if m.sum() < 0.5 {
		return DefaultSteer()
	}
	return m
}

func (m SteerMix) sum() float64 {
	return m.Frontier + m.Quality + m.Cheap + m.Fast + m.OSS + m.Deterministic
}

func (m SteerMix) get(axis string) float64 {
	switch axis {
	case "frontier":
		return m.Frontier
	case "quality":
		return m.Quality
	case "cheap":
		return m.Cheap
	case "fast":
		return m.Fast
	case "oss":
		return m.OSS
	case "deterministic":
		return m.Deterministic
	}
	return 0
}

func (m *SteerMix) set(axis string, v float64) {
	if v < 0 {
		v = 0
	}
	switch axis {
	case "frontier":
		m.Frontier = v
	case "quality":
		m.Quality = v
	case "cheap":
		m.Cheap = v
	case "fast":
		m.Fast = v
	case "oss":
		m.OSS = v
	case "deterministic":
		m.Deterministic = v
	}
}

// SteerCommand is one parsed `/captain` mix command. Ok is false when the
// line is not a mix command (a task, a helm word). Err is set when it is
// a mix command that cannot be applied.
type SteerCommand struct {
	Kind   string
	Axis   string
	More   bool
	Pct    float64
	HasPct bool
	Assign map[string]float64
	Err    string
}

// ParseSteerCommand reads the words after `/captain`. A line that is not
// a mix command returns ok=false so the helm and the task path still see it.
func ParseSteerCommand(word string) (SteerCommand, bool) {
	fields := strings.Fields(strings.ReplaceAll(strings.TrimSpace(word), ",", " "))
	if len(fields) == 0 {
		return SteerCommand{}, false
	}
	switch fields[0] {
	case "target", "targets", "mix", "balance":
		if len(fields) == 1 {
			return SteerCommand{Kind: "show"}, true
		}
		if len(fields) == 2 && fields[1] == "reset" {
			return SteerCommand{Kind: "reset"}, true
		}
		return SteerCommand{}, false
	case "more", "less":
		if len(fields) == 1 {
			return SteerCommand{Kind: "adjust", Err: "name an axis: frontier, quality, cheap, fast, oss, deterministic"}, true
		}
		ax, ok := canonSteerAxis(fields[1])
		if !ok {
			return SteerCommand{}, false
		}
		cmd := SteerCommand{Kind: "adjust", Axis: ax, More: fields[0] == "more"}
		if len(fields) == 2 {
			return cmd, true
		}
		if len(fields) != 3 {
			return SteerCommand{Kind: "adjust", Err: "say `/captain more oss` or `/captain more oss 20%`"}, true
		}
		pct, err := parseSteerPct(fields[2])
		if err != nil {
			return SteerCommand{Kind: "adjust", Err: err.Error()}, true
		}
		if pct > 1000 {
			return SteerCommand{Kind: "adjust", Err: "percent must be at most 1000"}, true
		}
		cmd.HasPct, cmd.Pct = true, pct
		return cmd, true
	}
	if !strings.Contains(fields[0], "=") {
		return SteerCommand{}, false
	}
	cmd := SteerCommand{Kind: "assign", Assign: map[string]float64{}}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" || v == "" {
			return SteerCommand{}, false
		}
		ax, ok := canonSteerAxis(k)
		if !ok {
			return SteerCommand{Kind: "assign", Err: fmt.Sprintf("unknown axis %q (frontier, quality, cheap, fast, oss, deterministic)", k)}, true
		}
		pct, err := parseSteerPct(v)
		if err != nil {
			return SteerCommand{Kind: "assign", Err: err.Error()}, true
		}
		if pct > 100 {
			return SteerCommand{Kind: "assign", Err: fmt.Sprintf("%s=%s is over 100%%", ax, formatSteerPct(pct))}, true
		}
		cmd.Assign[ax] = pct
	}
	return cmd, true
}

func canonSteerAxis(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "frontier":
		return "frontier", true
	case "quality", "best":
		return "quality", true
	case "cheap", "save":
		return "cheap", true
	case "fast", "speed":
		return "fast", true
	case "oss", "open":
		return "oss", true
	case "deterministic", "det", "adi":
		return "deterministic", true
	}
	return "", false
}

func parseSteerPct(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0, fmt.Errorf("missing percent")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("not a percent: %q", s)
	}
	return v, nil
}

// Adjust raises or lowers one axis. A bare more/less moves 5 points.
// `more X N%` multiplies that share by 1+N/100 (20% more of 20% is 24%).
// From zero, N% is absolute, so deterministic can leave 0. The other axes
// fund the change and the mix stays a balance of 100.
func (m SteerMix) Adjust(axis string, more, hasPct bool, pct float64) (SteerMix, string, error) {
	ax, ok := canonSteerAxis(axis)
	if !ok {
		return m, "", fmt.Errorf("unknown axis %q", axis)
	}
	cur := m.Resolved()
	before := cur.get(ax)
	delta, how := steerDelta(before, more, hasPct, pct)
	if before+delta > 100 {
		delta = 100 - before
	}
	if before+delta < 0 {
		delta = -before
	}
	next := cur
	next.set(ax, before+delta)
	steerFund(&next, ax, delta)
	next.Set = true
	note := fmt.Sprintf("%s %s → %s (%s)", ax, formatSteerPct(before), formatSteerPct(next.get(ax)), how)
	return next, note, nil
}

func steerDelta(before float64, more, hasPct bool, pct float64) (float64, string) {
	switch {
	case !hasPct && more:
		return steerStep, fmt.Sprintf("+%.0f points", steerStep)
	case !hasPct && !more:
		return -steerStep, fmt.Sprintf("-%.0f points", steerStep)
	case more && before == 0:
		return pct, fmt.Sprintf("was 0, set to %s", formatSteerPct(pct))
	case more:
		return before * pct / 100, fmt.Sprintf("%s more", formatSteerPct(pct))
	default:
		factor := 1 - pct/100
		if factor < 0 {
			factor = 0
		}
		return before*factor - before, fmt.Sprintf("%s less", formatSteerPct(pct))
	}
}

// steerFund moves -delta across the other axes so the mix still sums to 100.
// next[axis] is already the new value.
func steerFund(next *SteerMix, axis string, delta float64) {
	if delta == 0 {
		return
	}
	if delta > 0 {
		pool := 0.0
		var donors []string
		for _, ax := range steerAxes {
			if ax == axis || next.get(ax) <= 0 {
				continue
			}
			donors = append(donors, ax)
			pool += next.get(ax)
		}
		if pool <= 0 {
			next.set(axis, 100)
			for _, ax := range steerAxes {
				if ax != axis {
					next.set(ax, 0)
				}
			}
			return
		}
		take := delta
		if take > pool {
			take = pool
			next.set(axis, 100)
		}
		for _, ax := range donors {
			v := next.get(ax) - take*(next.get(ax)/pool)
			if v < 0.05 {
				v = 0
			}
			next.set(ax, v)
		}
		return
	}
	give := -delta
	pool := 0.0
	for _, ax := range steerAxes {
		if ax != axis {
			pool += next.get(ax)
		}
	}
	if pool <= 0 {
		each := give / float64(len(steerAxes)-1)
		for _, ax := range steerAxes {
			if ax != axis {
				next.set(ax, next.get(ax)+each)
			}
		}
		return
	}
	for _, ax := range steerAxes {
		if ax == axis {
			continue
		}
		next.set(ax, next.get(ax)+give*(next.get(ax)/pool))
	}
}

// Assign sets the named axes. They keep the numbers typed when those sum
// to at most 100; the unnamed axes scale to fill the rest. Over 100, the
// named axes scale down and the rest go to 0.
func (m SteerMix) Assign(named map[string]float64) (SteerMix, string, error) {
	if len(named) == 0 {
		return m, "", fmt.Errorf("name at least one axis")
	}
	cur := m.Resolved()
	var namedSum float64
	var namedAxes []string
	for _, ax := range steerAxes {
		if _, ok := named[ax]; ok {
			namedSum += named[ax]
			namedAxes = append(namedAxes, ax)
		}
	}
	next := SteerMix{Set: true}
	var note string
	if namedSum > 100 {
		scale := 100 / namedSum
		for _, ax := range namedAxes {
			next.set(ax, named[ax]*scale)
		}
		note = fmt.Sprintf("named targets summed to %s, scaled to 100%%", formatSteerPct(namedSum))
	} else {
		for _, ax := range namedAxes {
			next.set(ax, named[ax])
		}
		rest := 100 - namedSum
		pool := 0.0
		var unnamed []string
		for _, ax := range steerAxes {
			if _, ok := named[ax]; ok {
				continue
			}
			unnamed = append(unnamed, ax)
			pool += cur.get(ax)
		}
		if len(unnamed) > 0 {
			if pool <= 0 {
				each := rest / float64(len(unnamed))
				for _, ax := range unnamed {
					next.set(ax, each)
				}
			} else {
				for _, ax := range unnamed {
					next.set(ax, rest*(cur.get(ax)/pool))
				}
			}
		}
		note = "set " + strings.Join(namedAxes, ", ")
	}
	return next, note, nil
}

// FormatSteer is what `/captain more` prints: the targets, whether they
// steer, and (when recent routes exist) how the last window compares.
func FormatSteer(m SteerMix, biasOff bool, recent map[string]float64, n int) string {
	show := m.Resolved()
	var sb strings.Builder
	sb.WriteString("### Routing mix\n\n")
	sb.WriteString("| axis | target |\n|---|---:|\n")
	for _, ax := range steerAxes {
		fmt.Fprintf(&sb, "| %s | %s |\n", ax, formatSteerPct(show.get(ax)))
	}
	sb.WriteString("\n")
	switch {
	case biasOff:
		sb.WriteString("Saved, but `CAPTAIN_STEER=0` is set, so the ranking is not moved.\n")
	case !m.Set:
		sb.WriteString("Nothing is set. These are the defaults, and they do not steer. `/captain more oss` (or `less`, or `oss=40%`) saves a mix and the director starts preferring legs that move recent routes toward it.\n")
	default:
		sb.WriteString("Saved. Unprefixed turns prefer legs that close the gap to this mix. An explicit `/quality`, `/speed`, `/save`, `/frontier`, `/oss` or `/deterministic` on a turn still wins.\n")
	}
	if n > 0 && recent != nil {
		fmt.Fprintf(&sb, "\nRecent (%d routes): ", n)
		var parts []string
		for _, ax := range steerAxes {
			parts = append(parts, fmt.Sprintf("%s %s", ax, formatSteerPct(recent[ax])))
		}
		sb.WriteString(strings.Join(parts, " · "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// PromptNote is the one line the director sees when a mix is steering.
func (m SteerMix) PromptNote() string {
	if !m.Set || os.Getenv("CAPTAIN_STEER") == "0" {
		return ""
	}
	show := m.Resolved()
	var parts []string
	for _, ax := range steerAxes {
		parts = append(parts, ax+" "+formatSteerPct(show.get(ax)))
	}
	return "Standing routing mix (the user set this): " + strings.Join(parts, ", ") + ". Prefer a row that moves recent routes toward that mix when it can do the task. An explicit preference on this turn outranks the mix."
}

func formatSteerPct(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.05 {
		return fmt.Sprintf("%.0f%%", math.Round(v))
	}
	return fmt.Sprintf("%.1f%%", v)
}

// SteerEnabled reports whether a saved mix may move a ranking.
func SteerEnabled() bool { return os.Getenv("CAPTAIN_STEER") != "0" }

// ApplySteer adds a bonus to eligible rows that match axes still short of
// the target, then re-sorts. An unset mix, or CAPTAIN_STEER=0, returns the
// rows unchanged. recent is the last chosen legs (order does not matter).
// With a short window the bonus blends toward the gap from the default, so
// the first `more` moves the ranking before history exists, and fades once
// the window matches the target.
func ApplySteer(rows []Scored, mix SteerMix, recent []Leg) []Scored {
	if !mix.Set || !SteerEnabled() {
		return rows
	}
	target := mix.Resolved()
	base := DefaultSteer()
	observed := steerObserved(recent)
	n := len(recent)
	if n > steerWindow {
		n = steerWindow
	}
	w := float64(n) / float64(steerWarmup)
	if w > 1 {
		w = 1
	}
	deficit := map[string]float64{}
	for _, ax := range steerAxes {
		t := target.get(ax) / 100
		b := base.get(ax) / 100
		deficit[ax] = w*(t-observed[ax]) + (1-w)*(t-b)
	}
	out := append([]Scored(nil), rows...)
	for i := range out {
		if !out[i].Eligible() {
			continue
		}
		bonus := 0.0
		for _, ax := range steerLegAxes(out[i].Leg) {
			bonus += deficit[ax]
		}
		out[i].Value += steerGain * bonus
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Eligible(), out[j].Eligible(); a != b {
			return a
		}
		if out[i].Unreliable != out[j].Unreliable {
			return !out[i].Unreliable
		}
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].CostUSD < out[j].CostUSD
	})
	return out
}

func steerObserved(recent []Leg) map[string]float64 {
	out := map[string]float64{}
	if len(recent) == 0 {
		return out
	}
	window := recent
	if len(window) > steerWindow {
		window = window[len(window)-steerWindow:]
	}
	n := float64(len(window))
	counts := map[string]float64{}
	for _, l := range window {
		for _, ax := range steerLegAxes(l) {
			counts[ax]++
		}
	}
	for ax, c := range counts {
		out[ax] = c / n
	}
	return out
}

// SteerRecentShares is the percent of recent routes that matched each axis,
// for the mix printout.
func SteerRecentShares(recent []Leg) (map[string]float64, int) {
	obs := steerObserved(recent)
	n := len(recent)
	if n > steerWindow {
		n = steerWindow
	}
	pct := map[string]float64{}
	for _, ax := range steerAxes {
		pct[ax] = obs[ax] * 100
	}
	return pct, n
}

// steerLegAxes is which of the six axes a leg counts toward. A leg may
// match several; the mix tracks each axis on its own.
func steerLegAxes(l Leg) []string {
	spec, ok := Spec(l)
	if !ok {
		return nil
	}
	var ax []string
	if IsFrontierClass(l) || l == LegClaude {
		ax = append(ax, "frontier")
	}
	if spec.Prior >= 8 && !IsFrontierClass(l) && l != LegClaude {
		ax = append(ax, "quality")
	}
	if steerCheap(spec, l) {
		ax = append(ax, "cheap")
	}
	if steerFast(spec, l) {
		ax = append(ax, "fast")
	}
	if OpenWeights(l) {
		ax = append(ax, "oss")
	}
	if t, ok := ADIFor(l); ok && t.Green {
		ax = append(ax, "deterministic")
	}
	return ax
}

func steerCheap(spec LegSpec, l Leg) bool {
	if IsFrontierClass(l) || l == LegClaude || spec.Prior >= 8 {
		return false
	}
	if spec.PriceIn == 0 && spec.PriceOut == 0 {
		return true
	}
	return spec.PriceIn > 0 && spec.PriceIn < 0.6
}

func steerFast(spec LegSpec, l Leg) bool {
	m := strings.ToLower(spec.Model)
	if strings.Contains(m, "flash") || strings.Contains(m, "lite") || strings.Contains(m, "lightning") || strings.Contains(m, "luna") || strings.Contains(m, "grok-build") {
		return true
	}
	switch l {
	case LegFree, LegLuna, LegGrok, LegGemini, LegStep, LegDS4Flash:
		return true
	}
	return false
}
