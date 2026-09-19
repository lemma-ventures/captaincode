package main

// `captain doctor` - preflight. On a fresh machine the honest first question is
// not "what can this do?" but "what of it works here?": which legs have a
// binary and a configured provider, which are excluded by CAPTAIN_LEGS, whether
// the brain is up, and which config files exist. Every blocked line carries the
// one command that unblocks it, so the output doubles as the install guide.
//
//	captain doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type doctorOpts struct {
	lookPath       func(string) (string, error)            // stubbed in tests
	opencodeConfig string                                  // path to opencode.jsonc
	opencodeAuth   string                                  // path to opencode's auth store
	captainEnv     string                                  // path to ~/.config/captain/env
	brain          func() (string, error)                  // one-line brain summary, or why not
	runVersion     func(string, ...string) ([]byte, error) // stubbed in tests
	runHelp        func(string, ...string) ([]byte, error) // stubbed in tests (capability probes)
	self           captaincode.SelfProbe                   // captain's own build identity (ROADMAP M1.1)
}

// legTool maps a transport to the binary that drives it and how to get it.
// Both come from the pinned toolchain (ROADMAP M1.1) so doctor's install hint
// and its version contract can never drift apart.
func legTool(t captaincode.Transport) (bin, install string) {
	if p, ok := captaincode.ToolPinFor(t); ok {
		return p.Bin, p.Install
	}
	return "opencode", "curl -fsSL https://opencode.ai/install | bash"
}

// toolVersion runs `<bin> --version`, combining stderr, since some adapters
// print their version there.
func runToolVersion(path string, args ...string) ([]byte, error) {
	cmd := exec.Command(path, args...)
	return cmd.CombinedOutput()
}

// toolMark is the glyph for a probed adapter. A version captain could not read
// is marked apart from one it read and accepted: doctor never reports an
// unverified adapter as pinned.
func toolMark(s captaincode.ToolStatus) string {
	switch s.State {
	case captaincode.ToolOK:
		return "✓"
	case captaincode.ToolNewer, captaincode.ToolUnknown, captaincode.ToolDirty:
		return "~"
	default:
		return "✗"
	}
}

// selfMark is the glyph for a probed captain component. A dirty checkout is
// marked apart from a clean one: the binary works, but the revision it names is
// not the code that ran, and an evidence report may not quote it.
func selfMark(s captaincode.SelfStatus) string {
	switch s.State {
	case captaincode.ToolOK:
		return "✓"
	case captaincode.ToolMismatch:
		return "✗"
	default:
		return "~"
	}
}

