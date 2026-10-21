package presence

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"time"
)

type Registration struct {
	Machine           string     `json:"machine"`
	User              string     `json:"user"`
	Origin            string     `json:"origin"`
	RPID              string     `json:"rp_id"`
	Label             string     `json:"device_label"`
	Nonce             string     `json:"nonce"`
	Expires           int64      `json:"expires_at"`
	Code              string     `json:"code,omitempty"`
	CredentialID      string     `json:"credential_id"`
	ClientDataJSON    string     `json:"clientDataJSON"`
	AttestationObject string     `json:"attestationObject"`
	Approval          *Assertion `json:"approval,omitempty"`
}

func (r Registration) Challenge() []byte {
	b, _ := json.Marshal(struct {
		Purpose string `json:"purpose"`
		Machine string `json:"machine"`
		User    string `json:"user"`
		Origin  string `json:"origin"`
		RPID    string `json:"rp_id"`
		Label   string `json:"device_label"`
		Nonce   string `json:"nonce"`
		Expires int64  `json:"expires_at"`
		Code    string `json:"code"`
	}{"captain.presence.create.v1", r.Machine, r.User, r.Origin, r.RPID, r.Label, r.Nonce, r.Expires, r.Code})
	return Digest(b)
}
func (r Registration) ApprovalChallenge() []byte {
	r.Approval = nil
	b, _ := json.Marshal(r)
	return Digest(append([]byte("captain.presence.enroll.v1\n"), b...))
}
func (s *Store) Register(r Registration) error {
	return s.Update(func(c *Config) error {
		now := nowUnix()
		if r.Machine != c.Machine {
			return fail("wrong_machine")
		}
		if !ValidLabel(r.User) {
			return fail("invalid_user")
		}
		if r.Expires <= now || r.Expires-now > 300 {
			return fail("expired_registration")
		}
		nonce, e := Decode(r.Nonce)
		if e != nil || len(nonce) < 16 || len(nonce) > 64 {
			return fail("invalid_nonce")
		}
		if _, ok := c.Used[r.Nonce]; ok {
			return fail("replayed_nonce")
		}
		if !ValidOrigin(r.Origin, r.RPID) || !Allowed(c.Origins, r.Origin) {
			return fail("origin_not_allowed")
		}
		if r.Code != "" {
			ticket, ok := c.Tickets[HashText(r.Code)]
			if !ok || ticket.User != r.User || ticket.Expires <= now {
				return fail("invalid_enrollment_code")
			}
		} else if r.Approval == nil {
			return fail("enrollment_proof_required")
		}
		id, e := Decode(r.CredentialID)
		if e != nil || len(id) < 1 || len(id) > 1024 {
			return fail("invalid_credential")
		}
		for _, u := range c.Users {
			for _, k := range u.Credentials {
				if k.ID == r.CredentialID {
					return fail("duplicate_credential")
				}
			}
		}
		cd, e := Decode(r.ClientDataJSON)
		if e != nil {
			return e
		}
		if e = checkClient(cd, "webauthn.create", r.Challenge(), r.Origin, c.Origins); e != nil {
			return e
		}
		obj, e := Decode(r.AttestationObject)
		if e != nil {
			return e
		}
		v, n, e := decodeCBOR(obj)
		if e != nil || n != len(obj) {
			return fail("invalid_attestation")
		}
		m, ok := v.(cborMap)
		if !ok || len(m) != 3 || m["fmt"] != "none" {
			return fail("unsupported_attestation")
		}
		stmt, ok := m["attStmt"].(cborMap)
		if !ok || len(stmt) != 0 {
			return fail("invalid_attestation")
		}
		ad, ok := m["authData"].([]byte)
		if !ok {
			return fail("invalid_attestation")
		}
		count, e := checkAuth(ad, r.RPID, true)
		if e != nil {
			return e
		}
		if len(ad) < 55 || ad[32]&0x40 == 0 {
			return fail("invalid_attestation")
		}
		idlen := int(binary.BigEndian.Uint16(ad[53:55]))
		if idlen > len(ad)-55 || !bytes.Equal(id, ad[55:55+idlen]) {
			return fail("invalid_credential")
		}
		tail := ad[55+idlen:]
		keyValue, n, e := decodeCBOR(tail)
		if e != nil {
			return fail("invalid_key")
		}
		km, ok := keyValue.(cborMap)
		if !ok {
			return fail("invalid_key")
		}
		alg := ""
		switch km[int64(3)] {
		case int64(-7):
			alg = "ES256"
		case int64(-8):
			alg = "Ed25519"
		}
		if ad[32]&0x80 != 0 {
			v, end, e := decodeCBOR(tail[n:])
			if _, ok := v.(cborMap); e != nil || !ok || n+end != len(tail) {
				return fail("invalid_attestation")
			}
		} else if n != len(tail) {
			return fail("invalid_attestation")
		}
		k := Credential{ID: r.CredentialID, Key: Encode(tail[:n]), Algorithm: alg, RPID: r.RPID, Origin: r.Origin, SignCount: count, Label: r.Label, Created: time.Now().UTC().Format(time.RFC3339)}
		if e = validateCredential(k); e != nil {
			return e
		}
		if r.Approval != nil {
			if _, _, e = c.verify(*r.Approval, r.ApprovalChallenge(), r.User); e != nil {
				return e
			}
		}
		found := false
		for i := range c.Users {
			if c.Users[i].Name == r.User {
				c.Users[i].Credentials = append(c.Users[i].Credentials, k)
				found = true
				break
			}
		}
		if !found {
			c.Users = append(c.Users, User{Name: r.User, Credentials: []Credential{k}})
		}
		prune(c, now)
		if len(c.Used) >= 10000 {
			return fail("capacity")
		}
		if c.Used == nil {
			c.Used = map[string]int64{}
		}
		c.Used[r.Nonce] = r.Expires
		delete(c.Tickets, HashText(r.Code))
		return nil
	})
}
