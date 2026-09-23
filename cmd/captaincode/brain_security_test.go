package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncFakeSecuritySkill puts a security-audit skill in a throwaway catalog,
// the way `captain skills sync --source cloudflare/security-audit-skill`
// would, published by source.
func syncFakeSecuritySkill(t *testing.T, source string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(captaincode.SkillsDirEnv, home)
	dir := filepath.Join(home, captaincode.SecuritySkill)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: security-audit\n"+
		"description: Security guidance and vulnerability review for codebases, APIs and services.\n---\n\n# Security Audit\n"), 0o644))
	sk, err := captaincode.ParseSkill(dir)
	require.NoError(t, err)
	sk.Source, sk.Commit = source, "c1c8a8c1471069fb0e188eeaff69b8e8db6564a8"
	require.NoError(t, captaincode.WriteSkillLock(captaincode.SkillLock{Skills: []captaincode.Skill{sk}}))
}

// Every worker puts security first, most of all when it writes code or adds a
// library (2026-09-22). The dependency half names the attack it is there
// for: a package added by the name a model remembers.
func TestSecurityContractPutsSecurityFirst(t *testing.T) {
	c := securityContract()
	for _, want := range []string{"[captain] Security first", "official registry", "typosquatted", "lockfile",
		"install script", "parameterize SQL", "never hardcode, log or print secrets", "Name every dependency you added or changed"} {
		assert.Contains(t, c, want)
	}
	assert.NotContains(t, c, captaincode.SecuritySkill, "nothing synced: the line does not point at a skill that is not there")

	t.Setenv("CAPTAIN_WORKER_SECURITY", "0")
	assert.Empty(t, securityContract())
}

// With the skill synced, the line points at it - in guidance mode, the skill's
// own default; a full audit is the user's call.
func TestSecurityContractPointsAtTheSyncedSkill(t *testing.T) {
	syncFakeSecuritySkill(t, captaincode.SecuritySkillSource)
	c := securityContract()
	assert.Contains(t, c, ".agents/skills/security-audit/SKILL.md")
	assert.Contains(t, c, "guidance mode")
	assert.Contains(t, c, "only when the user asks for one")

	t.Setenv(captaincode.SkillsAlwaysEnv, "off")
	assert.NotContains(t, securityContract(), ".agents/skills/security-audit", "not stocked, so not promised")
	assert.Contains(t, securityContract(), "[captain] Security first", "the posture does not depend on the skill")
}

// …on every path that builds a worker prompt: team and workflow stage here,
// solo and frontier end to end below.
func TestTeamAndWorkflowWorkersPutSecurityFirst(t *testing.T) {
	ws := captaincode.Workspace{Dir: t.TempDir()}
	b := teamBrain()
	assert.Contains(t, b.teamWorkerPrompt(ws, "[user]\nadd a login form", "build the form", captaincode.LegGrok),
		"[captain] Security first")
	assert.Contains(t, b.workflowStagePrompt(ws, "[user]\nadd a login form", 1, 2, nil, "build the form", captaincode.LegCodexCLI),
		"[captain] Security first")
}

