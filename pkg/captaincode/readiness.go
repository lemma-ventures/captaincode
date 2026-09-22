package captaincode

// Leg readiness: ONE answer to "can this leg take work on this machine, right
// now?", and one place to fix when the answer is wrong.
//
// Three copies of that question used to disagree. `captain doctor` computed it
// inline while printing (cmd/captaincode/doctor_cmd.go), the director picker
// kept a second copy, and the router kept none at all - it filtered on
// cooldowns and dispatched everything else. So a run could read "6 legs ready"
// and then fail on nine of them in sequence, each one taking a real timeout to
// discover what doctor could have said instantly (2026-09-22).
//
// Two things make an answer here trustworthy:
//
//   - A running serve is the authority on opencode legs. It knows which
//     providers authenticated and which models that leaves runnable; guessing
//     from config files is a fallback for when no serve answers.
//   - An installed binary is not a logged-in CLI. Each agent CLI keeps its own
//     credential store, so readiness asks the CLI, not the filesystem.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Readiness is one leg's verdict. Reason is empty when OK, and otherwise
// carries the one command that changes the answer.
type Readiness struct {
	Leg    Leg
	OK     bool
	Reason string
}

// ServeRoster is what a running opencode serve reports it can run: the
// providers that authenticated, and the models those leave runnable.
// Answered is false when no serve replied, which is not evidence either way.
type ServeRoster struct {
	Answered  bool
	Providers map[string]bool
	Models    map[string]bool // "provider/model"
}

// ReadinessInputs are the machine facts a verdict is computed from. They are
// injected so the rules can be tested without a serve, a CLI or a HOME.
type ReadinessInputs struct {
	LookPath func(string) (string, error)
	Config   []byte          // opencode.jsonc, as read from disk
	Authed   map[string]bool // provider ids in opencode's auth store
	Getenv   func(string) string
	Serve    ServeRoster
	// Login answers "is this agent CLI logged in?" for a CLI transport. A
	// false verdict must be certain: "unknown" is reported as logged in,
	// because a probe that cannot tell must not bench a working leg.
	Login func(Transport) (bool, string)
}

// providerKeyEnv is the environment variable each provider's key lives in
// when it is not in opencode's auth store. opencode interpolates these into
// a provider block as {env:NAME}.
var providerKeyEnv = map[string]string{
	"xai":         "XAI_API_KEY",
	"openai":      "OPENAI_API_KEY",
	"anthropic":   "ANTHROPIC_API_KEY",
	"openrouter":  "OPENROUTER_API_KEY",
	"huggingface": "HF_TOKEN",
	"nim":         "NVIDIA_API_KEY",
	"nvidia":      "NVIDIA_API_KEY",
	"google":      "GEMINI_API_KEY",
}

// ProviderCredential reports whether an opencode provider can authenticate,
// and says what is missing when it cannot.
//
// The old test was `strings.Contains(cfg, "\"xai\"")` - a substring of the raw
// file. `captain init` writes an xai block carrying nothing but a baseURL (the
// proxy route for an OAuth provider), so grok and grok-max reported ready with
// no key anywhere, and failed at dispatch with "API key is missing"
// (2026-09-22). A provider is credentialed when opencode holds a login for it,
// when its block carries a key that resolves, or when its key is in the
// environment - never because its name appears in a file.
func ProviderCredential(cfg []byte, authed map[string]bool, getenv func(string) string, provider string) (bool, string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch {
	case provider == "": // CLI transports carry no opencode provider
		return true, ""
	case provider == "opencode": // opencode's own hosted roster needs no credential block
		return true, ""
	case authed[provider]:
		return true, ""
	}
	if env, ok := providerKeyEnv[provider]; ok && strings.TrimSpace(getenv(env)) != "" {
		return true, ""
	}
	if key, ok := configProviderKey(cfg, provider); ok && resolvesToAKey(key, getenv) {
		return true, ""
	}
	fix := "`opencode auth login`"
	if env, ok := providerKeyEnv[provider]; ok {
		fix += " or set " + env
	}
	return false, "provider " + provider + " has no credential - " + fix
}

