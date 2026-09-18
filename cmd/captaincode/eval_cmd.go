package main

// `captain eval` - the M1.3 harness driver. One verb per moment of the
// roadmap's evaluation protocol: freeze the fixtures (validate), prove they
// measure something (verify), confirm the cost of 144 executions BEFORE
// running them (plan), run, read the result (report) and settle the blinded
// verdicts it left open (review).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdEval(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, evalUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "validate":
		evalValidate(args[1:])
	case "verify":
		evalVerify(args[1:])
	case "plan":
		evalPlan(args[1:])
	case "run":
		evalRun(args[1:])
	case "report":
		evalReport(args[1:])
	case "review":
		evalReview(args[1:])
	default:
		fmt.Fprintln(os.Stderr, evalUsage)
		os.Exit(2)
	}
}

const evalUsage = `usage:
  captain eval validate <suite.json>          check the fixtures are reproducible
  captain eval verify   <suite.json> [flags]  prove each fixture fails before an arm runs
  captain eval plan     <suite.json>          list the executions the suite would run
  captain eval run      <suite.json> [flags]  run them serially and write a result
  captain eval report   <result.json>         aggregate a finished run
  captain eval review   <result.json> [flags] list or record blinded verdicts

review flags:
  --accept <key>    record acceptance for task/alias/repeat (e.g. t1/arm-A/1)
  --reject <key>    record rejection for the same
  --by <name>       the reviewer; required with a verdict, and recorded with it
  --note <text>     why; printed in the result record beside the verdict
  --amend           replace an existing verdict instead of refusing

verify flags:
  --work <dir>      where baseline snapshots are made (default a temp dir)
  --only <ids>      comma-separated task ids to restrict to

run flags:
  --out <path>      where to write the result record (default eval-result.json)
  --work <dir>      where snapshots are made (default a temp dir)
  --only <ids>      comma-separated task ids to restrict to
  --json            print the result record instead of a progress log`

func evalValidate(args []string) {
	s := mustLoadSuite(args)
	fmt.Printf("%s: %d tasks x %d arms x %d repeats = %d executions\n", s.Name, len(s.Tasks), len(s.Arms), s.Repeats, len(s.Plan()))
	for _, a := range s.Arms {
		fmt.Printf("  arm %-14s %-11s blinded as %s\n", a.Name, a.Kind, a.Alias)
	}
}

// evalVerify is the cheap step that must happen before the expensive one: it
// clones each task's pinned revision and runs the checks against the untouched
// tree. A fixture whose checks already pass there is accepted by every arm
// without any work being done, which inflates all four acceptance rates
// equally and is invisible in the report. Exits non-zero so a suite cannot be
// run from a script that ignored the finding.
func evalVerify(args []string) {
	s := mustLoadSuite(args)
	work, only := "", []string(nil)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--work":
			i++
			if i < len(args) {
				work = args[i]
			}
		case "--only":
			i++
			if i < len(args) {
				only = strings.Split(args[i], ",")
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baselines, err := captaincode.VerifySuite(ctx, s, work, only)
	if err != nil {
		fatal(err)
	}
	bad := 0
	for _, b := range baselines {
		fmt.Printf("%-24s %-14s %5.1fs  %s\n", b.TaskID, b.Status, float64(b.DurationMs)/1000, b.Reason)
		for _, c := range b.Checks {
			state := "fails"
			switch {
			case !c.Ran:
				state = "cannot run"
			case c.Passed:
				state = "passes"
			}
			fmt.Printf("    check %-20s %s at baseline (exit %d, wants %d)\n", c.Name, state, c.Exit, c.Want)
		}
		for _, g := range b.Absent {
			fmt.Printf("    guard %-20s does not exist at the pinned revision; it forbids nothing\n", g)
		}
		if !b.Runnable() {
			bad++
		}
	}
	fmt.Printf("\n%d fixture(s), %d not runnable\n", len(baselines), bad)
	if bad > 0 {
		os.Exit(1)
	}
}

// evalPlan prints the frozen order. The roadmap asks for randomized run order
// AND a reproducible report; printing the seeded order is how both hold.
func evalPlan(args []string) {
	s := mustLoadSuite(args)
	fmt.Printf("# %s, seed %d, %d executions in this order\n", s.Name, s.Seed, len(s.Plan()))
	for i, p := range s.Plan() {
		fmt.Printf("%4d  %-24s %-14s repeat %d\n", i+1, p.TaskID, p.Arm, p.Repeat)
	}
}

func evalRun(args []string) {
	s := mustLoadSuite(args)
	out, work, only, asJSON := "eval-result.json", "", []string(nil), false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--out":
			i++
			if i < len(args) {
				out = args[i]
			}
		case "--work":
			i++
			if i < len(args) {
				work = args[i]
			}
		case "--only":
			i++
			if i < len(args) {
				only = strings.Split(args[i], ",")
			}
		case "--json":
			asJSON = true
		}
	}

	ledger, err := captaincode.LoadLedger()
	if err != nil {
		fatal(err)
	}
	// Ctrl-C stops admitting further executions and still writes what ran:
	// an interrupted evaluation must not lose the hours it already spent.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := captaincode.EvalOptions{
		WorkDir: work,
		Ledger:  ledger,
		Only:    only,
		Build:   captaincode.ProbeSelf(captaincode.SelfProbe{SourceDir: captainSourceDir()}),
	}
	if !asJSON {
		opts.Progress = func(e captaincode.EvalExecution) {
			fmt.Printf("%-24s %-14s #%d  %-14s %5.1fs  %s\n", e.TaskID, e.Arm, e.Repeat, e.Status, float64(e.DurationMs)/1000, e.Reason)
		}
	}
	res, err := captaincode.RunSuite(ctx, s, opts)
	if err != nil {
		fatal(err)
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		fatal(err)
	}
	if asJSON {
		fmt.Println(string(data))
		return
	}
	fmt.Printf("\nwrote %s\n\n", out)
	captaincode.WriteReport(os.Stdout, res.Report())
	if pending := res.PendingReviews(); len(pending) > 0 {
		fmt.Printf("\n%d execution(s) await blinded review (task/alias/repeat):\n  %s\n", len(pending), strings.Join(pending, "\n  "))
	}
}

