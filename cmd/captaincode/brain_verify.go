package main

// Cheap, verify, escalate - for solo turns (stage 4). The workflow executor
// has run its gate → repair → escalate sequence since M2.5; the ordinary
// turn, which is most of captain's traffic, delivered whatever the worker
// said and never checked. Now: when a solo worker changed files in a
// repository whose test command captain can detect, the tests run before
// the turn ends. A failure buys, in order and each bounded by the
// escalation policy and the task's budget:
//
//  1. a REPAIR - the same leg, same effort, with the failure output;
//  2. an EFFORT ESCALATION - the same leg one rung up (the prompt cache and
//     the leg's context survive; a fresh leg starts cold);
//  3. a LEG ESCALATION - the next stronger leg on the frontier chain.
//
// jev's supervisor answers gate the sequence: "needs a human" above the bar
// stops it (no amount of escalation answers a question only the user can),
// and "off track" above the bar skips the repair - the same model at the
// same effort went the wrong way, so the retry goes straight to a change.
// Every attempt is recorded as its own event with its attempt number and
// the leg it answers for, and the outcome carries the whole sequence.
//
// CAPTAIN_SOLO_VERIFY=0 turns the check and the sequence off.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func soloVerifyEnabled() bool { return os.Getenv("CAPTAIN_SOLO_VERIFY") != "0" }

// superviseBar is the noul above which a supervisor answer gates the
// sequence. CAPTAIN_SUPERVISE_BAR, default 0.7: the confidence at which the
// gate points agreed with the user 97-98% of the time on this machine.
func superviseBar() float64 {
	if v, err := parseFloatEnv("CAPTAIN_SUPERVISE_BAR"); err == nil && v > 0 && v <= 1 {
		return v
	}
	return 0.7
}

// verifyResult is one objective check's outcome.
type verifyResult struct {
	ran     bool // a check actually ran
	passed  bool
	command string
	output  string
	// timedOut: the command was killed by the deadline, so it produced NO
	// verdict. Not a failure: nothing was judged, and buying a repair for it
	// spends a second worker on a guess (live 2026-09-21, lemma - `cargo
	// test` in a 49-crate workspace cannot finish inside any turn's budget,
	// and the repair went to the same cheap leg at low effort).
	timedOut bool
}

// soloCheck runs the repository's own tests when the worker changed files
// and a test command is detectable. No files changed, or no command: no
// check, and the sequence is not entered - nothing objective failed.
func (b *brain) soloCheck(ctx context.Context, ws captaincode.Workspace) verifyResult {
	if ws.Dir == "" {
		return verifyResult{}
	}
	if len(captaincode.ChangedFiles(ctx, ws.Dir)) == 0 {
		return verifyResult{}
	}
	ce, err := b.captureTest(ctx, ws.Dir)
	if err != nil || ce == nil {
		return verifyResult{}
	}
	return verifyResult{ran: true, passed: ce.Passed, command: strings.Join(ce.Command, " "), output: ce.Output, timedOut: ce.TimedOut}
}

// captureTest is CaptureTestEvidence behind the test seam the workflow
// path already uses.
func (b *brain) captureTest(ctx context.Context, dir string) (*captaincode.CheckEvidence, error) {
	if b.captureTestFn != nil {
		return b.captureTestFn(ctx, dir)
	}
	return captaincode.CaptureTestEvidence(ctx, dir)
}

