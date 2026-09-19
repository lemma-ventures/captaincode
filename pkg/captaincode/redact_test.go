package captaincode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redactTestSetup(t *testing.T) {
	t.Helper()
	vault.mu.Lock()
	vault.byHash, vault.loaded, vault.noStore = map[string]vaultEntry{}, true, true
	vault.mu.Unlock()
	SetIdentityRulesForTest(map[string]string{"/home/jdoe": "/home/captain", "jdoe": "captain-user", "romain@example.com": "captain@example.invalid"})
	t.Setenv("CAPTAIN_REDACT", "on")
}

func TestRedactMasksVendorKeysAndKeepsStructure(t *testing.T) {
	redactTestSetup(t)
	in := "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789\nNVIDIA_API_KEY=nvapi-AbCdEfGhIjKlMnOpQrStUvWxYz012345\nPORT=8080\nMAX_TOKENS=4096\n"
	out, rep := Redact(in)
	assert.Equal(t, 2, rep.Secrets)
	assert.Contains(t, out, "OPENAI_API_KEY=[[secret:openai:")
	assert.Contains(t, out, "NVIDIA_API_KEY=[[secret:nvidia:")
	assert.Contains(t, out, "PORT=8080", "settings are not secrets")
	assert.Contains(t, out, "MAX_TOKENS=4096")
	assert.NotContains(t, out, "sk-proj-")
	// Stable: the same value → the same placeholder.
	again, _ := Redact(in)
	assert.Equal(t, out, again)
}

func TestRedactGenericAssignmentsAndStructuralForms(t *testing.T) {
	redactTestSetup(t)
	in := strings.Join([]string{
		`db_password: Tr0ub4dor&3xyz`,
		`"client_secret": "zz9AbCdEfGh1234567"`,
		`DATABASE_URL=postgres://app:s3cretpw@db.internal:5432/app`,
		`Authorization: Bearer abcdef0123456789abcdef0123456789`,
		`token = eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
		`-----BEGIN RSA PRIVATE KEY-----`, `MIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy0AH`, `-----END RSA PRIVATE KEY-----`,
		`API_KEY=${OPENAI_API_KEY}`, `SECRET_KEY=<your-secret>`, `PASSWORD=changeme`, `token_limit=unlimited`,
	}, "\n")
	out, rep := Redact(in)
	assert.Contains(t, out, "db_password: [[secret:assignment:")
	assert.Contains(t, out, `"client_secret": "[[secret:assignment:`)
	assert.Contains(t, out, "postgres://app:[[secret:url-credential:")
	assert.Contains(t, out, "Bearer [[secret:bearer:")
	assert.Contains(t, out, "[[secret:jwt:")
	assert.Contains(t, out, "[[secret:pem:")
	assert.NotContains(t, out, "BEGIN RSA")
	assert.Contains(t, out, "API_KEY=${OPENAI_API_KEY}", "a reference is structure, not a secret")
	assert.Contains(t, out, "SECRET_KEY=<your-secret>")
	assert.Contains(t, out, "PASSWORD=changeme")
	assert.Contains(t, out, "token_limit=unlimited")
	assert.Equal(t, 6, rep.Secrets, out)
}

func TestRedactIdentityAndRestore(t *testing.T) {
	redactTestSetup(t)
	in := "read /home/jdoe/Gits/arc/main.go; author romain@example.com; key sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	out, rep := Redact(in)
	assert.Contains(t, out, "/home/captain/Gits/arc/main.go")
	assert.Contains(t, out, "captain@example.invalid")
	assert.NotContains(t, out, "jdoe")
	assert.Equal(t, 1, rep.Secrets)
	assert.GreaterOrEqual(t, rep.Identity, 2)

	// The answer side: identity comes back, the secret stays masked.
	back, _ := RestoreIdentity(out)
	assert.Contains(t, back, "/home/jdoe/Gits/arc/main.go")
	assert.Contains(t, back, "[[secret:anthropic:")
	// The tool side: everything comes back.
	full, n := Restore(out)
	assert.Equal(t, in, full)
	assert.GreaterOrEqual(t, n, 3)
}

func TestRedactModes(t *testing.T) {
	redactTestSetup(t)
	in := "/home/jdoe/x sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	t.Setenv("CAPTAIN_REDACT", "secrets")
	out, _ := Redact(in)
	assert.Contains(t, out, "/home/jdoe/x", "secrets mode leaves paths real")
	assert.Contains(t, out, "[[secret:")
	t.Setenv("CAPTAIN_REDACT", "off")
	out, rep := Redact(in)
	assert.Equal(t, in, out)
	assert.Equal(t, 0, rep.Total())
}

func TestSecretFiles(t *testing.T) {
	redactTestSetup(t)
	for _, p := range []string{"/x/id_rsa", "/x/server.pem", "/home/jdoe/.aws/credentials", "/x/.netrc", "/x/service-account-prod.json"} {
		deny, _ := IsSecretFile(p)
		assert.True(t, deny, p)
	}
	deny, _ := IsSecretFile("/x/.env")
	assert.False(t, deny, ".env is read masked, not refused, by default")
	require.False(t, func() bool { d, _ := IsSecretFile("/x/.env.example"); return d }())
	t.Setenv("CAPTAIN_REDACT", "strict")
	deny, _ = IsSecretFile("/x/.env")
	assert.True(t, deny, "strict refuses .env")
	deny, _ = IsSecretFile("/x/.env.example")
	assert.False(t, deny)
}

// A redacted body must still be JSON. Inside a JSON string the text carries
// escapes (\" \n \\); a secret value that swallowed the backslash before a
// quote left that quote unescaped, and the proxy sent Anthropic a body that
// was not JSON ("unexpected character: line 1 column 175532", 2026-09-19).
func TestRedactKeepsAJSONBodyValid(t *testing.T) {
	bodies := []string{
		`{"content":"set API_KEY=sk-live-abcdefghijklmnop\nthen run"}`,
		`{"content":"password=hunter2hunter2\" and then \"more\""}`,
		`{"content":"secret: abcdefghijklmnop\\path\\to"}`,
		`{"content":"see https://user:p4ssw0rdp4ss\\n@host/x"}`,
		`{"content":"token=\"abcdefghijklmnopq\"\nnext"}`,
	}
	for _, b := range bodies {
		var v any
		require.NoError(t, json.Unmarshal([]byte(b), &v), "fixture must be JSON: %s", b)
		out, _ := Redact(b)
		assert.NoError(t, json.Unmarshal([]byte(out), &v), "redacted body is not JSON: %s", out)
	}
	out, rep := Redact(`{"content":"password=hunter2hunter2\" and then \"more\""}`)
	assert.Equal(t, 1, rep.Secrets)
	assert.Contains(t, out, `[[secret:assignment:`)
	assert.Contains(t, out, `\" and then \"more\"`, "the escape after the value is intact")
}
