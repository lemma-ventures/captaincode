package captaincode

// Report aggregation for the evaluation harness (ROADMAP M1.3/M1.4). The
// metric definitions in the roadmap are unusually specific about denominators
// and about what may not be printed, and this file is where those rules live:
//
//   - cost and time per accepted task divide the WHOLE workload - failures,
//     timeouts, coordination - by the accepted count. Cheap failures are not
//     savings.
//   - with nothing accepted the ratios are undefined, not zero and not
//     infinity. Defined says which.
//   - a total built from calls whose usage was never reported is not a bill.
//     Coverage travels with every figure so no reader has to ask.

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

// ArmReport is one arm's economics over the workload it was given.
type ArmReport struct {
	Arm        string `json:"arm"`
	Alias      string `json:"alias"`
	Executions int    `json:"executions"`
	Accepted   int    `json:"accepted"`
	// ReviewAccepted is the subset of Accepted a person decided rather than
	// the checks. It is reported because "the checks passed" and "a reviewer
	// judged it good enough" are different kinds of evidence.
	ReviewAccepted int     `json:"review_accepted"`
	Rejected       int     `json:"rejected"`
	PendingReview  int     `json:"pending_review"`
	Errors         int     `json:"errors"`
	Timeouts       int     `json:"timeouts"`
	AcceptRate     float64 `json:"accept_rate"`
	// Defined is false when nothing was accepted: the two per-accepted-task
	// ratios below are then meaningless and must not be printed as 0.
	Defined       bool    `json:"defined"`
	CostPerAccept float64 `json:"cost_per_accepted_usd"`
	SecPerAccept  float64 `json:"seconds_per_accepted"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalSeconds  float64 `json:"total_seconds"`
	Coverage      Totals  `json:"coverage"`
}

// Complete reports whether this arm's money is a bill rather than a partial
// sum. It mirrors Totals.Complete so the report and `captain stats` agree.
func (a ArmReport) Complete() bool { return a.Coverage.Complete() }

// FamilyReport is one arm's acceptance within one task family - the
// stratification the roadmap requires before any headline number.
type FamilyReport struct {
	Family     string  `json:"family"`
	Arm        string  `json:"arm"`
	Executions int     `json:"executions"`
	Accepted   int     `json:"accepted"`
	AcceptRate float64 `json:"accept_rate"`
}

// EvalReport is the aggregate view of a result.
type EvalReport struct {
	Suite    string         `json:"suite"`
	Arms     []ArmReport    `json:"arms"`
	Families []FamilyReport `json:"families"`
}

// Report aggregates a finished run. It counts every execution, including the
// ones that failed: an arm whose errors are dropped looks better than it is.
func (r *EvalResult) Report() EvalReport {
	byArm := map[string]*ArmReport{}
	order := []string{}
	byFamily := map[string]*FamilyReport{}
	famOrder := []string{}
	for _, e := range r.Executions {
		a, ok := byArm[e.Arm]
		if !ok {
			a = &ArmReport{Arm: e.Arm, Alias: e.ArmAlias}
			byArm[e.Arm], order = a, append(order, e.Arm)
		}
		a.Executions++
		a.TotalSeconds += float64(e.DurationMs) / 1000
		a.TotalCostUSD += e.Totals.CostUSD
		a.Coverage.Calls += e.Totals.Calls
		a.Coverage.Tokens += e.Totals.Tokens
		a.Coverage.Measured += e.Totals.Measured
		a.Coverage.Estimated += e.Totals.Estimated
		a.Coverage.Unknown += e.Totals.Unknown
		a.Coverage.CostUSD += e.Totals.CostUSD
		switch e.Status {
		case EvalAccepted:
			a.Accepted++
			if e.ReviewedAccept() {
				a.ReviewAccepted++
			}
		case EvalRejected:
			a.Rejected++
		case EvalPendingReview:
			a.PendingReview++
		case EvalTimeout:
			a.Timeouts++
		default:
			a.Errors++
		}

		key := e.Family + "\x00" + e.Arm
		f, ok := byFamily[key]
		if !ok {
			f = &FamilyReport{Family: e.Family, Arm: e.Arm}
			byFamily[key], famOrder = f, append(famOrder, key)
		}
		f.Executions++
		if e.Accepted() {
			f.Accepted++
		}
	}

	out := EvalReport{Suite: r.Suite}
	for _, name := range order {
		a := byArm[name]
		if a.Executions > 0 {
			a.AcceptRate = float64(a.Accepted) / float64(a.Executions)
		}
		if a.Accepted > 0 {
			a.Defined = true
			a.CostPerAccept = a.TotalCostUSD / float64(a.Accepted)
			a.SecPerAccept = a.TotalSeconds / float64(a.Accepted)
		}
		out.Arms = append(out.Arms, *a)
	}
	for _, key := range famOrder {
		f := byFamily[key]
		if f.Executions > 0 {
			f.AcceptRate = float64(f.Accepted) / float64(f.Executions)
		}
		out.Families = append(out.Families, *f)
	}
	sort.SliceStable(out.Families, func(i, j int) bool {
		if out.Families[i].Family != out.Families[j].Family {
			return out.Families[i].Family < out.Families[j].Family
		}
		return out.Families[i].Arm < out.Families[j].Arm
	})
	return out
}

// WriteReport prints the aggregate. Undefined ratios print as "-" and an
// incomplete total is marked, so no line of this table can be quoted as a
// bill it is not.
func WriteReport(w io.Writer, rep EvalReport) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "suite\t%s\n\n", rep.Suite)
	fmt.Fprintln(tw, "arm\truns\tacc\t(reviewed)\trej\tpend\terr\tto\taccept\t$/accepted\ts/accepted\tcoverage")
	for _, a := range rep.Arms {
		cost, secs := "-", "-"
		if a.Defined {
			secs = fmt.Sprintf("%.1f", a.SecPerAccept)
			// No calls recorded at all is not "$0 per accepted task" - it is
			// an arm whose spending this ledger never saw.
			if a.Coverage.Calls > 0 {
				cost = fmt.Sprintf("$%.4f", a.CostPerAccept)
				if !a.Complete() {
					cost += "*"
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.0f%%\t%s\t%s\t%d/%d measured\n",
			a.Arm, a.Executions, a.Accepted, a.ReviewAccepted, a.Rejected, a.PendingReview, a.Errors, a.Timeouts,
			a.AcceptRate*100, cost, secs, a.Coverage.Measured, a.Coverage.Calls)
	}
	if incomplete(rep) {
		fmt.Fprintln(tw, "\n* partial sum: some calls reported no usage; not a billed total")
	}
	fmt.Fprintln(tw, "\nfamily\tarm\truns\taccepted")
	for _, f := range rep.Families {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d (%.0f%%)\n", f.Family, f.Arm, f.Executions, f.Accepted, f.AcceptRate*100)
	}
	_ = tw.Flush()
}

func incomplete(rep EvalReport) bool {
	for _, a := range rep.Arms {
		// The legend explains a "*" on a printed figure. An arm with no
		// recorded calls prints "-", not a marked total, so it must not
		// summon a footnote about a sum nobody was shown.
		if a.Defined && a.Coverage.Calls > 0 && !a.Complete() {
			return true
		}
	}
	return false
}

// PendingReviews lists the keys a blinded reviewer still owes a verdict on,
// identified by alias rather than arm so the reviewer cannot see which worker
// produced the change.
func (r *EvalResult) PendingReviews() []string {
	var out []string
	for _, p := range r.Pending() {
		out = append(out, p.Key())
	}
	return out
}
