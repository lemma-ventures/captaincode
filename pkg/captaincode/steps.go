package captaincode

// The steps a worker took during a turn, for the narrator (brain_narrate.go).
//
// The live feed shows each tool call as it starts ("⚙ bash curl -sS …"):
// accurate, but it reads like a shell history, and a successful command
// says nothing about what came of it. The transports also record each
// finished call here - what it ran and the head of what came back - and
// the narrator turns batches of them into one plain line per step, with the
// time: "14:41:16 · read the pricing page" (2026-10-03).

import (
	"strings"
	"time"
)

// Step is one finished tool call.
type Step struct {
	At     time.Time
	Tool   string
	Input  string // what it ran: the command, URL, file or query
	Output string // the head of what came back
	Failed bool
}

const (
	stepDetailMax = 400 // characters of input and output kept per step
	stepsMax      = 500 // steps kept per turn
)

// RecordStep adds a finished tool call to the turn's record. Nil-safe: a
// run outside a turn has no Steer.
func (s *Steer) RecordStep(st Step) {
	if s == nil {
		return
	}
	if st.At.IsZero() {
		st.At = time.Now()
	}
	st.Input = clipStep(st.Input)
	st.Output = clipStep(st.Output)
	s.smu.Lock()
	defer s.smu.Unlock()
	if len(s.steps) >= stepsMax {
		return
	}
	s.steps = append(s.steps, st)
}

// StepsFrom returns the steps recorded from index i on.
func (s *Steer) StepsFrom(i int) []Step {
	if s == nil {
		return nil
	}
	s.smu.Lock()
	defer s.smu.Unlock()
	if i >= len(s.steps) {
		return nil
	}
	return append([]Step(nil), s.steps[i:]...)
}

func clipStep(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if len(v) > stepDetailMax {
		return v[:stepDetailMax] + "…"
	}
	return v
}

// stepInput is the fullest description of what a tool call ran.
func stepInput(title string, input []byte) string {
	if d := pickDetail(input, "command", "url", "filePath", "file_path", "path", "pattern", "query", "prompt", "description"); d != "" {
		return d
	}
	return title
}
