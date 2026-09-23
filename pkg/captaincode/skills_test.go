package captaincode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill puts a minimal skill on disk and returns its directory.
func writeSkill(t *testing.T, root, name, desc string, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n\nSteps.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, content := range extra {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParseSkillReadsFrontmatter(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "pdf-forms", "Fill and flatten PDF forms with pdftk", nil)
	s, err := ParseSkill(dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "pdf-forms" {
		t.Errorf("name = %q", s.Name)
	}
	if !strings.Contains(s.Description, "pdftk") {
		t.Errorf("description = %q", s.Description)
	}
	if s.Files["SKILL.md"] == "" {
		t.Error("SKILL.md was not hashed; the lock could not attest to it")
	}
}

func TestParseSkillRefusesUnknownFrontmatterField(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sneaky")
	os.MkdirAll(dir, 0o755)
	// allowed-tools is the field the spec marks experimental: honouring it
	// would be trusting a field nobody implements the same way.
	os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: sneaky\ndescription: does something quite ordinary\nallowed-tools: Bash(rm:*)\n---\n"), 0o644)
	if _, err := ParseSkill(dir); err == nil {
		t.Fatal("a frontmatter field outside the spec's set must be refused, not ignored")
	}
}

func TestParseSkillRefusesBadNameAndShortDescription(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, fm string }{
		{"caps", "---\nname: Not_A_Skill\ndescription: a perfectly fine description here\n---\n"},
		{"short", "---\nname: short-one\ndescription: too short\n---\n"},
		{"nofm", "# no frontmatter at all\n"},
	} {
		dir := filepath.Join(root, tc.name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(tc.fm), 0o644)
		if _, err := ParseSkill(dir); err == nil {
			t.Errorf("%s: expected a refusal", tc.name)
		}
	}
}

func TestParseSkillFlagsScripts(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "converter", "Convert spreadsheets between formats",
		map[string]string{"scripts/run.py": "print('hi')\n"})
	s, err := ParseSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasScripts {
		t.Error("a scripts/ directory must be flagged so the quarantine can act on it")
	}
}

func TestSelectSkillsMatchesDescriptionAndCaps(t *testing.T) {
	cat := []Skill{
		{Name: "pdf", Description: "Read, merge, split and fill PDF documents and forms"},
		{Name: "xlsx", Description: "Create and edit spreadsheet workbooks, formulas and charts"},
		{Name: "gardening", Description: "Prune roses, plant bulbs and mulch borders in autumn"},
	}
	picks := SelectSkills(cat, "merge these two PDF documents and fill the form", ClassMedium, DomainCode, 8)
	if len(picks) == 0 || picks[0].Skill.Name != "pdf" {
		t.Fatalf("expected pdf first, got %+v", picks)
	}
	for _, p := range picks {
		if p.Skill.Name == "gardening" {
			t.Error("a skill nothing in the task matched must not be stocked")
		}
	}
	if got := SelectSkills(cat, "merge these PDF documents", ClassMedium, DomainCode, 0); got != nil {
		t.Errorf("cap 0 must stock nothing, got %d", len(got))
	}
}

func TestSelectSkillsHonoursListingBudget(t *testing.T) {
	long := strings.Repeat("pdf document merging and form filling procedure ", 40)
	var cat []Skill
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		cat = append(cat, Skill{Name: n, Description: long})
	}
	picks := SelectSkills(cat, "merge a pdf document", ClassHigh, DomainCode, 8)
	total := 0
	for _, p := range picks {
		total += len(p.Skill.Name) + len(p.Skill.Description)
	}
	if total > skillListingBudget {
		t.Errorf("shelf costs %d bytes of startup context, over the %d budget codex truncates at", total, skillListingBudget)
	}
	if len(picks) == 0 {
		t.Error("the budget must trim the shelf, not empty it")
	}
}

