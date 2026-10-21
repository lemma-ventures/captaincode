package presence

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"time"
)

const MaxBody = 1 << 20

type Error struct{ Code string }

func (e *Error) Error() string { return "presence: " + e.Code }
func fail(code string) error   { return &Error{code} }
func Code(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return "storage_error"
}

type Envelope struct {
	V          int    `json:"v"`
	Machine    string `json:"machine"`
	Folder     string `json:"folder"`
	TextSHA256 string `json:"text_sha256"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Nonce      string `json:"nonce"`
}

type Assertion struct {
	CredentialID      string `json:"credential_id"`
	AuthenticatorData string `json:"authenticatorData"`
	ClientDataJSON    string `json:"clientDataJSON"`
	Signature         string `json:"signature"`
}

type Signed struct {
	Envelope Envelope `json:"envelope"`
	Text     string   `json:"text"`
	Assertion
	HeldID string `json:"held_id,omitempty"`
}

type Credential struct {
	ID        string `json:"id"`
	Key       string `json:"public_key"`
	Algorithm string `json:"algorithm"`
	RPID      string `json:"rp_id"`
	Origin    string `json:"origin"`
	SignCount uint32 `json:"sign_count"`
	Label     string `json:"device_label"`
	Created   string `json:"created_at"`
}

type User struct {
	Name        string       `json:"name"`
	Credentials []Credential `json:"credentials"`
}
type Ticket struct {
	User    string `json:"user"`
	Expires int64  `json:"expires_at"`
}
type Config struct {
	Version   int                 `json:"v"`
	Machine   string              `json:"machine"`
	Origins   []string            `json:"allowed_origins"`
	Folders   map[string]string   `json:"folders"`
	Window    int64               `json:"window_seconds"`
	Users     []User              `json:"users"`
	Used      map[string]int64    `json:"used_nonces,omitempty"`
	Screened  map[string]int64    `json:"screened,omitempty"`
	Tickets   map[string]Ticket   `json:"enrollment_tickets,omitempty"`
	Grants    map[string]Grant    `json:"grants,omitempty"`
	Decisions map[string]Decision `json:"decisions,omitempty"`
	Audit     []Event             `json:"audit,omitempty"`
}

type Grant struct {
	ID           string `json:"id"`
	User         string `json:"user"`
	CredentialID string `json:"credential_id"`
	Label        string `json:"device_label"`
	Device       string `json:"device"`
	Machine      string `json:"machine"`
	Folder       string `json:"folder"`
	Dir          string `json:"dir"`
	TextSHA256   string `json:"text_sha256"`
	Expires      int64  `json:"expires_at"`
}
type Event struct {
	Grant
	At   int64  `json:"at"`
	Kind string `json:"kind"`
}
type Decision struct {
	ID       string `json:"id"`
	GrantID  string `json:"grant_id"`
	Action   string `json:"action"`
	Expires  int64  `json:"expires_at"`
	Approved bool   `json:"approved"`
}

var labelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var machineRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidLabel(s string) bool  { return labelRE.MatchString(s) }
func Digest(data []byte) []byte { h := sha256.Sum256(data); return h[:] }
func HashText(s string) string  { return hex.EncodeToString(Digest([]byte(s))) }
func Encode(b []byte) string    { return base64.RawURLEncoding.EncodeToString(b) }
func Decode(s string) ([]byte, error) {
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	if e != nil || Encode(b) != s {
		return nil, fail("invalid_encoding")
	}
	return b, nil
}
func RandomID() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return Encode(b), nil
}
func (e Envelope) Hash() ([]byte, error) {
	if e.V != 1 || !machineRE.MatchString(e.Machine) || !ValidLabel(e.Folder) || !digestRE.MatchString(e.TextSHA256) {
		return nil, fail("invalid_envelope")
	}
	n, err := Decode(e.Nonce)
	if err != nil || len(n) < 16 || len(n) > 64 {
		return nil, fail("invalid_nonce")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return Digest(b), nil
}
func (c *Config) checkEnvelope(e Envelope, text string, now int64) ([]byte, error) {
	h, err := e.Hash()
	if err != nil {
		return nil, err
	}
	if e.Machine != c.Machine {
		return nil, fail("wrong_machine")
	}
	if c.Folders[e.Folder] == "" {
		return nil, fail("unknown_folder")
	}
	if text == "" || len(text) > MaxBody/2 || HashText(text) != e.TextSHA256 {
		return nil, fail("text_mismatch")
	}
	if e.IssuedAt < 0 || e.IssuedAt > now {
		return nil, fail("future_envelope")
	}
	window := c.Window
	if window == 0 {
		window = 900
	}
	if e.ExpiresAt <= now || e.ExpiresAt <= e.IssuedAt {
		return nil, fail("expired_envelope")
	}
	if e.ExpiresAt-e.IssuedAt > window {
		return nil, fail("window_exceeded")
	}
	if _, ok := c.Used[e.Nonce]; ok {
		return nil, fail("replayed_nonce")
	}
	return h, nil
}
func ValidOrigin(origin, rp string) bool {
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Host == "" || u.String() != origin {
		return false
	}
	if net.ParseIP(rp) != nil || rp == "" || u.Hostname() != rp {
		return false
	}
	return (u.Scheme == "http" && rp == "localhost" && u.Port() != "") || (u.Scheme == "https" && rp != "localhost")
}
func Allowed(origins []string, origin string) bool { return slices.Contains(origins, origin) }
func LocalAuthority(source string, terminal bool) error {
	if source != "terminal" || !terminal {
		return fail("terminal_required")
	}
	return nil
}

func ReadJSON(r io.Reader, out any) error {
	b, e := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if e != nil || len(b) > MaxBody {
		return fail("invalid_json")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d, 0); e != nil {
		return fail("invalid_json")
	}
	if _, e = d.Token(); e != io.EOF {
		return fail("invalid_json")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(out); e != nil {
		return fail("invalid_json")
	}
	return nil
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 20 {
		return fmt.Errorf("depth")
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate")
			}
			seen[s] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("delimiter")
	}
	_, e = d.Token()
	return e
}
func nowUnix() int64 { return time.Now().Unix() }
