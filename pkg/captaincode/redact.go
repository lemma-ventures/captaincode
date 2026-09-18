package captaincode

// Secrets on the wire. Nothing that looks like a credential leaves this
// machine inside a prompt, and the person behind the keyboard is not named
// to the provider either. Three places apply the same engine:
//
//   - the tool boundary (opencode plugin hook, `captain redact` from a Claude
//     Code hook): a worker that cats .env sees the file with its VALUES
//     replaced by placeholders - the names, the structure, the fact that a
//     key exists all survive, so "fix the env loading" still works;
//   - the egress proxy (captain proxy): every request body to a provider is
//     scanned again, so a secret that reached the context by any other path
//     (a system prompt, memory, the brief) is caught before the socket;
//   - the way back: a placeholder the model writes into a file or a command
//     is restored to the real value at the tool boundary, never in the
//     provider's response - the model can move a secret without seeing it.
//
// Placeholders are STABLE: the same value always becomes the same
// [[secret:kind:hash]], so the model can reason about "the openai key" across
// turns and compare two occurrences. The vault (hash → value) lives in
// ~/.captaincode/vault.jsonl, mode 0600, shared by the brain, the CLI and
// the plugin.
//
// Identity: the operator's home directory is rewritten to a fixed fake home
// (/Users/captain, /home/captain) - an absolute path the model can use like
// any other - and restored on tool arguments and in streamed answers.
// Usernames, full names and e-mails from git config are placeholders too.
//
// What redaction costs a worker: nothing on structure (a file keeps its shape,
// a path stays absolute), the value of the secret itself (which a worker has
// no business reading), and one exception worth naming - a worker cannot
// tell two DIFFERENT keys of the same kind apart except by hash suffix, which
// is enough for "these two files use different keys".

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// RedactMode is CAPTAIN_REDACT: "off" (no redaction), "on" (default: secrets
// and identity), "secrets" (secrets only - paths stay real), "strict" (on,
// plus secret FILES are refused at the tool boundary, not just masked).
func RedactMode() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("CAPTAIN_REDACT"))); v {
	case "off", "0", "false", "no":
		return "off"
	case "secrets", "strict":
		return v
	default:
		return "on"
	}
}

// RedactIdentity reports whether identity (home dir, names, e-mail) is
// rewritten - "on" and "strict", not "secrets".
func RedactIdentity() bool { m := RedactMode(); return m == "on" || m == "strict" }

// ── patterns ─────────────────────────────────────────────────────────────────

type secretPattern struct {
	kind string
	re   *regexp.Regexp
	// group is the capture group holding the secret value (0 = whole match);
	// the rest of the match is kept, so `OPENAI_API_KEY=` stays readable.
	group int
}

