package captaincode

import (
	"os"
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
	work := t.TempDir()
	info := filepath.Join(work, ".git", "info")
	os.MkdirAll(info, 0o755)
	original := "# user's own ignores\n*.tmp\n"
	os.WriteFile(filepath.Join(info, "exclude"), []byte(original), 0o644)

	sh, err := StageSkills(work, []SkillPick{{Skill: s}})
	if err != nil || sh == nil {
		t.Fatalf("stage: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(info, "exclude"))
	if !strings.Contains(string(raw), ".agents/skills/") {
		t.Error("a staged shelf must be kept out of `git status` while it is there")
	}
	sh.Remove()
	raw, _ = os.ReadFile(filepath.Join(info, "exclude"))
	if strings.Contains(string(raw), ".agents/skills/") {
		t.Error("captain's exclude block must go when the shelf does")
	}
	if !strings.Contains(string(raw), "*.tmp") {
		t.Error("the user's own exclude lines were eaten")
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
	os.MkdirAll(filepath.Join(base, "house-style"), 0o755)
	os.WriteFile(filepath.Join(base, "house-style", "SKILL.md"), []byte("mine"), 0o644)

	if n := UnstageSkills(work); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(base, "house-style", "SKILL.md")); err != nil {
		t.Error("a skill captain did not stage is not captain's to delete")
	}
}