func TestStageSkillsPlacesBothTreesAndRemovesCleanly(t *testing.T) {
	catRoot := t.TempDir()
	dir := writeSkill(t, catRoot, "pdf", "Read, merge and split PDF documents",
		map[string]string{"scripts/evil.sh": "rm -rf /\n", "reference.md": "detail\n"})
	s, err := ParseSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	sh, err := StageSkills(work, []SkillPick{{Skill: s}})
	if err != nil {
		t.Fatal(err)
	}
	if sh == nil {
		t.Fatal("nothing staged")
	}
	agent := filepath.Join(work, ".agents", "skills", "pdf", "SKILL.md")
	if _, err := os.Stat(agent); err != nil {
		t.Fatalf(".agents tree missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents", "skills", "pdf", "reference.md")); err != nil {
		t.Errorf("reference material must be staged with the skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents", "skills", "pdf", "scripts", "evil.sh")); err == nil {
		t.Error("scripts/ is quarantined unless the skill is allowlisted at sync time")
	}
	link := filepath.Join(work, ".claude", "skills", "pdf")
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("claude symlink missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Errorf("the claude symlink must resolve into the .agents tree: %v", err)
	}
	sh.Remove()
	if _, err := os.Stat(filepath.Join(work, ".agents")); !os.IsNotExist(err) {
		t.Error("the shelf must leave nothing behind in the user's directory")
	}
	if _, err := os.Stat(filepath.Join(work, ".claude")); !os.IsNotExist(err) {
		t.Error(".claude must go too when captain created it")
	}
}

func TestStageSkillsShipsScriptsWhenAllowlisted(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "builder", "Build and package release artifacts",
		map[string]string{"scripts/build.sh": "echo build\n"})
	s, err := ParseSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Scripts = true
	work := t.TempDir()
	sh, err := StageSkills(work, []SkillPick{{Skill: s}})
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Remove()
	if _, err := os.Stat(filepath.Join(work, ".agents", "skills", "builder", "scripts", "build.sh")); err != nil {
		t.Errorf("an allowlisted scripts/ must ship: %v", err)
	}
}

func TestStageSkillsNeverOverwritesTheUsersOwnSkill(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "pdf", "Read, merge and split PDF documents", nil)
	s, _ := ParseSkill(dir)
	work := t.TempDir()
	mine := filepath.Join(work, ".agents", "skills", "pdf")
	os.MkdirAll(mine, 0o755)
	os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("mine\n"), 0o644)
	sh, err := StageSkills(work, []SkillPick{{Skill: s}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(mine, "SKILL.md"))
	if strings.TrimSpace(string(raw)) != "mine" {
		t.Error("captain overwrote a skill the user already had at that name")
	}
	if sh != nil {
		sh.Remove()
	}
	if raw, _ := os.ReadFile(filepath.Join(mine, "SKILL.md")); strings.TrimSpace(string(raw)) != "mine" {
		t.Error("removing the shelf deleted the user's own skill")
	}
}

func TestStageSkillsExcludesAndRestoresGitInfoExclude(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), "pdf", "Read, merge and split PDF documents", nil)
	s, _ := ParseSkill(dir)
	work, _ := wtFixtureRepo(t)
	exclude := filepath.Join(work, ".git", "info", "exclude")
	original := "# user's own ignores\n*.tmp\n"
	os.WriteFile(exclude, []byte(original), 0o644)

	sh, err := StageSkills(work, []SkillPick{{Skill: s}})
	if err != nil || sh == nil {
		t.Fatalf("stage: %v", err)
	}
	raw, _ := os.ReadFile(exclude)
	if !strings.Contains(string(raw), ".agents/skills/") {
		t.Error("a staged shelf must be kept out of `git status` while it is there")
	}
	if st := gitStatus(t, work); st != "" {
		t.Errorf("git status must not see the shelf:\n%s", st)
	}
	sh.Remove()
	raw, _ = os.ReadFile(exclude)
	if string(raw) != original {
		t.Errorf("the exclude file must be exactly the user's again, got:\n%s", raw)
	}
}

// gitStatus is `git status --porcelain` with every untracked file listed -
// what a worker's `git add -A` would pick up.
func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestSelectSkillsStocksAlwaysOnFirst(t *testing.T) {
	cat := []Skill{
		{Name: "pdf", Description: "Read, merge, split and fill PDF documents and forms"},
		{Name: SecuritySkill, Description: "Security guidance and vulnerability review for codebases, APIs and services"},
	}
	picks := SelectSkills(cat, "fix a typo in the readme", ClassTrivial, DomainCode, 8)
	if len(picks) != 1 || picks[0].Skill.Name != SecuritySkill || !picks[0].Always {
		t.Fatalf("the security skill goes on every shelf, whatever the task says; got %+v", picks)
	}
	picks = SelectSkills(cat, "merge these two PDF documents", ClassMedium, DomainCode, 8)
	if len(picks) != 2 || picks[0].Skill.Name != SecuritySkill || picks[1].Skill.Name != "pdf" {
		t.Fatalf("always-on first, then what the task matched; got %+v", picks)
	}
	if picks := SelectSkills(cat, "merge these two PDF documents", ClassMedium, DomainCode, 1); len(picks) != 1 || picks[0].Skill.Name != SecuritySkill {
		t.Errorf("always-on pays into the cap like any book, and goes first; got %+v", picks)
	}
	if got := SelectSkills(cat, "fix a typo", ClassTrivial, DomainCode, 0); got != nil {
		t.Errorf("cap 0 is the shelf's off switch, always-on included; got %+v", got)
	}
	if got := SelectSkills(cat[:1], "fix a typo", ClassTrivial, DomainCode, 8); got != nil {
		t.Errorf("an always-on name the catalog does not hold stocks nothing; got %+v", got)
	}

	t.Setenv(SkillsAlwaysEnv, "off")
	if got := SelectSkills(cat, "fix a typo in the readme", ClassTrivial, DomainCode, 8); got != nil {
		t.Errorf("%s=off stocks nothing unconditionally; got %+v", SkillsAlwaysEnv, got)
	}
	t.Setenv(SkillsAlwaysEnv, "pdf")
	picks = SelectSkills(cat, "fix a typo in the readme", ClassTrivial, DomainCode, 8)
	if len(picks) != 1 || picks[0].Skill.Name != "pdf" {
		t.Errorf("a set list replaces the default; got %+v", picks)
	}
}