// secretPatterns: vendor prefixes first (exact kinds, no false positives),
// then structural forms (PEM, JWT, URLs with credentials), then the generic
// KEY=value assignment, which only fires on a value that looks like a secret
// (long, no spaces, not a placeholder).
var secretPatterns = []secretPattern{
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`), 0},
	{"openrouter", regexp.MustCompile(`\bsk-or-v1-[A-Za-z0-9]{20,}`), 0},
	{"openai", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{20,}`), 0},
	{"nvidia", regexp.MustCompile(`\bnvapi-[A-Za-z0-9_\-]{20,}`), 0},
	{"xai", regexp.MustCompile(`\bxai-[A-Za-z0-9]{20,}`), 0},
	{"huggingface", regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`), 0},
	{"github", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`), 0},
	{"github", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), 0},
	{"aws", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"google", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`), 0},
	{"slack", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}`), 0},
	{"stripe", regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{24,}`), 0},
	{"pem", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), 0},
	{"pem", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^\n]*(?:\n[A-Za-z0-9+/=]{16,}[^\n]*)*`), 0}, // a block cut before its END line
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`), 0},
	{"bearer", regexp.MustCompile(`(?i)(authorization\s*[:=]\s*["']?(?:bearer|basic|token)\s+)([A-Za-z0-9._~+/=\-]{16,})`), 2},
	{"url-credential", regexp.MustCompile(`(://[^\s/:@]+:)([^\s/@]{4,})(@)`), 2},
	{"assignment", regexp.MustCompile(`(?i)((?:^|[\s"'{,(])[A-Za-z0-9_\-.]*(?:api[_\-]?key|apikey|secret|token|passw(?:or)?d|credential|private[_\-]?key|access[_\-]?key|client[_\-]?secret)[A-Za-z0-9_\-.]*["']?\s*[=:]\s*["']?)([^\s"',;]{8,})`), 2},
}

// placeholderRe matches what Redact writes, for Restore and for the proxy's
// streaming holdback.
var placeholderRe = regexp.MustCompile(`\[\[secret:[a-z\-]+:[0-9a-f]{6}\]\]`)

// notASecret rejects assignment values that are obviously placeholders or
// references - redacting `${OPENAI_API_KEY}` or `<your-key>` would hide the
// structure the worker needs.
func notASecret(v string) bool {
	l := strings.ToLower(v)
	switch {
	case strings.HasPrefix(v, "${"), strings.HasPrefix(v, "$"), strings.HasPrefix(v, "{env:"), strings.HasPrefix(v, "{{"):
		return true
	case strings.HasPrefix(v, "<") || strings.HasPrefix(v, "[["):
		return true
	case strings.HasPrefix(l, "your"), strings.HasPrefix(l, "changeme"), strings.HasPrefix(l, "example"), strings.HasPrefix(l, "placeholder"):
		return true
	case strings.Trim(l, "x*.-_0") == "":
		return true
	case strings.HasPrefix(l, "true"), strings.HasPrefix(l, "false"), strings.HasPrefix(l, "null"), strings.HasPrefix(l, "none"):
		return true
	case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "/"):
		return true // URLs and paths are values, not secrets (a URL with credentials is caught separately)
	}
	// A secret has letters AND digits (or is long): max_tokens=4096 and
	// token_limit=unlimited are settings, not credentials.
	var letters, digits bool
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			letters = true
		}
	}
	return !(letters && digits) && len(v) < 20
}

// ── vault ────────────────────────────────────────────────────────────────────

type vaultEntry struct {
	Hash  string `json:"h"`
	Kind  string `json:"k"`
	Value string `json:"v"`
}

var vault struct {
	mu      sync.Mutex
	byHash  map[string]vaultEntry
	loaded  bool
	path    string
	noStore bool // tests: keep the vault in memory
}

// VaultPath is the placeholder → value store, 0600.
func VaultPath() string {
	if vault.path != "" {
		return vault.path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "vault.jsonl")
}

func vaultLoadLocked() {
	if vault.byHash == nil {
		vault.byHash = map[string]vaultEntry{}
	}
	if vault.noStore {
		vault.loaded = true
		return
	}
	f, err := os.Open(VaultPath())
	if err != nil {
		vault.loaded = true
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e vaultEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Hash != "" {
			vault.byHash[e.Hash] = e
		}
	}
	vault.loaded = true
}

func vaultPut(kind, value string) string {
	sum := sha256.Sum256([]byte(value))
	h := hex.EncodeToString(sum[:3])
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if !vault.loaded {
		vaultLoadLocked()
	}
	if _, ok := vault.byHash[h]; ok {
		return h
	}
	e := vaultEntry{Hash: h, Kind: kind, Value: value}
	vault.byHash[h] = e
	if vault.noStore {
		return h
	}
	p := VaultPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return h
	}
	defer f.Close()
	line, _ := json.Marshal(e)
	_, _ = f.Write(append(line, '\n'))
	return h
}

func vaultGet(h string) (vaultEntry, bool) {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if !vault.loaded {
		vaultLoadLocked()
	}
	e, ok := vault.byHash[h]
	if !ok && !vault.noStore {
		// Another process (the plugin's CLI call) may have appended since.
		vaultLoadLocked()
		e, ok = vault.byHash[h]
	}
	return e, ok
}

// ── identity ─────────────────────────────────────────────────────────────────

// Identity is what names the operator: rewritten to fixed stand-ins so a
// path stays a path and a name stays a name, just not theirs.
type identityRule struct {
	real, fake string
}

