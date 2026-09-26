package captaincode

// `captain skills sync` - the supply side of M3.9.
//
// The sync fetches each source at a NAMED COMMIT. Never at spawn time, never
// a network call on the hot path: a worker starting a task reads a catalog
// that was fetched, vetted and hashed at some earlier, deliberate moment.
//
// Four sources, each a publisher shipping its own work. [anthropics/skills]
// is the primary - the standard's author publishing its own reference
// implementation, most of it Apache-2.0. [openai/plugins] is the secondary,
// where skills live inside plugins under a `.codex-plugin/plugin.json`
// manifest (`openai/skills` is deprecated and must not be pinned).
// [cloudflare/security-audit-skill] is one skill, MIT, from the company that
// wrote it - and it is PINNED IN CODE, because it is the skill every
// worker is stocked with whatever the task says (AlwaysSkills). The commit
// below was read in full before it was admitted: the frontmatter, every
// reference, both validators (plain Node that reads the files it is pointed
// at - no network, no install, no child process). A newer head is a new
// review, not a sync. [lemma-ventures/captaincode] is this repository's
// own skills/ directory - the procedures distilled from running captain -
// pinned in code the same way, so a shelf names the commit it came from.
//
// This is NOT a marketplace and it does not read community catalogs: a 2026
// audit found prompt injection in 36% of tested community skills, and the
// public directories index millions scraped from GitHub. That is not a
// supply captain can stand behind, and the standard offers no signing or
// attestation to lean on.
//
// One licensing line has to survive the fetch: Anthropic's four document
// skills (docx, pdf, pptx, xlsx) are SOURCE-AVAILABLE, not open source. They
// may be fetched onto the user's machine at their own request; they may not
// be vendored into this repository or redistributed with it. The lock records
// each skill's license next to its hash so the distinction outlives the sync
// that made it.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SkillSource is one catalog the sync may read.
type SkillSource struct {
	Repo   string // "anthropics/skills"
	URL    string
	Commit string // the pin; "" means the source's default branch head at sync time
	// Roots are the subdirectories skills live under. Empty means the whole
	// tree is walked for directories holding a SKILL.md.
	Roots []string
	// License is the repository-wide license, overridden per skill by a
	// LICENSE file beside the skill or a `license:` in its frontmatter.
	License string
	// SourceAvailable names the skills in this source that are NOT open
	// source. They are fetched at the user's request and never vendored.
	SourceAvailable []string
}

// SkillSources are the catalogs captain will read, primary first.
func SkillSources() []SkillSource {
	return []SkillSource{
		{
			Repo:            "anthropics/skills",
			URL:             "https://github.com/anthropics/skills.git",
			License:         "Apache-2.0",
			SourceAvailable: []string{"docx", "pdf", "pptx", "xlsx"},
		},
		{
			Repo:    "openai/plugins",
			URL:     "https://github.com/openai/plugins.git",
			License: "MIT",
		},
		{
			Repo:    "cloudflare/security-audit-skill",
			URL:     "https://github.com/cloudflare/security-audit-skill.git",
			Commit:  "c1c8a8c1471069fb0e188eeaff69b8e8db6564a8", // reviewed 2026-09-22
			Roots:   []string{"skills"},
			License: "MIT",
		},
		{
			Repo:    "lemma-ventures/captaincode",
			URL:     "https://github.com/lemma-ventures/captaincode.git",
			Commit:  "1b4572d956b816dbb7f19a6fd91455fc1ca5ccf3", // skills/ as published in 4af2176
			Roots:   []string{"skills"},
			License: "MIT",
		},
	}
}

// FindSkillSource resolves a source by repo name or its short half.
func FindSkillSource(name string) (SkillSource, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range SkillSources() {
		if strings.ToLower(s.Repo) == name || strings.HasPrefix(strings.ToLower(s.Repo), name+"/") ||
			strings.ToLower(pathOwner(s.Repo)) == name {
			return s, true
		}
	}
	return SkillSource{}, false
}

func pathOwner(repo string) string {
	if i := strings.Index(repo, "/"); i > 0 {
		return repo[:i]
	}
	return repo
}

// SyncOptions bounds one sync.
type SyncOptions struct {
	Source       SkillSource
	Only         []string // sync only these skill names; empty means every vetted one
	AllowScripts []string // skills whose scripts/ directory ships too
	DryRun       bool
	Timeout      time.Duration
}

// SyncResult reports what one sync did, including what it REFUSED: a skill
// that failed vetting is the most interesting line in the report, so it is
// never dropped silently.
type SyncResult struct {
	Source   SkillSource
	Commit   string
	Added    []string
	Updated  []string
	Rejected []SkillRejection
	Skipped  []string // already current at this commit
}

// SkillRejection is one skill the sync refused, and why.
type SkillRejection struct {
	Name   string
	Reason string
}

