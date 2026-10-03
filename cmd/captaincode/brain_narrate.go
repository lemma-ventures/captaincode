package main

// The narrator: one plain line per step the worker took, with the time, in
// the turn's live feed (2026-10-03).
//
// The feed's tool lines ("⚙ bash curl -sS -m 30 https://…") are accurate
// and live, but they read like a shell history and a successful command
// says nothing of what came of it. The transports record each finished call
// on the turn's Steer (pkg/captaincode/steps.go); every few steps, or when
// a step has waited narrateWait, the narrator hands the batch to the cheap
// summarizer leg and writes back one line per step:
//
//	≡ 14:41:16  read the pricing page: individual and team plans
//
// It runs beside the worker, never in its way: a failed or slow summary
// skips its batch, and the raw lines are still there. CAPTAIN_NARRATE=0
// turns it off.

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const narrateBatch = 3 // steps that make a batch

var (
	narrateWait = 20 * time.Second // how long a lone step waits for company
	narrateTick = 5 * time.Second
)

func narrationEnabled() bool { return os.Getenv("CAPTAIN_NARRATE") != "0" }

// narrate starts narrating the steps recorded on steer for the task, until
// the feed closes. summarize returns one line per step, "" for a step it
// had nothing to say about.
func (p *progressFeed) narrate(steer *captaincode.Steer, task string, summarize func(task string, steps []captaincode.Step) ([]string, error)) {
	if p == nil || p.emit == nil || steer == nil || summarize == nil || !narrationEnabled() {
		return
	}
	go func() {
		t := time.NewTicker(narrateTick)
		defer t.Stop()
		next := 0
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
			steps := steer.StepsFrom(next)
			if len(steps) == 0 || (len(steps) < narrateBatch && time.Since(steps[0].At) < narrateWait) {
				continue
			}
			next += len(steps)
			lines, err := summarize(task, steps)
			if err != nil {
				continue // the raw lines already said what ran
			}
			for i, l := range lines {
				if i >= len(steps) || strings.TrimSpace(l) == "" {
					continue
				}
				select {
				case <-p.stop:
					return
				default:
				}
				p.note(fmt.Sprintf("≡ %s  %s", steps[i].At.Format("15:04:05"), strings.TrimSpace(l)))
			}
		}
	}()
}

// narrateSteps asks the summarizer leg for one line per step. Test seam:
// b.narrateFn.
func (b *brain) narrateSteps(task string, steps []captaincode.Step) ([]string, error) {
	if b.narrateFn != nil {
		return b.narrateFn(task, steps)
	}
	d := captaincode.NewDispatcher(opencodePort)
	d.Title = "narrate"
	d.NoTools = true
	d.Timeout = 45 * time.Second
	leg := compactLeg()
	res, err := d.Run(leg, narrationPrompt(task, steps))
	if h := b.chargeOverhead(task); h != nil {
		h(leg, "narrate", res, err)
	}
	if err != nil {
		return nil, err
	}
	return parseNarration(res.Text, len(steps)), nil
}

// narrationPrompt is the summarizer's brief for a batch of steps.
func narrationPrompt(task string, steps []captaincode.Step) string {
	var sb strings.Builder
	sb.WriteString("An AI agent is working on this request:\n\n" + truncate(task, 600) + "\n\n")
	sb.WriteString("Below are the last steps it took. For each numbered step write ONE line, at most 15 words, " +
		"saying what it did and what came of it, in plain words for the person who asked: no shell syntax, " +
		"no flags, no quoting the command. Name the concrete thing (the site, the file, the test) and the result " +
		"(found X, 404, 3 tests failed, nothing useful). Reply with exactly one line per step, `N. text`, and nothing else.\n\n")
	for i, st := range steps {
		fmt.Fprintf(&sb, "%d. [%s] %s: %s", i+1, st.At.Format("15:04:05"), st.Tool, st.Input)
		if st.Failed {
			sb.WriteString(" (FAILED)")
		}
		if st.Output != "" {
			sb.WriteString("\n   result: " + st.Output)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

var narrationLine = regexp.MustCompile(`^\s*(\d+)[.)]\s*(.+?)\s*$`)

// parseNarration maps the summarizer's `N. text` lines onto the n steps.
func parseNarration(text string, n int) []string {
	out := make([]string, n)
	for _, l := range strings.Split(text, "\n") {
		m := narrationLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if i, err := strconv.Atoi(m[1]); err == nil && i >= 1 && i <= n {
			out[i-1] = strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(m[2]))
		}
	}
	return out
}
