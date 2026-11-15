package captaincode

// Role economics (ROADMAP M5.3). Compares the total accepted-task cost and
// time of four routing patterns so the operator can see whether the
// coordination overhead of teams, repair/escalation or frontier workers
// earns its cost:
//
//   - economical solo: one non-frontier worker, no fan-out, no gate repair
//   - frontier solo: one frontier-class worker, no fan-out, no gate repair
//   - repair/escalation: a task whose gate or objective check failed and was
//     retried (gate-repair) or escalated to a stronger leg
//   - teams: a task with fan-out (stage charges or team events)
//
// The classification is from the charge tree, which is the durable record of
// what each task actually spent. A task is accepted only when its outcome
// status is accepted; rejected and regressed outcomes never count as accepted.
// Finished tasks without an accepted or rejected outcome are unreviewed.
// Cost and time come from the call rows the same charge tree holds. The report
// refuses to present a cost-per-accepted figure when no task in a role was
// accepted (undefined, not zero) and marks totals containing unknown-usage
// calls — the same discipline M1.2 enforces.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RoleKind names a routing pattern for the role economics report.
type RoleKind string

const (
	RoleEconomicalSolo   RoleKind = "economical_solo"
	RoleFrontierSolo     RoleKind = "frontier_solo"
	RoleRepairEscalation RoleKind = "repair_escalation"
	RoleTeams            RoleKind = "teams"
)

