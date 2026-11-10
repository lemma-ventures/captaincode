package main

// Sibling captains: two TUIs on one repository.
//
// The brain keys a TUI by its folder (inbox, session, busy checks), so two
// TUIs in one checkout share one inbox and write one tree at once. A second
// TUI on a repository therefore opens in its own git worktree
// (`captaincode.sh new`). Those worktrees share the repository; their
// workers did not know about each other and could take the same files.
// Every worker prompt now lists the other TUIs open on the same repository
// on this machine - their folder, branch, what runs there now and which
// files they have uncommitted - and says how to warn a sibling before
// taking shared ground: a one-way `/btw` notice through `captain send`,
// which reaches that sibling's running worker mid-run.
// CAPTAIN_WORKER_SIBLINGS=0 drops the section.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// siblingFrom is the --from prefix of a sibling's notice; a sent turn whose
// sender starts with it is answered as a notice, not as a task.
const siblingFrom = "sibling "

// tuiFoldersCache keeps the process scan for a few seconds: a team stage can
// build several worker prompts in one second.
var tuiFoldersCache struct {
	sync.Mutex
	at      time.Time
	folders []string
}

// openTUIFolders lists the folder of every captain-code TUI on this machine:
// `opencode <absolute dir> …`, not the worker serve or a headless run.
func openTUIFolders() []string {
	tuiFoldersCache.Lock()
	defer tuiFoldersCache.Unlock()
	if time.Since(tuiFoldersCache.at) < 5*time.Second {
		return tuiFoldersCache.folders
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "args=").Output()
	if err != nil {
		return nil
	}
	tuiFoldersCache.folders = parseTUIFolders(string(out))
	tuiFoldersCache.at = time.Now()
	return tuiFoldersCache.folders
}

func parseTUIFolders(ps string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || filepath.Base(f[0]) != "opencode" || !filepath.IsAbs(f[1]) {
			continue
		}
		dir := filepath.Clean(f[1])
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out
}

func sibGit(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkoutOf is the top of the checkout dir is in and the repository it
// belongs to (the common git dir, the same for every worktree).
func checkoutOf(dir string) (top, common string) {
	out := sibGit(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		return "", ""
	}
	clean := func(p string) string {
		p = filepath.Clean(strings.TrimSpace(p))
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		return p
	}
	return clean(lines[0]), clean(lines[1])
}

type sibling struct {
	dir     string
	branch  string
	running []string // "<leg>: <task peek>"
	dirty   []string
	more    int
}

// siblingsOf finds the TUIs open in other checkouts of dir's repository.
func siblingsOf(dir string, tuis []string, runs []activeRun) []sibling {
	top, common := checkoutOf(dir)
	if top == "" {
		return nil
	}
	var out []sibling
	done := map[string]bool{top: true}
	for _, t := range tuis {
		ttop, tcommon := checkoutOf(t)
		if ttop == "" || tcommon != common || done[ttop] {
			continue
		}
		done[ttop] = true
		s := sibling{dir: ttop, branch: sibGit(ttop, "branch", "--show-current")}
		if s.branch == "" {
			s.branch = "detached at " + sibGit(ttop, "rev-parse", "--short", "HEAD")
		}
		inside := func(d string) bool {
			d = filepath.Clean(d)
			return d == ttop || strings.HasPrefix(d, ttop+string(filepath.Separator))
		}
		for _, r := range runs {
			if inside(r.dir) || (r.origin != "" && inside(r.origin)) {
				s.running = append(s.running, fmt.Sprintf("%s: %s", r.leg, promptPeek(r.task)))
			}
		}
		for _, line := range strings.Split(sibGit(ttop, "status", "--porcelain", "--untracked-files=normal"), "\n") {
			// A main checkout lists its own .worktrees/ folder untracked.
			if len(line) < 4 || strings.HasPrefix(strings.TrimSpace(line[3:]), ".worktrees/") {
				continue
			}
			if len(s.dirty) < 12 {
				s.dirty = append(s.dirty, strings.TrimSpace(line[3:]))
			} else {
				s.more++
			}
		}
		out = append(out, s)
	}
	return out
}

// siblingContract is the worker's view of the sibling captains of its
// repository, and the rules for not overlapping with them.
func (b *brain) siblingContract(ws captaincode.Workspace) string {
	if os.Getenv("CAPTAIN_WORKER_SIBLINGS") == "0" || ws.Dir == "" {
		return ""
	}
	var runs []activeRun
	if b != nil {
		runs = b.active.list()
	}
	return siblingText(ws, siblingsOf(ws.Dir, openTUIFolders(), runs))
}

func siblingText(ws captaincode.Workspace, sibs []sibling) string {
	notice := strings.HasPrefix(ws.SentBy, siblingFrom)
	if len(sibs) == 0 && !notice {
		return ""
	}
	var sb strings.Builder
	if notice {
		sb.WriteString("\n\n[captain] This turn is a notice from a sibling captain (another TUI on this repository). Acknowledge it in one line and keep clear of what it names; do not start other work for it and do not reply to it.")
	}
	if len(sibs) == 0 {
		return sb.String()
	}
	here := ws.Dir
	sb.WriteString("\n\n[captain] Sibling captains: other TUIs on this machine work on this same repository, each in its own checkout. Do not overlap with them:")
	for _, s := range sibs {
		fmt.Fprintf(&sb, "\n- %s (branch %s)", s.dir, s.branch)
		if len(s.running) > 0 {
			sb.WriteString(" - running now: " + strings.Join(s.running, "; "))
		} else {
			sb.WriteString(" - no worker running")
		}
		if len(s.dirty) > 0 {
			sb.WriteString(" - uncommitted: " + strings.Join(s.dirty, ", "))
			if s.more > 0 {
				fmt.Fprintf(&sb, " (+%d more)", s.more)
			}
		}
	}
	fmt.Fprintf(&sb, "\nLeave a sibling's uncommitted files and the area its running task names to it: pick other work, or say the overlap in your answer instead of editing them."+
		" Work in your own checkout (%s) and land through your own branch."+
		" Before you take shared ground - a file a sibling lists, the main branch (a merge or a push), a lockfile or a shared config - warn each sibling that has a worker running, once:"+
		" `captain send --cwd <its folder> --from \"%s%s\" \"/btw [sibling notice] <what you take, which files>\"`."+
		" A notice is one-way: never answer one, and send none to a sibling with no worker running (its next worker reads your uncommitted files in this list)."+
		" If a sibling already holds what you need, the one that started first keeps it.", here, siblingFrom, here)
	return sb.String()
}