// configProviderKey pulls provider.<id>.options.apiKey out of opencode.jsonc.
// Parsing is deliberately forgiving: the file is hand-edited JSONC, and a
// readiness check that dies on a stray comma helps nobody.
func configProviderKey(cfg []byte, provider string) (string, bool) {
	var parsed struct {
		Provider map[string]struct {
			Options struct {
				APIKey string `json:"apiKey"`
			} `json:"options"`
		} `json:"provider"`
	}
	if json.Unmarshal(StripJSONC(cfg), &parsed) != nil {
		return "", false
	}
	block, ok := parsed.Provider[provider]
	if !ok {
		return "", false
	}
	return block.Options.APIKey, true
}

// resolvesToAKey reports whether a block's apiKey is actually worth something:
// a literal, or an {env:NAME} whose variable is set.
func resolvesToAKey(key string, getenv func(string) string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(key, "{env:"); ok {
		name, ok := strings.CutSuffix(rest, "}")
		if !ok {
			return false
		}
		return strings.TrimSpace(getenv(name)) != ""
	}
	return true
}

// For computes one leg's verdict.
func (in ReadinessInputs) For(l Leg) Readiness {
	s, ok := Spec(l)
	if !ok {
		return Readiness{Leg: l, Reason: "unknown leg - `captain legs` lists the ones this build knows"}
	}
	if s.Disabled {
		return Readiness{Leg: l, Reason: "disabled in the registry"}
	}
	if s.Transport == TransportSystemOne {
		// A decision leg drives no binary: its readiness is its key.
		if key, _ := SystemOneKey(); key == "" {
			return Readiness{Leg: l, Reason: SystemOneKeyEnv + " not set - see console.typesafe.ai/settings/keys"}
		}
		return Readiness{Leg: l, OK: true}
	}
	lookPath := in.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	pin, havePin := ToolPinFor(s.Transport)
	if havePin {
		if _, err := lookPath(pin.Bin); err != nil {
			return Readiness{Leg: l, Reason: pin.Bin + " not found - " + pin.Install}
		}
	}
	if s.Transport != TransportOpencode {
		// An agent CLI: installed is not logged in.
		if in.Login != nil {
			if ok, why := in.Login(s.Transport); !ok {
				return Readiness{Leg: l, Reason: why}
			}
		}
		return Readiness{Leg: l, OK: true}
	}
	// An opencode leg. Credentials first: a serve lists a provider's whole
	// bundled catalog whether or not anyone ever logged into it (xai offers
	// twelve grok models here with no key on the machine), so roster presence
	// is not proof of authentication - it would have called grok ready for
	// exactly the reason the old substring check did.
	if ok, why := ProviderCredential(in.Config, in.Authed, in.Getenv, s.Provider); !ok {
		return Readiness{Leg: l, Reason: why}
	}
	// Then the serve, which is the authority on what the credential reaches:
	// a model the roster does not carry is precisely the dispatch that dies
	// with ProviderModelNotFoundError after a full round trip.
	if in.Serve.Answered {
		switch {
		case in.Serve.Models[s.Provider+"/"+s.Model]:
			return Readiness{Leg: l, OK: true}
		case !in.Serve.Providers[s.Provider]:
			return Readiness{Leg: l, Reason: "the running serve has no provider " + s.Provider + " - `opencode auth login`, then restart the serve"}
		default:
			return Readiness{Leg: l, Reason: "the running serve has no model " + s.Provider + "/" + s.Model + " - check the id with `captain legs`"}
		}
	}
	return Readiness{Leg: l, OK: true}
}

func (in ReadinessInputs) getenv(name string) string {
	if in.Getenv == nil {
		return os.Getenv(name)
	}
	return in.Getenv(name)
}

// All computes every active leg's verdict.
func (in ReadinessInputs) All() map[Leg]Readiness {
	out := make(map[Leg]Readiness, len(AllLegs))
	for _, s := range Registry() {
		if s.Disabled {
			continue
		}
		out[s.ID] = in.For(s.ID)
	}
	return out
}

// DefaultOpencodePort is the serve captain drives.
const DefaultOpencodePort = 14096

// OpencodeBaseURL is where readiness (and anything else asking the serve a
// question) reaches it.
func OpencodeBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("CAPTAIN_OPENCODE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return fmt.Sprintf("http://127.0.0.1:%d", DefaultOpencodePort)
}

