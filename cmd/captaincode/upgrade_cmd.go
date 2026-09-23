package main

// `captain upgrade` - bring every agent CLI current in one command.
//
//	captain upgrade            # update claude, cursor-agent, opencode, captain; restart services when idle
//	captain upgrade --check    # versions only, touch nothing
//	captain upgrade --no-restart
//
// Each tool's NATIVE updater does the real work (claude update, cursor-agent
// update, opencode upgrade); captain orchestrates, reports before → after, and
// restarts the brain + opencode serve ONLY when nothing is running - an
// upgrade must never kill an in-flight turn.

import (
	"flag"

	"fmt"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// captainSourceDir is the captaincode checkout: the source `captain upgrade`
// rebuilds from, and where the opencode plugins live.
func captainSourceDir() string {
	if src := os.Getenv("CAPTAIN_SRC"); src != "" {
		return src
	}
	return filepath.Join(os.Getenv("HOME"), "Gits", "captaincode")
}

type upgradeOpts struct {
	checkOnly bool
	noRestart bool
	restart   func()      // stubbed in tests
	idle      func() bool // stubbed in tests
}

type upgradeTool struct {
	name       string
	updateArgs []string
}

var upgradeTools = []upgradeTool{
	{"claude", []string{"update"}},
	{"cursor-agent", []string{"update"}},
	{"opencode", []string{"upgrade"}},
	{"codex", []string{"update"}}, // the codex-cli leg's CLI (gpt-6-astra via codex exec)
}

// toolVersion asks a tool its version, through the same parser the pinned
// toolchain uses, so `captain upgrade` and `captain doctor` never disagree
// about what is installed.
func toolVersion(name string) string {
	out, err := exec.Command(name, "--version").CombinedOutput()
	if err != nil {
		return "?"
	}
	if v := captaincode.ParseToolVersion(string(out)); v != "" {
		return v
	}
	return "?"
}

func runUpgrade(w io.Writer, opts upgradeOpts) {
	for _, tool := range upgradeTools {
		if _, err := exec.LookPath(tool.name); err != nil {
			fmt.Fprintf(w, "%-13s not installed - skipped\n", tool.name+":")
			continue
		}
		before := toolVersion(tool.name)
		if opts.checkOnly {
			fmt.Fprintf(w, "%-13s %s\n", tool.name+":", before)
			continue
		}
		cmd := exec.Command(tool.name, tool.updateArgs...)
		cmd.Env = os.Environ()
		out, err := runBounded(cmd, 5*time.Minute)
		after := toolVersion(tool.name)
		switch {
		case err != nil:
			fmt.Fprintf(w, "%-13s %s - updater failed: %v (%s)\n", tool.name+":", before, err, truncate(out, 120))
		case after != before:
			fmt.Fprintf(w, "%-13s %s → %s\n", tool.name+":", before, after)
		default:
			fmt.Fprintf(w, "%-13s %s (already current)\n", tool.name+":", before)
		}
		if out != "" && strings.Contains(out, "updated") {
			fmt.Fprintf(w, "              %s\n", truncate(strings.TrimSpace(out), 100))
		}
	}

	// API-backed legs have no local binary: their client path is opencode
	// (covered above); their VERSION is a model pin, changed via env overrides
	// (CAPTAIN_GROK_MODEL etc.) or a captain rebuild - not an updater.
	fmt.Fprintln(w, "\nvia opencode - model pins, no binary to update:")
	for _, p := range captaincode.LegModelPins() {
		fmt.Fprintf(w, "  %-9s %s/%s\n", p.Leg+":", p.Provider, p.Model)
	}
	fmt.Fprintln(w, "  claude:   claude -p (binary above) · frontier: "+frontierModelName())
	fmt.Fprintln(w, "  codex-cli:    codex exec (binary above) · "+codexCLIModelName()+" at "+codexCLIEffortName()+" reasoning")
	fmt.Fprintln(w, "")
	printTiers(w)
	fmt.Fprintln(w, "")

	// captain itself: rebuild from source when we know where it lives.
	src := captainSourceDir()
	if _, err := os.Stat(filepath.Join(src, "cmd", "captaincode")); err != nil {
		fmt.Fprintf(w, "captain:      source not found at %s (set CAPTAIN_SRC) - skipped\n", src)
	} else if opts.checkOnly {
		// Same probe doctor uses, so the two commands cannot disagree about
		// which revision this binary is (ROADMAP M1.1).
		for _, s := range captaincode.ProbeSelf(captaincode.SelfProbe{SourceDir: src}) {
			v := s.Version
			if v == "" {
				v = "?"
			}
			fmt.Fprintf(w, "%-13s %s (%s)\n", s.Component+":", v, s.State)
		}
		fmt.Fprintf(w, "%-13s %s\n", "source:", src)
	} else {
		self, _ := os.Executable()
		if self == "" {
			self = filepath.Join(os.Getenv("HOME"), ".local", "bin", "captain")
		}
		cmd := exec.Command("go", "build", "-o", self, "./cmd/captaincode/")
		cmd.Dir = src
		if out, err := runBounded(cmd, 5*time.Minute); err != nil {
			fmt.Fprintf(w, "captain:      rebuild FAILED: %v (%s)\n", err, truncate(out, 160))
		} else {
			fmt.Fprintf(w, "captain:      rebuilt from %s\n", src)
		}
	}

	if opts.checkOnly || opts.noRestart {
		return
	}
	// Restart so the new binaries take over - but never under a running turn.
	if !opts.idle() {
		fmt.Fprintln(w, "\nservices: busy (a turn or workflow is running) - restart skipped; run `captain upgrade` again when idle, or restart manually")
		return
	}
	opts.restart()
	fmt.Fprintln(w, "\nservices: brain + opencode serve restarted - new versions active")
}

func runBounded(cmd *exec.Cmd, timeout time.Duration) (string, error) {
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return string(out), err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", fmt.Errorf("timed out after %s", timeout)
	}
}

