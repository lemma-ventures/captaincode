package captaincode

import (
	"strings"
	"testing"
)

func TestRunRehearsalAllPass(t *testing.T) {
	deps := RehearsalDeps{
		ReadFile: stubReadFile(map[string]string{
			"docs/INSTALL.md": stubInstallDoc(),
		}),
		InitReport: func() (string, error) {
			return "captain init - would apply", nil
		},
	}
	r := RunRehearsal(deps)
	if !r.ExitOK {
		for _, c := range r.Checks {
			if !c.Passed {
				t.Errorf("check %s failed: %s (fix: %s)", c.ID, c.Detail, c.Fix)
			}
		}
	}
	if r.Passed != len(r.Checks) {
		t.Errorf("passed=%d, checks=%d", r.Passed, len(r.Checks))
	}
}

func TestRunRehearsalMissingDoc(t *testing.T) {
	deps := RehearsalDeps{
		ReadFile: stubReadFile(map[string]string{}),
	}
	r := RunRehearsal(deps)
	if r.ExitOK {
		t.Error("expected failure when INSTALL.md is missing")
	}
	found := false
	for _, c := range r.Checks {
		if c.ID == "install-doc" && !c.Passed {
			found = true
		}
	}
	if !found {
		t.Error("install-doc check should fail when doc is missing")
	}
}

func TestRunRehearsalDocMissingInstallRecipe(t *testing.T) {
	doc := "# Install\n\nSome text but no install recipes.\ncaptain doctor prints rollback.\n"
	deps := RehearsalDeps{
		ReadFile: stubReadFile(map[string]string{
			"docs/INSTALL.md": doc,
		}),
	}
	r := RunRehearsal(deps)
	for _, c := range r.Checks {
		if c.ID == "doc-matches-pins" && c.Passed {
			t.Error("doc-matches-pins should fail when install recipes are missing from doc")
		}
	}
}

func TestFormatRehearsal(t *testing.T) {
	deps := RehearsalDeps{
		ReadFile: stubReadFile(map[string]string{
			"docs/INSTALL.md": stubInstallDoc(),
		}),
		InitReport: func() (string, error) { return "ok", nil },
	}
	r := RunRehearsal(deps)
	out := FormatRehearsal(r)
	if !strings.Contains(out, "rehearsal") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "passed") {
		t.Error("missing pass/fail count")
	}
	if r.ExitOK && !strings.Contains(out, "no undocumented manual fix") {
		t.Error("missing exit gate message")
	}
}

func stubReadFile(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if body, ok := files[p]; ok {
			return []byte(body), nil
		}
		return nil, &fileMissingErr{path: p}
	}
}

type fileMissingErr struct{ path string }

func (e *fileMissingErr) Error() string { return "missing: " + e.path }

func stubInstallDoc() string {
	var b strings.Builder
	b.WriteString("# Installation\n\n")
	for _, p := range Toolchain() {
		b.WriteString(p.Install + "\n")
	}
	b.WriteString("go build -o ~/.local/bin/captain ./cmd/captaincode/\n")
	b.WriteString("Rollback pins the tested version back; `captain doctor` prints the exact string.\n")
	return b.String()
}
