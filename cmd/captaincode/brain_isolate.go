package main

// Isolating a worker that moves into a busy repository.
//
// A task that names another repository runs there (followTask): typed in one
// TUI, its worker writes the other repository's checkout. When that checkout
// already has work in flight - a worker, a /repeat loop or a program - two
// agents wrote one tree at once, neither aware of the other (2026-10-07: a
// README fix from another TUI ran in the checkout a roadmap loop was
// implementing in). Such a worker now gets its own git worktree at the
// repository's HEAD, and its changes come back as a patch to apply, not as
// edits mixed into work in progress. Messages between captains (`captain
// send`) are not affected: they deliver prompts, they do not move workers.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// repoBusy reports whether the repository at dir has work in flight: a
// worker running in it, or an unfinished /repeat thread or program there.
func (b *brain) repoBusy(dir string) (bool, string) {
	root := captaincode.GitRoot(dir)
	if root == "" {
		root = filepath.Clean(dir)
	}
	inside := func(d string) bool {
		d = filepath.Clean(d)
		return d == root || strings.HasPrefix(d, root+string(filepath.Separator))
	}
	for leg, run := range b.active.snapshot() {
		if inside(run.dir) {
			return true, fmt.Sprintf("%s is running there (%s)", leg, promptPeek(run.task))
		}
	}
	b.rmu.Lock()
	defer b.rmu.Unlock()
	for id, th := range b.repeatState() {
		if !th.finished && inside(th.dir) {
			return true, fmt.Sprintf("%s is running there", id)
		}
	}
	return false, ""
}

// isolateMovedWorker gives a worker that followTask moved into a busy
// repository its own worktree. It returns the workspace to run in and a
// function that saves the worker's changes as a patch and removes the
// worktree; both are no-ops when no isolation was needed or possible.
func (b *brain) isolateMovedWorker(ws captaincode.Workspace) (captaincode.Workspace, func()) {
	noop := func() {}
	if os.Getenv("CAPTAIN_ISOLATE_MOVED") == "0" || ws.Origin == "" || filepath.Clean(ws.Dir) == filepath.Clean(ws.Origin) {
		return ws, noop
	}
	busy, why := b.repoBusy(ws.Dir)
	if !busy {
		return ws, noop
	}
	repo := captaincode.GitRoot(ws.Dir)
	if repo == "" {
		fmt.Printf("captain brain: %s is busy (%s) and not a git repository - the moved worker shares its checkout\n", ws.Dir, why)
		return ws, noop
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return ws, noop
	}
	wt, err := captaincode.NewWorktree(context.Background(), repo, strings.TrimSpace(string(head)))
	if err != nil {
		fmt.Printf("captain brain: could not isolate the moved worker (%v) - it shares %s's checkout\n", err, filepath.Base(repo))
		return ws, noop
	}
	note := fmt.Sprintf("%s is busy (%s): this worker from %s runs in its own worktree; its changes come back as a patch", filepath.Base(repo), why, filepath.Base(ws.Origin))
	fmt.Printf("captain brain: %s\n", note)
	for _, d := range []string{ws.Origin, repo} {
		b.pushActivity(activity{Dir: d, Kind: "route", Leg: "workspace", Model: "isolated", Text: note})
	}
	origin := ws.Origin
	ws.Dir = wt.Dir
	return ws, func() {
		defer wt.Close()
		patch := worktreePatch(wt.Dir)
		if strings.TrimSpace(patch) == "" {
			return
		}
		dir := runDiffDir()
		_ = os.MkdirAll(dir, 0o700)
		file := filepath.Join(dir, fmt.Sprintf("%s-%s-from-%s.patch", time.Now().Format("20060102-150405"), filepath.Base(repo), filepath.Base(origin)))
		if err := os.WriteFile(file, []byte(patch), 0o600); err != nil {
			fmt.Printf("captain brain: could not save the isolated worker's patch: %v\n", err)
			return
		}
		msg := fmt.Sprintf("the isolated worker's changes to %s are in %s - apply them when that checkout is free: git -C %s apply --3way %s", filepath.Base(repo), file, repo, file)
		fmt.Printf("captain brain: %s\n", msg)
		for _, d := range []string{origin, repo} {
			b.pushActivity(activity{Dir: d, Kind: "done", Leg: "workspace", Model: "patch", Text: msg})
		}
	}
}

// worktreePatch is every change in the worktree, new files included, as one
// patch against its HEAD.
func worktreePatch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "git", "-C", dir, "add", "-A").Run(); err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--cached", "--binary", "HEAD").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// gitCommonDir is the repository's shared .git directory, the same for a
// checkout and all its worktrees.
func gitCommonDir(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