var identity struct {
	once  sync.Once
	rules []identityRule
}

// FakeHome is the stand-in home directory the model sees.
func FakeHome() string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(home, "/Users/") {
		return "/Users/captain"
	}
	return "/home/captain"
}

func identityRules() []identityRule {
	identity.once.Do(func() {
		var rules []identityRule
		home, _ := os.UserHomeDir()
		if home != "" && home != "/" {
			rules = append(rules, identityRule{home, FakeHome()})
		}
		// Stand-ins are DISTINCTIVE on purpose: Restore rewrites them in tool
		// arguments, so a stand-in that is an ordinary word ("captain") would
		// corrupt any command that happens to contain it (go build
		// ./cmd/captaincode → ./cmd/<username>code). Short usernames are not
		// rewritten at all for the mirror reason.
		if u, err := user.Current(); err == nil && len(u.Username) >= 4 {
			rules = append(rules, identityRule{u.Username, "captain-user"})
		}
		for _, key := range []string{"user.email", "user.name"} {
			out, err := exec.Command("git", "config", "--global", key).Output()
			if err != nil {
				continue
			}
			v := strings.TrimSpace(string(out))
			if v == "" {
				continue
			}
			if key == "user.email" {
				rules = append(rules, identityRule{v, "captain@example.invalid"})
			} else {
				rules = append(rules, identityRule{v, "Captain Operator"})
			}
		}
		if extra := os.Getenv("CAPTAIN_REDACT_IDENTITY"); extra != "" {
			for i, v := range strings.Split(extra, ",") {
				if v = strings.TrimSpace(v); v != "" {
					rules = append(rules, identityRule{v, fmt.Sprintf("identity-%d", i+1)})
				}
			}
		}
		identity.rules = sortRules(rules)
	})
	return identity.rules
}

// sortRules orders longest real first: "/Users/x" is rewritten before "x",
// so the home path becomes "/Users/captain" and not "/Users/captain" via a
// bare-username hit inside it. The reverse (Restore) sorts by fake length.
func sortRules(rules []identityRule) []identityRule {
	sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].real) > len(rules[j].real) })
	return rules
}

func rulesByFake() []identityRule {
	out := append([]identityRule{}, identityRules()...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].fake) > len(out[j].fake) })
	return out
}

// SetIdentityRulesForTest pins the identity table (tests).
func SetIdentityRulesForTest(pairs map[string]string) {
	identity.once.Do(func() {})
	identity.rules = nil
	for real, fake := range pairs {
		identity.rules = append(identity.rules, identityRule{real, fake})
	}
	identity.rules = sortRules(identity.rules)
}

// ── engine ───────────────────────────────────────────────────────────────────

// Redaction is what one pass found.
type Redaction struct {
	Secrets  int            `json:"secrets"`
	Identity int            `json:"identity"`
	Kinds    map[string]int `json:"kinds,omitempty"`
}

func (r Redaction) Total() int { return r.Secrets + r.Identity }

// Redact masks secrets (and identity, when the mode says so) in text.
func Redact(text string) (string, Redaction) {
	return RedactWith(text, RedactMode())
}

// RedactWith is Redact under an explicit mode.
func RedactWith(text, mode string) (string, Redaction) {
	var rep Redaction
	if mode == "off" || text == "" {
		return text, rep
	}
	out := maskSecrets(text, &rep, func(kind, value string) string {
		return "[[secret:" + kind + ":" + vaultPut(kind, value) + "]]"
	})
	if mode == "on" || mode == "strict" {
		for _, r := range identityRules() {
			if n := strings.Count(out, r.real); n > 0 {
				out = strings.ReplaceAll(out, r.real, r.fake)
				rep.Identity += n
			}
		}
	}
	return out, rep
}