// The shelf, end to end: with the skill synced, a solo worker and a frontier
// worker each find security-audit staged in their directory while they run,
// under both names the runtimes read, and the directory is back to what it
// was when the turn ends. Frontier carried no standing contract and stocked
// no shelf before this.
func TestSoloAndFrontierWorkersHoldTheSecuritySkill(t *testing.T) {
	syncFakeSecuritySkill(t, captaincode.SecuritySkillSource)
	for _, model := range []string{"grok", "frontier"} {
		t.Run(model, func(t *testing.T) {
			dir := t.TempDir()
			b := teamBrain()
			b.assessFn = func(task, output, objective string) (captaincode.Assessment, error) {
				return captaincode.Assessment{Quality: 9, Verdict: "good"}, nil
			}
			var mu sync.Mutex
			var seen, via string
			var staged, linked bool
			look := func(seam, prompt string) {
				mu.Lock()
				defer mu.Unlock()
				seen, via = prompt, seam
				_, err := os.Stat(filepath.Join(dir, ".agents", "skills", captaincode.SecuritySkill, "SKILL.md"))
				staged = err == nil
				_, err = os.Stat(filepath.Join(dir, ".claude", "skills", captaincode.SecuritySkill, "SKILL.md"))
				linked = err == nil
			}
			b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
				look("grok", prompt)
				return leg, captaincode.Result{Text: "added the form with a CSRF token", DurationMs: 5}, nil
			}
			b.frontierFn = func(task string, onDelta, onStatus func(string)) (captaincode.Result, error) {
				look("frontier", task)
				return captaincode.Result{Text: "added the form with a CSRF token", DurationMs: 5}, nil
			}
			body, _ := json.Marshal(map[string]any{"model": model, "stream": false,
				"messages": []map[string]string{{"role": "user", "content": "add a login form"}}})
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			r.Header.Set(workspaceHeader, dir)
			b.chatCompletions(rec, r)
			require.Equal(t, 200, rec.Code, rec.Body.String())

			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, model, via, "the %s path ran", model)
			assert.True(t, staged, "the skill is on the shelf while the worker runs")
			assert.True(t, linked, "…and where Claude Code reads skills")
			assert.Contains(t, seen, "[captain] Security first")
			assert.Contains(t, seen, ".agents/skills/security-audit/SKILL.md")
			assert.True(t, strings.Index(seen, "[captain] Security first") > strings.Index(seen, "add a login form"),
				"the contract trails the task")
			left, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, left, "the shelf leaves with the turn")
		})
	}
}

// A session title gets no contract at all - it is six words of housekeeping.
func TestTitlePromptHasNoSecurityLine(t *testing.T) {
	b := teamBrain()
	var seen string
	b.runWorkerFn = func(leg captaincode.Leg, prompt string, onDelta, onStatus func(string)) (captaincode.Leg, captaincode.Result, error) {
		seen = prompt
		return leg, captaincode.Result{Text: "a title", DurationMs: 1}, nil
	}
	body, _ := json.Marshal(map[string]any{"model": "auto", "stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": captaincode.TitleMarker},
			{"role": "user", "content": "name this conversation"},
		}})
	rec := httptest.NewRecorder()
	b.chatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NotEmpty(t, seen)
	assert.NotContains(t, seen, "Security first")
}

// Doctor says whether the required skill is on every shelf, and how to fix it
// when it is not - an empty catalog used to print nothing at all.
func TestDoctorReportsTheAlwaysOnSecuritySkill(t *testing.T) {
	t.Setenv(captaincode.SkillsDirEnv, t.TempDir())
	lines := strings.Join(alwaysSkillLines(captaincode.Catalog(), captaincode.AlwaysSkills()), "\n")
	assert.Contains(t, lines, "✗ security-audit")
	assert.Contains(t, lines, "captain skills sync --source cloudflare/security-audit-skill")

	syncFakeSecuritySkill(t, captaincode.SecuritySkillSource)
	lines = strings.Join(alwaysSkillLines(captaincode.Catalog(), captaincode.AlwaysSkills()), "\n")
	assert.Contains(t, lines, "✓ security-audit")
	assert.Contains(t, lines, "cloudflare/security-audit-skill@c1c8a8c")

	// One name is one skill and the first sync wins: a same-named skill from
	// another catalog is named, not passed off as Cloudflare's.
	syncFakeSecuritySkill(t, "someone/else")
	lines = strings.Join(alwaysSkillLines(captaincode.Catalog(), captaincode.AlwaysSkills()), "\n")
	assert.Contains(t, lines, "⚠ security-audit")
	assert.Contains(t, lines, "from someone/else")

	t.Setenv(captaincode.SkillsAlwaysEnv, "off")
	assert.Empty(t, alwaysSkillLines(captaincode.Catalog(), captaincode.AlwaysSkills()))
}
