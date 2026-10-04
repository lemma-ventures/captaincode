package main

// `captain euclid learn` - the persona learning loop, from the shell.
//
//	captain euclid learn                    the main brain + this repo's write brain
//	captain euclid learn --root <dir>       one brain explicitly
//	captain euclid learn --max 6            more passes before it stops
//	captain euclid learn --dry-run          show what it would write
//
// One pass reads what no pass has read yet (journal entries after the last
// distill, memory sections after the last crystallize), folds it into the
// persona registers, then reads its own output again. It stops when a pass
// has no new input and no edits to make. The dashboard data is rebuilt at the
// end, through the brain's own vendored copy:
//
//	python3 .euclid/bin/build-catalog.py && python3 .euclid/bin/build-dashboard.py

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdEuclidLearn(args []string) {
	if os.Getenv("CAPTAIN_EUCLID") == "0" {
		fatal(fmt.Errorf("euclid is off (CAPTAIN_EUCLID=0)"))
	}
	var roots []string
	max, apply, reindex := 4, true, true
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func(flag string) string {
			if i+1 >= len(args) {
				fatal(fmt.Errorf("%s needs a value", flag))
			}
			i++
			return args[i]
		}
		switch a {
		case "--root", "-root":
			roots = append(roots, need("--root"))
		case "--max", "-max":
			if n, err := parseIntFlag(need("--max")); err == nil {
				max = n
			} else {
				fatal(err)
			}
		case "--dry-run", "-dry-run", "dry-run":
			apply = false
		case "--no-reindex":
			reindex = false
		default:
			fatal(fmt.Errorf("usage: captain euclid learn [--root <brain dir>] [--max N] [--dry-run] [--no-reindex]"))
		}
	}
	cwd := euclidCwd()
	if len(roots) == 0 {
		if main := captaincode.MainBrainPath(); main != "" && isDirPath(main) {
			roots = append(roots, main)
		}
		if wb, ok := captaincode.WriteBrain(cwd); ok && wb.Root != "" && !hasRoot(roots, wb.Root) {
			roots = append(roots, wb.Root)
		}
	}
	if len(roots) == 0 {
		fatal(fmt.Errorf("no brain to learn from - run `captain euclid init` first"))
	}
	b := &brain{}
	learned := 0
	for _, root := range roots {
		root = filepath.Clean(root)
		eu := learnBrainAt(cwd, root)
		if !eu.Writable {
			fmt.Printf("learn: %s - skipped (the shared repo brain is written by `captain euclid share`)\n", eu.Label)
			continue
		}
		if _, err := os.Stat(filepath.Join(eu.Root, "BRAIN.md")); err != nil {
			fmt.Printf("learn: %s - skipped (no BRAIN.md there)\n", eu.Root)
			continue
		}
		passes, err := b.learnBrain(eu, max, apply, os.Stdout)
		if err != nil {
			fatal(err)
		}
		learned++
		if apply && len(passes) == 1 && passes[0].Journal == 0 && passes[0].Memories == 0 && len(passes[0].Edits) == 0 {
			fmt.Printf("learn: %s - persona is current, nothing to fold\n", eu.Label)
		}
	}
	if learned == 0 {
		fatal(fmt.Errorf("nothing was learned from %s root(s)", fmt.Sprint(len(roots))))
	}
	if !reindex {
		fmt.Println("`captain euclid reindex` rebuilds the dashboard data")
		return
	}
	seen := map[string]bool{}
	for _, root := range roots {
		target := indexRootOfRoot(root)
		if seen[target] || !captaincode.IsBrainRoot(target) {
			continue
		}
		seen[target] = true
		res := captaincode.Reindex(target, 0)
		fmt.Printf("%s: %s\n", target, describeReindex(res))
		for _, st := range res.Steps {
			fmt.Printf("  python3 %s  %.1fs\n", st.Script, st.DurationS)
			if !st.OK {
				fmt.Println("  " + strings.ReplaceAll(st.Tail, "\n", "\n  "))
			}
		}
	}
}

func hasRoot(roots []string, root string) bool {
	for _, r := range roots {
		if filepath.Clean(r) == filepath.Clean(root) {
			return true
		}
	}
	return false
}

