package captaincode

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Manager is Captain Code's router brain: the director model bootstraps a
// worker brief, picks the worker from live scorecards, and assesses results.
// The director is configurable (Director leg, default grok); it runs via
// claude -p for the Claude leg, otherwise through the local opencode server on
// Port. The deterministic ladder (Pick/StartRung) remains the guardrail: any
// manager failure falls back to it, and objective checks always outrank the
// manager's subjective quality score (MM19: objective anchors dominate).
type Manager struct {
	Director Leg // which model directs; defaults to grok when unset
	Port     int // opencode server port for non-Claude directors
	// Required binds legs the caller insists on (a leg or /frontier named
	// right after /team); they head the BINDING block of the plan prompt.
	Required []Leg
	// Memory is the project's Euclid block for the director (DirectorMemory):
	// what is decided, mapped and learned, so plans and briefs build on it.
	Memory string
	// Hints are per-leg numeric annotations for the menu (estimated $ for
	// this task, window pressure, value rank) computed by the brain.
	Hints map[Leg]string
	// OnCall, when set, is fired once per provider call this manager makes -
	// including directorJSON's corrective retry, which is a second real call.
	// The brain uses it to charge director and review calls to the task that
	// caused them, so the bill is the whole turn and not just the worker
	// (ROADMAP M1.2). CallLabel names what the call was for.
	OnCall    CallHook
	CallLabel string
	// Skills is the shelf that was staged for the worker being assessed
	// (ROADMAP M3.9). When it is non-empty the assessor is asked a second,
	// smaller question beside the quality one: of the procedures the worker
	// was handed, which did the answer actually use, and were they worth the
	// startup context they cost. Empty means no shelf, no question, and a
	// prompt byte-for-byte what it was before skills existed.
	Skills []SkillRef
	// Timeout caps one director call; 0 means directorTimeout. The typed
	// pick (Pick) runs under DirectorPickTimeout - a judge that answers a
	// two-field question is not given a plan's two minutes.
	Timeout time.Duration
	// SteerNote is the standing routing mix, when the user has set one
	// and this turn did not name a preference. Empty leaves the prompt
	// as it was.
	SteerNote string
}

// directorConstraint is prepended to every director prompt. Observed live:
// even with tools disabled server-side, reasoning-tuned models still narrate
// verification/tool-seeking intent in prose ("I'll verify this against the
// repo...") instead of answering - the model doesn't know it has no tools
// unless told. This forecloses that failure mode explicitly.
const directorConstraint = "You have NO TOOLS in this call and cannot browse, search, read files, or verify anything beyond the text given below. Do not describe verification steps, intended tool calls, or plans to check something - you have no way to act on them. Base your answer ONLY on the text provided. Your reply must be the JSON object and NOTHING else: no preamble, no reasoning, no markdown fences.\n\n"

// directorTimeout bounds every director call: routing must never hang the
// CLI. On expiry the opencode session is aborted server-side and the caller
// falls back to the deterministic ladder.
const directorTimeout = 2 * time.Minute

// directorText runs a prompt through the configured director model and returns
// its raw text. Claude uses claude -p (Max subscription); every other director
// goes through the local opencode server as a titled "director" session with
// ALL TOOLS DISABLED - the director is a judge, not an agent; with tools on,
// agentic models wander off exploring the repo and the call never returns.
func (m Manager) directorText(prompt string) (string, error) {
	director := m.Director
	if director == "" {
		director = Director
	}
	res, err := m.directorRun(director, prompt)
	if m.OnCall != nil {
		label := m.CallLabel
		if label == "" {
			label = "director"
		}
		m.OnCall(director, label, res, err)
	}
	return res.Text, err
}

func (m Manager) directorRun(director Leg, prompt string) (Result, error) {
	timeout := directorTimeout
	if m.Timeout > 0 {
		timeout = m.Timeout
	}
	if director == LegClaude {
		// The director plans from the task and the scorecard, never from a
		// repo: it runs in the brain's own cwd, so one project's CLAUDE.md
		// cannot colour the routing of another.
		return runClaudeTimeout("", prompt, timeout)
	}
	port := m.Port
	if port == 0 {
		port = 14096
	}
	d := NewDispatcher(port)
	d.Title = "director"
	d.NoTools = true
	d.AsDirector = true
	d.Timeout = timeout
	return d.Run(director, prompt)
}

