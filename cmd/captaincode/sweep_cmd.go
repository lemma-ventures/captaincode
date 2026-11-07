package main

// `captain sweep` - remove worktrees and branches that are already merged
// (pkg sweep.go). A dry run by default; a daily LaunchAgent runs it with
// --apply on each Mac.
//
//	captain sweep                      list what would go, under ~/Gits
//	captain sweep --apply              remove it, and log to ~/.captaincode/sweep.jsonl
//	captain sweep --root DIR           repositories directly under DIR (repeatable)
//	captain sweep --repo DIR           one repository
//	captain sweep --idle 2h            keep worktrees git touched more recently
//	captain sweep --verbose            also list every worktree kept, with the reason

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type sweepRoots []string

func (r *sweepRoots) String() string     { return strings.Join(*r, ",") }
func (r *sweepRoots) Set(v string) error { *r = append(*r, v); return nil }

func cmdSweep(args []string) {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	apply := fs.Bool("apply", false, "remove what is merged; without it nothing changes")
	idle := fs.Duration("idle", 2*time.Hour, "keep worktrees git touched within this long")
	verbose := fs.Bool("verbose", false, "list kept worktrees with their reason")
	var roots, repos sweepRoots
	fs.Var(&roots, "root", "a folder whose direct subfolders are repositories (default ~/Gits)")
	fs.Var(&repos, "repo", "one repository")
	_ = fs.Parse(args)
	if len(roots) == 0 && len(repos) == 0 {
		home, _ := os.UserHomeDir()
		roots = append(roots, filepath.Join(home, "Gits"))
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			fatal(err)
		}
		for _, e := range entries {
			// A .git directory, not a .git file: a linked worktree is swept
			// from its main repository.
			if st, err := os.Stat(filepath.Join(root, e.Name(), ".git")); err == nil && st.IsDir() {
				repos = append(repos, filepath.Join(root, e.Name()))
			}
		}
	}
	sort.Strings(repos)
	cwds := processCwds()
	o := captaincode.SweepOptions{Apply: *apply, MinIdle: *idle, Busy: func(p string) bool {
		for _, c := range cwds {
			if c == p || strings.HasPrefix(c, p+"/") {
				return true
			}
		}
		return false
	}}
	var log *os.File
	if *apply {
		_ = os.MkdirAll(captainHome(), 0o755)
		if f, err := os.OpenFile(filepath.Join(captainHome(), "sweep.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			log = f
			defer f.Close()
		}
	}
	verb := "would remove"
	if *apply {
		verb = "removed"
	}
	at := time.Now().UTC().Format(time.RFC3339)
	for _, repo := range repos {
		acts, err := captaincode.SweepRepo(repo, o)
		if err != nil {
			fmt.Fprintf(os.Stderr, "captain sweep: %v\n", err)
			continue
		}
		var wt, br, kept, failed int
		for _, a := range acts {
			switch {
			case a.Err != "":
				failed++
				fmt.Printf("  %s %s: %s\n", a.Kind, a.Target, a.Err)
			case a.Skip != "":
				kept++
				if *verbose {
					fmt.Printf("  keep %s: %s\n", a.Target, a.Skip)
				}
			case a.Kind == "worktree":
				wt++
			default:
				br++
			}
			if log != nil && a.Skip == "" {
				line, _ := json.Marshal(struct {
					At string `json:"at"`
					captaincode.SweepAction
				}{at, a})
				fmt.Fprintln(log, string(line))
			}
		}
		if wt+br+kept+failed > 0 {
			fmt.Printf("%s: %s %d worktrees and %d branches; kept %d worktrees", filepath.Base(repo), verb, wt, br, kept)
			if failed > 0 {
				fmt.Printf("; %d failed", failed)
			}
			fmt.Println()
		}
	}
	if !*apply {
		fmt.Println("dry run: nothing changed; rerun with --apply")
	}
}

// processCwds lists every process's working directory, as lsof prints it.
func processCwds() []string {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "n").Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "n"); ok && p != "" {
			cwds = append(cwds, p)
		}
	}
	return cwds
}