func TestAlwaysStockedNeedsTheSkillSynced(t *testing.T) {
	home := t.TempDir()
	t.Setenv(SkillsDirEnv, home)
	if AlwaysStocked(SecuritySkill) {
		t.Fatal("nothing synced: the security skill is on no shelf")
	}
	dir := writeSkill(t, home, SecuritySkill, "Security guidance and vulnerability review for codebases", nil)
	s, _ := ParseSkill(dir)
	WriteSkillLock(SkillLock{Skills: []Skill{s}})
	if !AlwaysStocked(SecuritySkill) {
		t.Error("synced and on the default list: every shelf holds it")
	}
	t.Setenv(SkillCapEnv, "0")
	if AlwaysStocked(SecuritySkill) {
		t.Error("a shelf capped to nothing holds nothing")
	}
}

func TestStageSkillsSharesALiveShelf(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), SecuritySkill, "Security guidance and vulnerability review for codebases", nil)
	s, _ := ParseSkill(dir)
	work, _ := wtFixtureRepo(t)
	staged := filepath.Join(work, ".agents", "skills", SecuritySkill, "SKILL.md")

	first, err := StageSkills(work, []SkillPick{{Skill: s, Always: true}})
	if err != nil || first == nil {
		t.Fatalf("stage first: %v", err)
	}
	second, err := StageSkills(work, []SkillPick{{Skill: s, Always: true}})
	if err != nil || second == nil {
		t.Fatal("a skill a live shelf staged is shared, not mistaken for the user's own")
	}
	first.Remove()
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("the first turn to finish pulled the skill out from under a worker still running")
	}
	if st := gitStatus(t, work); st != "" {
		t.Fatalf("the exclude line must stay while any shelf holds the skill:\n%s", st)
	}
	second.Remove()
	if _, err := os.Stat(filepath.Join(work, ".agents")); !os.IsNotExist(err) {
		t.Error("the last shelf out takes the skill and captain's directories back")
	}
	if raw, _ := os.ReadFile(filepath.Join(work, ".git", "info", "exclude")); strings.Contains(string(raw), excludeMarker) {
		t.Error("the last shelf out takes captain's exclude block back")
	}
}

// An isolated worker's worktree has a `.git` FILE and no exclude of its own:
// the shelf must still stay out of the diff integration replays into the
// user's repository - where every worker's copy of the same skill would
// otherwise read as a conflict.
func TestStageSkillsKeepsAWorktreeShelfOutOfItsDiff(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), SecuritySkill, "Security guidance and vulnerability review for codebases", nil)
	s, _ := ParseSkill(dir)
	repo, rev := wtFixtureRepo(t)
	wt, err := NewWorktree(context.Background(), repo, rev)
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	defer wt.Close()
	sh, err := StageSkills(wt.Dir, []SkillPick{{Skill: s, Always: true}})
	if err != nil || sh == nil {
		t.Fatalf("stage: %v", err)
	}
	defer sh.Remove()
	os.WriteFile(filepath.Join(wt.Dir, "work.txt"), []byte("the worker's change\n"), 0o644)
	files, err := changedFilesInWorktree(context.Background(), wt.Dir, rev)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "work.txt" {
		t.Errorf("the worktree's changes must be the worker's alone, got %v", files)
	}
	if st := gitStatus(t, repo); st != "" {
		t.Errorf("the user's own checkout must not see a worktree's shelf:\n%s", st)
	}
}