// WorkerPick is the director's typed answer over a precomputed menu (the
// first quick win): one leg off the value-ranked field, and its own reading
// of the class. No brief - the task is the brief - and no prose rationale:
// the ranking the menu came from IS the rationale, and a judge asked for a
// sentence spends twenty seconds writing it. Measured on our own traffic
// the plan path took 22.8s median to decide; a typed pick is a few hundred
// tokens of prompt and a dozen of answer.
type WorkerPick struct {
	Leg   Leg   `json:"leg"`
	Class Class `json:"class,omitempty"`
}

// DirectorPickTimeout caps the typed pick. CAPTAIN_DIRECTOR_PICK_TIMEOUT
// (Go duration), default 8s; past it the menu's own first row runs.
func DirectorPickTimeout() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("CAPTAIN_DIRECTOR_PICK_TIMEOUT"))); err == nil && d > 0 {
		return d
	}
	return 8 * time.Second
}

// pickTaskMax is how much of the task the pick prompt carries: enough to
// judge scope, not the whole conversation.
const pickTaskMax = 4000

// BuildPickPrompt renders the typed-choice prompt: the task, the menu with
// each row's numbers, and the one rule that matters - take the first row
// that clears the bar unless the task itself says otherwise. self, when on
// the menu, is the director's own leg, and the prompt says so: for a high
// class task the best leg may be the judge, and refusing itself was how
// the hardest work never reached claude when claude directed.
func BuildPickPrompt(task string, class Class, prefer string, menu []Scored, self Leg, steer ...string) string {
	var sb strings.Builder
	sb.WriteString("You are Captain Code's director. Pick ONE worker for the task below from the menu. The menu is already ranked by value (quality against cost and latency for this task's class and domain); the numbers are live scorecards.\n\nTask:\n")
	sb.WriteString(truncateStr(task, pickTaskMax))
	sb.WriteString("\n\n")
	if c, ok := ParseClass(string(class)); ok {
		fmt.Fprintf(&sb, "Triage rated it %s. ", c)
	}
	switch prefer {
	case "quality":
		sb.WriteString("The user asked for maximum quality (/quality): take the strongest row whatever it costs. ")
	case "speed":
		sb.WriteString("The user asked for speed (/speed): take the fastest row that can do it. ")
	case "save":
		sb.WriteString("The user asked for savings (/save): take the cheapest row that can plausibly do it. ")
	}
	sb.WriteString("\n\nMenu (best value first):\n")
	for i, r := range menu {
		note := ""
		if r.Unreliable {
			note = " UNRELIABLE (failed a third of its runs)"
		}
		if r.Leg == self {
			note += " (this is you - allowed for high-class work when you rank first)"
		}
		fmt.Fprintf(&sb, "%d. %s: value %+.2f, quality %.1f/10 (%d scored runs), est $%.3f, ~%ds, %s%s\n",
			i+1, r.Leg, r.Value, r.Quality, r.ScoredRuns, r.CostUSD, r.LatencyMs/1000, legDescription(r.Leg), note)
	}
	if prefer == "" && len(steer) > 0 && steer[0] != "" {
		sb.WriteString("\n")
		sb.WriteString(steer[0])
		sb.WriteString("\n")
	}
	sb.WriteString("\nRules: take row 1 unless the task needs something a lower row has (a stronger model for architecture, security, concurrency or a hard bug; vision; a specific tool). Judge the task's real difficulty yourself and report it as class. Reply with STRICT JSON only: {\"leg\":\"<a leg from the menu>\",\"class\":\"<trivial|medium|high>\"}")
	return sb.String()
}

// Pick asks the director for a typed choice over menu, within
// DirectorPickTimeout, and validates it against the menu. One call, no
// corrective retry: a judge that cannot answer a two-field question in one
// go is not worth a second timeout, and the menu's first row is a sound
// answer on its own.
func (m Manager) Pick(task string, class Class, prefer string, menu []Scored, self Leg) (WorkerPick, error) {
	if len(menu) == 0 {
		return WorkerPick{}, fmt.Errorf("pick: empty menu")
	}
	if m.Timeout == 0 {
		m.Timeout = DirectorPickTimeout()
	}
	if m.CallLabel == "" {
		m.CallLabel = "pick"
	}
	note := ""
	if prefer == "" {
		note = m.SteerNote
	}
	text, err := m.directorText(directorConstraint + BuildPickPrompt(task, class, prefer, menu, self, note))
	if err != nil {
		return WorkerPick{}, err
	}
	return parsePick(text, menu)
}

