package captaincode

// Objective signals for solo turns (stages 1 and 4): what the worker
// changed, whether the repo's own tests pass on it, and whether the user
// later committed it. The workflow executor had all three behind its gates;
// a solo turn - most of captain's traffic - had none, which is why 502 of
// 507 pending outcomes carried no check at all.

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HasEffortKnob reports whether a leg's transport takes an effort at all -
// the same-model effort escalation (stage 4) is only worth an attempt on
// one that does. Claude and the Codex CLI take a flag, cursor a pinned rung,
// an opencode leg a variant when its model offers one (fitted at dispatch);
// the free leg's models offer none worth climbing.
func HasEffortKnob(l Leg) bool {
	if l == LegFree || l == LegFrontier {
		return l == LegFrontier
	}
	s, ok := specs[l]
	if !ok {
		return false
	}
	switch s.Transport {
	case TransportClaudeCLI, TransportCodexCLI, TransportCursorCLI:
		return true
	}
	return true // opencode: Effort.Variant fits to what the model offers, or nothing
}

// ChangedFiles lists the files the working tree differs from HEAD by,
// tracked and untracked, sorted. Empty when dir is not a repository.
func ChangedFiles(ctx context.Context, dir string) []string {
	if dir == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		f := strings.TrimSpace(line[3:])
		if i := strings.LastIndex(f, " -> "); i >= 0 {
			f = f[i+4:]
		}
		f = strings.Trim(f, `"`)
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// CommitsTouching finds the newest commit after since that touched any of
// files in dir's repository. The seam every caller goes through, so tests
// can answer without a repository.
var CommitsTouching = func(ctx context.Context, dir string, since time.Time, files []string) (CommitRecord, bool) {
	if dir == "" || len(files) == 0 {
		return CommitRecord{}, false
	}
	args := []string{"log", "-1", "--since=" + strconv.FormatInt(since.Unix(), 10), "--format=%H%x1f%s%x1f%ct", "--"}
	args = append(args, files...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return CommitRecord{}, false
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return CommitRecord{}, false
	}
	parts := strings.SplitN(line, "\x1f", 3)
	c := CommitRecord{SHA: parts[0], Files: files}
	if len(parts) > 1 {
		c.Subject = parts[1]
	}
	if len(parts) > 2 {
		if ts, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); err == nil {
			c.At = time.Unix(ts, 0)
		}
	}
	return c, true
}

// commitSweepMaxAge bounds how far back the sweep looks: a week-old pending
// outcome with no commit is settled by silence long before.
const commitSweepMaxAge = 7 * 24 * time.Hour

// commitSweepMax bounds one sweep's git calls.
const commitSweepMax = 20

// SweepCommits looks, for every pending outcome that changed files, for a
// commit that has since touched them, and records it. Returns how many
// outcomes gained commit evidence. The caller settles afterwards.
func (l *Ledger) SweepCommits(ctx context.Context, now time.Time) int {
	found, looked := 0, 0
	for i := range l.Outcomes {
		o := &l.Outcomes[i]
		if o.Settled() || o.Commit != nil || o.Dir == "" || len(o.ChangedFiles) == 0 {
			continue
		}
		if now.Sub(o.deliveredAt()) > commitSweepMaxAge {
			continue
		}
		if looked >= commitSweepMax {
			break
		}
		looked++
		if c, ok := CommitsTouching(ctx, o.Dir, o.deliveredAt(), o.ChangedFiles); ok {
			o.Commit = &c
			o.UpdatedAt = now
			found++
		}
	}
	return found
}