// brainSummary asks the running brain how it is, in one line.
func brainSummary() (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(brainURL() + "/v1/health")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var h struct {
		OK       bool   `json:"ok"`
		Director string `json:"director"`
		Busy     int    `json:"busy"`
		Cwd      string `json:"cwd"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return "", err
	}
	state := "idle"
	if h.Busy > 0 {
		state = fmt.Sprintf("%d run(s) in flight", h.Busy)
	}
	return fmt.Sprintf("ok · director %s · %s · cwd %s", h.Director, state, h.Cwd), nil
}

// allowedLegs is the CAPTAIN_LEGS allowlist, or nil when unset (= all legs).
func allowedLegs() map[string]bool {
	v := strings.TrimSpace(os.Getenv("CAPTAIN_LEGS"))
	if v == "" {
		return nil
	}
	m := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}

// runDoctor writes the report and returns how many legs are actually runnable.
func runDoctor(w io.Writer, o doctorOpts) int {
	if o.lookPath == nil {
		o.lookPath = exec.LookPath
	}
	if o.opencodeConfig == "" {
		o.opencodeConfig = captaincode.OpencodeConfigPath()
	}
	if o.opencodeAuth == "" {
		o.opencodeAuth = opencodeAuthPath()
	}
	if o.captainEnv == "" {
		o.captainEnv = captaincode.CaptainEnvPath()
	}
	if o.brain == nil {
		o.brain = brainSummary
	}

	// Brain.
	if s, err := o.brain(); err == nil {
		fmt.Fprintf(w, "brain      %s\n", s)
	} else {
		fmt.Fprintf(w, "brain      down (%v) - start it with `captain brain`\n", err)
	}

	// Memory: the main brain and the local brain of the project doctor runs in.
	fmt.Fprint(w, captaincode.RenderBrainChecks(captaincode.CheckBrains(euclidCwd())))

	// Config files. Their absence is a fixable state, not an error.
	cfg, cfgErr := os.ReadFile(o.opencodeConfig)
	authed := authedProviders(o.opencodeAuth)
	fmt.Fprintf(w, "config     opencode %s\n", presence(o.opencodeConfig, cfgErr == nil, "run `captain init`"))
	_, envErr := os.Stat(o.captainEnv)
	fmt.Fprintf(w, "           captain  %s\n", presence(o.captainEnv, envErr == nil, "run `captain init`"))

	// Toolchain. The version contract the legs below are driven through.
	if o.runVersion == nil {
		o.runVersion = runToolVersion
	}

	// Build. The routing engine and the terminal it serves - the half of the
	// version contract that is captain itself. Reported before the adapters,
	// because a report that names four CLIs and not the binary that chose
	// between them is still not reproducible.
	if o.self.SourceDir == "" {
		o.self.SourceDir = captainSourceDir()
	}
	if o.self.LookPath == nil {
		o.self.LookPath = o.lookPath // one PATH for the whole report
	}
	selves := captaincode.ProbeSelf(o.self)
	if pin, ok := captaincode.BunPin(o.self.SourceDir, o.self.ReadFile); ok {
		st := captaincode.ProbeTool(pin, o.lookPath, o.runVersion)
		selves = append(selves, captaincode.SelfStatus{
			Component: "bun", State: st.State, Version: st.Version, Path: st.Path,
			Detail: strings.TrimSpace(st.Detail + " (pinned " + pin.Tested + " by the fork's package.json)"),
		})
	}
	named := 0
	for _, s := range selves {
		if s.Reproducible() {
			named++
		}
	}
	fmt.Fprintf(w, "build      %d of %d components name a revision an evidence run can quote\n", named, len(selves))
	for _, s := range selves {
		found := s.Version
		if found == "" {
			found = "?"
		}
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("  %s %-13s %-11s %-34s %s",
			selfMark(s), s.Component, found, truncate(s.Path, 34), s.Detail), " "))
	}

	probes := captaincode.ProbeToolchain(o.lookPath, o.runVersion)
	blocked := 0
	for _, s := range probes {
		if s.Blocked() {
			blocked++
		}
	}
	fmt.Fprintf(w, "toolchain  %d of %d adapters within their pinned contract\n", len(probes)-blocked, len(probes))
	for _, s := range probes {
		found := s.Version
		if found == "" {
			found = "?"
		}
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("  %s %-13s %-11s tested %-10s  %s",
			toolMark(s), s.Pin.Bin, found, s.Pin.Tested, s.Detail), " "))
	}

	probed := map[string]captaincode.ToolStatus{}
	for _, s := range probes {
		probed[s.Pin.Bin] = s
	}

	// Capability probes (ROADMAP M2.2). The registry declares what each
	// transport supports; this exercises the binary's --help to catch a CLI
	// that dropped a flag between releases. A missing flag does not block a
	// leg (the version contract above already gates that), but it is reported
	// so a decision record's capability claim is not trusted blindly.
	if o.runHelp == nil {
		o.runHelp = runToolVersion // --help output via the same combined-output path
	}
	capResults := captaincode.ProbeCapabilities(o.lookPath, o.runHelp)
	capMissing := 0
	for _, cr := range capResults {
		if cr.State == captaincode.CapProbeMissing {
			capMissing++
		}
	}
	capVerified := 0
	for _, cr := range capResults {
		if cr.State == captaincode.CapProbeVerified {
			capVerified++
		}
	}
	fmt.Fprintf(w, "caps       %d verified, %d missing, %d skipped of %d probed\n",
		capVerified, capMissing, len(capResults)-capVerified-capMissing, len(capResults))
	if capMissing > 0 {
		for _, cr := range capResults {
			if cr.State != captaincode.CapProbeMissing {
				continue
			}
			fmt.Fprintf(w, "  %s %-11s %-13s %s\n",
				captaincode.CapProbeMark(cr.State), cr.Transport, cr.Cap, cr.Detail)
		}
	}

	// Legs.
	allow := allowedLegs()
	specs := captaincode.Registry()
	var lines []string
	ready, decision := 0, 0 // legs that can take work, and decision legs that cannot
	envPath := o.captainEnv
	if envPath == "" {
		envPath = captaincode.CaptainEnvPath()
	}
	for _, s := range specs {
		if s.Disabled {
			continue
		}
		mark, note := "✓", ""
		bin, install := legTool(s.Transport)
		switch {
		case s.Transport == captaincode.TransportSystemOne:
			// A decision leg drives no binary and is outside CAPTAIN_LEGS (a
			// worker allowlist): its readiness is its key.
			if key, source := captaincode.SystemOneKey(); key == "" {
				mark, note = "✗", fmt.Sprintf("%s not set - add it to %s, or the console's API_KEY=… line to ~/.config/captain/%s (keys: console.typesafe.ai/settings/keys)", captaincode.SystemOneKeyEnv, envPath, captaincode.SystemOneKeyFile)
			} else {
				decision++
				note = fmt.Sprintf("decision leg (key from %s): triage classify and `captain jev` - never a worker", source)
			}
		case allow != nil && !allow[string(s.ID)]:
			mark, note = "·", fmt.Sprintf("not in CAPTAIN_LEGS (runs only when forced: /%s)", s.ID)
		default:
			if st, ok := probed[bin]; ok && st.State == captaincode.ToolMissing {
				mark, note = "✗", fmt.Sprintf("%s not found - %s", bin, st.Detail)
			} else if st, ok := probed[bin]; ok && st.Blocked() {
				mark, note = "✗", fmt.Sprintf("%s %s %s - %s", bin, st.Version, st.State, st.Detail)
			} else if _, err := o.lookPath(bin); err != nil {
				mark, note = "✗", fmt.Sprintf("%s not found - %s", bin, install)
			} else if s.Transport == captaincode.TransportOpencode && !providerConfigured(cfg, authed, s.Provider) {
				mark, note = "✗", fmt.Sprintf("provider %q has no credential - `opencode auth login` or add it to %s",
					s.Provider, o.opencodeConfig)
			} else if s.Provider == "huggingface" && os.Getenv("HF_TOKEN") == "" && !authed["huggingface"] {
				mark, note = "✗", fmt.Sprintf("HF_TOKEN not set - add it to %s (token: https://huggingface.co/settings/tokens)", envPath)
			} else {
				ready++
			}
		}
		model := s.Model
		if s.Provider != "" {
			model = s.Provider + "/" + s.Model
		}
		lines = append(lines, fmt.Sprintf("  %s %-10s %-11s %-34s prior %.1f  %s",
			mark, s.ID, s.Transport, truncate(model, 34), s.Prior, note))
	}

	legsLine := fmt.Sprintf("legs       %d ready of %d", ready, len(lines))
	if decision > 0 {
		legsLine += fmt.Sprintf(" (+%d decision leg, which answers questions and takes no work)", decision)
	}
	fmt.Fprintln(w, legsLine)
	for _, l := range lines {
		fmt.Fprintln(w, strings.TrimRight(l, " "))
	}
	if ready == 0 {
		fmt.Fprintln(w, "\nno leg is ready - install one agent CLI above, then re-run `captain doctor`.")
	}

	// Director from env (or compile default) may be absent; brain now derives a
	// runnable one at start (firstRunnableDirector), but the env should name one
	// you have so doctor and /captain are consistent without a surprise.
	envDir := strings.TrimSpace(os.Getenv("CAPTAIN_DIRECTOR"))
	if envDir == "" {
		envDir = string(captaincode.Director)
	}
	dirReady := false
	for _, s := range specs {
		if string(s.ID) == envDir && !s.Disabled {
			bin, _ := legTool(s.Transport)
			if st, ok := probed[bin]; ok && st.Blocked() {
				break
			}
			if _, err := o.lookPath(bin); err == nil {
				if s.Transport != captaincode.TransportOpencode || providerConfigured(cfg, authed, s.Provider) {
					dirReady = true
				}
			}
			break
		}
	}
	if !dirReady && envDir != "" {
		fmt.Fprintf(w, "\n⚠ CAPTAIN_DIRECTOR=%s (from env) is not ready (see legs ✗). The running brain will pick the first runnable instead (see startup log), but to stop the trap edit the env and restart the brain.\n  Suggested: CAPTAIN_DIRECTOR set to one of the ✓ legs above.\n", envDir)
	}
	return ready
}

// presence renders "<path> ✓" or "<path> ✗ (<fix>)".
func presence(path string, ok bool, fix string) string {
	if ok {
		return path + " ✓"
	}
	return fmt.Sprintf("%s ✗ (%s)", path, fix)
}

// providerConfigured reports whether a leg's opencode provider can actually
// authenticate: named in the config file, present in opencode's auth store
// (`opencode auth login` writes there, and xai/openai OAuth legs appear ONLY
// there), or built into opencode itself. Config parsing is deliberately loose -
// the file is hand-edited JSONC and a doctor that dies on a stray comma helps
// nobody.
func providerConfigured(cfg []byte, authed map[string]bool, provider string) bool {
	switch {
	case provider == "": // CLI transports carry no opencode provider
		return true
	case provider == "opencode": // opencode's own hosted roster needs no credential block
		return true
	case authed[provider]:
		return true
	}
	return strings.Contains(string(cfg), `"`+provider+`"`)
}

// opencodeAuthPath is where opencode stores provider credentials.
func opencodeAuthPath() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode", "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

// authedProviders lists the provider ids in opencode's auth store. Only the
// KEYS are read - the credentials themselves are never loaded or logged.
func authedProviders(path string) map[string]bool {
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

func cmdDoctor(args []string) {
	for _, a := range args {
		if a == "--rehearse" || a == "rehearse" {
			cmdRehearse()
			return
		}
	}
	if runDoctor(os.Stdout, doctorOpts{}) == 0 {
		os.Exit(1)
	}
}

func cmdRehearse() {
	src := captainSourceDir()
	deps := captaincode.RehearsalDeps{
		ReadFile: func(p string) ([]byte, error) {
			return os.ReadFile(filepath.Join(src, p))
		},
		InitReport: func() (string, error) {
			report, _, err := captaincode.RunInit(captaincode.InitOptions{Apply: false})
			return report, err
		},
	}
	r := captaincode.RunRehearsal(deps)
	fmt.Print(captaincode.FormatRehearsal(r))
	if !r.ExitOK {
		os.Exit(1)
	}
}