// RoleSummary is one routing pattern's aggregate economics.
type RoleSummary struct {
	Role         RoleKind `json:"role"`
	Tasks        int      `json:"tasks"`
	Finished     int      `json:"finished"`
	Accepted     int      `json:"accepted"`
	Rejected     int      `json:"rejected"`
	Unreviewed   int      `json:"unreviewed"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	TotalMs      int64    `json:"total_ms"`
	Calls        int      `json:"calls"`
	UnknownUsage int      `json:"unknown_usage"`
}

// CostPerAccepted is total cost divided by accepted tasks, or 0 when none
// are accepted (the caller must check Accepted before presenting this).
func (r RoleSummary) CostPerAccepted() float64 {
	if r.Accepted == 0 {
		return 0
	}
	return r.TotalCostUSD / float64(r.Accepted)
}

// TimePerAccepted is total time divided by accepted tasks, or 0 when none.
func (r RoleSummary) TimePerAccepted() time.Duration {
	if r.Accepted == 0 {
		return 0
	}
	return time.Duration(r.TotalMs/int64(r.Accepted)) * time.Millisecond
}

// AcceptanceRate is accepted divided by total tasks.
func (r RoleSummary) AcceptanceRate() float64 {
	if r.Tasks == 0 {
		return 0
	}
	return float64(r.Accepted) / float64(r.Tasks)
}

// HasUnknownUsage reports whether any call in this role had unknown usage.
func (r RoleSummary) HasUnknownUsage() bool { return r.UnknownUsage > 0 }

// RoleEconomics classifies every task in the ledger by routing pattern and
// aggregates cost, time and acceptance per role. Tasks with no charges (a
// fast-pathed turn that asked no model) are not counted — they have no
// economic signal.
func RoleEconomics(l *Ledger) []RoleSummary {
	if l == nil {
		return nil
	}
	taskIDs := tasksFromCharges(l.Charges)
	chargesByTask := make(map[string][]Charge, len(taskIDs))
	for _, charge := range l.Charges {
		chargesByTask[charge.TaskID] = append(chargesByTask[charge.TaskID], charge)
	}
	states := make(map[string]LifecycleState, len(l.TaskStates))
	for _, ts := range l.TaskStates {
		if _, exists := states[ts.TaskID]; !exists {
			states[ts.TaskID] = ts.State
		}
	}
	outcomes := make(map[string]AcceptanceStatus, len(l.Outcomes))
	for _, outcome := range l.Outcomes {
		if _, exists := outcomes[outcome.TaskID]; !exists {
			outcomes[outcome.TaskID] = outcome.Status
		}
	}
	byRole := map[RoleKind]*RoleSummary{}
	ensure := func(r RoleKind) *RoleSummary {
		s, ok := byRole[r]
		if !ok {
			s = &RoleSummary{Role: r}
			byRole[r] = s
		}
		return s
	}
	for _, taskID := range taskIDs {
		charges := chargesByTask[taskID]
		role := classifyRole(charges)
		s := ensure(role)
		s.Tasks++
		totals := totalCalls(charges, "")
		s.TotalCostUSD += totals.CostUSD
		s.TotalMs += totalDurationMs(charges, taskID)
		s.Calls += totals.Calls
		s.UnknownUsage += totals.Unknown
		finished := states[taskID].IsTerminal()
		if finished {
			s.Finished++
		}
		switch outcomes[taskID] {
		case AcceptanceAccepted:
			s.Accepted++
		case AcceptanceRejected, AcceptanceRegressed:
			s.Rejected++
		default:
			if finished {
				s.Unreviewed++
			}
		}
	}
	out := make([]RoleSummary, 0, len(byRole))
	for _, r := range byRole {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return roleOrder(out[i].Role) < roleOrder(out[j].Role)
	})
	return out
}

func roleOrder(r RoleKind) int {
	switch r {
	case RoleEconomicalSolo:
		return 0
	case RoleFrontierSolo:
		return 1
	case RoleRepairEscalation:
		return 2
	case RoleTeams:
		return 3
	}
	return 99
}

func classifyRole(charges []Charge) RoleKind {
	hasStage := false
	hasRepair := false
	hasEscalation := false
	hasFrontierWorker := false
	hasNonFrontierWorker := false
	for _, c := range charges {
		switch {
		case c.Kind == KindStage:
			hasStage = true
		case c.Label == "gate-repair" || c.Label == "repair":
			hasRepair = true
		case c.Label == "escalation":
			hasEscalation = true
		case c.Label == "worker" && c.Leg != "":
			if IsFrontierClass(c.Leg) || c.Leg == LegClaude {
				hasFrontierWorker = true
			} else {
				hasNonFrontierWorker = true
			}
		}
	}
	if hasStage {
		return RoleTeams
	}
	if hasRepair || hasEscalation {
		return RoleRepairEscalation
	}
	if hasFrontierWorker && !hasNonFrontierWorker {
		return RoleFrontierSolo
	}
	return RoleEconomicalSolo
}

func tasksFromCharges(charges []Charge) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range charges {
		if c.TaskID == "" || c.Kind != KindTask {
			continue
		}
		if !seen[c.TaskID] {
			seen[c.TaskID] = true
			out = append(out, c.TaskID)
		}
	}
	sort.Strings(out)
	return out
}

func totalDurationMs(charges []Charge, taskID string) int64 {
	var total int64
	for _, c := range charges {
		if c.TaskID != taskID || c.Kind != KindCall {
			continue
		}
		total += c.DurationMs
	}
	return total
}

// FormatRoleEconomics renders the role economics comparison as a table for
// `captain roles` or the M1 baseline report. A role with no accepted tasks
// shows "-" for cost/time per accepted, not $0 or 0ms.
func FormatRoleEconomics(summaries []RoleSummary) string {
	if len(summaries) == 0 {
		return "no task data.\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-20s %5s %5s %5s %5s %5s %9s %10s %5s %5s\n",
		"role", "tasks", "done", "accpt", "rejct", "unrev", "$/accpt", "time/accpt", "calls", "unk"))
	for _, r := range summaries {
		costPer := "-"
		if r.Accepted > 0 {
			costPer = fmt.Sprintf("$%.4f", r.CostPerAccepted())
		}
		timePer := "-"
		if r.Accepted > 0 {
			timePer = r.TimePerAccepted().Round(time.Second).String()
		}
		unk := ""
		if r.HasUnknownUsage() {
			unk = " *"
		}
		sb.WriteString(fmt.Sprintf("%-20s %5d %5d %5d %5d %5d %9s %10s %5d %5d%s\n",
			r.Role, r.Tasks, r.Finished, r.Accepted, r.Rejected, r.Unreviewed, costPer, timePer, r.Calls, r.UnknownUsage, unk))
	}
	sb.WriteString("\n  * = total includes calls with unknown usage (not a billed total)\n")
	return sb.String()
}
