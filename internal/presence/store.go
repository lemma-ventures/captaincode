package presence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

type Store struct{ Path string }

var storeMu sync.Mutex

func DefaultStore() *Store {
	home, _ := os.UserHomeDir()
	return &Store{Path: filepath.Join(home, ".config", "captain", "presence.json")}
}
func (s *Store) transaction(write bool, fn func(*Config) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	dir := filepath.Dir(s.Path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	fd, e := syscall.Open(s.Path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	lock := os.NewFile(uintptr(fd), s.Path+".lock")
	defer lock.Close()
	if e = syscall.Flock(fd, syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	c := Config{Version: 1, Window: 900}
	f, e := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e == nil {
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
			f.Close()
			return fail("unsafe_store")
		}
		raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
		f.Close()
		if err != nil {
			return err
		}
		if len(raw) >= 8<<20 || json.Unmarshal(raw, &c) != nil || c.Version != 1 {
			return fail("invalid_store")
		}
	} else if !os.IsNotExist(e) {
		return e
	} else if !write {
		return fail("not_configured")
	}
	if c.Machine == "" {
		c.Machine, e = RandomID()
		if e != nil {
			return e
		}
	}
	if e = fn(&c); e != nil {
		return e
	}
	if !write {
		return nil
	}
	raw, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	if len(raw) >= 8<<20 {
		return fail("store_full")
	}
	tmp, e := os.CreateTemp(dir, ".presence-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(append(raw, '\n')); e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp.Name(), s.Path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e == nil {
		e = d.Sync()
		d.Close()
	}
	return e
}
func (s *Store) Update(fn func(*Config) error) error { return s.transaction(true, fn) }
func (s *Store) Read() (Config, error) {
	var out Config
	e := s.transaction(false, func(c *Config) error { out = *c; return nil })
	return out, e
}
func prune(c *Config, now int64) {
	for k, v := range c.Used {
		if v <= now {
			delete(c.Used, k)
		}
	}
	for k, v := range c.Screened {
		if v <= now {
			delete(c.Screened, k)
		}
	}
	for k, v := range c.Tickets {
		if v.Expires <= now {
			delete(c.Tickets, k)
		}
	}
	for k, v := range c.Grants {
		if v.Expires <= now {
			delete(c.Grants, k)
		}
	}
	for k, v := range c.Decisions {
		if v.Expires <= now {
			delete(c.Decisions, k)
		}
	}
}
func (s *Store) ValidatePrompt(e Envelope, text string) error {
	return s.transaction(false, func(c *Config) error { _, err := c.checkEnvelope(e, text, nowUnix()); return err })
}

func (s *Store) Prepare(e Envelope, text string) error {
	return s.Update(func(c *Config) error {
		now := nowUnix()
		h, err := c.checkEnvelope(e, text, now)
		if err != nil {
			return err
		}
		prune(c, now)
		if len(c.Screened) >= 1024 {
			return fail("capacity")
		}
		if c.Screened == nil {
			c.Screened = map[string]int64{}
		}
		c.Screened[Encode(h)] = e.ExpiresAt
		return nil
	})
}
func (s *Store) VerifyPrompt(p Signed) (Grant, error) {
	var g Grant
	err := s.Update(func(c *Config) error {
		now := nowUnix()
		h, e := c.checkEnvelope(p.Envelope, p.Text, now)
		if e != nil {
			return e
		}
		if c.Screened[Encode(h)] <= now {
			return fail("screen_required")
		}
		key, user, e := c.verify(p.Assertion, h, "")
		if e != nil {
			return e
		}
		dir := c.Folders[p.Envelope.Folder]
		st, e := os.Stat(dir)
		if e != nil || !st.IsDir() {
			return fail("unknown_folder")
		}
		prune(c, now)
		if len(c.Used) >= 10000 || len(c.Grants) >= 10000 {
			return fail("capacity")
		}
		id, e := RandomID()
		if e != nil {
			return e
		}
		g = Grant{ID: id, User: user, CredentialID: key.ID, Label: key.Label, Device: key.Label, Machine: c.Machine, Folder: p.Envelope.Folder, Dir: dir, TextSHA256: p.Envelope.TextSHA256, Expires: p.Envelope.ExpiresAt}
		if c.Used == nil {
			c.Used = map[string]int64{}
		}
		c.Used[p.Envelope.Nonce] = p.Envelope.ExpiresAt
		delete(c.Screened, Encode(h))
		if c.Grants == nil {
			c.Grants = map[string]Grant{}
		}
		c.Grants[id] = g
		c.Audit = append(c.Audit, Event{Grant: g, At: now, Kind: "prompt"})
		return nil
	})
	return g, err
}
func (s *Store) EnrollCode(user string) (string, error) {
	if !ValidLabel(user) {
		return "", fail("invalid_user")
	}
	code, e := RandomID()
	if e != nil {
		return "", e
	}
	e = s.Update(func(c *Config) error {
		now := nowUnix()
		prune(c, now)
		if len(c.Tickets) >= 32 {
			return fail("capacity")
		}
		if c.Tickets == nil {
			c.Tickets = map[string]Ticket{}
		}
		c.Tickets[HashText(code)] = Ticket{User: user, Expires: now + 300}
		return nil
	})
	return code, e
}
func (s *Store) Export() ([]byte, error) {
	c, e := s.Read()
	if e != nil {
		return nil, e
	}
	return json.MarshalIndent(struct {
		V     int    `json:"v"`
		Users []User `json:"users"`
	}{1, c.Users}, "", "  ")
}
func (s *Store) Import(raw []byte) error {
	var export struct {
		V     int    `json:"v"`
		Users []User `json:"users"`
	}
	if e := ReadJSON(bytes.NewReader(raw), &export); e != nil {
		return e
	}
	if export.V != 1 {
		return fail("invalid_import")
	}
	return s.Update(func(c *Config) error {
		seen := map[string]bool{}
		for _, u := range c.Users {
			for _, k := range u.Credentials {
				seen[k.ID] = true
			}
		}
		for _, u := range export.Users {
			if !ValidLabel(u.Name) || len(u.Credentials) == 0 {
				return fail("invalid_import")
			}
			for _, k := range u.Credentials {
				if e := validateCredential(k); e != nil {
					return e
				}
				if seen[k.ID] {
					return fail("duplicate_credential")
				}
				seen[k.ID] = true
			}
			found := false
			for i := range c.Users {
				if c.Users[i].Name == u.Name {
					c.Users[i].Credentials = append(c.Users[i].Credentials, u.Credentials...)
					found = true
					break
				}
			}
			if !found {
				c.Users = append(c.Users, u)
			}
		}
		return nil
	})
}
func validateCredential(k Credential) error {
	if _, err := time.Parse(time.RFC3339, k.Created); err != nil {
		return fail("invalid_credential")
	}
	if strings.IndexFunc(k.Label, unicode.IsControl) >= 0 {
		return fail("invalid_credential")
	}
	id, e := Decode(k.ID)
	if e != nil || len(id) < 1 || len(id) > 1024 || !ValidOrigin(k.Origin, k.RPID) || len(k.Label) > 128 || k.Label == "" {
		return fail("invalid_credential")
	}
	_, e = publicKey(k)
	return e
}
func (s *Store) Remove(user, id string) error {
	return s.Update(func(c *Config) error {
		for i := range c.Users {
			if c.Users[i].Name != user {
				continue
			}
			for j, k := range c.Users[i].Credentials {
				if k.ID == id {
					c.Users[i].Credentials = append(c.Users[i].Credentials[:j:j], c.Users[i].Credentials[j+1:]...)
					for g, v := range c.Grants {
						if v.CredentialID == id {
							delete(c.Grants, g)
						}
					}
					return nil
				}
			}
		}
		return fail("unknown_credential")
	})
}
func (s *Store) Info() (any, error) {
	c, e := s.Read()
	if e != nil {
		return nil, e
	}
	type keyInfo struct {
		ID     string `json:"id"`
		RPID   string `json:"rp_id"`
		Origin string `json:"origin"`
		Label  string `json:"device_label"`
	}
	type userInfo struct {
		Name        string    `json:"name"`
		Credentials []keyInfo `json:"credentials"`
	}
	users := []userInfo{}
	for _, u := range c.Users {
		v := userInfo{Name: u.Name, Credentials: []keyInfo{}}
		for _, k := range u.Credentials {
			v.Credentials = append(v.Credentials, keyInfo{k.ID, k.RPID, k.Origin, k.Label})
		}
		users = append(users, v)
	}
	folders := []string{}
	for f := range c.Folders {
		folders = append(folders, f)
	}
	return map[string]any{"v": 1, "machine": c.Machine, "allowed_origins": c.Origins, "window_seconds": c.Window, "folders": folders, "users": users}, nil
}
func (s *Store) Grant(id string) (Grant, error) {
	c, e := s.Read()
	if e != nil {
		return Grant{}, e
	}
	g, ok := c.Grants[id]
	if !ok || g.Expires <= nowUnix() {
		return Grant{}, fail("expired_grant")
	}
	return g, nil
}
func (s *Store) RequireDecision(grantID, action string) (Decision, error) {
	return s.requireDecision(grantID, action, true)
}
func (s *Store) requireDecision(grantID, action string, consume bool) (Decision, error) {
	var out Decision
	e := s.Update(func(c *Config) error {
		g, ok := c.Grants[grantID]
		if !ok || g.Expires <= nowUnix() {
			return fail("expired_grant")
		}
		for id, d := range c.Decisions {
			if d.GrantID == grantID && d.Action == action && d.Expires > nowUnix() {
				out = d
				if d.Approved && consume {
					delete(c.Decisions, id)
				}
				return nil
			}
		}
		id, e := RandomID()
		if e != nil {
			return e
		}
		out = Decision{ID: id, GrantID: grantID, Action: action, Expires: g.Expires}
		if c.Decisions == nil {
			c.Decisions = map[string]Decision{}
		}
		c.Decisions[id] = out
		return nil
	})
	return out, e
}
func DecisionChallenge(machine string, d Decision) []byte {
	b, _ := json.Marshal(struct {
		Purpose  string   `json:"purpose"`
		Machine  string   `json:"machine"`
		Decision Decision `json:"decision"`
	}{"captain.presence.decision.v1", machine, d})
	return Digest(b)
}
func (s *Store) ApproveDecision(id string, a Assertion) error {
	return s.Update(func(c *Config) error {
		d, ok := c.Decisions[id]
		if !ok || d.Approved || d.Expires <= nowUnix() {
			return fail("unknown_decision")
		}
		g, ok := c.Grants[d.GrantID]
		if !ok {
			return fail("expired_grant")
		}
		if _, _, e := c.verify(a, DecisionChallenge(c.Machine, d), g.User); e != nil {
			return e
		}
		d.Approved = true
		c.Decisions[id] = d
		c.Audit = append(c.Audit, Event{Grant: g, At: nowUnix(), Kind: "decision:" + id})
		return nil
	})
}

// DecisionsChallenge binds one passkey approval to every listed decision,
// sorted by ID, on this machine.
func DecisionsChallenge(machine string, ds []Decision) []byte {
	b, _ := json.Marshal(struct {
		Purpose   string     `json:"purpose"`
		Machine   string     `json:"machine"`
		Decisions []Decision `json:"decisions"`
	}{"captain.presence.decisions.v1", machine, ds})
	return Digest(b)
}

// PendingDecisions are the decisions ids name, sorted by ID, as one approval
// would sign them: every one pending, unexpired and signed for one user.
func (c *Config) pendingDecisions(ids []string) ([]Decision, string, error) {
	if len(ids) == 0 || len(ids) > 32 {
		return nil, "", fail("unknown_decision")
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
		return nil, "", fail("unknown_decision")
	}
	var ds []Decision
	user := ""
	for _, id := range sorted {
		d, ok := c.Decisions[id]
		if !ok || d.Approved || d.Expires <= nowUnix() {
			return nil, "", fail("unknown_decision")
		}
		g, ok := c.Grants[d.GrantID]
		if !ok {
			return nil, "", fail("expired_grant")
		}
		if user != "" && g.User != user {
			return nil, "", fail("mixed_users")
		}
		user = g.User
		ds = append(ds, d)
	}
	return ds, user, nil
}

// DecisionsChallengeFor is the challenge ApproveDecisions expects for ids.
func (s *Store) DecisionsChallengeFor(ids []string) ([]byte, error) {
	c, e := s.Read()
	if e != nil {
		return nil, e
	}
	ds, _, e := c.pendingDecisions(ids)
	if e != nil {
		return nil, e
	}
	return DecisionsChallenge(c.Machine, ds), nil
}

// ApproveDecisions approves every listed decision with one passkey assertion
// over DecisionsChallenge. Only the user who signed their prompts can.
func (s *Store) ApproveDecisions(ids []string, a Assertion) error {
	return s.Update(func(c *Config) error {
		ds, user, e := c.pendingDecisions(ids)
		if e != nil {
			return e
		}
		if _, _, e := c.verify(a, DecisionsChallenge(c.Machine, ds), user); e != nil {
			return e
		}
		for _, d := range ds {
			d.Approved = true
			c.Decisions[d.ID] = d
			c.Audit = append(c.Audit, Event{Grant: c.Grants[d.GrantID], At: nowUnix(), Kind: "decision:" + d.ID})
		}
		return nil
	})
}

// PurgeDecisions removes pending decisions without approving them: the
// actions they hold stay refused. It needs no passkey; it can only refuse.
func (s *Store) PurgeDecisions(ids []string) (int, error) {
	n := 0
	e := s.Update(func(c *Config) error {
		for _, id := range ids {
			if d, ok := c.Decisions[id]; ok && !d.Approved {
				delete(c.Decisions, id)
				c.Audit = append(c.Audit, Event{Grant: c.Grants[d.GrantID], At: nowUnix(), Kind: "purge:" + id})
				n++
			}
		}
		return nil
	})
	return n, e
}

func (s *Store) Decisions() ([]Decision, error) {
	c, e := s.Read()
	out := []Decision{}
	for _, d := range c.Decisions {
		if d.Expires > nowUnix() {
			out = append(out, d)
		}
	}
	return out, e
}
func (s *Store) CheckAction(grant, action string) error {
	d, e := s.RequireDecision(grant, action)
	if e != nil {
		return e
	}
	if !d.Approved {
		return fmt.Errorf("signed_decision_required: %s", d.ID)
	}
	return nil
}

func (s *Store) PreviewAction(grant, action string) error {
	d, e := s.requireDecision(grant, action, false)
	if e != nil {
		return e
	}
	if !d.Approved {
		return fmt.Errorf("signed_decision_required: %s", d.ID)
	}
	return nil
}