func evalReport(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, evalUsage)
		os.Exit(2)
	}
	res := mustLoadResult(args[0])
	for _, b := range res.Build {
		if !b.Reproducible() {
			fmt.Printf("# %s: %s %s - %s\n", b.Component, b.State, b.Version, b.Detail)
		}
	}
	captaincode.WriteReport(os.Stdout, res.Report())
}

// evalReview is the path back from a blinded verdict into the result record.
// With no verdict flag it lists what is owed, by alias: the reviewer must not
// be able to see which worker produced the change they are judging.
func evalReview(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, evalUsage)
		os.Exit(2)
	}
	path := args[0]
	verdict, key, by, note, amend := "", "", "", "", false
	for i := 1; i < len(args); i++ {
		next := func() string {
			i++
			if i < len(args) {
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--accept":
			verdict, key = captaincode.ReviewAccept, next()
		case "--reject":
			verdict, key = captaincode.ReviewReject, next()
		case "--by":
			by = next()
		case "--note":
			note = next()
		case "--amend":
			amend = true
		}
	}

	res := mustLoadResult(path)
	if verdict == "" {
		pending := res.Pending()
		if len(pending) == 0 {
			fmt.Println("no executions await review")
			return
		}
		fmt.Printf("# %d execution(s) await a blinded verdict\n", len(pending))
		for _, p := range pending {
			fmt.Printf("\n%s  (%s)\n  prompt   %s\n  snapshot %s\n  changed  %s\n",
				p.Key(), p.Family, p.Prompt, p.Dir, strings.Join(p.Changed, " "))
		}
		fmt.Printf("\nrecord one with:\n  captain eval review %s --accept <key> --by <you> --note <why>\n", path)
		return
	}

	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		fatal(fmt.Errorf("key %q is not task/alias/repeat", key))
	}
	repeat, err := strconv.Atoi(parts[2])
	if err != nil {
		fatal(fmt.Errorf("key %q: repeat %q is not a number", key, parts[2]))
	}
	if err := res.RecordReview(parts[0], parts[1], repeat, verdict, by, note, amend); err != nil {
		fatal(err)
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("%s: %sed by %s\n", key, verdict, by)
	if left := res.PendingReviews(); len(left) > 0 {
		fmt.Printf("%d still pending\n", len(left))
	}
}

func mustLoadResult(path string) *captaincode.EvalResult {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	var res captaincode.EvalResult
	if err := json.Unmarshal(data, &res); err != nil {
		fatal(err)
	}
	if res.Version != captaincode.EvalSuiteVersion {
		fatal(fmt.Errorf("%s: result version %d, this build understands %d", path, res.Version, captaincode.EvalSuiteVersion))
	}
	return &res
}

func mustLoadSuite(args []string) *captaincode.EvalSuite {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, evalUsage)
		os.Exit(2)
	}
	s, err := captaincode.LoadSuite(args[0])
	if err != nil {
		fatal(err)
	}
	return s
}
