package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `captain doctor` - the preflight a new install needs: which legs are wired,
// which credentials/binaries are missing, and what to do about each one. It is
// the first command in the README, so it must be honest on a machine that has
// nothing set up yet.

func fakeBin(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
}

// noServe keeps doctor from probing a local opencode brain. Without it, a
// running serve on the developer's machine flips openrouter/nim/hf legs to
// ready and the "blocked without credential" assertions fail.
func noServe() *captaincode.ServeRoster {
	return &captaincode.ServeRoster{}
}

// clearProviderKeys drops every key readiness would treat as a credential, so
// a developer's shell cannot make a leg ready that the test config left out.
func clearProviderKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"XAI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"OPENROUTER_API_KEY", "HF_TOKEN", "NVIDIA_API_KEY", "GEMINI_API_KEY",
		"TYPESAFE_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

// opencodeConfigWith writes a minimal opencode config declaring the given
// providers WITH a key each, which is what makes an opencode leg runnable.
// A block alone is not enough and must not be: `captain init` writes an xai
// block carrying only a baseURL, and doctor called grok ready on a machine
// with no xAI credential at all (2026-09-22) - see
// opencodeConfigWithBareBlocks for that case.
func opencodeConfigWith(t *testing.T, providers ...string) string {
	t.Helper()
	body := `{"provider":{`
	for i, p := range providers {
		if i > 0 {
			body += ","
		}
		body += `"` + p + `":{"options":{"apiKey":"sk-test"},"models":{}}`
	}
	body += `}}`
	p := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// opencodeConfigWithBareBlocks writes provider blocks that carry no key -
// the shape `captain init` leaves behind when it routes a provider through
// the redaction proxy.
func opencodeConfigWithBareBlocks(t *testing.T, providers ...string) string {
	t.Helper()
	body := `{"provider":{`
	for i, p := range providers {
		if i > 0 {
			body += ","
		}
		body += `"` + p + `":{"options":{"baseURL":"http://127.0.0.1:14098/` + p + `/v1"}}`
	}
	body += `}}`
	p := filepath.Join(t.TempDir(), "opencode.jsonc")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestDoctorRefusesToCallAKeylessProviderReady(t *testing.T) {
	// The regression that cost ninety seconds a run: a provider NAMED in the
	// config is not a provider that can authenticate. doctor said grok was
	// ready; the dispatch said "xAI API key API key is missing".
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWithBareBlocks(t, "xai"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
	})
	out := sb.String()

	assert.Regexp(t, `(?m)^\s*✗\s+grok\b.*no credential`, out, "a block with no key is not a credential")
	assert.Contains(t, out, "XAI_API_KEY", "and the line names the variable that would fix it")
}

func TestDoctorAcceptsAKeyFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)
	t.Setenv("XAI_API_KEY", "xai-live-key")

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWithBareBlocks(t, "xai"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
	})

	assert.Regexp(t, `(?m)^\s*✓\s+grok\b`, sb.String(), "the key opencode would interpolate counts as one")
}

func TestDoctorSeparatesReadyLegsFromBlockedOnes(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	fakeBin(t, dir, "claude")
	// no `codex`, no `cursor-agent` on this machine
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)

	var sb strings.Builder
	ready := runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		captainEnv:     filepath.Join(t.TempDir(), "env"), // absent
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
	})
	out := sb.String()

	assert.Positive(t, ready, "claude and the xai-backed leg are wired")
	assert.Regexp(t, `(?m)^\s*✓\s+claude\b`, out, "claude CLI is on PATH → ready")
	assert.Regexp(t, `(?m)^\s*✓\s+grok\b`, out, "opencode + the xai provider present → ready")
	assert.Regexp(t, `(?m)^\s*✗\s+codex-cli\b`, out, "codex binary missing → blocked")
	assert.Contains(t, out, "npm i -g @openai/codex", "a blocked CLI leg says how to install it")
	assert.Regexp(t, `(?m)^\s*✗\s+kimi\b.*provider`, out,
		"an opencode leg whose provider is not in the config is blocked, and says so")
	assert.Contains(t, out, "captain init", "a missing captain env points at the command that writes it")
	assert.Contains(t, out, "connection refused", "a brain that is down reports why")
	assert.Contains(t, out, "captain brain", "…and how to start it")
}

func TestDoctorMarksLegsExcludedByCaptainLegs(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	fakeBin(t, dir, "claude")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "grok,claude")
	clearProviderKeys(t)

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai", "nim", "openrouter"),
		brain:          func() (string, error) { return "ok · idle", nil },
		serve:          noServe(),
	})
	out := sb.String()

	assert.Regexp(t, `(?m)^\s*✓\s+grok\b`, out, "an allowed leg stays ready")
	assert.Regexp(t, `(?m)^\s*·\s+minimax\b.*CAPTAIN_LEGS`, out,
		"a leg outside CAPTAIN_LEGS is skipped, not reported as broken")
}