// parsePick reads the director's typed answer and validates it against the
// menu it was given.
func parsePick(text string, menu []Scored) (WorkerPick, error) {
	var p WorkerPick
	if err := extractJSON(text, &p); err != nil {
		return WorkerPick{}, fmt.Errorf("pick: no JSON in the director's reply: %.120s", text)
	}
	p.Leg = Leg(strings.ToLower(strings.TrimSpace(string(p.Leg))))
	onMenu := false
	for _, r := range menu {
		if r.Leg == p.Leg {
			onMenu = true
			break
		}
	}
	if !onMenu {
		return WorkerPick{}, fmt.Errorf("pick: director named %q, not on the menu", p.Leg)
	}
	if c, ok := ParseClass(string(p.Class)); ok {
		p.Class = c
	} else {
		p.Class = ""
	}
	return p, nil
}

// Worker is one manager assignment: a leg plus a standalone brief.
type Worker struct {
	Leg   Leg    `json:"leg"`
	Brief string `json:"brief"`
}

type Plan struct {
	Class     Class    `json:"class"`
	Workers   []Worker `json:"workers"`
	Rationale string   `json:"rationale"`
}

type Assessment struct {
	Quality float64 `json:"quality"` // 0-10
	Verdict string  `json:"verdict"` // good | acceptable | poor
	Notes   string  `json:"notes"`
	// Skills grades the shelf the worker held, one line per stocked skill
	// (M3.9). Empty when no skill was staged, or when the director declined
	// to mention one - an unmentioned skill is recorded as stocked-and-unused,
	// never as a bad skill.
	Skills []SkillGrade `json:"skills,omitempty"`
}

// WorkerOutput is one fan-out worker's result, tagged with its leg for the
// assessor's prompt. Keyed by the worker's unique tab title (not Leg) in
// AssessMulti's input/output - the manager may assign the SAME leg to
// multiple parallel workers (e.g. two independent "free" lookups), and a
// Leg-keyed map would silently collide, dropping one worker's result.
type WorkerOutput struct {
	Leg  Leg
	Text string
}

// MultiAssessment scores each parallel worker and synthesizes their outputs
// into one final answer. Scores are keyed by Worker (the tab title passed in
// via WorkerOutput's map key), never by Leg - see WorkerOutput's doc.
type MultiAssessment struct {
	Synthesis string `json:"synthesis"`
	Scores    []struct {
		Worker  string  `json:"worker"`
		Quality float64 `json:"quality"`
		Verdict string  `json:"verdict"`
		Notes   string  `json:"notes"`
	} `json:"scores"`
	// Skills grades the shelf every worker in this stage held (M3.9). It is
	// one list, not one per worker: the stage stages one selection, and a
	// per-worker split would ask the director to attribute a procedure to a
	// tab title it barely saw.
	Skills []SkillGrade `json:"skills,omitempty"`
}

const maxWorkers = 3

func (m Manager) Plan(task string, class Class, prefer string, open []Leg, stats map[Leg]LegStats, teams map[string]TeamStat, allowFanOut bool) (Plan, error) {
	// class is an optional heuristic prior only. Empty means the director owns
	// complexity assessment end-to-end (the normal managed path).
	hint, hasHint := ParseClass(string(class))
	note := ""
	if prefer == "" {
		note = m.SteerNote
	}
	prompt := buildPlanPromptHints(task, string(class), prefer, open, stats, teams, allowFanOut, m.Required, m.Hints, note)
	if m.Memory != "" {
		prompt += "\n" + m.Memory + "\nWhen a brief touches something the memory covers, carry the relevant decision or lesson into the brief verbatim; do not plan a step the FAILURES already record as failing.\n"
	}

	var p Plan
	if err := m.directorJSON(prompt, &p); err != nil {
		return Plan{}, err
	}
	if c, ok := ParseClass(string(p.Class)); ok {
		p.Class = c
	} else if hasHint {
		p.Class = hint
	} else {
		p.Class = ClassMedium
	}
	return validatePlan(p, task, open, allowFanOut)
}

// validatePlan enforces the worker-count rules and repairs off-menu picks.
// The director sometimes names a leg that is not on its menu (live
// 2026-09-09: codex, excluded by CAPTAIN_LEGS, on a "/team /frontier" turn);
// rejecting the whole plan for that threw away an otherwise sound plan and
// ended the turn with no answer. Substitute the strongest open leg not
// already in the plan and say so in the rationale - no substitute at all is
// still an error, never a silent drop.
func validatePlan(p Plan, task string, open []Leg, allowFanOut bool) (Plan, error) {
	if len(p.Workers) == 0 || len(p.Workers) > maxWorkers || (!allowFanOut && len(p.Workers) > 1) {
		return Plan{}, fmt.Errorf("manager returned %d workers", len(p.Workers))
	}
	taken := map[Leg]bool{}
	for _, w := range p.Workers {
		if legIn(w.Leg, open) {
			taken[w.Leg] = true
		}
	}
	for i, w := range p.Workers {
		if !legIn(w.Leg, open) {
			sub, ok := Leg(""), false
			for _, l := range open {
				if !taken[l] {
					sub, ok = l, true
					break
				}
			}
			if !ok {
				return Plan{}, fmt.Errorf("manager picked unavailable leg %q and no open leg is left to substitute", w.Leg)
			}
			taken[sub] = true
			p.Workers[i].Leg = sub
			note := fmt.Sprintf("director picked %s (not available) → %s", w.Leg, sub)
			if strings.TrimSpace(p.Rationale) == "" {
				p.Rationale = note
			} else {
				p.Rationale += " · " + note
			}
		}
		if strings.TrimSpace(w.Brief) == "" {
			p.Workers[i].Brief = task
		}
	}
	return p, nil
}

