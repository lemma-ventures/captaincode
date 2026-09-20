package captaincode

// Supervisor shadow points (ROADMAP M5.4): the decision leg asked about a
// worker that is STILL RUNNING.
//
// Captain's three existing shadow points are all dispatch - what shape the
// turn is, which leg takes it, which worker a note concerns. They are asked
// once, before or beside the work. Nothing in captain watches the floor: a
// worker that has been re-reading the same file for six minutes, a worker
// doing careful work on the wrong thing, a worker that has hit something only
// the user can answer. The stall watchdog (notimeout.go) sees silence and
// kills on a clock; it cannot tell a long `cargo test --release` from a loop.
// `/btw` and `/interrupt` exist, and both need the user to notice first.
//
// These four nouls are that missing axis, and they go through the same
// machinery the routing points did: asked beside the run, recorded with what
// happened afterwards, ACTED ON NEVER. The point of putting them in the
// shadow rather than wiring them to an interrupt is that nobody - including
// the people who published the idea - has shown a decision model is accurate
// at this, and captain already owns the apparatus for finding out.
//
// Three of the four have an answer captain later observes, so the record can
// be stamped without a human labelling it:
//
//	worker_stuck    → the stall watchdog fired, or the run was rerouted for silence
//	work_off_track  → the task's acceptance evidence came back rejected or regressed
//	needs_human     → the user interrupted, or sent a note, after the question was asked
//	agents_md_drift → nothing observes it; the rows stay uncompared and the report says so
//
// The fourth is asked anyway because Euclid makes its state cheap to build
// and because an honest "not compared" is a better record than a missing one.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// The supervisor's decision points.
const (
	PointWorkerStuck  = "worker-stuck"
	PointWorkOffTrack = "work-off-track"
	PointNeedsHuman   = "needs-human"
	PointAgentsDrift  = "agents-md-drift"
)

// SupervisePoints are the supervisor's points in report order.
var SupervisePoints = []string{PointWorkerStuck, PointWorkOffTrack, PointNeedsHuman, PointAgentsDrift}

// PointSupervise is the row point a supervisor sample is filed under; the row
// carries all four answers (ShadowRecord.Points).
const PointSupervise = "supervise"

// SuperviseEnv turns the sampling off; SuperviseEveryEnv moves the interval.
const (
	SuperviseEnv      = "CAPTAIN_JEV_SUPERVISE"
	SuperviseEveryEnv = "CAPTAIN_SUPERVISE_EVERY"
)

// superviseEveryDefault is how often a running worker is sampled. It is
// deliberately slow: the questions are about a floor state that changes over
// minutes, and a sample per minute on a ten-worker fan-out is ten calls a
// minute for a record nothing acts on.
const superviseEveryDefault = 90 * time.Second

// SuperviseEnabled says whether running workers are sampled. Off by default
// only in the sense that no sample happens without a decision leg: with one
// configured it rides along unless CAPTAIN_JEV_SUPERVISE=0.
func SuperviseEnabled() bool { return os.Getenv(SuperviseEnv) != "0" }

// SuperviseEvery is the sampling interval.
func SuperviseEvery() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(SuperviseEveryEnv))); err == nil && d >= 10*time.Second {
		return d
	}
	return superviseEveryDefault
}

// SuperviseSnapshot is what captain can honestly say about a running worker
// without asking the worker: its assignment, how long it has been going, what
// it last did, how long it has been quiet, and what the repository's own
// guidance says it should be doing.
type SuperviseSnapshot struct {
	Leg        Leg           `json:"leg"`
	Task       string        `json:"task,omitempty"`        // the turn's prompt, head
	Brief      string        `json:"brief,omitempty"`       // this worker's assignment on a team
	Elapsed    time.Duration `json:"elapsed,omitempty"`     // since the worker started
	Quiet      time.Duration `json:"quiet,omitempty"`       // since its last observable event
	LastTool   string        `json:"last_tool,omitempty"`   // the tool it last called
	LastDetail string        `json:"last_detail,omitempty"` // what it called it with
	Recent     []string      `json:"recent,omitempty"`      // the last few statuses, oldest first
	Guidance   string        `json:"guidance,omitempty"`    // AGENTS.md / CLAUDE.md head, for the drift question
	TaskID     string        `json:"task_id,omitempty"`
}

// superviseStateMax bounds the state: a floor report, not a transcript.
const superviseStateMax = 2400

// superviseRecent bounds the activity tail carried into the state.
const superviseRecent = 8

