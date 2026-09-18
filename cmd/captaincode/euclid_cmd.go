package main

// `captain euclid` - brains from the shell (MM38).
//
//	captain euclid init            scaffold the MAIN brain (~/.euclid)
//	captain euclid init --repo     also scaffold <cwd repo>/.euclid (shared) + developers/<me>/
//	captain euclid status          read set, write brain, journal counts (asks the brain, falls back to local)
//	captain euclid check           the launch check: main brain + local brain, filesystem only (the launcher prints it)
//	captain euclid distill [--apply]
//	                               propose register edits from the journal via the running brain;
//	                               --apply writes them to your write brain (never the shared repo brain)
//	captain euclid link <repo> [--related]
//	                               declare a link (depends_on by default) from the current repo
//	captain euclid links [--apply] list resolved links and proposals from manifests + journal
//	captain euclid share [--apply] make the repo brain shareable (euclid_share_cmd.go)
//	captain euclid fold [--dry-run] fold promoted notes into the shared brain (the CI step)
//	captain euclid mcp             stdio MCP server over the brains (registered by init)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdEuclid(args []string) {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "init":
		repoToo := false
		for _, a := range args[1:] {
			if a == "--repo" {
				repoToo = true
			}
		}
		cmdEuclidInit(repoToo)
	case "check":
		fmt.Print(captaincode.RenderBrainChecks(captaincode.CheckBrains(euclidCwd())))
	case "share":
		cmdEuclidShare(args[1:])
	case "fold":
		cmdEuclidFold(args[1:])
	case "ensure":
		cmdEuclidEnsure()
	case "probes":
		// The benchmark gold sets for an existing brain (a bootstrap writes
		// them for a new one): one model call through the worker serve,
		// every probe verified before it is written.
		cwd := euclidCwd()
		repo := captaincode.RepoRoot(cwd)
		if repo == "" {
			fatal(fmt.Errorf("%s is not inside a git repository", cwd))
		}
		root := filepath.Join(repo, ".euclid")
		if !isDirPath(root) {
			fatal(fmt.Errorf("no repo brain at %s - run `captain euclid init --repo`", root))
		}
		force := false
		for _, a := range args[1:] {
			if a == "--force" {
				force = true
			}
		}
		if isDirPath(filepath.Join(root, "probes")) && !force {
			fmt.Println("probes already exist in " + filepath.Join(root, "probes") + " (--force regenerates)")
			return
		}
		b := &brain{}
		brain := captaincode.EuclidBrain{Root: root, Kind: "repo", Label: "repo:" + filepath.Base(repo)}
		prompt, docs := captaincode.ProbePrompt(brain, repo)
		if len(docs) == 0 {
			fatal(fmt.Errorf("nothing to author probes from: no README/AGENTS/docs in %s", repo))
		}
		leg := b.distillLeg()
		fmt.Printf("authoring probes for %s on %s from %d doc(s)…\n", brain.Label, leg, len(docs))
		res, err := captaincode.Workspace{Dir: repo}.RunWorkerStreamHooks(leg, prompt, opencodePort, nil, nil)
		if err != nil {
			fatal(err)
		}
		ps, err := captaincode.ParseProbes(res.Text, brain, repo)
		if err != nil {
			fatal(err)
		}
		written, err := captaincode.WriteProbes(brain, ps)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%d orientation, %d recall, %d composition probes kept (%d dropped as unverifiable)\n", len(ps.Orientation), len(ps.Recall), len(ps.Composition), ps.Dropped)
		for _, w := range written {
			fmt.Println("  wrote " + w)
		}
		if len(written) > 0 {
			fmt.Println("`captain euclid reindex` rebuilds the dashboard with them")
		}
	case "corpus":
		repo := captaincode.RepoRoot(euclidCwd())
		if repo == "" {
			fatal(fmt.Errorf("%s is not inside a git repository", euclidCwd()))
		}
		roots, exts := captaincode.DetectCorpus(repo)
		fmt.Printf("roots: %s\ncode_exts: %s\n", strings.Join(roots, ", "), strings.Join(exts, " "))
		for _, a := range args[1:] {
			if a == "--apply" {
				if _, _, err := captaincode.ConfigureCorpus(repo); err != nil {
					fatal(err)
				}
				fmt.Println("written to .euclid/euclid.yml - `captain euclid reindex` rebuilds the index")
			}
		}
	case "reindex":
		cwd := euclidCwd()
		roots := []string{captaincode.MainBrainPath()}
		if repo := captaincode.RepoRoot(cwd); repo != "" && isDirPath(filepath.Join(repo, ".euclid")) {
			roots = append(roots, filepath.Join(repo, ".euclid"))
		}
		for _, r := range roots {
			res := captaincode.Reindex(r, 0)
			fmt.Printf("%s: ok=%v\n", r, res.OK)
			for _, st := range res.Steps {
				fmt.Printf("  %-16s %.1fs exit %d\n", st.Step, st.DurationS, st.ReturnCode)
				if !st.OK {
					fmt.Println("  " + strings.ReplaceAll(st.Tail, "\n", "\n  "))
				}
			}
			if res.Error != "" {
				fmt.Println("  " + res.Error)
			}
		}
	case "status":
		if st, ok := brainEuclidStatus(); ok {
			fmt.Print(renderEuclidStatus(st))
			return
		}
		b := &brain{}
		fmt.Print(renderEuclidStatus(b.euclidStatusNow(defaultWorkspace())))
	case "distill", "bootstrap":
		apply := false
		for _, a := range args[1:] {
			if a == "--apply" || a == "apply" {
				apply = true
			}
		}
		body, _ := json.Marshal(map[string]any{"apply": apply})
		resp, err := (&http.Client{Timeout: 10 * time.Minute}).Post(brainURL()+"/v1/euclid/"+args[0], "application/json", bytes.NewReader(body))
		if err != nil {
			fatal(fmt.Errorf("the brain is not running (%v) - distillation needs a leg; start `captain brain` first", err))
		}
		defer resp.Body.Close()
		var out struct {
			Report string `json:"report"`
			Error  struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			fatal(fmt.Errorf("%s", out.Error.Message))
		}
		fmt.Print(out.Report)
	case "link":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain euclid link <repo|path|url> [--related]"))
		}
		kind := "depends_on"
		for _, a := range args[2:] {
			if a == "--related" {
				kind = "related"
			}
		}
		repo := captaincode.RepoRoot(euclidCwd())
		where, err := captaincode.AddLink(repo, args[1], kind)
		if err != nil {
			fatal(err)
		}
		resolved := captaincode.ResolveLinkTarget(args[1], repo)
		if resolved == "" {
			fmt.Printf("declared %s → %s (%s) in %s - no local checkout with a brain found yet; it will apply once one exists\n", filepath.Base(repo), args[1], kind, where)
		} else {
			fmt.Printf("declared %s → %s (%s) in %s\n", filepath.Base(repo), resolved, kind, where)
		}
	case "links":
		apply := false
		for _, a := range args[1:] {
			if a == "--apply" {
				apply = true
			}
		}
		cmdEuclidLinks(apply)
	case "mcp":
		cmdEuclidMCP()
	default:
		fatal(fmt.Errorf("usage: captain euclid [init [--repo]|ensure|reindex|corpus [--apply]|probes [--force]|status|check|distill [--apply]|bootstrap [--apply]|link <repo>|links [--apply]|mcp]"))
	}
}

