package main

// `captain euclid share` and `captain euclid fold` - the shared brain
// (pkg euclid_share.go).
//
//	captain euclid share [--apply]   make this repository's brain shareable: developer
//	                                 brains local, notes/ for promoted notes, the fold
//	                                 workflow; without --apply it prints what it would do
//	captain euclid fold [--dry-run]  fold the pending notes into the shared registers and
//	                                 ledgers (what the workflow runs on main); a model when
//	                                 one is reachable (API key, else the running brain),
//	                                 the ledgers alone otherwise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// foldWorkflow is the job every shared repository runs after a merge to its
// default branch: fold, then commit what changed. It only ever changes
// `.euclid/` (registers, ledgers, the folded notes), only on main, so no
// branch can conflict with it. `[skip ci]` keeps the bot commit from
// re-triggering the repository's own CI; the path filter re-fires this
// workflow on the deletion of the notes, finds nothing pending and stops.
const foldWorkflow = `name: euclid-fold

# Folds the notes promoted in merged pull requests (.euclid/notes/*.md) into
# the shared brain - BRAIN.md and WISDOM.md by a model, the memory ledgers
# verbatim - and commits the result on the default branch. Written by
# ` + "`captain euclid share --apply`" + `; see docs/EUCLID.md in captaincode.
#
# Needs: the ` + "`captain`" + ` binary the fleet-captain workflow of
# lemma-ventures/captaincode installs on every runner (~/.local/bin/captain),
# and the repository secret OPENROUTER_API_KEY for the fold's model (without
# it the ledgers are still folded, the registers are not re-synthesized).

on:
  push:
    branches: [main]
    paths:
      - ".euclid/notes/**"
  workflow_dispatch:

permissions:
  contents: write

concurrency:
  group: euclid-fold
  cancel-in-progress: false

jobs:
  fold:
    if: "!contains(github.event.head_commit.message, '[skip ci]')"
    runs-on: [self-hosted, lemma]
    steps:
      - uses: actions/checkout@v4
      - name: Fold pending notes
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          CAPTAIN_CWD: ${{ github.workspace }}
        run: |
          set -eu
          CAPTAIN="$HOME/.local/bin/captain"
          test -x "$CAPTAIN" || { echo "::error::captain is not installed on this runner - run the fleet-captain workflow in lemma-ventures/captaincode"; exit 1; }
          "$CAPTAIN" euclid fold
      - name: Commit the shared brain
        run: |
          set -eu
          git add -A .euclid
          if git diff --cached --quiet; then echo "nothing folded"; exit 0; fi
          git -c user.name="euclid-fold" -c user.email="euclid-fold@users.noreply.github.com" \
            commit -q -m "chore(euclid): fold promoted notes into the shared brain [skip ci]"
          for i in 1 2 3; do
            git pull -q --rebase origin main && git push -q origin HEAD:main && exit 0
            sleep 5
          done
          echo "::error::could not push the fold"; exit 1
`

func cmdEuclidShare(args []string) {
	apply := false
	for _, a := range args {
		if a == "--apply" || a == "apply" {
			apply = true
		}
	}
	cwd := euclidCwd()
	repo := captaincode.RepoRoot(cwd)
	if repo == "" {
		fatal(fmt.Errorf("%s is not inside a git repository", cwd))
	}
	shared := filepath.Join(repo, ".euclid")
	if !isDirPath(shared) {
		fatal(fmt.Errorf("no repo brain at %s - run `captain euclid init --repo` first", shared))
	}
	// Is .euclid itself ignored at the root? Then nothing can be shared.
	if out, _ := gitOut(repo, "check-ignore", ".euclid"); strings.TrimSpace(out) != "" {
		fmt.Printf("%s ignores .euclid/ entirely (a root .gitignore rule): the brain is local, nothing to share.\n", filepath.Base(repo))
		fmt.Println("Remove that rule (and keep .euclid/ out of any public cut) to share the brain, then run this again.")
		return
	}
	var steps []string
	ig := filepath.Join(shared, ".gitignore")
	cur, _ := os.ReadFile(ig)
	if !strings.Contains(string(cur), "developers/") {
		steps = append(steps, "add developers/ to .euclid/.gitignore (developer brains are local)")
		if apply {
			text := strings.TrimRight(string(cur), "\n")
			if text != "" {
				text += "\n"
			}
			text += "# each developer's own brain (journal, notes, personal distill) is local;\n# what is worth sharing is promoted as a note file (notes/) and folded on main\ndevelopers/\n"
			if err := os.WriteFile(ig, []byte(text), 0o644); err != nil {
				fatal(err)
			}
		}
	}
	if out, _ := gitOut(repo, "ls-files", ".euclid/developers"); strings.TrimSpace(out) != "" {
		n := len(strings.Split(strings.TrimSpace(out), "\n"))
		steps = append(steps, fmt.Sprintf("untrack %d developer-brain file(s) (git rm --cached; the files stay on disk)", n))
		if apply {
			if _, err := gitOut(repo, "rm", "-r", "-q", "--cached", ".euclid/developers"); err != nil {
				fatal(err)
			}
		}
	}
	keep := filepath.Join(shared, captaincode.SharedNotesDir, ".gitkeep")
	if !isFile(keep) {
		steps = append(steps, "create .euclid/notes/ (promoted notes wait here for the fold)")
		if apply {
			if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
				fatal(err)
			}
			if err := os.WriteFile(keep, nil, 0o644); err != nil {
				fatal(err)
			}
		}
	}
	wf := filepath.Join(repo, ".github", "workflows", "euclid-fold.yml")
	if b, err := os.ReadFile(wf); err != nil || string(b) != foldWorkflow {
		steps = append(steps, "write .github/workflows/euclid-fold.yml (fold on every merge to main)")
		if apply {
			if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
				fatal(err)
			}
			if err := os.WriteFile(wf, []byte(foldWorkflow), 0o644); err != nil {
				fatal(err)
			}
		}
	}
	if len(steps) == 0 {
		fmt.Printf("%s: the shared brain is already set up.\n", filepath.Base(repo))
		return
	}
	verb := "would"
	if apply {
		verb = "did"
	}
	fmt.Printf("%s - captain euclid share %s:\n", filepath.Base(repo), verb)
	for _, s := range steps {
		fmt.Println("  - " + s)
	}
	if !apply {
		fmt.Println("run `captain euclid share --apply` to do it, then commit .euclid and .github/workflows/euclid-fold.yml")
		return
	}
	fmt.Println("now: commit .euclid and .github/workflows/euclid-fold.yml; add the repository secret OPENROUTER_API_KEY")
}

