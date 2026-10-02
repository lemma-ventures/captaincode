package main

// `captain openshell` - run a team of tasks in NVIDIA OpenShell sandboxes.
// Every task gets its own sandbox through the task-mode pilot in
// examples/openshell-pilot. Captain re-checks each export on the host, lands
// the survivors through one integration candidate (a tool-less director
// rules on overlapping changes), and verifies the integrated tree in a fresh
// sandbox. The working tree is never written: the result is a patch.
//
//	captain openshell --team team.json --pilot examples/openshell-pilot --prepared /tmp/cc-prep
//	captain openshell --team team.json --pilot DIR --prepared DIR --concurrency 6 --director claude

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const openShellUsage = `usage: captain openshell --team <team.json> --pilot <dir> --prepared <dir> [flags]
       captain openshell --resume <run-directory>
       captain openshell profiles --pilot <dir> [--dry-run]
       captain openshell qualify --pilot <dir> --prepared <dir> --profile <name> [--runtime vm|docker]

  --resume       continue a saved sequence from verified stages; never replay in-flight work
  --team         the team spec: {"schema":1,"id":...,"tasks":[...]} (see examples/openshell-pilot/README.md)
  --pilot        the directory holding task.py and pilot.py
  --prepared     a prepare.py state: OpenShell binaries, the Shield, the Python environment
  --repo         the repository the tasks change (default .)
  --revision     the commit every sandbox snapshots (default HEAD)
  --concurrency  sandboxes at once, 1-8 (default 2)
  --director     who rules on tasks that changed the same files: none or claude (default none: they are not landed)
  --state-root   parent of the per-task states (default /tmp; the microVM socket path must stay short)
  --runtime      vm or docker (default vm)

  profiles  rebuild <pilot>/catalog.json from the registry's API-key legs and
            OpenRouter's public zero-data-retention endpoint list
  qualify   run the pilot's 18-check fixture on one profile three times, each
            with the one repair a task gets; only 3 of 3 full passes record it
            in <pilot>/qualified.json, and only then can a task use it`

func cmdOpenShell(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "profiles":
			cmdOpenShellProfiles(args[1:])
			return
		case "qualify":
			cmdOpenShellQualify(args[1:])
			return
		}
	}
	fs := flag.NewFlagSet("openshell", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, openShellUsage) }
	teamFile := fs.String("team", "", "")
	pilot := fs.String("pilot", "", "")
	prepared := fs.String("prepared", "", "")
	repo := fs.String("repo", ".", "")
	revision := fs.String("revision", "HEAD", "")
	concurrency := fs.Int("concurrency", 2, "")
	director := fs.String("director", "none", "")
	stateRoot := fs.String("state-root", "/tmp", "")
	runtime := fs.String("runtime", "vm", "")
	resume := fs.String("resume", "", "")
	fs.Parse(args)
	if *resume != "" {
		invalid := fs.NArg() > 0
		fs.Visit(func(f *flag.Flag) { invalid = invalid || f.Name != "resume" })
		if invalid {
			fatal(fmt.Errorf("openshell: --resume uses saved settings and cannot be combined with other arguments"))
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := captaincode.ResumeOpenShellSequence(ctx, *resume, func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "openshell: "+format+"\n", args...)
		})
		if result.Text != "" {
			fmt.Print(result.Text)
		}
		if err != nil {
			fatal(err)
		}
		return
	}
	if *teamFile == "" || *pilot == "" || *prepared == "" || fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	team, err := captaincode.LoadOpenShellTeam(*teamFile)
	if err != nil {
		fatal(err)
	}
	r := &captaincode.OpenShellRunner{Runtime: *runtime, Concurrency: *concurrency, DirectorName: *director,
		Log: func(format string, args ...any) { fmt.Fprintf(os.Stderr, "openshell: "+format+"\n", args...) }}
	for _, p := range []struct {
		dst *string
		src string
	}{{&r.Pilot, *pilot}, {&r.Prepared, *prepared}, {&r.StateRoot, *stateRoot}} {
		if *p.dst, err = filepath.Abs(p.src); err != nil {
			fatal(err)
		}
	}
	// task.py's microVM socket lives at <state>/vm/run/compute-driver.sock,
	// and darwin caps a socket path at 104 bytes.
	if *runtime == "vm" && len(r.StateRoot) > 56 {
		fatal(fmt.Errorf("openshell: --state-root %s is too long for the microVM socket path; use /tmp", r.StateRoot))
	}
	switch *director {
	case "none":
	case "claude":
		if _, err := exec.LookPath("claude"); err != nil {
			fatal(fmt.Errorf("openshell: --director claude needs the claude CLI on PATH"))
		}
		r.Director = captaincode.ToolLessClaudeDirector
	default:
		fatal(fmt.Errorf("openshell: --director must be none or claude"))
	}
	if r.Repo, r.Revision, err = openShellRepo(*repo, *revision); err != nil {
		fatal(err)
	}
	if out, _ := exec.Command("git", "-C", r.Repo, "status", "--porcelain").Output(); len(out) > 0 {
		fmt.Fprintf(os.Stderr, "openshell: note: uncommitted changes are not in the snapshot; every sandbox sees %s\n", r.Revision[:12])
	}
	base := filepath.Join(captainHome(), "openshell")
	if err := os.MkdirAll(base, 0o700); err != nil {
		fatal(err)
	}
	if r.RunDir, err = os.MkdirTemp(base, time.Now().UTC().Format("20060102T150405Z")+"-"+team.ID+"-"); err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "openshell: team %s: %d task(s), %d at once, %s runtime, revision %s, director %s\n",
		team.ID, len(team.Tasks), r.Concurrency, r.Runtime, r.Revision[:12], *director)
	run, err := r.RunTeam(ctx, team)
	printOpenShellRun(run, r.RunDir)
	if err != nil {
		fatal(err)
	}
	if run.Verdict != "pass" {
		os.Exit(1)
	}
}