// brainIdle: nothing running per the status endpoint AND the log's last line.
func brainIdle() bool {
	out, err := exec.Command("/bin/sh", "-c", "tail -1 /tmp/captain-brain.log 2>/dev/null").Output()
	if err == nil && strings.Contains(string(out), "running") {
		return false
	}
	resp, err := exec.Command("/bin/sh", "-c", "curl -s -m 3 http://127.0.0.1:14097/v1/workflow/status").Output()
	if err != nil {
		return true // no brain → nothing to interrupt
	}
	return !strings.Contains(string(resp), `"active":true`)
}

func restartServices() {
	_ = exec.Command("pkill", "-f", "opencode serve --port 14096").Run()
	_ = exec.Command("pkill", "-f", "captain brain").Run() // supervisor respawns with the new binary
}

func cmdUpgrade(args []string) {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	check := fs.Bool("check", false, "report versions only")
	noRestart := fs.Bool("no-restart", false, "update binaries but do not restart services")
	models := fs.Bool("models", false, "retarget model pins the ranking flagged (⇡ in the sidebar); with --apply writes them")
	apply := fs.Bool("apply", false, "with --models: write the new pins to the registry overlay")
	_ = fs.Parse(args)
	if *models {
		if _, err := captaincode.LoadRegistry(""); err != nil {
			fatal(err)
		}
		fmt.Printf("model pins vs the ranking (%s):\n", refreshPerfNow())
		upgradeModels(*apply)
		return
	}
	runUpgrade(os.Stdout, upgradeOpts{
		checkOnly: *check, noRestart: *noRestart,
		restart: restartServices, idle: brainIdle,
	})
}

// refreshPerfNow reads today's feed when a key is configured - `captain
// upgrade --models` exists to answer "is anything newer out", so it looks at
// the list of the day, not the brain's last one - and caches it for the brain
// and doctor. Without a key it reads the cache, then the compiled snapshot.
func refreshPerfNow() string {
	if key := aaKey(); key != "" {
		models, err := fetchAAModels(aaModelsURL, key)
		if err == nil {
			if err := captaincode.SetPerfModels(models); err != nil {
				fmt.Println("perf cache:", err)
			}
			return fmt.Sprintf("live feed, %d models", len(models))
		}
		fmt.Printf("perf feed: %v\n", err)
	}
	captaincode.LoadPerfCache()
	_, src, asOf := captaincode.PerfModels()
	return src + " from " + asOf
}

// printTiers lists the model each worker leg runs per band (tiers.go):
// cheap at low effort (/save, trivial work), quality in between, frontier
// under /frontier. A band that repeats the leg's own model has no sibling
// on that credential.
func printTiers(w io.Writer) {
	fmt.Fprintln(w, "tiers - cheap · quality · frontier (<LEG>_CHEAP_MODEL / _MODEL / _FRONTIER_MODEL pin one):")
	for _, s := range captaincode.Registry() {
		if s.Disabled || !captaincode.ServesTasks(s.ID) {
			continue
		}
		m := captaincode.TierModels(s.ID)
		fmt.Fprintf(w, "  %-10s %s · %s · %s\n", s.ID+":", m[captaincode.TierCheap], m[captaincode.TierQuality], m[captaincode.TierFrontier])
	}
}

func codexCLIModelName() string {
	if m := os.Getenv("CAPTAIN_CODEX_CLI_MODEL"); m != "" {
		return m
	}
	return "gpt-6-astra"
}

func codexCLIEffortName() string {
	if e := os.Getenv("CAPTAIN_CODEX_CLI_EFFORT"); e != "" {
		return e
	}
	return "xhigh"
}

func frontierModelName() string {
	return captaincode.FrontierModel() // the `opus` alias unless CAPTAIN_FRONTIER_MODEL pins one
}
