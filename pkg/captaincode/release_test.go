package captaincode

import (
	"strings"
	"testing"
)

func TestBuildManifest(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	m := BuildManifest(deps)
	if m.Captain.Revision != "abc1234" {
		t.Errorf("revision: %q", m.Captain.Revision)
	}
	if m.Captain.Dirty {
		t.Error("should not be dirty")
	}
	if m.GoVersion == "" {
		t.Error("go version empty")
	}
	if len(m.Toolchain) == 0 {
		t.Error("no toolchain pins")
	}
	if len(m.Legs) == 0 {
		t.Error("no legs")
	}
	if m.SchemaVer != AccountingVersion {
		t.Errorf("schema version: %d", m.SchemaVer)
	}
	if m.TaskAPIVer != TaskAPIVersion {
		t.Errorf("task api version: %d", m.TaskAPIVer)
	}
}

func TestBuildManifestDirtyBuild(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", true, true },
	}
	m := BuildManifest(deps)
	if !m.Captain.Dirty {
		t.Error("should be dirty")
	}
}

func TestBuildCompatTable(t *testing.T) {
	rows := BuildCompatTable()
	if len(rows) != len(Toolchain()) {
		t.Errorf("rows: %d, toolchain: %d", len(rows), len(Toolchain()))
	}
	for _, row := range rows {
		if len(row.Caps) != len(Capabilities) {
			t.Errorf("%s: caps %d, expected %d", row.Binary, len(row.Caps), len(Capabilities))
		}
		if row.Tested == "" {
			t.Errorf("%s: empty tested", row.Binary)
		}
	}
}

func TestRunReleaseChecksAllDocsExist(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{
		"docs/INSTALL.md", "docs/ROADMAP.md", "docs/eval/README.md",
		"docs/TASK_API_COMPATIBILITY.md", "docs/TASK_MCP_CONTRACT.md",
		"docs/eval/pilot-12.json", "docs/eval/fixtures/captainfix.bundle",
	} {
		files[path] = "content"
	}
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(files),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	checks := RunReleaseChecks(deps)
	for _, c := range checks {
		if !c.Passed {
			t.Errorf("check %s failed: %s", c.ID, c.Detail)
		}
	}
}

func TestRunReleaseChecksMissingDoc(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	checks := RunReleaseChecks(deps)
	docChecksFailed := 0
	for _, c := range checks {
		if strings.HasPrefix(c.ID, "doc:") && !c.Passed {
			docChecksFailed++
		}
	}
	if docChecksFailed == 0 {
		t.Error("expected doc checks to fail with no files")
	}
}

func TestRunReleaseChecksDirtyBuild(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{
		"docs/INSTALL.md", "docs/ROADMAP.md", "docs/eval/README.md",
		"docs/TASK_API_COMPATIBILITY.md", "docs/TASK_MCP_CONTRACT.md",
		"docs/eval/pilot-12.json", "docs/eval/fixtures/captainfix.bundle",
	} {
		files[path] = "content"
	}
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(files),
		BuildInfo: func() (string, bool, bool) { return "abc1234", true, true },
	}
	checks := RunReleaseChecks(deps)
	for _, c := range checks {
		if c.ID == "build-identity" && c.Passed {
			t.Error("build-identity should fail for dirty build")
		}
	}
}

func TestBuildReleaseReportExitOK(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{
		"docs/INSTALL.md", "docs/ROADMAP.md", "docs/eval/README.md",
		"docs/TASK_API_COMPATIBILITY.md", "docs/TASK_MCP_CONTRACT.md",
		"docs/eval/pilot-12.json", "docs/eval/fixtures/captainfix.bundle",
	} {
		files[path] = "content"
	}
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(files),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	r := BuildReleaseReport(deps)
	if !r.ExitOK {
		for _, c := range r.Checks {
			if !c.Passed {
				t.Errorf("check %s failed: %s", c.ID, c.Detail)
			}
		}
	}
}

func TestFormatReleaseManifest(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	m := BuildManifest(deps)
	out := FormatReleaseManifest(m)
	if !strings.Contains(out, "manifest") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "toolchain") {
		t.Error("missing toolchain section")
	}
	if !strings.Contains(out, "legs") {
		t.Error("missing legs section")
	}
}

func TestFormatReleaseReport(t *testing.T) {
	files := map[string]string{}
	for _, path := range []string{
		"docs/INSTALL.md", "docs/ROADMAP.md", "docs/eval/README.md",
		"docs/TASK_API_COMPATIBILITY.md", "docs/TASK_MCP_CONTRACT.md",
		"docs/eval/pilot-12.json", "docs/eval/fixtures/captainfix.bundle",
	} {
		files[path] = "content"
	}
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(files),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	r := BuildReleaseReport(deps)
	out := FormatReleaseReport(r)
	if !strings.Contains(out, "checks") {
		t.Error("missing checks section")
	}
	if !strings.Contains(out, "compat") {
		t.Error("missing compat section")
	}
}

func TestReleaseJSON(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	r := BuildReleaseReport(deps)
	body, err := ReleaseJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "manifest") {
		t.Error("JSON missing manifest")
	}
}