// classBreakdown renders a compact per-class quality suffix, e.g.
// "[trivial 9.0×2 | medium 8.5×3]" - the dimension flat averages hide.
func classBreakdown(by map[Class]ClassStat) string {
	if len(by) == 0 {
		return ""
	}
	parts := []string{}
	for _, c := range []Class{ClassTrivial, ClassMedium, ClassHigh} {
		if b, ok := by[c]; ok && b.Scored > 0 {
			parts = append(parts, fmt.Sprintf("%s %.1f×%d", c, b.AvgQuality, b.Scored))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " | ") + "]"
}

// buildPlanPrompt renders the director's routing menu (factored out of Plan
// so its content is testable without a live director call).
//
// required are legs the CALLER binds (a leg or /frontier named right after
// /team, 2026-09-09) - merged ahead of the ones the prose names.
func buildPlanPrompt(task, class, prefer string, open []Leg, stats map[Leg]LegStats, teams map[string]TeamStat, allowFanOut bool, required ...Leg) string {
	return buildPlanPromptHints(task, class, prefer, open, stats, teams, allowFanOut, required, nil, "")
}

// buildPlanPromptHints is buildPlanPrompt with per-leg numeric menu hints.
func buildPlanPromptHints(task, class, prefer string, open []Leg, stats map[Leg]LegStats, teams map[string]TeamStat, allowFanOut bool, required []Leg, hints map[Leg]string, steer string) string {
	hint, hasHint := ParseClass(class)
	var sb strings.Builder
	fmt.Fprintf(&sb, `You are Captain Code's router manager (director). First assess task complexity, then assign worker model(s) and write their brief(s).

Task:
%s

Preference hint: %s.
`, task, dashOrEmpty(prefer))
	switch prefer {
	case "quality":
		sb.WriteString("The user EXPLICITLY requested maximum quality for this task (/quality): assign the strongest available worker (or strongest team) regardless of cost. The cost-optimization rules below do NOT apply to this task.\n")
	case "speed":
		sb.WriteString("The user explicitly requested speed (/speed): assign the fastest leg that can do the job; avoid slow frontier legs unless nothing else can.\n")
	case "save":
		sb.WriteString("The user explicitly requested savings (/save): assign the cheapest leg that can plausibly do the job, accepting quality risk.\n")
	}
	if steer != "" && prefer == "" {
		sb.WriteString(steer)
		sb.WriteString("\n")
	}
	if hasHint {
		fmt.Fprintf(&sb, "Heuristic prior (optional, non-binding): class=%s - you may override.\n", hint)
	}
	// The user named the workers: that is BINDING. Substituting them - even for
	// a better-scoring leg, even to respect "never assign yourself" - answers a
	// question nobody asked (live 2026-07-30: "have claude, grok and codex
	// review this" was planned as cursor+grok+glm).
	named := append([]Leg{}, required...)
	for _, l := range NamedAssignees(task) {
		if !legIn(l, named) {
			named = append(named, l)
		}
	}
	if len(named) > 0 {
		fmt.Fprintf(&sb, `
BINDING: the user named the workers for this task: %s.
Assign exactly these legs, one worker each, in this order - including YOURSELF if you are named (an explicit user request overrides "never assign yourself"; you execute as a worker like any other leg). Do NOT substitute a named leg for a higher-scoring one, and do NOT drop one because it is benched or scores poorly: say so in "rationale" and assign it anyway. Only if a named leg is missing from the worker list below may you replace it - name the substitution and the reason in "rationale".
NOTE: every worker you assign runs in PARALLEL in a single stage - this plan cannot sequence them. Never claim in "rationale" that workers are ordered, staged, or pipelined; if the user asked for an order, say in "rationale" that the order could not be honored.
`, legList(named))
	}
	sb.WriteString(`
Available workers (live scorecards; quality 0-10; cost = estimated $ for THIS task for API legs, or the subscription window for sub legs; "value rank" orders legs by quality−cost−latency for this task's class/domain - prefer the cheapest that clears the bar; bracketed = per-class quality×runs):
`)
	for _, leg := range open {
		s := stats[leg]
		rel := ""
		if s.Fails > 0 {
			rel = fmt.Sprintf(" FAILED_RUNS=%d (stalls/outages - weigh reliability, not just quality)", s.Fails)
		}
		note := ""
		if n := legNotes[leg]; n != "" {
			note = " · note: " + n
		}
		if h := hints[leg]; h != "" {
			note += " · " + h
		}
		fmt.Fprintf(&sb, "- %s: quality=%.1f/10 (benchmark prior %.1f, local avg %.1f over %d scored runs) avg_ms=%d avg_tokens=%d%s%s - %s%s\n",
			leg, BlendedQuality(leg, s), QualityPrior(leg), s.AvgQuality, s.Scored, s.AvgDurationMs, s.AvgTokens, rel, classBreakdown(s.ByClass), legDescription(leg), note)
	}
	if allowFanOut && len(teams) > 0 {
		sb.WriteString(`
Ensembles already tried (fan-out teams; an aggregate of models can beat any solo pick on the right class of task):
`)
		keys := make([]string, 0, len(teams))
		for k := range teams {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := teams[k]
			fmt.Fprintf(&sb, "- %s: quality=%.1f over %d runs%s\n", k, t.AvgQuality, t.N, classBreakdown(t.ByClass))
		}
		sb.WriteString("- Prefer re-using a team composition that has scored well on THIS task's class; avoid ones that scored poorly.\n")
	}
	sb.WriteString(`
Rules:
- YOU assess complexity: set "class" to exactly one of trivial|medium|high based on THIS task (not the heuristic prior).
  - trivial: typo/rename/lint/docs/one-liner
  - medium: ordinary implementation or focused bugfix
  - high: architecture, concurrency/race, security, multi-file redesign, hard debugging
- You are the director. You plan, direct, and assess. Trivial and medium work never goes to you; for HIGH class work you may assign yourself when you appear in the worker list above and rank strongest for it - the hardest task should reach the best leg, and that is sometimes the judge. Every worker you name must be on the list.
- Optimize jointly for quality needed by THIS task, speed, and cost: prefer cheaper legs when they clear the quality bar, escalate to a stronger worker only when the task needs it.
- Be fair: judge legs by their scorecards and task fit, not by family loyalty.
- Each "brief" must be complete standalone instructions for that worker: it sees neither this planning call nor the other briefs. In the TUI wrapper it DOES see the user's conversation, so carry the user's OWN wording of every requirement through verbatim - never paraphrase away one that is anchored in the conversation ("in my writing style", "the file we discussed", "fix that bug"); a generic restatement makes the worker answer a question nobody asked.
`)
	if allowFanOut {
		fmt.Fprintf(&sb, "- Usually assign ONE worker. Split into up to %d parallel workers ONLY when the task decomposes into independent subtasks (different files/subsystems/questions); their outputs will be synthesized afterwards.\n", maxWorkers)
	} else {
		sb.WriteString("- Assign exactly ONE worker.\n")
	}
	sb.WriteString(`Reply with STRICT JSON only, no prose: {"class":"<trivial|medium|high>","workers":[{"leg":"<listed leg>","brief":"..."}],"rationale":"<=140 chars"}`)

	return sb.String()
}

func dashOrEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// AssessMulti scores every parallel worker and synthesizes the final answer
// in a single manager call (one Max-quota call regardless of fan-out width).
// outputs is keyed by each worker's unique tab title (see WorkerOutput) so
// two workers on the same leg never collide.
// ReviewWorkflow is AssessMulti with room for real deliverables: a workflow's
// terminal outputs ARE the answer (the user sees nothing else), so truncating
// them to the assessment limit would silently discard the work being reviewed.
func (m Manager) ReviewWorkflow(task string, outputs map[string]WorkerOutput, objective string) (MultiAssessment, error) {
	return m.assessMulti(task, outputs, objective, 24000)
}

func (m Manager) AssessMulti(task string, outputs map[string]WorkerOutput, objective string) (MultiAssessment, error) {
	return m.assessMulti(task, outputs, objective, 4000)
}

func (m Manager) assessMulti(task string, outputs map[string]WorkerOutput, objective string, textLimit int) (MultiAssessment, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, `You are Captain Code's assessor. Parallel workers each handled part of this task. Score each fairly (judge on the rubric only, regardless of which model produced it) and synthesize one final answer.

Task:
%s

`, truncateStr(task, 2000))
	for title, o := range outputs {
		fmt.Fprintf(&sb, "--- worker %q (leg=%s) ---\n%s\n\n", title, o.Leg, truncateStr(o.Text, textLimit))
	}
	fmt.Fprintf(&sb, `Objective check: %s (objective evidence outranks impression).

Rubric per worker: correctness first, then completeness, then clarity. 8-10 fully solves its brief; 5-7 usable with gaps; 0-4 wrong or off-task. If a worker's brief covered only PART of the overall task, judge it only on its own brief - do not penalize it for another worker's scope.
Combine workers' answers only where they cover DIFFERENT parts of the task. Where two workers answer the SAME question differently (a different fix, plan, recommendation or conclusion), do not blend them into a compromise: pick one, deliver it, and say in one line which worker it came from and why. You make that call; the user should not have to.
The synthesis is delivered to the user verbatim - format it for reading: markdown with \n newlines inside the JSON string, short paragraphs, bullet lists for enumerations, ### headers per worker/section when long. Never one large run-on paragraph.
Reply with STRICT JSON only, using the exact worker id shown above (the quoted string after "worker"): {"synthesis":"<combined final answer for the user, covering every worker's part, markdown-formatted with \n newlines>","scores":[{"worker":"<id>","quality":<0-10>,"verdict":"good|acceptable|poor","notes":"<=100 chars"}]%s}`, objective, skillJSONField(m.Skills))

	var ma MultiAssessment
	if err := m.directorJSON(skillBlock(m.Skills)+sb.String(), &ma); err != nil {
		return MultiAssessment{}, err
	}
	ma.Skills = keepStockedGrades(ma.Skills, m.Skills)
	return ma, nil
}

