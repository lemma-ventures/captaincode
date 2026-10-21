package presence

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/binary"
	"encoding/json"
	"math/big"
)

func publicKey(c Credential) (any, error) {
	raw, e := Decode(c.Key)
	if e != nil {
		return nil, e
	}
	v, n, e := decodeCBOR(raw)
	if e != nil || n != len(raw) {
		return nil, fail("invalid_key")
	}
	m, ok := v.(cborMap)
	if !ok {
		return nil, fail("invalid_key")
	}
	x, _ := m[int64(-2)].([]byte)
	if c.Algorithm == "ES256" && m[int64(1)] == int64(2) && m[int64(3)] == int64(-7) && m[int64(-1)] == int64(1) && len(m) == 5 {
		y, _ := m[int64(-3)].([]byte)
		if len(x) != 32 || len(y) != 32 {
			return nil, fail("invalid_key")
		}
		k := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !k.Curve.IsOnCurve(k.X, k.Y) {
			return nil, fail("invalid_key")
		}
		return k, nil
	}
	if c.Algorithm == "Ed25519" && m[int64(1)] == int64(1) && m[int64(3)] == int64(-8) && m[int64(-1)] == int64(6) && len(x) == 32 && len(m) == 4 {
		return ed25519.PublicKey(x), nil
	}
	return nil, fail("invalid_key")
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
	TopOrigin   string `json:"topOrigin"`
}

func checkClient(raw []byte, typ string, challenge []byte, origin string, allowed []string) error {
	var cd clientData
	d := json.NewDecoder(bytes.NewReader(raw))
	if e := uniqueJSON(d, 0); e != nil {
		return fail("invalid_client_data")
	}
	if json.Unmarshal(raw, &cd) != nil || cd.Type != typ {
		return fail("wrong_type")
	}
	if cd.CrossOrigin || cd.TopOrigin != "" {
		return fail("cross_origin")
	}
	if !Allowed(allowed, cd.Origin) {
		return fail("origin_not_allowed")
	}
	if cd.Origin != origin {
		return fail("credential_origin_mismatch")
	}
	ch, e := Decode(cd.Challenge)
	if e != nil || !bytes.Equal(ch, challenge) {
		return fail("challenge_mismatch")
	}
	return nil
}
func checkAuth(raw []byte, rp string, registration bool) (uint32, error) {
	if len(raw) < 37 {
		return 0, fail("invalid_authenticator_data")
	}
	h := sha256.Sum256([]byte(rp))
	if !bytes.Equal(raw[:32], h[:]) {
		return 0, fail("wrong_rp_id")
	}
	flags := raw[32]
	if flags&1 == 0 {
		return 0, fail("user_presence_required")
	}
	if flags&4 == 0 {
		return 0, fail("user_verification_required")
	}
	if flags&0x22 != 0 || flags&0x10 != 0 && flags&8 == 0 {
		return 0, fail("invalid_authenticator_data")
	}
	if !registration {
		if flags&0x40 != 0 {
			return 0, fail("invalid_authenticator_data")
		}
		if flags&0x80 != 0 {
			v, n, e := decodeCBOR(raw[37:])
			if _, ok := v.(cborMap); e != nil || !ok || n != len(raw)-37 {
				return 0, fail("invalid_authenticator_data")
			}
		} else if len(raw) != 37 {
			return 0, fail("invalid_authenticator_data")
		}
	}
	return binary.BigEndian.Uint32(raw[33:37]), nil
}
func (c *Config) verify(a Assertion, challenge []byte, user string) (*Credential, string, error) {
	var key *Credential
	var name string
	for i := range c.Users {
		u := &c.Users[i]
		for j := range u.Credentials {
			if u.Credentials[j].ID == a.CredentialID {
				key = &u.Credentials[j]
				name = u.Name
			}
		}
	}
	if key == nil {
		return nil, "", fail("unknown_credential")
	}
	if user != "" && user != name {
		return nil, "", fail("wrong_user")
	}
	ad, e := Decode(a.AuthenticatorData)
	if e != nil {
		return nil, "", e
	}
	cd, e := Decode(a.ClientDataJSON)
	if e != nil {
		return nil, "", e
	}
	signature, e := Decode(a.Signature)
	if e != nil {
		return nil, "", e
	}
	if e = checkClient(cd, "webauthn.get", challenge, key.Origin, c.Origins); e != nil {
		return nil, "", e
	}
	count, e := checkAuth(ad, key.RPID, false)
	if e != nil {
		return nil, "", e
	}
	if (key.SignCount != 0 || count != 0) && count <= key.SignCount {
		return nil, "", fail("counter_not_increased")
	}
	k, e := publicKey(*key)
	if e != nil {
		return nil, "", e
	}
	cdHash := sha256.Sum256(cd)
	data := append(append([]byte{}, ad...), cdHash[:]...)
	valid := false
	switch pub := k.(type) {
	case *ecdsa.PublicKey:
		var rs struct{ R, S *big.Int }
		rest, e := asn1.Unmarshal(signature, &rs)
		if e == nil && len(rest) == 0 && rs.R != nil && rs.S != nil {
			h := sha256.Sum256(data)
			valid = ecdsa.Verify(pub, h[:], rs.R, rs.S)
		}
	case ed25519.PublicKey:
		valid = ed25519.Verify(pub, data, signature)
	}
	if !valid {
		return nil, "", fail("bad_signature")
	}
	key.SignCount = count
	return key, name, nil
}