func TestDoctorReportsAHealthyBrain(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		brain:          func() (string, error) { return "ok · director claude · idle", nil },
		serve:          noServe(),
	})

	assert.Contains(t, sb.String(), "director claude", "the live brain's own summary is shown verbatim")
}

func TestDoctorCountsNothingReadyOnABareMachine(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no agent CLI at all
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)

	var sb strings.Builder
	ready := runDoctor(&sb, doctorOpts{
		opencodeConfig: filepath.Join(t.TempDir(), "missing.jsonc"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
	})

	assert.Zero(t, ready, "nothing is runnable, and doctor says so rather than pretending")
	assert.Contains(t, sb.String(), "no leg is ready")
}

func TestDoctorAcceptsProvidersFromTheOpencodeAuthStore(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")
	clearProviderKeys(t)

	// xai/openai legs authenticate through `opencode auth login`, which writes
	// the auth store - they never appear in opencode.jsonc. Reporting them as
	// broken because the config file does not name them is a false alarm.
	auth := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"xai":{"type":"oauth"},"openai":{"type":"oauth"}}`), 0o600))

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t), // no providers declared
		opencodeAuth:   auth,
		brain:          func() (string, error) { return "ok · idle", nil },
		serve:          noServe(),
	})
	out := sb.String()

	assert.Regexp(t, `(?m)^\s*✓\s+grok\b`, out, "xai is authenticated in the auth store")
	assert.Regexp(t, `(?m)^\s*✓\s+free\b`, out, "opencode's own roster needs no credential block")
	assert.Regexp(t, `(?m)^\s*✗\s+glm\b`, out, "openrouter is in neither place → still blocked")
	assert.NotContains(t, out, "oauth", "credential values are never read or printed")
}

// The adapter version contract (ROADMAP M1.1). A binary on PATH is not the
// question doctor should answer: a CLI can be present and too old to honour
// the flags captain compiles against, or be a different program wearing the
// name.
func TestDoctorReportsThePinnedAdapterVersions(t *testing.T) {
	dir := t.TempDir()
	for _, b := range []string{"opencode", "claude", "codex", "cursor-agent"} {
		fakeBin(t, dir, b)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")

	versions := map[string]string{
		"opencode":     "1.18.30",             // at the tested pin
		"claude":       "1.0.0 (Claude Code)", // below the minimum
		"codex":        "codex-cli 9.9.9",     // usable, but not what was tested
		"cursor-agent": "",                    // says nothing about itself
	}
	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		captainEnv:     filepath.Join(t.TempDir(), "env"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
		runVersion: func(path string, _ ...string) ([]byte, error) {
			return []byte(versions[filepath.Base(path)]), nil
		},
	})
	out := sb.String()

	assert.Regexp(t, `(?m)^\s*✓\s+opencode\s+1\.18\.30\b`, out, "the tested version is accepted")
	assert.Regexp(t, `(?m)^\s*✗\s+claude\s+1\.0\.0\b.*below pinned minimum`, out,
		"too old to honour the contract is blocked, with the upgrade command")
	assert.Regexp(t, `(?m)^\s*~\s+codex\s+9\.9\.9\b.*newer than tested`, out,
		"newer is usable but named, so an evidence run cannot quote the wrong version")
	assert.Regexp(t, `(?m)^\s*~\s+cursor-agent\s+\?`, out,
		"a version captain could not read is never reported as satisfying the pin")

	// A blocked adapter blocks its leg, and says which version blocked it.
	assert.Regexp(t, `(?m)^\s*✗\s+claude\s+claude-cli\b.*1\.0\.0`, out)
	// An unreadable version does not block a leg the user has installed.
	assert.Regexp(t, `(?m)^\s*✓\s+cursor\s+cursor-cli\b`, out)
}

// The build section (ROADMAP M1.1): the report names the brain binary and the
// terminal fork alongside the adapters, and says which of them an evidence run
// may quote.
func TestDoctorNamesItsOwnBuildAndTheTerminalFork(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	fakeBin(t, dir, "bun")
	t.Setenv("PATH", dir)

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "package.json"),
		[]byte(`{"packageManager":"bun@1.3.14"}`), 0o644))

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		captainEnv:     filepath.Join(t.TempDir(), "env"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
		runVersion:     func(string, ...string) ([]byte, error) { return []byte("1.3.14"), nil },
		self: captaincode.SelfProbe{
			SourceDir:  src,
			Executable: func() (string, error) { return filepath.Join(dir, "captain"), nil },
			LookPath:   func(string) (string, error) { return "", errors.New("not found") },
			BuildInfo:  func() (string, bool, bool) { return "abcdef1234567890", false, true },
			Git: func(_ string, args ...string) ([]byte, error) {
				if args[0] == "rev-parse" {
					return []byte("0d48e2a0000000\n"), nil
				}
				return []byte(" M cmd/captaincode/main.go\n"), nil
			},
		},
	})
	out := sb.String()

	assert.Regexp(t, `(?m)^build\s+2 of 3 components name a revision`, out,
		"the dirty fork is counted apart from the clean binary and the pinned runtime")
	assert.Regexp(t, `(?m)^\s*✓\s+captain\s+abcdef1\b`, out, "the brain names the revision it was built from")
	assert.Regexp(t, `(?m)^\s*~\s+terminal\s+0d48e2a\b`, out, "a modified fork checkout is reported, not hidden")
	assert.Regexp(t, `(?m)^\s*✓\s+bun\s+1\.3\.14\b`, out, "the fork's own packageManager pin is the authority")
	assert.Contains(t, out, "toolchain ", "the adapters are still reported below the build")
}

// The capability probes (ROADMAP M2.2). The registry declares what each
// transport supports; doctor exercises the binary's --help to catch a CLI
// that dropped a flag between releases. A missing flag is reported but does
// not block the leg — the version contract above already gates that.
func TestDoctorReportsCapabilityProbes(t *testing.T) {
	dir := t.TempDir()
	for _, b := range []string{"opencode", "claude", "codex", "cursor-agent"} {
		fakeBin(t, dir, b)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t, "xai"),
		captainEnv:     filepath.Join(t.TempDir(), "env"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
		runVersion:     func(string, ...string) ([]byte, error) { return []byte("1.0.0"), nil },
		runHelp: func(path string, _ ...string) ([]byte, error) {
			switch filepath.Base(path) {
			case "claude":
				return []byte("--print --permission-mode --output-format"), nil
			case "codex":
				return []byte("exec --sandbox --json"), nil
			case "cursor-agent":
				return []byte("-p --trust --output-format"), nil
			case "opencode":
				return []byte("serve auth"), nil
			}
			return nil, nil
		},
	})
	out := sb.String()

	assert.Contains(t, out, "caps ", "doctor reports a caps section")
	assert.Regexp(t, `(?m)^caps\s+\d+ verified`, out, "verified count is reported")

	capsStart := strings.Index(out, "caps ")
	capsEnd := strings.Index(out, "legs ")
	if capsStart < 0 || capsEnd < 0 || capsEnd <= capsStart {
		t.Fatal("caps section not bounded in output")
	}
	capsSection := out[capsStart:capsEnd]
	assert.NotContains(t, capsSection, "✗", "no missing flags when all help output is present")
}

func TestDoctorReportsMissingCapabilityFlag(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "claude")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "")

	var sb strings.Builder
	runDoctor(&sb, doctorOpts{
		opencodeConfig: opencodeConfigWith(t),
		captainEnv:     filepath.Join(t.TempDir(), "env"),
		brain:          func() (string, error) { return "", errors.New("connection refused") },
		serve:          noServe(),
		runVersion:     func(string, ...string) ([]byte, error) { return []byte("2.1.270"), nil },
		runHelp: func(_ string, _ ...string) ([]byte, error) {
			return []byte("--print"), nil // no --permission-mode
		},
	})
	out := sb.String()

	assert.Contains(t, out, "caps ", "caps section is present")
	assert.Regexp(t, `(?m)^\s*✗\s+claude-cli\s+permissions`, out,
		"missing --permission-mode is reported as a capability mismatch")
}

// The decision leg drives no binary and sits outside CAPTAIN_LEGS: doctor
// reports it by its key, and says what it is for so nobody expects a worker.
func TestDoctorReportsTheDecisionLegByItsKey(t *testing.T) {
	dir := t.TempDir()
	fakeBin(t, dir, "opencode")
	t.Setenv("PATH", dir)
	t.Setenv("CAPTAIN_LEGS", "glm,claude")
	envPath := filepath.Join(t.TempDir(), "env")

	t.Setenv(captaincode.SystemOneKeyEnv, "")
	var sb strings.Builder
	runDoctor(&sb, doctorOpts{opencodeConfig: opencodeConfigWith(t, "openrouter"), captainEnv: envPath,
		brain: func() (string, error) { return "ok · idle", nil }, serve: noServe()})
	assert.Regexp(t, `(?m)^\s*✗\s+jev\b.*TYPESAFE_API_KEY not set`, sb.String())
	assert.Contains(t, sb.String(), envPath, "…and where to put it")

	t.Setenv(captaincode.SystemOneKeyEnv, "k")
	sb.Reset()
	runDoctor(&sb, doctorOpts{opencodeConfig: opencodeConfigWith(t, "openrouter"), captainEnv: envPath,
		brain: func() (string, error) { return "ok · idle", nil }, serve: noServe()})
	assert.Regexp(t, `(?m)^\s*✓\s+jev\s+system-one\s+typesafe/jev-latest.*decision leg`, sb.String(),
		"ready by its key, outside the worker allowlist, and named for what it is")
}