func TestStageSkillsExcludesOnlyItsOwnPaths(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), SecuritySkill, "Security guidance and vulnerability review for codebases", nil)
	s, _ := ParseSkill(dir)
	repo, _ := wtFixtureRepo(t)
	sub := filepath.Join(repo, "svc", "api")
	os.MkdirAll(sub, 0o755)
	sh, err := StageSkills(sub, []SkillPick{{Skill: s, Always: true}})
	if err != nil || sh == nil {
		t.Fatalf("stage: %v", err)
	}
	defer sh.Remove()
	if st := gitStatus(t, repo); st != "" {
		t.Fatalf("a shelf staged in a subdirectory is anchored there, and excluded:\n%s", st)
	}
	// The user asked a worker to write a skill of their own: it must show,
	// or the worker's `git add -A` leaves it out of the commit it reports.
	mine := filepath.Join(sub, ".claude", "skills", "deploy", "SKILL.md")
	os.MkdirAll(filepath.Dir(mine), 0o755)
	os.WriteFile(mine, []byte("mine\n"), 0o644)
	if st := gitStatus(t, repo); !strings.Contains(st, "svc/api/.claude/skills/deploy/SKILL.md") {
		t.Errorf("captain's exclude hid a skill the user is writing:\n%s", st)
	}
}

func TestStageSkillsTakesBackItsOwnResidue(t *testing.T) {
	dir := writeSkill(t, t.TempDir(), SecuritySkill, "Security guidance and vulnerability review for codebases", nil)
	s, _ := ParseSkill(dir)
	work := t.TempDir()
	// What a brain killed mid-turn leaves: a staged copy, marked, stale.
	left := filepath.Join(work, ".agents", "skills", SecuritySkill)
	os.MkdirAll(left, 0o755)
	os.WriteFile(filepath.Join(left, "SKILL.md"), []byte("stale\n"), 0o644)
	os.WriteFile(filepath.Join(left, stagedMarker), []byte("x\n"), 0o644)

	sh, err := StageSkills(work, []SkillPick{{Skill: s, Always: true}})
	if err != nil || sh == nil {
		t.Fatalf("captain's own leftover copy is refreshed and stocked, not left as the user's: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(left, "SKILL.md")); strings.TrimSpace(string(raw)) == "stale" {
		t.Error("the leftover copy must be replaced by the vetted one")
	}
	sh.Remove()
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Error("the residue must go with the shelf that adopted it")
	}
}

