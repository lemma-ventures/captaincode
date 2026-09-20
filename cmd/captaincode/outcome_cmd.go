package main

// Task-wide outcome evidence (ROADMAP M5.1). HTTP handler + CLI command.
//
//	captain outcomes                     # list all outcome evidence
//	captain outcomes <task-id>           # show one task's outcome
//	captain outcome <task-id> review <verdict> --reviewer <name> [--note <text>] [--amend]
//	captain outcome <task-id> correction <minutes> [--reason <text>]
//	captain outcome <task-id> regression <reason> [--source <name>]
//	captain outcomes --settle            # settle every outcome its evidence decides
//
// The brain serves GET /v1/outcome (list or single) and POST /v1/outcome
// (review, correction, regression, settle) so the CLI and the TUI can
// record acceptance evidence while the brain is running.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) outcomeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.mu.Lock()
		defer b.mu.Unlock()
		taskID := r.URL.Query().Get("task")
		if taskID != "" {
			o := b.ledger.OutcomeFor(taskID)
			if o == nil {
				writeJSON(w, 404, map[string]any{"error": "no outcome for task " + taskID})
				return
			}
			writeJSON(w, 200, o)
			return
		}
		writeJSON(w, 200, map[string]any{"outcomes": b.ledger.Outcomes})
	case http.MethodPost:
		var req struct {
			TaskID   string `json:"task_id"`
			Action   string `json:"action"`
			Verdict  string `json:"verdict,omitempty"`
			Reviewer string `json:"reviewer,omitempty"`
			Note     string `json:"note,omitempty"`
			Amend    bool   `json:"amend,omitempty"`
			Minutes  int    `json:"minutes,omitempty"`
			Reason   string `json:"reason,omitempty"`
			Source   string `json:"source,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid body: " + err.Error()})
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		// A sweep is task-wide: it settles every pending outcome whose own
		// evidence has decided it (settle.go), so it carries no task id.
		if req.Action == "settle" {
			n := b.ledger.SettleOutcomes(time.Now())
			if err := b.ledger.Save(); err != nil {
				fmt.Fprintf(os.Stderr, "captain brain: save outcome sweep: %v\n", err)
			}
			total, byStatus, _ := b.ledger.OutcomeCoverage()
			writeJSON(w, 200, map[string]any{"settled": n, "total": total, "by_status": byStatus})
			return
		}
		if req.TaskID == "" {
			writeJSON(w, 400, map[string]any{"error": "task_id required"})
			return
		}
		switch req.Action {
		case "review":
			if err := b.ledger.RecordTaskReview(req.TaskID, req.Verdict, req.Reviewer, req.Note, req.Amend); err != nil {
				writeJSON(w, 400, map[string]any{"error": err.Error()})
				return
			}
		case "correction":
			b.ledger.RecordCorrection(req.TaskID, req.Minutes, req.Reason)
		case "regression":
			b.ledger.RecordRegression(req.TaskID, req.Reason, req.Source)
		default:
			writeJSON(w, 400, map[string]any{"error": "unknown action: " + req.Action + " (review|correction|regression|settle)"})
			return
		}
		if err := b.ledger.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "captain brain: save outcome: %v\n", err)
		}
		o := b.ledger.OutcomeFor(req.TaskID)
		writeJSON(w, 200, o)
	default:
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
	}
}

func cmdOutcomes(args []string) {
	c := &http.Client{Timeout: 10 * time.Second}
	if len(args) == 1 && (args[0] == "--settle" || args[0] == "-settle") {
		outcomeSweep(c)
		return
	}
	if len(args) == 0 {
		resp, err := c.Get("http://127.0.0.1:14097/v1/outcome")
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		var out struct {
			Outcomes []captaincode.OutcomeEvidence `json:"outcomes"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		fmt.Print(captaincode.FormatOutcomeSummary(out.Outcomes))
		return
	}
	if len(args) == 1 {
		resp, err := c.Get("http://127.0.0.1:14097/v1/outcome?task=" + args[0])
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&e)
			if e.Error != "" {
				fatal(fmt.Errorf("%s", e.Error))
			}
			fatal(fmt.Errorf("brain returned %d", resp.StatusCode))
		}
		var o captaincode.OutcomeEvidence
		_ = json.NewDecoder(resp.Body).Decode(&o)
		fmt.Print(captaincode.FormatOutcomeEvidence(o))
		return
	}
	fatal(fmt.Errorf("usage: captain outcomes [task-id|--settle]"))
}