// SyncSkills fetches one source at its pinned commit, vets every skill it
// finds, and writes the ones that pass into the catalog with their hashes in
// the lock. Skills from OTHER sources are left alone: a sync of one catalog
// is not a reason to drop another's.
func SyncSkills(ctx context.Context, o SyncOptions) (SyncResult, error) {
	res := SyncResult{Source: o.Source}
	if o.Source.URL == "" {
		return res, fmt.Errorf("skills: no source to sync (known: %s)", strings.Join(sourceNames(), ", "))
	}
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	tmp, err := os.MkdirTemp("", "captain-skills-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)

	commit, err := fetchSource(ctx, o.Source, tmp)
	if err != nil {
		return res, err
	}
	res.Commit = commit

	found, err := findSkillDirs(tmp, o.Source.Roots)
	if err != nil {
		return res, err
	}
	only := map[string]bool{}
	for _, n := range o.Only {
		only[strings.ToLower(strings.TrimSpace(n))] = true
	}
	scripts := map[string]bool{}
	for _, n := range o.AllowScripts {
		scripts[strings.ToLower(strings.TrimSpace(n))] = true
	}
	sourceAvailable := map[string]bool{}
	for _, n := range o.Source.SourceAvailable {
		sourceAvailable[strings.ToLower(n)] = true
	}

	lock, err := ReadSkillLock()
	if err != nil {
		return res, err
	}
	have := map[string]Skill{}
	for _, s := range lock.Skills {
		have[s.Name] = s
	}

	for _, dir := range found {
		s, perr := ParseSkill(dir)
		if perr != nil {
			res.Rejected = append(res.Rejected, SkillRejection{Name: filepath.Base(dir), Reason: perr.Error()})
			continue
		}
		if len(only) > 0 && !only[strings.ToLower(s.Name)] {
			continue
		}
		if old, ok := have[s.Name]; ok && old.Source != "" && old.Source != o.Source.Repo {
			res.Rejected = append(res.Rejected, SkillRejection{Name: s.Name,
				Reason: fmt.Sprintf("already synced from %s; one name is one skill", old.Source)})
			continue
		}
		s.Source, s.Commit = o.Source.Repo, commit
		s.Scripts = scripts[strings.ToLower(s.Name)]
		s.Redistribute = !sourceAvailable[strings.ToLower(s.Name)]
		if s.License == "" {
			s.License = skillLicense(dir, o.Source.License)
		}
		if old, ok := have[s.Name]; ok && old.Commit == commit && sameHashes(old.Files, s.Files) {
			res.Skipped = append(res.Skipped, s.Name)
			continue
		}
		if o.DryRun {
			if _, ok := have[s.Name]; ok {
				res.Updated = append(res.Updated, s.Name)
			} else {
				res.Added = append(res.Added, s.Name)
			}
			continue
		}
		dst := filepath.Join(SkillsHome(), s.Name)
		if err := os.MkdirAll(SkillsHome(), 0o700); err != nil {
			return res, err
		}
		os.RemoveAll(dst)
		if err := copySkill(Skill{Dir: dir, Scripts: s.Scripts}, dst); err != nil {
			os.RemoveAll(dst)
			res.Rejected = append(res.Rejected, SkillRejection{Name: s.Name, Reason: "copy: " + err.Error()})
			continue
		}
		// Re-hash from the catalog, not from the checkout: the lock must
		// attest what is ON DISK here, which is the quarantined subset when
		// scripts/ did not ship.
		staged, serr := ParseSkill(dst)
		if serr != nil {
			os.RemoveAll(dst)
			res.Rejected = append(res.Rejected, SkillRejection{Name: s.Name, Reason: serr.Error()})
			continue
		}
		s.Files, s.Bytes, s.Dir = staged.Files, staged.Bytes, dst
		if _, ok := have[s.Name]; ok {
			res.Updated = append(res.Updated, s.Name)
		} else {
			res.Added = append(res.Added, s.Name)
		}
		have[s.Name] = s
	}
	if o.DryRun {
		sortResult(&res)
		return res, nil
	}

	lock.Skills = lock.Skills[:0]
	for _, s := range have {
		lock.Skills = append(lock.Skills, s)
	}
	lock.Sources = upsertSource(lock.Sources, LockSource{
		Repo: o.Source.Repo, Commit: commit, License: o.Source.License, At: time.Now(),
	})
	lock.UpdatedAt = time.Now()
	if err := WriteSkillLock(lock); err != nil {
		return res, err
	}
	sortResult(&res)
	return res, nil
}

func sortResult(r *SyncResult) {
	sort.Strings(r.Added)
	sort.Strings(r.Updated)
	sort.Strings(r.Skipped)
	sort.Slice(r.Rejected, func(i, j int) bool { return r.Rejected[i].Name < r.Rejected[j].Name })
}

func sourceNames() []string {
	var out []string
	for _, s := range SkillSources() {
		out = append(out, s.Repo)
	}
	return out
}

func upsertSource(list []LockSource, s LockSource) []LockSource {
	for i, old := range list {
		if old.Repo == s.Repo {
			list[i] = s
			return list
		}
	}
	return append(list, s)
}

func sameHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// fetchSource clones the source shallowly at the pinned commit and returns
// the commit it landed on. An unpinned source records the head it fetched, so
// the lock names a commit even when the caller did not.
func fetchSource(ctx context.Context, src SkillSource, dir string) (string, error) {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.CombinedOutput()
	}
	if out, err := run("init", "--quiet"); err != nil {
		return "", fmt.Errorf("skills: git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := run("remote", "add", "origin", src.URL); err != nil {
		return "", fmt.Errorf("skills: git remote add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ref := src.Commit
	if ref == "" {
		ref = "HEAD"
	}
	// A shallow fetch of a commit needs the FULL sha: an abbreviated one is a
	// local convenience git cannot ask a remote for, and the error it returns
	// ("couldn't find remote ref") reads like the commit does not exist.
	if isAbbrevSHA(ref) {
		return "", fmt.Errorf("skills: --commit %s is abbreviated; pin the full 40-character sha (`git ls-remote %s`)", ref, src.URL)
	}
	if out, err := run("fetch", "--depth", "1", "--quiet", "origin", ref); err != nil {
		return "", fmt.Errorf("skills: fetch %s at %s: %w: %s", src.Repo, ref, err, strings.TrimSpace(string(out)))
	}
	if out, err := run("checkout", "--quiet", "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("skills: checkout %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	out, err := run("rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("skills: rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isAbbrevSHA reports a hex string short enough that a remote cannot resolve it.
func isAbbrevSHA(ref string) bool {
	if len(ref) == 0 || len(ref) >= 40 {
		return false
	}
	for _, c := range ref {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return len(ref) >= 6
}

// findSkillDirs walks a checkout for directories holding a SKILL.md. The
// walk stays out of .git and node_modules, and does not descend INTO a skill:
// a SKILL.md inside a skill's own reference material is documentation, not a
// second skill.
func findSkillDirs(root string, roots []string) ([]string, error) {
	bases := []string{root}
	if len(roots) > 0 {
		bases = bases[:0]
		for _, r := range roots {
			bases = append(bases, filepath.Join(root, r))
		}
	}
	var out []string
	for _, base := range bases {
		err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !info.IsDir() {
				return nil
			}
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err == nil {
				out = append(out, p)
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// skillLicense prefers a LICENSE beside the skill over the repository's.
func skillLicense(dir, repoLicense string) string {
	for _, n := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		if id := licenseID(string(raw)); id != "" {
			return id
		}
	}
	return repoLicense
}

// licenseID names the common licenses from their text. An unrecognised
// license is reported as unrecorded rather than guessed - "unrecorded" is a
// question the user can answer, a wrong SPDX id is one they will not think to
// ask.
func licenseID(text string) string {
	head := strings.ToLower(truncateStr(text, 4000))
	switch {
	case strings.Contains(head, "apache license") && strings.Contains(head, "version 2.0"):
		return "Apache-2.0"
	case strings.Contains(head, "mit license"):
		return "MIT"
	case strings.Contains(head, "bsd 3-clause"):
		return "BSD-3-Clause"
	case strings.Contains(head, "source-available") || strings.Contains(head, "source available") ||
		strings.Contains(head, "proprietary"):
		return "source-available"
	}
	return ""
}

// FormatSyncResult renders one sync for the CLI.
func FormatSyncResult(r SyncResult, dry bool) string {
	var sb strings.Builder
	verb := "synced"
	if dry {
		verb = "would sync"
	}
	fmt.Fprintf(&sb, "%s %s at %s\n", verb, r.Source.Repo, shortCommit(r.Commit))
	if len(r.Added) > 0 {
		fmt.Fprintf(&sb, "  added:    %s\n", strings.Join(r.Added, ", "))
	}
	if len(r.Updated) > 0 {
		fmt.Fprintf(&sb, "  updated:  %s\n", strings.Join(r.Updated, ", "))
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&sb, "  current:  %d skill(s) already at this commit\n", len(r.Skipped))
	}
	if len(r.Rejected) > 0 {
		fmt.Fprintf(&sb, "  refused:  %d\n", len(r.Rejected))
		for _, x := range r.Rejected {
			fmt.Fprintf(&sb, "    ✗ %-20s %s\n", x.Name, truncateStr(oneLine(x.Reason), 110))
		}
	}
	if len(r.Added)+len(r.Updated) == 0 && len(r.Rejected) == 0 {
		sb.WriteString("  nothing changed\n")
	}
	return sb.String()
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	if c == "" {
		return "(unknown)"
	}
	return c
}
