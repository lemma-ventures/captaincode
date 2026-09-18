package captaincode

// The workflow SKILL: compile plain English into a CWL expression.
//
// The user describes what they want ("grok analyses this file, then cursor
// reviews it, then codex and claude red-team it"); the director returns an
// expression, which is shown for confirmation and only then executed. The skill
// text is embedded here - one source of truth, kept in sync with
// docs/WORKFLOW_LANGUAGE.md by TestWorkflowSkillMatchesTheSpec.

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed skills/workflow_language.md
var workflowSkill string

// WorkflowSkill returns the injected compile instructions (exported for the
// drift test and for `captain skills`).
func WorkflowSkill() string { return workflowSkill }

// CompiledWorkflow is a director-compiled workflow, validated by re-parsing.
type CompiledWorkflow struct {
	Expression string   `json:"expression"`
	Rationale  string   `json:"rationale"`
	Warnings   []string `json:"warnings,omitempty"`
	Workflow   Workflow `json:"-"`
}

// compileReply is the director's raw JSON contract (spec §4.2).
type compileReply struct {
	Expression string `json:"expression"`
	Stages     []struct {
		Legs []struct {
			Leg    string `json:"leg"`
			Prompt string `json:"prompt"`
		} `json:"legs"`
		Purpose string `json:"purpose"`
	} `json:"stages"`
	Rationale string   `json:"rationale"`
	Warnings  []string `json:"warnings"`
}

// CompileWorkflow turns intent into a workflow. convo is the recent
// conversation (may be empty), open lists the legs currently available, and
// stats/cooldowns let the director choose where the user was silent.
func (m Manager) CompileWorkflow(intent, convo string, open []Leg, stats map[Leg]LegStats, cooldowns map[Leg]time.Time) (CompiledWorkflow, error) {
	if strings.TrimSpace(intent) == "" {
		return CompiledWorkflow{}, fmt.Errorf("no intent to compile")
	}
	var sb strings.Builder
	sb.WriteString(workflowSkill)
	sb.WriteString("\n\n---\n\n## This request\n\n")
	if c := strings.TrimSpace(convo); c != "" {
		fmt.Fprintf(&sb, "Recent conversation (context - the workers will see it too):\n%s\n\n", truncateStr(c, 6000))
	}
	fmt.Fprintf(&sb, "The user's intent:\n%s\n\n", truncateStr(intent, 4000))
	fmt.Fprintf(&sb, "Legs open right now: %s\n", legList(open))
	sb.WriteString(scorecardBlock(open, stats))
	if benched := benchedList(cooldowns); benched != "" {
		fmt.Fprintf(&sb, "Benched (avoid unless the user named them): %s\n", benched)
	}
	sb.WriteString("\nReply with the STRICT JSON object described above. No prose.\n")

	var reply compileReply
	if err := m.directorJSON(sb.String(), &reply); err != nil {
		return CompiledWorkflow{}, err
	}

	wf, err := ParseWorkflow(strings.TrimSpace(reply.Expression))
	if err != nil {
		return CompiledWorkflow{}, fmt.Errorf("director wrote an invalid workflow (%w): %s", err, truncateStr(reply.Expression, 160))
	}
	// The expression is what the user confirms and what runs, so it must match
	// the stages the director declared - otherwise it would execute something
	// nobody reviewed.
	if err := matchesDeclaredStages(wf, reply); err != nil {
		return CompiledWorkflow{}, err
	}
	return CompiledWorkflow{Expression: wf.String(), Rationale: strings.TrimSpace(reply.Rationale),
		Warnings: reply.Warnings, Workflow: wf}, nil
}

// matchesDeclaredStages checks the expression against the declared structure.
// Prompts are compared loosely (whitespace/case) - legs and topology exactly.
func matchesDeclaredStages(wf Workflow, reply compileReply) error {
	if len(reply.Stages) == 0 {
		return nil // nothing declared: the expression is the whole contract
	}
	if len(wf.Stages) != len(reply.Stages) {
		return fmt.Errorf("director's expression has %d stages but it declared %d", len(wf.Stages), len(reply.Stages))
	}
	for i, st := range wf.Stages {
		dec := reply.Stages[i]
		if len(st.Legs) != len(dec.Legs) {
			return fmt.Errorf("stage %d: expression runs %d legs, declaration says %d", i+1, len(st.Legs), len(dec.Legs))
		}
		for j, l := range st.Legs {
			if !strings.EqualFold(string(l.Leg), strings.TrimPrefix(dec.Legs[j].Leg, "/")) {
				return fmt.Errorf("stage %d leg %d: expression says %s, declaration says %s", i+1, j+1, l.Leg, dec.Legs[j].Leg)
			}
		}
	}
	return nil
}

func legList(legs []Leg) string {
	if len(legs) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(legs))
	for _, l := range legs {
		names = append(names, string(l))
	}
	return strings.Join(names, ", ")
}

func benchedList(cooldowns map[Leg]time.Time) string {
	var out []string
	now := time.Now()
	for l, until := range cooldowns {
		if until.After(now) {
			out = append(out, fmt.Sprintf("%s (%s)", l, until.Sub(now).Round(time.Minute)))
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// scorecardBlock is the evidence the director chooses from where the user was
// silent: quality, runs, reliability, speed.
func scorecardBlock(open []Leg, stats map[Leg]LegStats) string {
	if len(stats) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Live scorecards (quality 0-10 from assessed runs; fails weigh as much as quality):\n")
	for _, l := range open {
		st, ok := stats[l]
		if !ok {
			fmt.Fprintf(&sb, "- %s: no runs yet\n", l)
			continue
		}
		fmt.Fprintf(&sb, "- %s: q=%.1f (%d scored of %d runs), fails=%d, avg %ds\n",
			l, st.AvgQuality, st.Scored, st.N, st.Fails, st.AvgDurationMs/1000)
	}
	return sb.String()
}

// EstimateWorkflow returns a wall-clock range for a workflow: stages are
// barriers, so each contributes its slowest leg, plus the director review.
// Legs with fewer than 3 scored runs widen the range rather than pretending to
// a point estimate.
func EstimateWorkflow(wf Workflow, stats map[Leg]LegStats, reviewMedian time.Duration) (low, high time.Duration) {
	if reviewMedian <= 0 {
		reviewMedian = 25 * time.Second
	}
	for _, st := range wf.Stages {
		var lo, hi time.Duration
		for _, l := range st.Legs {
			leg := l.Leg
			if IsFrontier(leg) {
				leg = LegClaude
			}
			d := 90 * time.Second // no evidence: assume a middling run
			confident := false
			if s, ok := stats[leg]; ok && s.N > 0 && s.AvgDurationMs > 0 {
				d = time.Duration(s.AvgDurationMs) * time.Millisecond
				confident = s.Scored >= 3
			}
			legLo, legHi := d/2, d*2
			if confident {
				legLo, legHi = d*2/3, d*3/2
			}
			if IsFrontierClass(l.Leg) {
				legLo, legHi = legLo*2, legHi*2 // frontier-class gets double the budget
			}
			if legLo > lo {
				lo = legLo
			}
			if legHi > hi {
				hi = legHi
			}
		}
		low += lo
		high += hi
	}
	return low + reviewMedian, high + reviewMedian
}