// verifyAndEscalate is the sequence. It returns the leg, workspace (effort
// may have climbed), result and error the turn should deliver, and the
// escalation record when the sequence ran (nil when no check applied).
// Intermediate attempts are recorded here; the caller records the final
// one with the attempt number the record carries.
func (b *brain) verifyAndEscalate(ws captaincode.Workspace, leg captaincode.Leg, prompt string, res captaincode.Result, taskID string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Workspace, captaincode.Result, *captaincode.EscalationOutcome) {
	if !soloVerifyEnabled() || taskID == "" {
		return leg, ws, res, nil
	}
	task := lastUserTurn(prompt)
	note := func(s string) {
		if onStatus != nil {
			onStatus(s)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	note("verifying: running the repository's tests on what changed")
	check := b.soloCheck(ctx, ws)
	if !check.ran {
		return leg, ws, res, nil
	}
	// No verdict: say so in the answer and stop. The sequence exists to
	// answer an OBJECTIVE failure; a suite that never finished is not one.
	if check.timedOut {
		note("verification inconclusive: " + check.command + " did not finish in time")
		res.Text += fmt.Sprintf("\n\n[captain] `%s` did not finish within %s, so this turn was not verified. Run it yourself, or set CAPTAIN_VERIFY_TIMEOUT (or CAPTAIN_SOLO_VERIFY=0 for a repository whose suite cannot finish inside a turn).", check.command, captaincode.TestEvidenceTimeout())
		return leg, ws, res, &captaincode.EscalationOutcome{Check: check.command, Attempts: 1, From: leg, FinalLeg: leg, FinalEffort: ws.Effort,
			Steps: []string{"stopped: the check did not finish in time - no verdict"}}
	}
	rec := &captaincode.EscalationOutcome{Check: check.command, Attempts: 1, From: leg, FinalLeg: leg, FinalEffort: ws.Effort}
	b.recordCheck(taskID, check, 1)
	if check.passed {
		rec.ObjectiveMet = true
		note("verified: " + check.command + " passed")
		return leg, ws, res, rec
	}
	note(fmt.Sprintf("verification failed: %s", check.command))
	verdicts := b.superviseVerdicts(leg, task)
	bar := superviseBar()
	if v := verdicts[captaincode.PointNeedsHuman]; v >= bar {
		res.Text += fmt.Sprintf("\n\n[captain] `%s` failed after this turn, and the supervisor judged the work needs you before it can go further (%.0f%%). Not retried.", check.command, v*100)
		rec.Steps = append(rec.Steps, "stopped: supervisor says this needs the user")
		return leg, ws, res, rec
	}
	offTrack := verdicts[captaincode.PointWorkOffTrack] >= bar
	tried := []captaincode.Leg{leg}
	attempt := 1
	failure := func(r verifyResult) string {
		return "\n\n[captain] Your previous attempt ended with:\n" + truncate(res.Text, 2000) +
			"\n\nThe repository's tests `" + r.command + "` FAILED with:\n" + r.output +
			"\nFix the underlying problem so the tests pass, then report what you changed."
	}
	run := func(l captaincode.Leg, w captaincode.Workspace, p string, label string, from captaincode.Leg) (captaincode.Result, error, verifyResult) {
		attempt++
		if onDelta != nil {
			onDelta(fmt.Sprintf("\n\n[captain: %s failed `%s` - %s on %s at %s effort]\n\n", leg, check.command, label, l, w.Effort))
		}
		ran, r, err := b.runWorkerRerouted(w, l, p, onDelta, onStatus, taskID)
		b.reconcileAttempt(taskID, r.CostUSD)
		if err != nil {
			return r, err, verifyResult{}
		}
		vr := b.soloCheck(ctx, w)
		b.recordCheck(taskID, vr, attempt)
		// Recorded in line, not in a goroutine: the next attempt reserves
		// against the same budget this record settles, and an intermediate
		// attempt is never graded by the director (the check graded it), so
		// the record is quick.
		b.recordRunAt(ran, p, r, w, taskID, attempt, from, label)
		return r, nil, vr
	}
	stopUnverified := func(l captaincode.Leg, w captaincode.Workspace, r captaincode.Result, vr verifyResult) (captaincode.Leg, captaincode.Workspace, captaincode.Result, *captaincode.EscalationOutcome) {
		reason := "the check returned no verdict"
		if vr.timedOut {
			reason = "the check timed out"
		}
		note("verification inconclusive: " + reason)
		r.Text += "\n\n[captain] verification inconclusive: " + reason + ". The latest attempt is unverified; automatic retries stopped."
		rec.Attempts, rec.FinalLeg, rec.FinalEffort = attempt, l, w.Effort
		rec.Steps = append(rec.Steps, "stopped: "+reason)
		b.recordEscalation(taskID, *rec)
		return l, w, r, rec
	}
	// 1. repair: same leg, same effort - unless the supervisor saw the leg
	// heading the wrong way, in which case more of the same is not the fix.
	if b.escalation.CanRepair(0) && !offTrack && b.reserveAttempt(taskID) {
		rec.RepairsUsed++
		r, err, vr := run(leg, ws, prompt+failure(check), "repair", leg)
		if err == nil && (!vr.ran || vr.timedOut) {
			return stopUnverified(leg, ws, r, vr)
		}
		if err == nil && vr.passed {
			rec.Repaired, rec.ObjectiveMet, rec.Attempts = true, true, attempt
			rec.Steps = append(rec.Steps, fmt.Sprintf("repaired on %s at %s", leg, ws.Effort))
			b.recordEscalation(taskID, *rec)
			return leg, ws, r, rec
		}
		rec.Steps = append(rec.Steps, fmt.Sprintf("repair on %s failed", leg))
		if err == nil {
			res, check = r, vr
		}
	} else if offTrack {
		rec.Steps = append(rec.Steps, "repair skipped: supervisor saw the worker off track")
	}
	// 2. effort escalation: the same model, one rung up.
	if b.escalation.CanEscalateEffort(0) && captaincode.HasEffortKnob(leg) && captaincode.NextEffort(ws.Effort) != ws.Effort && b.reserveAttempt(taskID) {
		up := ws.WithEffort(captaincode.NextEffort(ws.Effort))
		if ws.Effort == "" {
			up = ws.WithEffort(captaincode.EffortHigh)
		}
		rec.EffortEscalations++
		rec.EscalatedEffort = up.Effort
		r, err, vr := run(leg, up, prompt+failure(check), "effort escalation", leg)
		if err == nil && (!vr.ran || vr.timedOut) {
			return stopUnverified(leg, up, r, vr)
		}
		if err == nil && vr.passed {
			rec.ObjectiveMet, rec.Attempts, rec.FinalEffort = true, attempt, up.Effort
			rec.Steps = append(rec.Steps, fmt.Sprintf("passed on %s at %s effort", leg, up.Effort))
			b.recordEscalation(taskID, *rec)
			return leg, up, r, rec
		}
		rec.Steps = append(rec.Steps, fmt.Sprintf("%s at %s effort still failed", leg, up.Effort))
		if err == nil {
			res, check, ws = r, vr, up
		}
	}
	// 3. leg escalation: the next stronger leg on the frontier chain.
	if b.escalation.CanEscalate(0) && b.reserveAttempt(taskID) {
		b.mu.Lock()
		next, found := b.escalation.NextEscalation(leg, tried, b.allowed, b.ledger.Cooldowns, time.Now())
		b.mu.Unlock()
		if found {
			rec.EscalationsUsed++
			rec.Escalated, rec.EscalatedTo = true, next
			nw := ws.WithEffort(captaincode.DecideEffort("", b.classOf(taskID, task), next, false, 2))
			r, err, vr := run(next, nw, prompt+failure(check), "escalation", leg)
			if err == nil && (!vr.ran || vr.timedOut) {
				return stopUnverified(next, nw, r, vr)
			}
			rec.Attempts = attempt
			if err == nil && vr.passed {
				rec.ObjectiveMet, rec.FinalLeg, rec.FinalEffort = true, next, nw.Effort
				rec.Steps = append(rec.Steps, fmt.Sprintf("passed on %s at %s effort", next, nw.Effort))
				b.recordEscalation(taskID, *rec)
				return next, nw, r, rec
			}
			rec.Steps = append(rec.Steps, fmt.Sprintf("%s still failed", next))
			if err == nil {
				res, ws, leg = r, nw, next
			}
		} else {
			rec.Steps = append(rec.Steps, "no stronger leg open to escalate to")
		}
	}
	rec.Attempts = attempt
	rec.FinalLeg, rec.FinalEffort = leg, ws.Effort
	b.stopBudget(taskID, captaincode.StopObjectiveFailed)
	b.recordEscalation(taskID, *rec)
	res.Text += fmt.Sprintf("\n\n[captain] `%s` still fails after %d attempt(s) (%s). The output above is delivered as-is; the failure is on the record.", check.command, attempt, strings.Join(rec.Steps, "; "))
	return leg, ws, res, rec
}

// recordCheck writes one objective check onto the task's outcome.
func (b *brain) recordCheck(taskID string, vr verifyResult, attempt int) {
	if !vr.ran || vr.timedOut || taskID == "" {
		return
	}
	code := 0
	if !vr.passed {
		code = 1
	}
	b.mu.Lock()
	b.ledger.RecordCheckResult(taskID, captaincode.CheckResult{Command: vr.command, ExitCode: code, Passed: vr.passed, Source: "tests", At: time.Now()})
	b.mu.Unlock()
}

func (b *brain) recordEscalation(taskID string, rec captaincode.EscalationOutcome) {
	b.mu.Lock()
	b.ledger.RecordEscalationOutcome(taskID, rec)
	b.mu.Unlock()
}

// classOf is the routing class recorded for the task, medium when none is.
func (b *brain) classOf(taskID, task string) captaincode.Class {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d, ok := b.ledger.DecisionFor(taskID); ok && d.Class != "" {
		return d.Class
	}
	return captaincode.TriageTask(task).Class
}

func parseFloatEnv(name string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(os.Getenv(name)), "%g", &f)
	return f, err
}