// Contender is one worker whose file changes overlap another's: what it
// said, which files it changed, and the objective evidence its worktree
// produced.
type Contender struct {
	Leg      Leg
	Text     string
	Files    []string
	Evidence string // "gate passed", "tests failed", … empty when nothing ran
}

// Ruling is the director's call on a conflict: whose changes land.
type Ruling struct {
	Winner string `json:"winner"`
	Reason string `json:"reason"`
}

// Arbitrate asks the director which one of several conflicting workers'
// changes should land. Workers that edited the same files are not merged -
// two plausible patches spliced together are often neither - so the director
// picks exactly one, and the caller applies that worker's changes whole. A
// reply naming a worker that is not a contender is an error, never a guess.
func (m Manager) Arbitrate(task string, contenders map[string]Contender) (Ruling, error) {
	if len(contenders) < 2 {
		return Ruling{}, fmt.Errorf("arbitrate: need two or more contenders, got %d", len(contenders))
	}
	var r Ruling
	if err := m.directorJSON(arbitrationPrompt(task, contenders), &r); err != nil {
		return Ruling{}, err
	}
	return checkRuling(r, contenders)
}

// landParallelSkill is the published land-parallel-agent-work skill
// (skills/ at the repository root), copied here so it can be embedded;
// TestLandParallelSkillMatchesThePublishedOne keeps the two identical. The
// director cannot open a skill - it is one JSON call with no working
// directory - so the step it performs is handed to it as prompt text.
//
//go:embed skills/land_parallel_agent_work.md
var landParallelSkill string