// openShellRepo resolves the repository's top level and pins the revision to
// a commit sha.
func openShellRepo(repo, revision string) (string, string, error) {
	return captaincode.ResolveOpenShellRepo(context.Background(), repo, revision)
}

func printOpenShellRun(run *captaincode.OpenShellRun, dir string) {
	if run == nil {
		return
	}
	if budget := run.AttemptBudget; budget != nil && budget.Limit > 0 {
		fmt.Printf("attempt admission: %d worst-case slots / %d cap (not measured usage)\n", budget.Required, budget.Limit)
	}
	var taskSeconds, startup []float64
	passed := 0
	var shield captaincode.OpenShellShield
	fmt.Println()
	for _, res := range run.Tasks {
		if res == nil {
			continue
		}
		detail := ""
		if rep := res.Report; rep != nil {
			if g, ok := rep.Timings["gateway_ready"]; ok {
				if s, ok := rep.Timings["sandbox_create"]; ok {
					startup = append(startup, g+s)
				}
			}
			detail = "attempts unknown"
			if rep.WorkerAttempts != nil {
				detail = fmt.Sprintf("%d attempt(s)", *rep.WorkerAttempts)
			}
			if rep.Shield != nil {
				shield.Requests += rep.Shield.Requests
				shield.Blocked += rep.Shield.Blocked
				shield.SecretsMasked += rep.Shield.SecretsMasked
				shield.IdentitiesMasked += rep.Shield.IdentitiesMasked
				if len(rep.Shield.ServedBy) > 0 {
					detail += ", served by " + strings.Join(rep.Shield.ServedBy, "/")
				}
			}
		}
		if res.Outcome != captaincode.OpenShellFailed {
			passed++
		}
		if res.Seconds > 0 {
			taskSeconds = append(taskSeconds, res.Seconds)
		}
		if res.Error != "" {
			detail += " - " + res.Error
		}
		fmt.Printf("  %-24s %-9s %6.1fs  %s\n", res.Task, res.Outcome, res.Seconds, terminalSafe(detail, 160))
	}
	minutes := run.Seconds / 60
	fmt.Printf("\n%d/%d task(s) passed in their sandbox in %.1fs wall", passed, len(run.Tasks), run.Seconds)
	if minutes > 0 {
		fmt.Printf(" (%.2f tasks/min)", float64(passed)/minutes)
	}
	fmt.Printf("; median task %.1fs, median sandbox startup %.1fs\n", median(taskSeconds), median(startup))
	// The pilot summarizes Shield only in the report of a task that passed; a
	// failed task's requests are in its evidence directory's shield-audit.jsonl.
	fmt.Printf("Shield, tasks that passed: %d model request(s), %d blocked, %d secret(s) and %d identit(ies) masked\n",
		shield.Requests, shield.Blocked, shield.SecretsMasked, shield.IdentitiesMasked)
	if p := run.Provenance; p != nil {
		fmt.Printf("built from: captain %s, shield %s\n", describeBuild(p.Captain), describeBuild(p.Shield))
	}
	for _, ru := range run.Rulings {
		if ru.Winner != "" {
			fmt.Printf("ruling on %s: %s lands, %s dropped - %s\n", strings.Join(ru.Files, ", "), ru.Winner,
				strings.Join(ru.Dropped, ", "), terminalSafe(ru.Reason, 200))
		} else {
			fmt.Printf("ruling on %s: none landed (%s) - %s\n", strings.Join(ru.Files, ", "),
				strings.Join(ru.Dropped, ", "), terminalSafe(ru.Error, 200))
		}
	}
	if in := run.Integrated; in != nil {
		state := "verified"
		if !in.Passed {
			state = "FAILED - " + terminalSafe(in.Error, 200)
		}
		fmt.Printf("integrated: %d file(s), %s in a fresh sandbox in %.1fs\n", len(in.ChangedFiles), state, in.Seconds)
	}
	fmt.Printf("verdict: %s", run.Verdict)
	if run.Error != "" {
		fmt.Printf(" - %s", terminalSafe(run.Error, 200))
	}
	fmt.Printf("\nrun record: %s\n", filepath.Join(dir, "run.json"))
	if run.Verdict == "pass" && run.Integrated != nil {
		if len(run.Integrated.ChangedFiles) == 0 {
			fmt.Println("verified unchanged snapshot; nothing to apply")
		} else {
			fmt.Printf("apply with: git -C %s apply %s\n", run.Repo, run.Integrated.Patch)
		}
	}
}