func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// cmdEuclidEnsure is the launch-time step, run after the brain is up: every
// brain the folder reads exists and is freshly indexed. A repo brain scaffolded here is bootstrapped from the
// repo's docs through the brain, then indexed again so the dashboard shows
// the filled registers. Nothing here blocks the TUI.
func cmdEuclidEnsure() {
	cwd := euclidCwd()
	rep := captaincode.EnsureBrains(cwd, os.Stdout)
	if rep.Local == "" {
		return
	}
	freshlyCreated := false
	for _, l := range rep.Lines {
		if strings.Contains(l, "created "+rep.Local) {
			freshlyCreated = true
		}
	}
	if !freshlyCreated {
		return
	}
	repo := filepath.Dir(rep.Local)
	bootstrapNow(repo)
	res := captaincode.Reindex(rep.Local, 0)
	fmt.Printf("euclid local brain: reindexed after bootstrap (ok=%v)\n", res.OK)
	if changed, err := captaincode.EnsureEuclidMCP(captaincode.OpencodeConfigPath(), captainExecutable()); err == nil && changed {
		fmt.Println("euclid: registered the euclid MCP server in opencode.jsonc")
	}
}

func cmdEuclidLinks(apply bool) {
	cwd := euclidCwd()
	repo := captaincode.RepoRoot(cwd)
	if repo == "" {
		fatal(fmt.Errorf("%s is not inside a git repository", cwd))
	}
	fmt.Printf("links for %s:\n", repo)
	linked := captaincode.LinkedBrains(repo)
	if len(linked) == 0 {
		fmt.Println("  (none declared, or none resolve to a local checkout with a brain)")
	}
	for _, b := range linked {
		fmt.Printf("  %-6s %.1f  %s\n", b.Kind, b.Weight, b.Root)
	}
	var journal []captaincode.JournalEntry
	if wb, ok := captaincode.WriteBrain(cwd); ok {
		journal, _ = captaincode.ReadJournal(wb, time.Time{})
	}
	props := captaincode.ProposeLinks(repo, journal)
	if len(props) == 0 {
		fmt.Println("proposals: none (manifests and the journal point at no other checkout with a brain)")
		return
	}
	fmt.Println("proposals:")
	for _, p := range props {
		fmt.Printf("  %-10s %s - %s\n", p.Kind, p.Target, p.Why)
	}
	if !apply {
		fmt.Println("re-run with --apply to declare them (or `captain euclid link <repo>` one by one)")
		return
	}
	for _, p := range props {
		if where, err := captaincode.AddLink(repo, p.Target, p.Kind); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", p.Target, err)
		} else {
			fmt.Printf("  ✓ %s → %s\n", p.Target, where)
		}
	}
}

