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

func TestReleaseReportEvalSection(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	res := &EvalResult{Suite: "pilot", Executions: []EvalExecution{
		{TaskID: "checks", Family: "edit", Arm: "fixed", Status: EvalAccepted, DurationMs: 1000, Totals: Totals{Calls: 1, Measured: 1, CostUSD: 1}},
		{TaskID: "review", Family: "review", Arm: "fixed", Status: EvalAccepted, DurationMs: 2000, Review: &EvalReview{Verdict: ReviewAccept}, Totals: Totals{Calls: 1, Measured: 1, CostUSD: 2}},
	}}
	r := BuildReleaseReportWithEval(deps, res)
	if r.Evaluation == nil || len(r.Evaluation.Arms) != 1 {
		t.Fatalf("missing evaluation: %+v", r.Evaluation)
	}
	arm := r.Evaluation.Arms[0]
	if arm.Accepted != 2 || arm.ReviewAccepted != 1 || arm.TotalSeconds != 3 || arm.TotalCostUSD != 3 {
		t.Fatalf("wrong evaluation totals: %+v", arm)
	}
	out := FormatReleaseReport(r)
	for _, want := range []string{"evaluation", "checks", "review", "$3.0000", "3.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("release report missing %q:\n%s", want, out)
		}
	}
}

func TestReleaseReportNoTotalWithUnknownUsage(t *testing.T) {
	deps := ReleaseDeps{
		ReadFile:  stubReadFile(map[string]string{}),
		BuildInfo: func() (string, bool, bool) { return "abc1234", false, true },
	}
	res := &EvalResult{Suite: "pilot", Executions: []EvalExecution{
		{TaskID: "known", Arm: "fixed", Status: EvalAccepted, Totals: Totals{Calls: 1, Measured: 1, CostUSD: 1}},
		{TaskID: "unknown", Arm: "fixed", Status: EvalRejected, Totals: Totals{Calls: 1, Unknown: 1, CostUSD: 0.25}},
	}}
	out := FormatReleaseReport(BuildReleaseReportWithEval(deps, res))
	if !strings.Contains(out, "at least $1.2500; 1 runs unknown") {
		t.Fatalf("unknown usage was not marked as a lower bound:\n%s", out)
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