// arbitrationStep is the one step of that skill the director performs:
// section 5, picking one whole winner per conflict group.
var arbitrationStep = skillSection(landParallelSkill, "## 5.")

// skillSection returns a markdown section from its heading up to the next
// "## " heading.
func skillSection(md, heading string) string {
	i := strings.Index(md, "\n"+heading)
	if i < 0 {
		return ""
	}
	body := md[i+1:]
	if j := strings.Index(body[len(heading):], "\n## "); j >= 0 {
		body = body[:len(heading)+j]
	}
	return strings.TrimSpace(body)
}

// arbitrationPrompt lays the contenders out in a stable order.
func arbitrationPrompt(task string, contenders map[string]Contender) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `You are Captain Code's director. Parallel workers changed the SAME files for this task, and only one worker's changes can land: their edits are not merged. Decide whose changes land. You are the decider in this step of the land-parallel-agent-work procedure:

%s

Task:
%s

`, arbitrationStep, truncateStr(task, 2000))
	for _, id := range contenderIDs(contenders) {
		c := contenders[id]
		fmt.Fprintf(&sb, "--- worker %q (leg=%s) ---\nchanged: %s\n", id, c.Leg, truncateStr(strings.Join(c.Files, ", "), 600))
		if c.Evidence != "" {
			fmt.Fprintf(&sb, "evidence: %s\n", c.Evidence)
		}
		fmt.Fprintf(&sb, "report:\n%s\n\n", truncateStr(c.Text, 3000))
	}
	sb.WriteString(`Reply with STRICT JSON only, using the exact worker id shown above: {"winner":"<id>","reason":"<one line, <=140 chars>"}`)
	return sb.String()
}