// FetchServeRoster asks a running serve what it can run. A serve that does
// not answer leaves Answered false, and readiness falls back to the config
// files rather than inventing a verdict.
func FetchServeRoster(baseURL string) ServeRoster {
	out := ServeRoster{Providers: map[string]bool{}, Models: map[string]bool{}}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(baseURL + "/config/providers")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var cfg struct {
		Providers *[]struct {
			ID     string                     `json:"id"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if json.NewDecoder(resp.Body).Decode(&cfg) != nil || cfg.Providers == nil {
		return out // not a providers answer (a fake, an older serve): no verdict
	}
	for _, p := range *cfg.Providers {
		out.Providers[p.ID] = true
		for id := range p.Models {
			out.Models[p.ID+"/"+id] = true
		}
	}
	out.Answered = true
	return out
}

// CLILoginState asks an agent CLI whether it is logged in. Certainty is the
// whole point: only a clear "no" blocks a leg. A CLI that is slow, that fails,
// or that words its answer differently than expected is reported as logged in
// and left to fail at dispatch, where the error is now classified anyway.
func CLILoginState(t Transport) (bool, string) {
	pin, ok := ToolPinFor(t)
	if !ok {
		return true, ""
	}
	var args []string
	switch t {
	case TransportClaudeCLI:
		args = []string{"auth", "status"}
	case TransportCursorCLI:
		args = []string{"status"}
	default:
		// codex exec has no status subcommand that does not start a session.
		return true, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pin.Bin, args...).CombinedOutput()
	if err != nil && len(out) == 0 {
		return true, ""
	}
	text := strings.ToLower(stripANSI(string(out)))
	if t == TransportClaudeCLI {
		// claude answers in JSON; trust the field, not the prose.
		var st struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal(out, &st) == nil && st.LoggedIn != nil {
			if *st.LoggedIn {
				return true, ""
			}
			return false, "not logged in - run `claude /login`"
		}
	}
	if strings.Contains(text, "not logged in") || strings.Contains(text, "authentication required") {
		return false, "not logged in - run `" + pin.Bin + " login`"
	}
	return true, ""
}

// stripANSI removes the escape sequences a CLI writes when it redraws its
// progress lines - cursor-agent's "Not logged in" arrives wrapped in them.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		for i++; i < len(s); i++ {
			c := s[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				break
			}
		}
	}
	return b.String()
}

// DefaultReadinessInputs reads the machine: opencode's config and auth store,
// a running serve's roster, and each agent CLI's login state.
func DefaultReadinessInputs() ReadinessInputs {
	cfg, _ := os.ReadFile(OpencodeConfigPath())
	return ReadinessInputs{
		Config: cfg,
		Authed: AuthedProviders(OpencodeAuthPath()),
		Serve:  FetchServeRoster(OpencodeBaseURL()),
		Login:  CLILoginState,
	}
}

// OpencodeAuthPath is where opencode stores provider credentials.
func OpencodeAuthPath() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode", "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

// AuthedProviders lists the provider ids in opencode's auth store. Only the
// KEYS are read - the credentials themselves are never loaded or logged.
func AuthedProviders(path string) map[string]bool {
	out := map[string]bool{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var store map[string]json.RawMessage
	if json.Unmarshal(body, &store) != nil {
		return out
	}
	for k := range store {
		out[k] = true
	}
	return out
}

// Cached verdicts. Readiness probes a serve and two CLIs, so it is computed
// once and reused for a minute: PATH, credentials and a serve's roster do not
// change between two dispatches, and the router asks on every turn.
var (
	readyMu   sync.Mutex
	readyAt   time.Time
	readyMap  map[Leg]Readiness
	readyTTL                         = time.Minute
	readyOnce func() ReadinessInputs = DefaultReadinessInputs
)

// LegReadiness is every active leg's verdict, cached for a minute.
func LegReadiness() map[Leg]Readiness {
	readyMu.Lock()
	defer readyMu.Unlock()
	if readyMap != nil && time.Since(readyAt) < readyTTL {
		return readyMap
	}
	readyMap = readyOnce().All()
	readyAt = time.Now()
	return readyMap
}

// LegReady is one leg's verdict, from the same cache.
func LegReady(l Leg) Readiness {
	if r, ok := LegReadiness()[l]; ok {
		return r
	}
	return Readiness{Leg: l, OK: true} // a leg outside the registry is not ours to block
}

// RefreshReadiness drops the cache: after `opencode auth login`, a serve
// restart, or a leg added at runtime.
func RefreshReadiness() {
	readyMu.Lock()
	readyMap, readyAt = nil, time.Time{}
	readyMu.Unlock()
}
