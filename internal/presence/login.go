package presence

import "encoding/json"

// LoginRepairCLIs are the worker CLIs whose documented login flow can run
// headless and print a verification URL with a user code. The repair route
// runs only these fixed commands; a request never carries an argv. Claude
// and Cursor log in through a browser or the Keychain with no code to hand
// back, so they stay terminal-only.
var LoginRepairCLIs = map[string][]string{
	"codex": {"codex", "login", "--device-auth"},
	"grok":  {"grok", "login", "--device-auth"},
}

// LoginRepairChallenge binds one passkey approval to a login repair on this
// machine for one CLI. The CLI id is inside the signed challenge, so an
// approval for one tool cannot start another.
func LoginRepairChallenge(machine, cli string) []byte {
	b, _ := json.Marshal(struct {
		Purpose string `json:"purpose"`
		Machine string `json:"machine"`
		CLI     string `json:"cli"`
	}{"captain.presence.login.v1", machine, cli})
	return Digest(b)
}

// LoginChallenge is the challenge a repair assertion for cli must sign.
func (s *Store) LoginChallenge(cli string) ([]byte, error) {
	if _, ok := LoginRepairCLIs[cli]; !ok {
		return nil, fail("unknown_cli")
	}
	c, e := s.Read()
	if e != nil {
		return nil, e
	}
	return LoginRepairChallenge(c.Machine, cli), nil
}

// VerifyLoginRepair checks a repair assertion for cli against any enrolled
// owner and records the attempt in the audit trail. The verification URL
// and user code the CLI prints never reach this path; the audit event
// names the tool and the signer only.
func (s *Store) VerifyLoginRepair(cli string, a Assertion) error {
	if _, ok := LoginRepairCLIs[cli]; !ok {
		return fail("unknown_cli")
	}
	return s.Update(func(c *Config) error {
		if len(c.Users) == 0 {
			return fail("unknown_credential")
		}
		cred, user, e := c.verify(a, LoginRepairChallenge(c.Machine, cli), "")
		if e != nil {
			return e
		}
		c.Audit = append(c.Audit, Event{Grant: Grant{User: user, CredentialID: cred.ID, Machine: c.Machine}, At: nowUnix(), Kind: "login:" + cli})
		return nil
	})
}