func brainURL() string {
	if v := os.Getenv("CAPTAIN_BRAIN_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:14097"
}

func brainEuclidStatus() (euclidStatus, bool) {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(brainURL() + "/v1/euclid/status")
	if err != nil || resp.StatusCode != 200 {
		return euclidStatus{}, false
	}
	defer resp.Body.Close()
	var st euclidStatus
	if json.NewDecoder(resp.Body).Decode(&st) != nil {
		return euclidStatus{}, false
	}
	return st, true
}

func cmdEuclidInit(repoToo bool) {
	main := captaincode.MainBrainPath()
	created, err := captaincode.Scaffold(main, "main")
	if err != nil {
		fatal(err)
	}
	report := func(root string, files []string) {
		if len(files) == 0 {
			fmt.Printf("  ✓ %s already initialized\n", root)
			return
		}
		fmt.Printf("  • %s: created %d files\n", root, len(files))
	}
	fmt.Println("Euclid main brain (never committed):")
	report(main, created)
	handle := captaincode.DeveloperHandle()
	if _, err := os.Stat(filepath.Join(main, "handle")); err != nil {
		_ = os.WriteFile(filepath.Join(main, "handle"), []byte(handle+"\n"), 0o644)
	}
	fmt.Printf("  handle: %s (edit %s to change)\n", handle, filepath.Join(main, "handle"))
	if repoToo {
		cwd := euclidCwd()
		repo := captaincode.RepoRoot(cwd)
		if repo == "" {
			fatal(fmt.Errorf("%s is not inside a git repository", cwd))
		}
		shared := filepath.Join(repo, ".euclid")
		fmt.Printf("Repo brain (local, gitignored) in %s:\n", repo)
		files, err := captaincode.Scaffold(shared, "repo")
		if err != nil {
			fatal(err)
		}
		report(shared, files)
		dev := filepath.Join(shared, "developers", handle)
		files, err = captaincode.Scaffold(dev, "developer")
		if err != nil {
			fatal(err)
		}
		report(dev, files)
		fmt.Println("  local only — gitignored; recreate with `captain euclid init --repo` on a fresh clone.")
		// A brain that knows nothing about its repo helps nobody: fill VISION,
		// MAP and BRAIN from the repo's own docs. Needs the brain for the model
		// call; when it is down, say how to do it later rather than fail init.
		if len(files) > 0 || len(created) > 0 {
			bootstrapNow(repo)
		}
	}
	if changed, err := captaincode.EnsureEuclidMCP(captaincode.OpencodeConfigPath(), captainExecutable()); err != nil {
		fmt.Fprintf(os.Stderr, "opencode.jsonc: %v\n", err)
	} else if changed {
		fmt.Println("  • registered the euclid MCP server in opencode.jsonc (euclid_search/read_register/recent_runs/status) - recycle `opencode serve` and relaunch the TUI to load it")
	}
	fmt.Println("\nCaptain now injects Euclid orientation into worker prompts for this project and journals every run into your write brain (the brain reads brains live - no restart needed).")
}

// captainExecutable is the absolute path opencode should spawn for the MCP
// server (the serve process may not share this shell's PATH).
func captainExecutable() string {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			return real
		}
		return exe
	}
	return "captain"
}

// bootstrapNow asks the running brain to fill a fresh repo brain's strategy
// registers from the repository's docs. Best effort: init must not depend on
// the brain being up.
func bootstrapNow(repo string) {
	body, _ := json.Marshal(map[string]any{"apply": true})
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Post(brainURL()+"/v1/euclid/bootstrap", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("  • bootstrap skipped (brain not running): later, from %s: captain euclid bootstrap --apply\n", repo)
		return
	}
	defer resp.Body.Close()
	var out struct {
		Report string `json:"report"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		fmt.Printf("  • bootstrap failed: %s\n", out.Error.Message)
		return
	}
	fmt.Print("  • bootstrapped from the repo's docs:\n")
	for _, l := range strings.Split(strings.TrimSpace(out.Report), "\n") {
		fmt.Println("    " + l)
	}
}