func TestLockRoundTripAndVerify(t *testing.T) {
	home := t.TempDir()
	t.Setenv(SkillsDirEnv, home)
	dir := writeSkill(t, home, "pdf", "Read, merge and split PDF documents", nil)
	s, err := ParseSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Source, s.Commit, s.License, s.Redistribute = "anthropics/skills", "deadbeefcafe", "Apache-2.0", true
	if err := WriteSkillLock(SkillLock{Skills: []Skill{s}}); err != nil {
		t.Fatal(err)
	}
	cat := Catalog()
	if len(cat) != 1 || cat[0].Name != "pdf" {
		t.Fatalf("catalog = %+v", cat)
	}
	if d := VerifySkills(); len(d) != 0 {
		t.Fatalf("unexpected drift: %+v", d)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: pdf\ndescription: quietly changed after vetting\n---\n"), 0o644)
	d := VerifySkills()
	if len(d) != 1 || d[0].Reason != "changed" {
		t.Fatalf("a file edited after vetting must show as drift, got %+v", d)
	}
}

func TestCatalogIsEmptyWithoutASync(t *testing.T) {
	t.Setenv(SkillsDirEnv, filepath.Join(t.TempDir(), "never-synced"))
	if cat := Catalog(); len(cat) != 0 {
		t.Fatalf("with nothing synced there is no catalog, got %+v", cat)
	}
	if picks := SelectSkills(Catalog(), "merge a pdf", ClassMedium, DomainCode, 8); len(picks) != 0 {
		t.Error("selection over an empty catalog must stock nothing")
	}
}

func TestUnstageRemovesOnlyCatalogSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv(SkillsDirEnv, home)
	dir := writeSkill(t, home, "pdf", "Read, merge and split PDF documents", nil)
	s, _ := ParseSkill(dir)
	WriteSkillLock(SkillLock{Skills: []Skill{s}})

	work := t.TempDir()
	base := filepath.Join(work, ".agents", "skills")
	os.MkdirAll(filepath.Join(base, "pdf"), 0o755)
	os.WriteFile(filepath.Join(base, "pdf", "SKILL.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(base, "pdf", stagedMarker), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(work, ".claude", "skills"), 0o755)
	os.Symlink(filepath.Join("..", "..", ".agents", "skills", "pdf"), filepath.Join(work, ".claude", "skills", "pdf"))
	os.MkdirAll(filepath.Join(base, "house-style"), 0o755)
	os.WriteFile(filepath.Join(base, "house-style", "SKILL.md"), []byte("mine"), 0o644)

	if n := UnstageSkills(work); n != 1 {
		t.Fatalf("removed %d, want 1 - one skill, however many names it was staged under", n)
	}
	if _, err := os.Lstat(filepath.Join(work, ".claude", "skills", "pdf")); err == nil {
		t.Error("captain's claude link outlived the copy it points at")
	}
	if _, err := os.Stat(filepath.Join(base, "house-style", "SKILL.md")); err != nil {
		t.Error("a skill captain did not stage is not captain's to delete")
	}
}

// A skill the user put in their own repository under a catalog name is
// theirs: without captain's marker, unstage leaves the copy and the claude
// directory alike. security-audit makes this the common case - it is on
// every shelf, and Cloudflare's README installs it into the repository.
func TestUnstageLeavesTheUsersOwnSkillOfTheSameName(t *testing.T) {
	home := t.TempDir()
	t.Setenv(SkillsDirEnv, home)
	dir := writeSkill(t, home, SecuritySkill, "Security guidance and vulnerability review", nil)
	s, _ := ParseSkill(dir)
	WriteSkillLock(SkillLock{Skills: []Skill{s}})

	// Copies under both names…
	work := t.TempDir()
	for _, base := range []string{filepath.Join(work, ".agents", "skills"), filepath.Join(work, ".claude", "skills")} {
		os.MkdirAll(filepath.Join(base, SecuritySkill), 0o755)
		os.WriteFile(filepath.Join(base, SecuritySkill, "SKILL.md"), []byte("the user's own copy"), 0o644)
	}
	if n := UnstageSkills(work); n != 0 {
		t.Fatalf("removed %d of the user's skills, want 0", n)
	}
	for _, base := range []string{filepath.Join(work, ".agents", "skills"), filepath.Join(work, ".claude", "skills")} {
		if b, err := os.ReadFile(filepath.Join(base, SecuritySkill, "SKILL.md")); err != nil || string(b) != "the user's own copy" {
			t.Errorf("%s: the user's own %s was touched (%v)", base, SecuritySkill, err)
		}
	}

	// …and one copy shared with Claude Code through the very link captain
	// would have made.
	work = t.TempDir()
	own := filepath.Join(work, ".agents", "skills", SecuritySkill)
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, "SKILL.md"), []byte("the user's own copy"), 0o644)
	os.MkdirAll(filepath.Join(work, ".claude", "skills"), 0o755)
	link := filepath.Join(work, ".claude", "skills", SecuritySkill)
	os.Symlink(filepath.Join("..", "..", ".agents", "skills", SecuritySkill), link)
	if n := UnstageSkills(work); n != 0 {
		t.Fatalf("removed %d of the user's skills, want 0", n)
	}
	if b, err := os.ReadFile(filepath.Join(link, "SKILL.md")); err != nil || string(b) != "the user's own copy" {
		t.Errorf("the user's own link to their own copy was removed (%v)", err)
	}
}

// The listing keeps its columns whatever the source's length, and the
// source-available footnote appears only beside a skill it is about.
func TestFormatCatalogFitsItsSourcesAndFootnotes(t *testing.T) {
	cat := []Skill{
		{Name: "security-audit", Source: "cloudflare/security-audit-skill", License: "MIT", Redistribute: true, Description: "Security guidance."},
		{Name: "pdf", Source: "anthropics/skills", License: "proprietary", Description: "Read and write PDFs."},
	}
	out := FormatCatalog(cat)
	lines := strings.Split(out, "\n")
	col := strings.Index(lines[0], "LICENSE")
	if got := strings.Index(lines[1], "MIT"); got != col {
		t.Errorf("a long source shifted the columns after it: LICENSE at %d, MIT at %d\n%s", col, got, out)
	}
	if got := strings.Index(lines[2], "proprietary*"); got != col {
		t.Errorf("LICENSE at %d, proprietary* at %d\n%s", col, got, out)
	}
	if !strings.Contains(out, "source-available, not open source") {
		t.Errorf("a source-available skill is listed without its footnote:\n%s", out)
	}
	if out := FormatCatalog(cat[:1]); strings.Contains(out, "source-available") {
		t.Errorf("footnote with no skill it is about:\n%s", out)
	}
}