func cmdEuclidFold(args []string) {
	dry := false
	for _, a := range args {
		if a == "--dry-run" || a == "-n" {
			dry = true
		}
	}
	cwd := euclidCwd()
	repo := captaincode.RepoRoot(cwd)
	if repo == "" {
		fatal(fmt.Errorf("%s is not inside a git repository", cwd))
	}
	shared := filepath.Join(repo, ".euclid")
	notes, err := captaincode.PendingNotes(shared)
	if err != nil {
		fatal(err)
	}
	if len(notes) == 0 {
		fmt.Println("nothing pending in .euclid/notes")
		return
	}
	fmt.Printf("%d note(s) pending in %s\n", len(notes), shared)
	var d *captaincode.Distillation
	prompt := captaincode.FoldPrompt(shared, notes)
	captaincode.LoadCaptainEnv()
	switch {
	case captaincode.ChatAPIAvailable():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		text, err := captaincode.ChatAPI(ctx, prompt)
		if err != nil {
			fmt.Printf("fold: the model did not answer (%v) - folding the ledgers only\n", err)
		} else if parsed, err := captaincode.ParseFold(text); err != nil {
			fmt.Printf("fold: the model's answer was not usable (%v) - folding the ledgers only\n", err)
		} else {
			d = &parsed
		}
	default:
		if text, ok := brainFold(prompt); ok {
			if parsed, err := captaincode.ParseFold(text); err == nil {
				d = &parsed
			} else {
				fmt.Printf("fold: the brain's answer was not usable (%v) - folding the ledgers only\n", err)
			}
		} else {
			fmt.Println("fold: no model reachable (no OPENROUTER_API_KEY, no running brain) - folding the ledgers only")
		}
	}
	if dry {
		if d != nil {
			fmt.Print(captaincode.RenderPatch(captaincode.EuclidBrain{Root: shared, Kind: "repo", Label: "repo"}, *d))
		}
		for _, n := range notes {
			fmt.Println("  " + n.LedgerLine())
		}
		fmt.Println("(dry run - nothing written)")
		return
	}
	r, err := captaincode.Fold(shared, notes, d)
	if err != nil {
		fatal(err)
	}
	if r.ByModel {
		fmt.Printf("registers: %d edit(s) by the model", len(r.Edits))
		if r.Summary != "" {
			fmt.Printf(" - %s", strings.TrimSpace(r.Summary))
		}
		fmt.Println()
	}
	fmt.Printf("ledgers: %d note(s) appended to %s; %d note file(s) folded away\n", r.Notes, strings.Join(relPaths(shared, r.Ledgers), ", "), len(r.Removed))
}

// brainFold asks the running brain to answer the fold prompt with its
// distill leg (POST /v1/euclid/fold); ok=false when no brain answers.
func brainFold(prompt string) (string, bool) {
	body, _ := json.Marshal(map[string]any{"prompt": prompt})
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Post(brainURL()+"/v1/euclid/fold", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil || strings.TrimSpace(out.Text) == "" {
		return "", false
	}
	return out.Text, true
}

func gitOut(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func relPaths(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if r, err := filepath.Rel(root, p); err == nil {
			out = append(out, r)
		} else {
			out = append(out, p)
		}
	}
	return out
}
