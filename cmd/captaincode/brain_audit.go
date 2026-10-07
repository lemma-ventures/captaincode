package main

// The audit: a routine pass over what workers did, which penalizes misconduct
// the way a reviewer would. On 2026-10-08 a worker's unrequested public
// release, cut from a commit that failed CI, was found by hand; this finds
// it on its own. Every CAPTAIN_AUDIT_EVERY (30m; 0 turns it off) and on
// `captain audit`, it reads:
//
//   - the worker guard's refusals (~/.captaincode/conduct.jsonl): a worker
//     tried to publish without a request, or to bulk-stage other sessions'
//     work;
//   - the GitHub releases of each folder workers delivered in: one created in
//     the last minutes of a run whose request did not ask to publish;
//   - the commits a worker made during its run: one whose CI failed.
//
// The penalty is the reviewer's: the run is reviewed "reject" by "audit", or
// marked regressed when the user had accepted it, so the ranking counts it
// against the leg. Each finding is penalized once (~/.captaincode/audit.jsonl)
// and named on the folder's activity feed.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type auditFinding struct {
	At     time.Time `json:"at"`
	TaskID string    `json:"task_id"`
	Leg    string    `json:"leg,omitempty"`
	Dir    string    `json:"dir,omitempty"`
	Rule   string    `json:"rule"`
	Detail string    `json:"detail"`
}

func (f auditFinding) key() string { return f.TaskID + "|" + f.Rule + "|" + f.Detail }

const (
	auditAttribution = 3 * time.Hour    // a refusal belongs to the run that delivered next in its folder, within this
	auditReleaseTail = 10 * time.Minute // a release in a run's last minutes is that run's
	auditCommitSpan  = 2 * time.Hour    // the longest a run's commits are looked for before its delivery
)

func auditEvery() time.Duration {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_AUDIT_EVERY"))
	if v == "0" || v == "off" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return 30 * time.Minute
}

func auditLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "audit.jsonl")
}

// auditLoop runs the audit until done; each pass looks back two intervals,
// so a CI run still going on one pass is judged on the next.
func (b *brain) auditLoop(done <-chan struct{}) {
	every := auditEvery()
	if every == 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if fs := b.audit(time.Now().Add(-2 * every)); len(fs) > 0 {
				fmt.Printf("captain brain: audit - %d new finding(s)\n", len(fs))
			}
		}
	}
}

// audit finds misconduct since since, penalizes each new finding once and
// returns them.
func (b *brain) audit(since time.Time) []auditFinding {
	b.mu.Lock()
	outs := append([]captaincode.OutcomeEvidence(nil), b.ledger.Outcomes...)
	b.mu.Unlock()
	run := b.auditRunFn
	if run == nil {
		run = auditRun
	}
	var found []auditFinding
	found = append(found, auditConduct(outs, captaincode.ReadConduct(since))...)
	found = append(found, auditPublished(outs, since, run)...)
	seen := readAuditKeys()
	var fresh []auditFinding
	for _, f := range found {
		if f.TaskID == "" || seen[f.key()] {
			continue
		}
		seen[f.key()] = true
		fresh = append(fresh, f)
		b.penalize(f)
	}
	return fresh
}

// auditConduct: each guard refusal is its run's finding.
func auditConduct(outs []captaincode.OutcomeEvidence, events []captaincode.ConductEvent) []auditFinding {
	var out []auditFinding
	for _, e := range events {
		o := attributeRun(outs, e.TaskID, e.Dir, e.At)
		if o == nil {
			continue
		}
		rule := "tried to bulk-stage other sessions' work"
		if strings.Contains(e.Rule, "did not ask for a release") {
			rule = "tried to publish without a request"
		}
		out = append(out, auditFinding{At: e.At, TaskID: o.TaskID, Leg: string(o.Leg), Dir: o.Dir, Rule: rule, Detail: truncate(e.Command, 160)})
	}
	return out
}

// auditPublished looks at GitHub for what runs delivered since since left
// behind: an unrequested release, and commits whose CI failed.
func auditPublished(outs []captaincode.OutcomeEvidence, since time.Time, run func(dir string, args ...string) (string, error)) []auditFinding {
	var out []auditFinding
	repos := map[string]string{}
	releases := map[string][]struct {
		TagName   string    `json:"tagName"`
		CreatedAt time.Time `json:"createdAt"`
	}{}
	for i := range outs {
		o := &outs[i]
		end := o.DeliveredAt
		if end.IsZero() || end.Before(since) || o.Dir == "" {
			continue
		}
		repo, ok := repos[o.Dir]
		if !ok {
			s, _ := run(o.Dir, "gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
			repo = strings.TrimSpace(s)
			repos[o.Dir] = repo
		}
		if repo == "" {
			continue
		}
		rels, ok := releases[repo]
		if !ok {
			s, _ := run(o.Dir, "gh", "release", "list", "-R", repo, "--limit", "20", "--json", "tagName,createdAt")
			_ = json.Unmarshal([]byte(s), &rels)
			releases[repo] = rels
		}
		if !captaincode.AsksToPublish(o.Task) {
			for _, r := range rels {
				if !r.CreatedAt.After(end) && end.Sub(r.CreatedAt) <= auditReleaseTail {
					out = append(out, auditFinding{At: r.CreatedAt, TaskID: o.TaskID, Leg: string(o.Leg), Dir: o.Dir,
						Rule: "published a release without a request", Detail: repo + " " + r.TagName})
				}
			}
		}
		for _, sha := range runCommits(outs, o, run) {
			s, _ := run(o.Dir, "gh", "run", "list", "-R", repo, "--commit", sha, "--json", "status,conclusion")
			var runs []struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			}
			_ = json.Unmarshal([]byte(s), &runs)
			for _, r := range runs {
				if r.Status == "completed" && r.Conclusion == "failure" {
					out = append(out, auditFinding{At: end, TaskID: o.TaskID, Leg: string(o.Leg), Dir: o.Dir,
						Rule: "pushed a commit that failed CI", Detail: repo + " " + sha[:minInt(7, len(sha))]})
					break
				}
			}
		}
	}
	return out
}

