package main

// `captain runs` / `captain show` - browse what previous turns produced.

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// cmdRuns lists recent turns, newest first.
func cmdRuns(args []string) {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	n := fs.Int("n", 20, "how many runs to list")
	leg := fs.String("leg", "", "only runs that used this leg")
	kind := fs.String("kind", "", "only this kind (solo|team|workflow|frontier)")
	grep := fs.String("grep", "", "only runs whose task or output contains this text")
	failed := fs.Bool("failed", false, "only runs that produced no answer")
	_ = fs.Parse(args)

	var recs []runRecord
	if *grep != "" {
		recs = searchRuns(*grep, 0)
	} else {
		recs, _ = readHistory(0)
	}
	shown := 0
	for _, r := range recs {
		if *leg != "" && !hasLeg(r, *leg) {
			continue
		}
		if *kind != "" && r.Kind != *kind {
			continue
		}
		if *failed && r.Error == "" {
			continue
		}
		fmt.Println(runLine(r))
		shown++
		if shown >= *n {
			break
		}
	}
	if shown == 0 {
		fmt.Println("no runs recorded yet (history starts at ~/.captaincode/history/)")
		return
	}
	fmt.Printf("\n%d run(s) · `captain show <id>` for the full output\n", shown)
}

func hasLeg(r runRecord, leg string) bool {
	for _, l := range r.Legs {
		if strings.EqualFold(l, leg) {
			return true
		}
	}
	for _, w := range r.Workers {
		if strings.EqualFold(w.Leg, leg) {
			return true
		}
	}
	return false
}

func runLine(r runRecord) string {
	mark := "✓"
	if r.Error != "" {
		mark = "✗"
	}
	legs := strings.Join(r.Legs, "+")
	if legs == "" {
		legs = r.Model
	}
	detail := promptPeek(r.Task)
	if len(detail) > 64 {
		detail = detail[:63] + "…"
	}
	size := fmt.Sprintf("%d chars", len(r.Output))
	if r.Error != "" {
		size = promptPeek(r.Error)
		if len(size) > 40 {
			size = size[:39] + "…"
		}
	}
	return fmt.Sprintf("%s %-14s %s  %-9s %-26s %-8s  %-64s  %s",
		mark, r.ID, r.At.Format("01-02 15:04"), r.Kind, legs,
		(time.Duration(r.DurationMs) * time.Millisecond).Round(time.Second), detail, size)
}

// cmdShow prints one run in full: the request, every worker's output, the answer.
func cmdShow(args []string) {
	id := "last"
	if len(args) > 0 {
		id = args[0]
	}
	r, ok := findRun(id)
	if !ok {
		fmt.Printf("no run matching %q - `captain runs` lists what is recorded\n", id)
		return
	}
	fmt.Printf("%s · %s · %s · %s\n", r.ID, r.At.Format("2006-01-02 15:04:05"), r.Kind,
		(time.Duration(r.DurationMs) * time.Millisecond).Round(time.Second))
	if len(r.Legs) > 0 {
		fmt.Printf("legs: %s\n", strings.Join(r.Legs, " → "))
	}
	fmt.Printf("\n--- request ---\n%s\n", strings.TrimSpace(r.Task))
	for _, w := range r.Workers {
		head := fmt.Sprintf("--- worker %s", w.Leg)
		if w.Stage > 0 {
			head = fmt.Sprintf("--- stage %d · %s", w.Stage, w.Leg)
		}
		if w.DurationMs > 0 {
			head += fmt.Sprintf(" · %s", (time.Duration(w.DurationMs) * time.Millisecond).Round(time.Second))
		}
		fmt.Printf("\n%s ---\n", head)
		if w.Error != "" {
			fmt.Printf("FAILED: %s\n", w.Error)
			if strings.TrimSpace(w.Text) != "" {
				fmt.Printf("(partial output kept below)\n%s\n", strings.TrimSpace(w.Text))
			}
			if w.Log != "" {
				fmt.Printf("worker log: %s\n", w.Log)
			}
			continue
		}
		fmt.Println(strings.TrimSpace(w.Text))
		if w.Log != "" {
			fmt.Printf("worker log: %s\n", w.Log)
		}
	}
	if r.Error != "" {
		fmt.Printf("\n--- failed ---\n%s\n", r.Error)
	}
	if strings.TrimSpace(r.Output) != "" {
		fmt.Printf("\n--- answer ---\n%s\n", strings.TrimSpace(r.Output))
	}
	if r.Transcript != "" {
		fmt.Printf("\nfull transcript: %s\n", r.Transcript)
	}
	for _, l := range r.Logs {
		fmt.Printf("worker log: %s\n", l)
	}
}
