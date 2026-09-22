package captaincode

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One readiness verdict, shared by doctor, the director picker and the
// router. Before it, the three disagreed: doctor printed "6 legs ready" and
// the very next dispatch failed on nine of them (2026-09-22).

func onPath(string) (string, error) { return "/usr/local/bin/x", nil }

func notOnPath(bin string) (string, error) { return "", errors.New(bin + " not found") }

func TestAProviderNamedInTheConfigIsNotACredential(t *testing.T) {
	// `captain init` writes an xai block carrying only a baseURL, for the
	// redaction proxy. The old check was a substring of the raw file, so that
	// block alone made grok "ready" on a machine with no xAI key anywhere.
	cfg := []byte(`{"provider":{"xai":{"options":{"baseURL":"http://127.0.0.1:14098/xai/v1"}}}}`)
	ok, why := ProviderCredential(cfg, nil, func(string) string { return "" }, "xai")
	assert.False(t, ok)
	assert.Contains(t, why, "XAI_API_KEY", "the reason names the variable that would fix it")
}

func TestACredentialIsAKeyALoginOrAnEnvVar(t *testing.T) {
	bare := []byte(`{"provider":{"xai":{"options":{"baseURL":"http://x"}}}}`)
	keyed := []byte(`{"provider":{"nim":{"options":{"apiKey":"{env:NVIDIA_API_KEY}"}}}}`)
	none := func(string) string { return "" }

	ok, _ := ProviderCredential(bare, map[string]bool{"xai": true}, none, "xai")
	assert.True(t, ok, "a login in opencode's auth store is a credential")

	ok, _ = ProviderCredential(bare, nil, func(n string) string {
		if n == "XAI_API_KEY" {
			return "xai-key"
		}
		return ""
	}, "xai")
	assert.True(t, ok, "so is the key in the environment")

	ok, _ = ProviderCredential(keyed, nil, none, "nim")
	assert.False(t, ok, "{env:…} pointing at an unset variable resolves to nothing")

	ok, _ = ProviderCredential(keyed, nil, func(n string) string {
		if n == "NVIDIA_API_KEY" {
			return "nv-key"
		}
		return ""
	}, "nim")
	assert.True(t, ok, "…and to a credential once it is set")

	ok, _ = ProviderCredential(nil, nil, none, "")
	assert.True(t, ok, "a CLI transport carries no opencode provider")
}

func TestReadinessTreatsAMalformedConfigAsNoCredential(t *testing.T) {
	// Forgiving parse, honest verdict: a file captain cannot read is not
	// evidence that a provider is authenticated.
	ok, _ := ProviderCredential([]byte(`{"provider":{ oops`), nil, func(string) string { return "" }, "openrouter")
	assert.False(t, ok)
}

func TestReadinessNeedsTheBinaryBeforeAnythingElse(t *testing.T) {
	in := ReadinessInputs{LookPath: notOnPath, Getenv: func(string) string { return "" }}
	r := in.For(LegCodexCLI)
	assert.False(t, r.OK)
	assert.Contains(t, r.Reason, "codex not found")
	assert.Contains(t, r.Reason, "npm i -g @openai/codex", "the verdict carries its own fix")
}

func TestReadinessAsksTheCLIWhetherItIsLoggedIn(t *testing.T) {
	// An installed binary is not a logged-in CLI - each keeps its own
	// credential store, and claude reported ready while `claude auth status`
	// said loggedIn:false.
	in := ReadinessInputs{
		LookPath: onPath,
		Login: func(t Transport) (bool, string) {
			if t == TransportClaudeCLI {
				return false, "not logged in - run `claude /login`"
			}
			return true, ""
		},
	}
	r := in.For(LegClaude)
	assert.False(t, r.OK)
	assert.Contains(t, r.Reason, "claude /login")
	assert.True(t, in.For(LegCursor).OK, "a CLI that IS logged in stays ready")
}

func TestReadinessTrustsAServeOverTheConfigFiles(t *testing.T) {
	// The serve is the authority on what a credential actually reaches: a
	// model missing from its roster is the dispatch that dies with
	// ProviderModelNotFoundError after a full round trip.
	in := ReadinessInputs{
		LookPath: onPath,
		Authed:   map[string]bool{"openrouter": true, "xai": true},
		Getenv:   func(string) string { return "" },
		Serve: ServeRoster{
			Answered:  true,
			Providers: map[string]bool{"xai": true},
			Models:    map[string]bool{"xai/grok-build-0.1": true},
		},
	}
	assert.True(t, in.For(LegGrok).OK, "credentialed and served")

	r := in.For(LegQwen) // openrouter: authenticated here, but absent from the serve
	assert.False(t, r.OK)
	assert.Contains(t, r.Reason, "no provider openrouter")

	r = in.For(LegGrokMax) // xai is served; grok-4.7 is not in this roster
	assert.False(t, r.OK)
	assert.Contains(t, r.Reason, "no model xai/grok-4.7")
}

func TestAServedModelStillNeedsACredential(t *testing.T) {
	// A serve lists a provider's whole bundled catalog whether or not anyone
	// logged in: the live serve offered twelve grok models on a machine with
	// no xAI key. Roster presence must not override the credential check.
	in := ReadinessInputs{
		LookPath: onPath,
		Getenv:   func(string) string { return "" },
		Serve: ServeRoster{
			Answered:  true,
			Providers: map[string]bool{"xai": true},
			Models:    map[string]bool{"xai/grok-build-0.1": true},
		},
	}
	r := in.For(LegGrok)
	assert.False(t, r.OK)
	assert.Contains(t, r.Reason, "no credential")
}

func TestReadinessFallsBackWhenNoServeAnswers(t *testing.T) {
	// No serve is not evidence against a leg: fall back to the config files
	// rather than benching everything.
	in := ReadinessInputs{
		LookPath: onPath,
		Authed:   map[string]bool{"openrouter": true},
		Getenv:   func(string) string { return "" },
	}
	assert.True(t, in.For(LegQwen).OK)
}

func TestReadinessNeverBlocksOnAnUncertainLoginProbe(t *testing.T) {
	// A probe that cannot tell must not bench a working leg: the dispatch
	// classifies the failure now anyway.
	in := ReadinessInputs{
		LookPath: onPath,
		Login:    func(Transport) (bool, string) { return true, "" },
	}
	assert.True(t, in.For(LegClaude).OK)
}

func TestAllSkipsTheDisabledAndCoversTheRegistry(t *testing.T) {
	in := ReadinessInputs{LookPath: onPath, Getenv: func(string) string { return "" }}
	all := in.All()
	require.NotEmpty(t, all)
	for _, s := range Registry() {
		if s.Disabled {
			continue
		}
		_, ok := all[s.ID]
		assert.True(t, ok, "every active leg gets a verdict: %s", s.ID)
	}
}

func TestStripANSIUnwrapsARedrawnCLIAnswer(t *testing.T) {
	// cursor-agent's "Not logged in" arrives inside its progress redraw.
	assert.Contains(t, stripANSI("\x1b[2K\x1b[G\n Not logged in\n"), "Not logged in")
}