// JevSuperviseQuestions are the four nouls. Each is written so that the
// ordinary case - a worker doing long, correct work - answers false: a
// supervisor that fires on every slow build is a supervisor nobody leaves on.
func JevSuperviseQuestions(hasGuidance bool) map[string]S1Question {
	qs := map[string]S1Question{
		PointWorkerStuck: {Type: "noul",
			Instructions: "Is this worker stuck? The state is a live report of a coding agent mid-task: how long it has run, how long since its last observable action, and what it last did. Stuck means no longer making progress - repeating the same action, re-reading the same files, or silent with nothing running. A long single command (a build, a test suite, a large download) is work, not a stall, and a worker that has been going a long time while its actions keep changing is making progress."},
		PointWorkOffTrack: {Type: "noul",
			Instructions: "Is this worker working on the wrong thing? Compare what it is actually doing against the assignment stated in the state. Off track means the actions do not serve the assignment: a different part of the codebase, a problem nobody asked about, a rewrite where a fix was asked for. Exploration, reading around the problem, and setting up to do the work are on track."},
		PointNeedsHuman: {Type: "noul",
			Instructions: "Does this need the user before it can go further? There is no human watching: this worker cannot be asked a question. Answer true only when the work has reached something the user alone can settle - a missing credential or access, a destructive choice with no recoverable option, a genuine ambiguity in the request where the two readings lead to different work. A decision the worker can reasonably make itself, and state in its answer, does not need the user."},
	}
	if hasGuidance {
		qs[PointAgentsDrift] = S1Question{Type: "noul",
			Instructions: "Is this worker departing from the repository's own stated conventions? The state carries the project's guidance file (AGENTS.md or CLAUDE.md) and what the worker is doing. Answer true only when an action contradicts something the guidance states - a different toolchain, a forbidden command, a convention the guidance names and the action breaks. Doing something the guidance simply does not mention is not drift."}
	}
	return qs
}

// superviseState renders the snapshot as the state the leg judges: a short
// report in the terms the questions are written in, redacted like every
// other state that leaves the machine.
func superviseState(s SuperviseSnapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "worker: %s\n", s.Leg)
	if a := strings.TrimSpace(firstNonEmpty(s.Brief, s.Task)); a != "" {
		fmt.Fprintf(&sb, "assignment: %s\n", truncateStr(strings.Join(strings.Fields(a), " "), 600))
	}
	fmt.Fprintf(&sb, "running for: %s\n", roundDur(s.Elapsed))
	fmt.Fprintf(&sb, "quiet for: %s\n", roundDur(s.Quiet))
	if s.LastTool != "" {
		fmt.Fprintf(&sb, "last action: %s %s\n", s.LastTool, truncateStr(s.LastDetail, 160))
	}
	if r := s.Recent; len(r) > 0 {
		if len(r) > superviseRecent {
			r = r[len(r)-superviseRecent:]
		}
		sb.WriteString("recent actions, oldest first:\n")
		for _, line := range r {
			fmt.Fprintf(&sb, "  - %s\n", truncateStr(strings.Join(strings.Fields(line), " "), 160))
		}
	}
	if g := strings.TrimSpace(s.Guidance); g != "" {
		fmt.Fprintf(&sb, "the repository's guidance file says:\n%s\n", truncateStr(g, 800))
	}
	out, _ := Redact(truncateStr(sb.String(), superviseStateMax))
	return out
}

// SuperviseWithJev asks the four nouls about one running worker. The answers
// are a shadow row and nothing else: no caller acts on them, and the function
// returns no verdict for one to act on.
func SuperviseWithJev(ctx context.Context, c *SystemOneClient, s SuperviseSnapshot) (*Shadow, Result, error) {
	if c == nil {
		return nil, Result{}, nil // no decision leg: nothing is asked and nothing is recorded
	}
	qs := JevSuperviseQuestions(strings.TrimSpace(s.Guidance) != "")
	resp, res, err := c.Ask(ctx, superviseState(s), qs)
	sh := shadowFrom(c, resp, res, err, nil)
	if err == nil {
		sh.Answers = nulAnswers(resp.Answers)
	}
	return sh, res, err
}

// SuperviseRecord files one sample as a shadow row carrying every point the
// call answered.
func SuperviseRecord(s SuperviseSnapshot, sh *Shadow) (ShadowRecord, bool) {
	if sh == nil {
		return ShadowRecord{}, false
	}
	points := make([]string, 0, len(SupervisePoints))
	for _, p := range SupervisePoints {
		if _, ok := sh.Answers[p]; ok || sh.Err != "" {
			points = append(points, p)
		}
	}
	if len(points) == 0 {
		return ShadowRecord{}, false
	}
	head, _ := Redact(truncateStr(strings.Join(strings.Fields(firstNonEmpty(s.Brief, s.Task)), " "), 120))
	return ShadowRecord{Point: PointSupervise, Points: points, TaskID: s.TaskID,
		Task: string(s.Leg) + ": " + head, Shadow: *sh}, true
}

// SuperviseOutcome is what captain later observed about the run a sample was
// taken during - the ground truth the prediction is stamped against.
type SuperviseOutcome struct {
	Stalled     bool // the stall watchdog fired, or the run was rerouted for silence
	Interrupted bool // the user interrupted or sent a note after the sample
}

// StampSupervise records what happened after the sample. work-off-track is
// left alone: its ground truth is the task's acceptance evidence, which the
// calibration already joins by task id, and which is not known when the run
// ends. agents-md-drift is left alone because nothing observes it - an
// uncompared row is the honest record, not an invented one.
func StampSupervise(r *ShadowRecord, o SuperviseOutcome) {
	if r == nil {
		return
	}
	r.Stamp(PointWorkerStuck, boolChoice(o.Stalled), "watchdog")
	r.Stamp(PointNeedsHuman, boolChoice(o.Interrupted), "user")
}

// boolChoice is the answer shape a stamped noul compares against.
func boolChoice(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// roundDur reads a duration the way the question is written: seconds under a
// minute, whole minutes above.
func roundDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Round(time.Second).Seconds())%60)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