// checkRuling accepts a ruling only when it names one of the contenders.
func checkRuling(r Ruling, contenders map[string]Contender) (Ruling, error) {
	r.Winner = strings.TrimSpace(r.Winner)
	if _, ok := contenders[r.Winner]; !ok {
		return Ruling{}, fmt.Errorf("arbitrate: director named %q, not one of %s", r.Winner, strings.Join(contenderIDs(contenders), ", "))
	}
	r.Reason = truncateStr(oneLine(r.Reason), 200)
	return r, nil
}

func contenderIDs(contenders map[string]Contender) []string {
	ids := make([]string, 0, len(contenders))
	for id := range contenders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ── grading the shelf (ROADMAP M3.9) ────────────────────────────────────────
//
// Captain stages procedures it believes fit the task, and each one costs
// every worker its name and description in startup context. The director is
// already reading the output closely enough to grade it; asking it, in the
// same call, whether those procedures show up in the work is the cheapest
// honest measurement available - no extra call, no extra quota.
//
// The question is deliberately narrow. The director is not asked whether a
// skill is good in general, only whether THIS answer shows it being used and
// whether it earned its place on THIS task. A skill it cannot see being used
// is graded as unused, which is a fact about selection; inventing a quality
// score for it would be a fact about nothing.

// skillBlock is the shelf, shown to the assessor before the task.
func skillBlock(refs []SkillRef) string {
	if len(refs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Captain staged these Agent Skills in the worker's working directory before it ran. The worker's runtime decides on its own whether to open one; nothing forced it to.\n")
	for _, r := range refs {
		fmt.Fprintf(&sb, "- %s: %s\n", r.Name, truncateStr(oneLine(r.Description), 200))
	}
	sb.WriteString(`For each skill above, also judge: does the result show that procedure actually being followed ("used"), and was having it on hand worth the context it cost on THIS task ("usefulness", 0-10)? Grade usefulness ONLY for a skill you can see was used; for one you cannot, set "used":false and "usefulness":0. Do not reward a skill for work the worker would plainly have done without it.

`)
	return sb.String()
}

// skillJSONField is the extra field the reply must carry when a shelf was
// staged. It is spliced into the existing JSON shape rather than replacing
// it, so a director that ignores the skills question still returns a
// parseable assessment.
func skillJSONField(refs []SkillRef) string {
	if len(refs) == 0 {
		return ""
	}
	return `,"skills":[{"skill":"<name from the list>","used":<true|false>,"usefulness":<0-10>,"note":"<=100 chars"}]`
}

// keepStockedGrades drops grades for skills that were never on the shelf. A
// director that invents a skill name is grading something captain did not
// stage, and a usage record built from it would attribute work to a
// procedure nobody had.
func keepStockedGrades(grades []SkillGrade, refs []SkillRef) []SkillGrade {
	if len(grades) == 0 || len(refs) == 0 {
		return nil
	}
	known := map[string]string{}
	for _, r := range refs {
		known[strings.ToLower(r.Name)] = r.Name
	}
	seen := map[string]bool{}
	out := make([]SkillGrade, 0, len(grades))
	for _, g := range grades {
		name, ok := known[strings.ToLower(strings.TrimSpace(g.Skill))]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		g.Skill = name
		if g.Usefulness < 0 {
			g.Usefulness = 0
		}
		if g.Usefulness > 10 {
			g.Usefulness = 10
		}
		out = append(out, g)
	}
	return out
}

func legIn(l Leg, open []Leg) bool {
	for _, o := range open {
		if o == l {
			return true
		}
	}
	return false
}

func (m Manager) Assess(task, output, objective string) (Assessment, error) {
	prompt := fmt.Sprintf(`You are Captain Code's assessor, scoring a worker model's result. Judge only on the rubric, fairly and strictly, regardless of which model produced it.

Task:
%s

Worker result:
%s

Objective check: %s (objective evidence outranks your impression - a passing check caps how low you score, a failing one caps how high).

Rubric: correctness first, then completeness, then clarity. 8-10 fully solves it; 5-7 usable with gaps; 0-4 wrong or off-task.
Reply with STRICT JSON only: {"quality":<0-10>,"verdict":"good|acceptable|poor","notes":"<=140 chars"%s}`,
		truncateStr(task, 2000), truncateStr(output, 6000), objective, skillJSONField(m.Skills))

	var a Assessment
	if err := m.directorJSON(skillBlock(m.Skills)+prompt, &a); err != nil {
		return Assessment{}, err
	}
	a.Skills = keepStockedGrades(a.Skills, m.Skills)
	if a.Quality < 0 || a.Quality > 10 {
		return Assessment{}, fmt.Errorf("manager returned out-of-range quality %v", a.Quality)
	}
	return a, nil
}

// directorJSON runs a prompt through the configured director model and parses
// the first JSON object in the reply.
// directorJSON runs a director prompt and parses its JSON verdict, with one
// corrective retry: if the model narrates instead of answering (observed
// live even with tools disabled and directorConstraint in place), it's told
// exactly what it did wrong and asked again once before giving up.
func (m Manager) directorJSON(prompt string, v any) error {
	full := directorConstraint + prompt
	text, err := m.directorText(full)
	if err != nil {
		return err
	}
	if err := extractJSON(text, v); err == nil {
		return nil
	}
	retry := full + "\n\nYour previous reply did not contain valid JSON - it was:\n" + truncateStr(text, 300) +
		"\n\nThat is not acceptable. Reply again with ONLY the JSON object, no other text."
	text2, err := m.directorText(retry)
	if err != nil {
		return err
	}
	if err := extractJSON(text2, v); err != nil {
		return fmt.Errorf("no valid JSON in director reply after retry (%w): %.160s", err, text2)
	}
	return nil
}

// extractJSON finds the director's verdict inside a reply that may also
// contain echoed worker output (e.g. Go structs, code blocks) with their own
// braces. First-'{'-to-last-'}' breaks in that case, so instead this finds
// every balanced-brace span and tries the LAST one that actually unmarshals
// - the verdict is the model's own final answer, not quoted code.
func extractJSON(text string, v any) error {
	var spans [][2]int
	depth, start := 0, -1
	for i, c := range text {
		switch {
		case c == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case c == '}' && depth > 0:
			depth--
			if depth == 0 && start >= 0 {
				spans = append(spans, [2]int{start, i + 1})
			}
		}
	}
	if len(spans) == 0 {
		return errors.New("no balanced {...} span found")
	}
	var lastErr error
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		if err := json.Unmarshal([]byte(text[s[0]:s[1]]), v); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// legDescription is the director's briefing on each worker, grounded in
// July-2026 benchmarks (see priors.go for sources) - it must be honest about
// weaknesses so the director doesn't over-assign on brand reputation alone.
func legDescription(l Leg) string {
	if IsFrontier(l) {
		return "claude at MAXIMUM effort (/frontier: strongest release, xhigh) - the strongest single worker available; slow (minutes) and Max-sub quota; assign when the user asked for it or the task is the hardest kind"
	}
	if s, ok := specs[l]; ok {
		return s.Note
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return CutHead(s, n) + "…"
}

// RouteNote asks the director which of a turn's workers a mid-run note
// concerns, from their briefs: the legs to deliver it to (empty = all) and
// one line of why. A /btw during a team, parallel or workflow turn reaches
// the worker whose assignment it amends, not every worker (2026-09-18).
func (m Manager) RouteNote(note string, briefs map[Leg]string) ([]Leg, string, error) {
	if len(briefs) == 0 {
		return nil, "", errors.New("no briefs to route by")
	}
	var sb strings.Builder
	sb.WriteString("The user sent a note while several workers were running on their assignments. Decide which worker(s) the note concerns.\n\nWorkers and their assignments:\n")
	legs := make([]Leg, 0, len(briefs))
	for l := range briefs {
		legs = append(legs, l)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i] < legs[j] })
	for _, l := range legs {
		fmt.Fprintf(&sb, "- %s: %s\n", l, truncateStr(strings.Join(strings.Fields(briefs[l]), " "), 400))
	}
	fmt.Fprintf(&sb, "\nThe note:\n%s\n\nReply with STRICT JSON only: {\"legs\":[\"<leg>\",...],\"why\":\"<=100 chars\"} - name every worker whose assignment the note changes; ALL of them when it applies to the whole task.", truncateStr(note, 1200))
	var out struct {
		Legs []string `json:"legs"`
		Why  string   `json:"why"`
	}
	if err := m.directorJSON(sb.String(), &out); err != nil {
		return nil, "", err
	}
	var chosen []Leg
	for _, raw := range out.Legs {
		l := Leg(strings.ToLower(strings.TrimSpace(raw)))
		if _, ok := briefs[l]; ok && !legIn(l, chosen) {
			chosen = append(chosen, l)
		}
	}
	return chosen, strings.TrimSpace(out.Why), nil
}