// maskSecrets runs the patterns over text, replacing each secret with what
// mask returns for it. The vault-backed placeholder is one mask; the
// write-time scrubbers (Euclid journal, shared context) use a flat one.
func maskSecrets(text string, rep *Redaction, mask func(kind, value string) string) string {
	out := text
	for _, p := range secretPatterns {
		out = p.re.ReplaceAllStringFunc(out, func(m string) string {
			if p.group == 0 {
				rep.Secrets++
				rep.count(p.kind)
				return mask(p.kind, m)
			}
			sub := p.re.FindStringSubmatchIndex(m)
			if sub == nil || len(sub) <= 2*p.group+1 {
				return m
			}
			s, e := sub[2*p.group], sub[2*p.group+1]
			val := m[s:e]
			if p.kind == "assignment" && notASecret(val) {
				return m
			}
			if placeholderRe.MatchString(val) || strings.HasPrefix(val, "<redacted") || strings.HasPrefix(val, "[redacted") {
				return m // already masked
			}
			rep.Secrets++
			rep.count(p.kind)
			return m[:s] + mask(p.kind, val) + m[e:]
		})
	}
	return out
}

// MaskSecrets is the non-reversible form: every secret becomes replacement,
// nothing is vaulted. For text that leaves the machine as a document (a
// pushed brain, a published digest) rather than a prompt.
func MaskSecrets(text, replacement string) (string, int) {
	var rep Redaction
	out := maskSecrets(text, &rep, func(string, string) string { return replacement })
	return out, rep.Secrets
}

func (r *Redaction) count(kind string) {
	if r.Kinds == nil {
		r.Kinds = map[string]int{}
	}
	r.Kinds[kind]++
}

// Restore puts real values back: placeholders from the vault, identity
// stand-ins from the table. Used on tool ARGUMENTS (the model wrote a file or
// a command with a placeholder in it) and, identity only, on answers.
func Restore(text string) (string, int) {
	out, n := RestoreIdentity(text)
	out = placeholderRe.ReplaceAllStringFunc(out, func(m string) string {
		parts := strings.Split(strings.Trim(m, "[]"), ":")
		if len(parts) != 3 {
			return m
		}
		if e, ok := vaultGet(parts[2]); ok {
			n++
			return e.Value
		}
		return m
	})
	return out, n
}

// RestoreIdentity undoes the identity rewrite only - what a provider's
// answer gets, so paths in it are real again and secrets stay masked.
func RestoreIdentity(text string) (string, int) {
	if !RedactIdentity() {
		return text, 0
	}
	n := 0
	out := text
	for _, r := range rulesByFake() {
		if c := strings.Count(out, r.fake); c > 0 {
			out = strings.ReplaceAll(out, r.fake, r.real)
			n += c
		}
	}
	return out, n
}

// IdentityStandIns lists the fake strings a response may contain, longest
// first - the proxy holds back a trailing prefix of any of them across
// stream chunks so a split "/Users/cap|tain" is still restored.
func IdentityStandIns() []string {
	var out []string
	for _, r := range identityRules() {
		out = append(out, r.fake)
	}
	return out
}

// ── secret files ─────────────────────────────────────────────────────────────

// secretFileGlobs are files a worker has no business reading in full: the
// private half of a keypair, a credentials store. Under "strict", .env files
// join them; otherwise .env is READ but masked (its names matter).
var secretFileGlobs = []string{
	"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*",
	".netrc", ".npmrc", ".pypirc", "credentials", "credentials.json", "service-account*.json",
	".git-credentials", "*.kdbx", "vault.jsonl", ".credentials.json",
}

// IsSecretFile reports whether a path names a file the worker must not read
// under the active mode, and why.
func IsSecretFile(path string) (bool, string) {
	mode := RedactMode()
	if mode == "off" {
		return false, ""
	}
	base := filepath.Base(path)
	for _, g := range secretFileGlobs {
		if ok, _ := filepath.Match(g, base); ok {
			return true, "private key or credential store"
		}
	}
	if strings.Contains(path, "/.ssh/") || strings.Contains(path, "/.aws/") || strings.Contains(path, "/.gnupg/") {
		return true, "credential directory"
	}
	if mode == "strict" && (base == ".env" || strings.HasPrefix(base, ".env.")) && !strings.HasSuffix(base, ".example") && !strings.HasSuffix(base, ".sample") {
		return true, "env file (CAPTAIN_REDACT=strict)"
	}
	return false, ""
}
