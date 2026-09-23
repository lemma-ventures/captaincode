package main

// Supervisor shadow points in the brain (pkg supervise.go).
//
// Captain's routing shadow asks the decision leg what it would have done
// BEFORE the work. This asks it about the work while it is happening: is
// this worker stuck, is it on the wrong thing, does it need the user, is it
// departing from the repository's own conventions. Four nouls, sampled on a
// slow interval off the status feed the worker already produces, recorded
// against what actually happened, and acted on by nothing.
//
// The sample is free of the run: it rides the status callback the worker log
// already wraps, and the call happens on its own goroutine with its own
// timeout, so a slow or dead decision leg costs a running worker nothing.
//
// It stamps two of the four from what captain itself observes afterwards -
// the stall watchdog fired, or the user interrupted - so the record builds a
// calibration without anyone labelling rows by hand. work-off-track is left
// to the task's acceptance evidence, which the calibration already joins by
// task id; agents-md-drift is left uncompared, because nothing observes it
// and an invented label is worse than a missing one.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// superviseRecentMax is how much of the status tail a sample carries.
const superviseRecentMax = 12

// guidanceMax bounds the AGENTS.md head the drift question is asked over.
const guidanceMax = 1500

// superviseWatch samples one running worker. Nil is a working watch that
// does nothing, so every call site can be unconditional.
type superviseWatch struct {
	b       *brain
	leg     captaincode.Leg
	task    string
	taskID  string
	guide   string
	started time.Time

	mu     sync.Mutex
	last   time.Time
	recent []string
	rows   []captaincode.ShadowRecord

	stop chan struct{}
	done sync.WaitGroup
}

// superviseStart opens a watch on a worker that is about to run, or nil when
// there is nothing to sample with: no decision leg, or the sampling off.
// Without a decision leg this is the whole cost of the feature - one nil
// check per worker run.
func (b *brain) superviseStart(ws captaincode.Workspace, leg captaincode.Leg, prompt string) *superviseWatch {
	if b == nil || b.jev == nil || !captaincode.SuperviseEnabled() {
		return nil
	}
	w := &superviseWatch{
		b: b, leg: leg, started: time.Now(), last: time.Now(),
		task:   promptPeek(lastUserTurn(prompt)),
		taskID: ws.Steer.Task(),
		guide:  repoGuidance(ws.Dir),
		stop:   make(chan struct{}),
	}
	w.done.Add(1)
	go w.loop()
	return w
}

// status is the status callback's tap: it keeps the tail the sample is built
// from and when the worker was last observed doing anything.
func (w *superviseWatch) status(s string) {
	if w == nil {
		return
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	w.mu.Lock()
	w.last = time.Now()
	w.recent = append(w.recent, s)
	if len(w.recent) > superviseRecentMax {
		w.recent = w.recent[len(w.recent)-superviseRecentMax:]
	}
	w.mu.Unlock()
}

// wrap tees a status callback through the watch.
func (w *superviseWatch) wrap(onStatus func(string)) func(string) {
	if w == nil {
		return onStatus
	}
	return func(s string) {
		w.status(s)
		if onStatus != nil {
			onStatus(s)
		}
	}
}

// snapshot is what the decision leg is shown.
func (w *superviseWatch) snapshot() captaincode.SuperviseSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := captaincode.SuperviseSnapshot{
		Leg: w.leg, Task: w.task, TaskID: w.taskID, Guidance: w.guide,
		Elapsed: time.Since(w.started), Quiet: time.Since(w.last),
		Recent: append([]string(nil), w.recent...),
	}
	if n := len(w.recent); n > 0 {
		s.LastDetail = w.recent[n-1]
	}
	return s
}

// loop samples on the interval until the run ends.
func (w *superviseWatch) loop() {
	defer w.done.Done()
	t := time.NewTicker(captaincode.SuperviseEvery())
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.sample()
		}
	}
}

// sample asks the four nouls once and keeps the row. A failure is kept as a
// failed row, the way every other shadow call is: a decision leg that cannot
// answer is a fact about the decision leg.
func (w *superviseWatch) sample() {
	ctx, cancel := context.WithTimeout(context.Background(), jevTimeout)
	defer cancel()
	snap := w.snapshot()
	sh, res, err := captaincode.SuperviseWithJev(ctx, w.b.jev, snap)
	r, ok := captaincode.SuperviseRecord(snap, sh)
	if ok {
		w.mu.Lock()
		w.rows = append(w.rows, r)
		w.mu.Unlock()
	}
	// Charged like any other decision-leg call the brain makes on its own
	// behalf, so a shadow that nothing acts on is still visible in the spend.
	if hook := w.b.chargeOwnTask("jev supervisor shadow of a running " + string(w.leg)); hook != nil {
		hook(captaincode.LegJev, captaincode.PointSupervise, res, err)
	}
}

// close ends the watch and files its rows, stamped with what captain went on
// to observe: whether the run was cut for silence, and whether the user had
// to step in. Both are the ground truth of a question that was asked while
// the answer was still open, which is the only reason these rows are worth
// keeping.
func (w *superviseWatch) close(res captaincode.Result, err error) {
	if w == nil {
		return
	}
	close(w.stop)
	w.done.Wait()
	w.mu.Lock()
	rows := w.rows
	w.rows = nil
	w.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	out := captaincode.SuperviseOutcome{
		Stalled:     errors.Is(err, captaincode.ErrWorkerStalled) || errors.Is(err, captaincode.ErrWorkerTimeout),
		Interrupted: errors.Is(err, captaincode.ErrInterrupted),
	}
	// The last sample's answers, kept for the verify sequence
	// (brain_verify.go): whether this worker needs the user, or went off
	// track, gates what a failed check may buy.
	if last := rows[len(rows)-1]; last.Err == "" {
		verdicts := map[string]float64{}
		for point, a := range last.Answers {
			verdicts[point] = a.Confidence
			if a.Choice == "true" || a.Choice == "false" {
				if a.Choice == "false" {
					verdicts[point] = 1 - a.Confidence
				}
			}
		}
		w.b.smu.Lock()
		if w.b.lastSupervise == nil {
			w.b.lastSupervise = map[string]map[string]float64{}
		}
		w.b.lastSupervise[superviseKey(w.leg, w.task)] = verdicts
		w.b.smu.Unlock()
	}
	w.b.mu.Lock()
	for i := range rows {
		captaincode.StampSupervise(&rows[i], out)
		w.b.ledger.RecordShadow(rows[i])
	}
	saveErr := w.b.ledger.Save()
	w.b.mu.Unlock()
	_ = saveErr
	_ = res
}

// repoGuidance is the project's own conventions file, for the drift question:
// the head of AGENTS.md or CLAUDE.md, redacted. Absent, the question is not
// asked at all rather than asked against nothing.
// superviseKey names a running worker's verdict slot.
func superviseKey(leg captaincode.Leg, task string) string {
	return string(leg) + "\x00" + truncate(task, 120)
}

// superviseVerdicts is the supervisor's last answers for a worker on a
// task: point → probability that the answer is "true". Empty when no
// decision leg is configured or nothing was sampled.
func (b *brain) superviseVerdicts(leg captaincode.Leg, task string) map[string]float64 {
	b.smu.Lock()
	defer b.smu.Unlock()
	return b.lastSupervise[superviseKey(leg, task)]
}

func repoGuidance(dir string) string {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out, _ := captaincode.Redact(truncate(string(raw), guidanceMax))
		return out
	}
	return ""
}