// describeBuild names the commit a binary was built from, and says when that
// commit does not name the code because the checkout had uncommitted changes.
func describeBuild(b captaincode.OpenShellBuild) string {
	rev := terminalSafe(b.Revision[:min(12, len(b.Revision))], 12)
	switch {
	case rev == "":
		return "an unknown revision"
	case b.Modified:
		return rev + " with uncommitted changes"
	}
	return rev
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	if n := len(sorted); n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[len(sorted)/2]
}

// terminalSafe drops control and bidi characters from text a sandbox may
// have shaped, so it cannot move the cursor or reorder what the user reads.
func terminalSafe(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}

// cmdOpenShellProfiles rebuilds the pilot's catalog from the registry. The
// pilot keeps its hand-kept profiles beside it; `python3 profiles.py` lists
// both with their qualification.
func cmdOpenShellProfiles(args []string) {
	fs := flag.NewFlagSet("openshell profiles", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, openShellUsage) }
	pilot := fs.String("pilot", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	fs.Parse(args)
	if *pilot == "" || fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(*pilot, "profiles.py")); err != nil {
		fatal(fmt.Errorf("openshell: --pilot %s has no profiles.py", *pilot))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	endpoints, err := captaincode.FetchOpenRouterZDR(ctx, captaincode.OpenRouterZDRURL)
	if err != nil {
		fatal(err)
	}
	catalog := captaincode.OpenShellRegistryCatalog(endpoints, time.Now())
	names := make([]string, 0, len(catalog.Profiles))
	for name := range catalog.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		p := catalog.Profiles[name]
		fmt.Printf("%-48s %-32s %-28s $%.2f/$%.2f\n", name, p.Model, p.Route, p.Ceiling[0], p.Ceiling[1])
	}
	for _, s := range catalog.Skipped {
		fmt.Printf("skipped  %s\n", s)
	}
	if *dryRun {
		fmt.Fprintf(os.Stderr, "openshell: %d profile(s) from %d ZDR endpoint(s); not written (--dry-run)\n", len(names), len(endpoints))
		return
	}
	path, err := captaincode.WriteOpenShellCatalog(*pilot, catalog)
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "openshell: %d profile(s) from %d ZDR endpoint(s) written to %s; none is selectable until `captain openshell qualify` passes on it\n",
		len(names), len(endpoints), path)
}

// cmdOpenShellQualify runs the pilot's fixture on one profile.
func cmdOpenShellQualify(args []string) {
	fs := flag.NewFlagSet("openshell qualify", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, openShellUsage) }
	pilot := fs.String("pilot", "", "")
	prepared := fs.String("prepared", "", "")
	profile := fs.String("profile", "", "")
	stateRoot := fs.String("state-root", "/tmp", "")
	runtime := fs.String("runtime", "vm", "")
	fs.Parse(args)
	if *pilot == "" || *prepared == "" || *profile == "" || fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	r := &captaincode.OpenShellRunner{Runtime: *runtime}
	var err error
	for _, p := range []struct {
		dst *string
		src string
	}{{&r.Pilot, *pilot}, {&r.Prepared, *prepared}, {&r.StateRoot, *stateRoot}} {
		if *p.dst, err = filepath.Abs(p.src); err != nil {
			fatal(err)
		}
	}
	if *runtime == "vm" && len(r.StateRoot) > 56 {
		fatal(fmt.Errorf("openshell: --state-root %s is too long for the microVM socket path; use /tmp", r.StateRoot))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "openshell: qualifying %s with the pilot fixture (%s runtime)\n", *profile, *runtime)
	runs, err := r.Qualify(ctx, *profile, os.Stderr)
	for i, run := range runs {
		if run.Report == nil {
			fmt.Printf("profile %s run %d: no report (%v), state %s\n", *profile, i+1, run.Err, run.State)
			continue
		}
		passed := 0
		for _, c := range run.Report.Checks {
			if c.Verdict == "pass" {
				passed++
			}
		}
		attempts := 0
		if run.Report.WorkerAttempts != nil {
			attempts = *run.Report.WorkerAttempts
		}
		fmt.Printf("profile %s run %d: verdict %s, %d/%d checks passed, %d worker attempt(s), state %s\n",
			*profile, i+1, run.Report.Verdict, passed, len(run.Report.Checks), attempts, run.State)
	}
	if err != nil {
		fatal(err)
	}
}