// outcomeSweep asks the brain to settle every pending outcome its evidence
// has decided. The brain does this on every turn it records; the command
// exists so a settle window that has just elapsed can be applied now rather
// than at the next turn, and so the counts are visible.
func outcomeSweep(c *http.Client) {
	resp, err := c.Post("http://127.0.0.1:14097/v1/outcome", "application/json",
		strings.NewReader(`{"action":"settle"}`))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	var out struct {
		Settled  int            `json:"settled"`
		Total    int            `json:"total"`
		ByStatus map[string]int `json:"by_status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("settled %d of %d outcome(s)\n", out.Settled, out.Total)
	for _, st := range []string{"accepted", "rejected", "regressed", "pending"} {
		if n := out.ByStatus[st]; n > 0 {
			fmt.Printf("  %-10s %d\n", st, n)
		}
	}
	if out.ByStatus["pending"] > 0 {
		fmt.Printf("pending outcomes carry no check, or passed their checks less than %s ago,\n", captaincode.OutcomeSettleWindow())
		fmt.Println("or cost the user correction minutes - which only a human verdict can call.")
	}
}

func cmdOutcome(args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: captain outcome <task-id> <review|correction|regression> ..."))
	}
	taskID := args[0]
	action := args[1]
	rest := args[2:]
	c := &http.Client{Timeout: 10 * time.Second}

	var body string
	switch action {
	case "review":
		if len(rest) == 0 {
			fatal(fmt.Errorf("usage: captain outcome %s review <accept|reject> --reviewer <name> [--note <text>] [--amend]", taskID))
		}
		verdict := rest[0]
		reviewer := ""
		note := ""
		amend := false
		for i := 1; i < len(rest); i++ {
			switch rest[i] {
			case "--reviewer", "-r":
				if i+1 < len(rest) {
					reviewer = rest[i+1]
					i++
				}
			case "--note", "-n":
				if i+1 < len(rest) {
					note = rest[i+1]
					i++
				}
			case "--amend":
				amend = true
			}
		}
		if reviewer == "" {
			reviewer = os.Getenv("USER")
			if reviewer == "" {
				reviewer = "unknown"
			}
		}
		body = fmt.Sprintf(`{"task_id":%q,"action":"review","verdict":%q,"reviewer":%q,"note":%q,"amend":%t}`,
			taskID, verdict, reviewer, note, amend)
	case "correction":
		if len(rest) == 0 {
			fatal(fmt.Errorf("usage: captain outcome %s correction <minutes> [--reason <text>]", taskID))
		}
		minutes, err := strconv.Atoi(rest[0])
		if err != nil {
			fatal(fmt.Errorf("invalid minutes: %s", rest[0]))
		}
		reason := ""
		for i := 1; i < len(rest); i++ {
			if rest[i] == "--reason" && i+1 < len(rest) {
				reason = rest[i+1]
				i++
			}
		}
		body = fmt.Sprintf(`{"task_id":%q,"action":"correction","minutes":%d,"reason":%q}`,
			taskID, minutes, reason)
	case "regression":
		if len(rest) == 0 {
			fatal(fmt.Errorf("usage: captain outcome %s regression <reason> [--source <name>]", taskID))
		}
		reason := strings.Join(rest, " ")
		source := ""
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--source" && i+1 < len(rest) {
				source = rest[i+1]
				reason = strings.TrimSpace(strings.Join(rest[:i], " "))
				break
			}
		}
		if reason == "" {
			reason = strings.Join(rest, " ")
		}
		body = fmt.Sprintf(`{"task_id":%q,"action":"regression","reason":%q,"source":%q}`,
			taskID, reason, source)
	default:
		fatal(fmt.Errorf("unknown action %q: use review|correction|regression", action))
	}

	resp, err := c.Post("http://127.0.0.1:14097/v1/outcome", "application/json", strings.NewReader(body))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		fatal(fmt.Errorf("brain rejected: %s", e.Error))
	}
	var o captaincode.OutcomeEvidence
	_ = json.NewDecoder(resp.Body).Decode(&o)
	fmt.Print(captaincode.FormatOutcomeEvidence(o))
}