// runCommits are the commits made in o's folder while it ran: its recorded
// commit, and those committed since the previous delivery in that folder.
func runCommits(outs []captaincode.OutcomeEvidence, o *captaincode.OutcomeEvidence, run func(dir string, args ...string) (string, error)) []string {
	start := o.DeliveredAt.Add(-auditCommitSpan)
	for _, p := range outs {
		if p.TaskID != o.TaskID && p.Dir == o.Dir && p.DeliveredAt.Before(o.DeliveredAt) && p.DeliveredAt.After(start) {
			start = p.DeliveredAt
		}
	}
	shas := map[string]bool{}
	var out []string
	if o.Commit != nil && o.Commit.SHA != "" {
		shas[o.Commit.SHA] = true
		out = append(out, o.Commit.SHA)
	}
	s, _ := run(o.Dir, "git", "log", "--all", "--format=%H", "--since="+start.Format(time.RFC3339), "--until="+o.DeliveredAt.Add(time.Minute).Format(time.RFC3339), "-n", "10")
	for _, sha := range strings.Fields(s) {
		if !shas[sha] {
			shas[sha] = true
			out = append(out, sha)
		}
	}
	return out
}

// attributeRun is the run a piece of evidence belongs to: the named task,
// else the run that delivered next in that folder.
func attributeRun(outs []captaincode.OutcomeEvidence, taskID, dir string, at time.Time) *captaincode.OutcomeEvidence {
	var best *captaincode.OutcomeEvidence
	for i := range outs {
		o := &outs[i]
		if taskID != "" {
			if o.TaskID == taskID {
				return o
			}
			continue
		}
		if dir == "" || o.Dir == "" || o.DeliveredAt.Before(at) || o.DeliveredAt.Sub(at) > auditAttribution {
			continue
		}
		if !(o.Dir == dir || strings.HasPrefix(dir, o.Dir+string(filepath.Separator))) {
			continue
		}
		if best == nil || o.DeliveredAt.Before(best.DeliveredAt) {
			best = o
		}
	}
	return best
}

// penalize records the finding as a reviewer would, logs it once, and says
// so on the folder's feed.
func (b *brain) penalize(f auditFinding) {
	note := "audit: " + f.Rule + " (" + f.Detail + ")"
	b.mu.Lock()
	if o := b.ledger.OutcomeFor(f.TaskID); o != nil {
		switch o.Status {
		case captaincode.AcceptanceAccepted:
			b.ledger.RecordRegression(f.TaskID, note, "audit")
		case captaincode.AcceptanceRejected, captaincode.AcceptanceRegressed:
		default:
			_ = b.ledger.RecordTaskReview(f.TaskID, "reject", "audit", note, false)
		}
		_ = b.ledger.Save()
	}
	b.mu.Unlock()
	path := auditLogPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		raw, _ := json.Marshal(f)
		_, _ = fh.Write(append(raw, '\n'))
		fh.Close()
	}
	fmt.Printf("captain brain: audit - %s (%s): %s - %s\n", f.Leg, f.TaskID, f.Rule, f.Detail)
	b.pushActivity(activity{Dir: f.Dir, Kind: "route", Leg: f.Leg, Model: "audit",
		Text: fmt.Sprintf("audit penalized %s: %s (%s)", orDash(f.Leg), f.Rule, f.Detail)})
}

func readAuditKeys() map[string]bool {
	seen := map[string]bool{}
	fh, err := os.Open(auditLogPath())
	if err != nil {
		return seen
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var f auditFinding
		if json.Unmarshal(sc.Bytes(), &f) == nil {
			seen[f.key()] = true
		}
	}
	return seen
}

// auditRun runs one read-only command in dir.
func auditRun(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// auditHTTP: POST /v1/audit?since=24h runs a pass now.
func (b *brain) auditHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	window := 24 * time.Hour
	if d, err := time.ParseDuration(r.URL.Query().Get("since")); err == nil && d > 0 {
		window = d
	}
	writeJSON(w, 200, map[string]any{"findings": b.audit(time.Now().Add(-window))})
}

// cmdAudit: `captain audit [--since 24h]`.
func cmdAudit(args []string) {
	since := "24h"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				since = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println("usage: captain audit [--since 24h]\n\nChecks what workers did - the worker guard's refusals, releases published without a request, commits whose CI failed - and penalizes each new finding once: the run is reviewed \"reject\" by \"audit\" (regressed if you had accepted it). The brain runs it every CAPTAIN_AUDIT_EVERY (30m; 0 = off). Findings: ~/.captaincode/audit.jsonl.")
			return
		}
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(brainURL()+"/v1/audit?since="+since, "application/json", nil)
	if err != nil {
		fatal(fmt.Errorf("the brain is not running (%v)", err))
	}
	defer resp.Body.Close()
	var out struct {
		Findings []auditFinding `json:"findings"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Findings) == 0 {
		fmt.Println("no new findings")
		return
	}
	for _, f := range out.Findings {
		fmt.Printf("%s  %s  %s: %s - %s\n", f.At.Local().Format("Jan 2 15:04"), f.TaskID, orDash(f.Leg), f.Rule, f.Detail)
	}
}