func parseIntFlag(v string) (int, error) {
	var n int
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q is not a number", v)
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 {
		return 0, fmt.Errorf("%q must be 1 or more", v)
	}
	return n, nil
}

// learnBrainAt resolves a brain directory. The read set names the label and
// kind, but its "one write brain" rule is about a session's turn - a loop the
// operator asked for by name may write every personal brain (the main one and
// a developer subtree) and never the shared repo brain.
func learnBrainAt(cwd, root string) captaincode.EuclidBrain {
	root = filepath.Clean(root)
	e := captaincode.EuclidBrain{Root: root, Label: filepath.Base(root)}
	for _, found := range captaincode.ReadSet(cwd) {
		if filepath.Clean(found.Root) == root {
			e.Kind, e.Label = found.Kind, found.Label
			break
		}
	}
	switch {
	case root == captaincode.MainBrainPath():
		e.Kind, e.Label, e.Writable = "main", "main", true
	case filepath.Base(filepath.Dir(root)) == "developers":
		if e.Label == "" || e.Label == filepath.Base(root) {
			e.Label = "me@" + filepath.Base(filepath.Dir(filepath.Dir(root)))
		}
		e.Kind, e.Writable = "developer", true
	default:
		if e.Kind == "" {
			e.Kind = "repo"
		}
	}
	return e
}

// indexRootOfRoot is where a brain's dashboard data lives: a developer
// subtree is indexed with its repo brain.
func indexRootOfRoot(root string) string {
	if filepath.Base(filepath.Dir(root)) == "developers" {
		return filepath.Dir(filepath.Dir(root))
	}
	return root
}

// learnBrain is one brain's loop: passes until the persona has nothing left
// to fold. A model call per pass on the distill leg, charged to no ledger -
// the running brain owns its ledger, and two writers would lose rows.
func (b *brain) learnBrain(eu captaincode.EuclidBrain, max int, apply bool, w io.Writer) ([]captaincode.LearnPass, error) {
	ws := captaincode.Workspace{Dir: euclidCwd()}
	leg := b.distillLeg()
	pass := 0
	// The reply is a JSON envelope, not prose for a human: the deltas are
	// swallowed (a nil hook makes the dispatcher print every token) and the
	// loop's own lines are the report.
	run := func(prompt string) (string, error) {
		pass++
		fmt.Fprintf(w, "learn: %s · pass %d: asking %s…\n", eu.Label, pass, leg)
		var res captaincode.Result
		var err error
		if b.runWorkerFn != nil {
			_, res, err = b.runWorkerFn(leg, prompt, nil, nil)
		} else {
			say := func(st string) { fmt.Fprintf(w, "  %s\n", st) }
			res, err = ws.RunWorkerStreamHooks(leg, prompt, opencodePort, func(string) {}, say)
		}
		return res.Text, err
	}
	passes, err := captaincode.LearnLoop(eu, max, apply, run)
	for i, p := range passes {
		converged := i == len(passes)-1 && p.Journal == 0 && p.Memories == 0 && len(p.Edits) == 0
		fmt.Fprintln(w, captaincode.RenderLearnPass(eu.Label, i+1, len(passes), p, converged))
	}
	if err != nil {
		return passes, fmt.Errorf("%s: %w", eu.Label, err)
	}
	if !apply && len(passes) > 0 {
		fmt.Fprintf(w, "learn: %s - dry run, nothing written (both cursors unchanged)\n", eu.Label)
	}
	return passes, nil
}

// describeReindex is one rebuilt-index line, script names included.
func describeReindex(res captaincode.IndexResult) string {
	if res.Error != "" {
		return "index not rebuilt - " + res.Error
	}
	parts := []string{}
	for _, st := range res.Steps {
		if st.OK {
			parts = append(parts, fmt.Sprintf("ok %.1fs", st.DurationS))
		} else {
			parts = append(parts, fmt.Sprintf("FAILED (exit %d)", st.ReturnCode))
		}
	}
	return "dashboard data rebuilt: " + strings.Join(parts, ", ")
}
