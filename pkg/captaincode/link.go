package captaincode

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/internal/presence"
)

// This file is the local half of the link: the machine's signing identity and
// the folders that opted in to talk to a hub. The brain dials out and never
// listens (protocol rules 1-3 of the link spec). Pairing, the event stream and
// the message types follow; until a folder is listed here the brain refuses
// every message for it, whatever a hub says.
//
// One key per brain at ~/.config/captain/link.key, mode 0600. That key is
// inside ~/.config/captain, which the sent-turn jail already refuses, so a
// jailed worker never reads it. Nothing in this file opens a network
// connection, and no payload built here ever carries an absolute path: a
// folder travels as a label.

// linkProtocolDomain separates this protocol's signatures from every other
// signature on the machine. The hub signs and verifies the same domain string.
const linkProtocolDomain = "captain.link.v1"

const (
	linkStateVersion = 1
	linkMaxState     = 1 << 20
	linkMaxFolders   = 512
	linkMaxBody      = 64 * 1024
	linkTTL          = 30 * time.Second
	linkTTLSec       = 30
	linkSkew         = 5
)

// Link folder modes. "active" runs a read-only prompt as a sent turn; "queue"
// holds it until a keyboard is free (the D7 opt-out). Both list the folder.
const (
	LinkModeActive = "active"
	LinkModeQueue  = "queue"
)

var linkIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// LinkKeyPath is the link signing key, mode 0600.
func LinkKeyPath() string { return filepath.Join(configHome(), "captain", "link.key") }

// LinkStatePath is the list of folders that opted in, mode 0600.
func LinkStatePath() string { return filepath.Join(configHome(), "captain", "link.json") }

// LinkMachineID is the identity this brain already reports on its presence
// endpoint, so one machine keeps one name across both protocols. Pairing binds
// that name to the link key; a machine that never configured presence has no
// link identity yet.
func LinkMachineID() (string, error) {
	config, err := presence.DefaultStore().Read()
	if err != nil {
		return "", err
	}
	if config.Machine == "" {
		return "", fmt.Errorf("machine identity is not configured")
	}
	return config.Machine, nil
}

// LinkFingerprint is the shareable form of a link public key: the first half
// of its SHA-256, in hex. Two machines can compare fingerprints out of band
// without exchanging a key file.
func LinkFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:16])
}

// LoadLinkKey reads the machine's link key. It refuses everything a credential
// file can be instead of a regular, single-link, user-owned file of exactly
// one key: a symlink, a directory, a hard link, a group-readable file or a
// truncated file fails before any byte reaches the caller.
func LoadLinkKey() (ed25519.PrivateKey, error) {
	path := LinkKeyPath()
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("link key must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, fmt.Errorf("link key must have one link and belong to the current user")
	}
	if mode := info.Mode().Perm(); mode != 0o600 && mode != 0o400 {
		return nil, fmt.Errorf("link key must have mode 0600 or 0400, want %#o", mode)
	}
	if info.Size() != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("link key must be %d bytes, got %d", ed25519.PrivateKeySize, info.Size())
	}
	raw, err := io.ReadAll(io.LimitReader(f, ed25519.PrivateKeySize+1))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("link key must read as %d bytes", ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

// EnsureLinkKey returns the machine's link public key, creating the key file
// the first time. Two processes racing on a fresh machine settle on one key:
// the loser opens the file the winner created.
func EnsureLinkKey() (ed25519.PublicKey, error) {
	for attempt := 0; attempt < 2; attempt++ {
		priv, err := LoadLinkKey()
		if err == nil {
			return priv.Public().(ed25519.PublicKey), nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		dir := filepath.Dir(LinkKeyPath())
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		_, fresh, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(LinkKeyPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		_, err = f.Write(fresh)
		if e := f.Sync(); err == nil {
			err = e
		}
		if e := f.Close(); err == nil {
			err = e
		}
		if err != nil {
			os.Remove(LinkKeyPath())
			return nil, err
		}
		return fresh.Public().(ed25519.PublicKey), nil
	}
	return nil, fmt.Errorf("link key could not be created")
}

// LinkFolder is one folder that opted in. Root is machine-local state, never
// sent: a peer sees the folder's label alone.
type LinkFolder struct {
	Root    string `json:"root"`
	Mode    string `json:"mode"`
	AddedAt int64  `json:"added_at"`
}

type linkState struct {
	Version int          `json:"version"`
	Folders []LinkFolder `json:"folders"`
}

// LinkCanonicalRoot resolves a folder to the path the registry stores: fully
// qualified, symlinks resolved, and a real directory. A folder that is missing,
// is not a directory, or resolves elsewhere is refused instead of stored.
func LinkCanonicalRoot(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("folder is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("folder %q is not reachable: %v", dir, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("folder %q is not reachable: %v", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("folder %q is not a directory", dir)
	}
	return filepath.Clean(root), nil
}

var linkMu sync.Mutex

// linkRead and linkUpdate hold an exclusive lock over the registry, refuse an
// unsafe or unreadable file, and write through a temporary file so an
// interrupted write never publishes half a list.
func linkRead() (linkState, error) {
	return linkUpdate(false, nil)
}

func linkUpdate(write bool, fn func(*linkState) error) (linkState, error) {
	linkMu.Lock()
	defer linkMu.Unlock()
	path := LinkStatePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return linkState{}, err
	}
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return linkState{}, err
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	defer lock.Close()
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return linkState{}, err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)

	state := linkState{Version: linkStateVersion, Folders: []LinkFolder{}}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		st, statErr := f.Stat()
		if statErr != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
			f.Close()
			return linkState{}, fmt.Errorf("link registry is not a regular 0600 file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, linkMaxState))
		f.Close()
		if readErr != nil {
			return linkState{}, readErr
		}
		if len(raw) >= linkMaxState {
			return linkState{}, fmt.Errorf("link registry is too large")
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if dec.Decode(&state) != nil || state.Version != linkStateVersion {
			return linkState{}, fmt.Errorf("link registry is unreadable")
		}
	} else if !os.IsNotExist(err) {
		return linkState{}, err
	} else if !write {
		return state, nil
	}
	if state.Folders == nil {
		state.Folders = []LinkFolder{}
	}
	if fn != nil {
		if err := fn(&state); err != nil {
			return linkState{}, err
		}
	}
	if !write {
		return state, nil
	}
	if len(state.Folders) > linkMaxFolders {
		return linkState{}, fmt.Errorf("link registry holds at most %d folders", linkMaxFolders)
	}
	sortLinkFolders(state.Folders)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return linkState{}, err
	}
	if len(raw) >= linkMaxState {
		return linkState{}, fmt.Errorf("link registry is too large")
	}
	tmp, err := os.CreateTemp(dir, ".link-*")
	if err != nil {
		return linkState{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Sync()
	}
	if err != nil {
		tmp.Close()
		return linkState{}, err
	}
	if err = os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return linkState{}, err
	}
	if err = tmp.Close(); err != nil {
		return linkState{}, err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return linkState{}, err
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	return state, err
}

func sortLinkFolders(folders []LinkFolder) {
	sort.Slice(folders, func(i, j int) bool { return folders[i].Root < folders[j].Root })
}

// LinkFolders lists the opted-in folders, sorted by root.
func LinkFolders() ([]LinkFolder, error) {
	state, err := linkRead()
	if err != nil {
		return nil, err
	}
	out := append([]LinkFolder{}, state.Folders...)
	sortLinkFolders(out)
	return out, nil
}

// LinkAdd opts a folder in. Adding an already linked folder updates its mode
// instead of duplicating it. The link stays off for every other folder on the
// machine.
func LinkAdd(dir, mode string) (LinkFolder, error) {
	if mode != LinkModeActive && mode != LinkModeQueue {
		return LinkFolder{}, fmt.Errorf("link mode must be %q or %q", LinkModeActive, LinkModeQueue)
	}
	root, err := LinkCanonicalRoot(dir)
	if err != nil {
		return LinkFolder{}, err
	}
	var out LinkFolder
	_, err = linkUpdate(true, func(s *linkState) error {
		for i, folder := range s.Folders {
			if folder.Root == root {
				s.Folders[i].Mode = mode
				out = s.Folders[i]
				return nil
			}
		}
		out = LinkFolder{Root: root, Mode: mode, AddedAt: time.Now().Unix()}
		s.Folders = append(s.Folders, out)
		return nil
	})
	if err != nil {
		return LinkFolder{}, err
	}
	return out, nil
}

// LinkOff drops one folder from the link, or every folder when dir is empty.
// It reports how many folders stopped listening. Dropping the folder also
// drops anything the hub queued for it: nothing waits for a link that is off.
func LinkOff(dir string) (int, error) {
	if strings.TrimSpace(dir) == "" {
		stopped := 0
		_, err := linkUpdate(true, func(s *linkState) error {
			stopped = len(s.Folders)
			s.Folders = []LinkFolder{}
			return nil
		})
		return stopped, err
	}
	root, err := LinkCanonicalRoot(dir)
	if err != nil {
		return 0, err
	}
	stopped := 0
	_, err = linkUpdate(true, func(s *linkState) error {
		kept := make([]LinkFolder, 0, len(s.Folders))
		for _, folder := range s.Folders {
			if folder.Root == root {
				stopped++
				continue
			}
			kept = append(kept, folder)
		}
		s.Folders = kept
		return nil
	})
	return stopped, err
}

// LinkMode reports how a folder is linked: the mode it was linked under, and
// whether it is linked at all. The brain asks this before it accepts anything
// a hub sends for that folder.
func LinkMode(dir string) (string, bool) {
	root, err := LinkCanonicalRoot(dir)
	if err != nil {
		return "", false
	}
	folders, err := LinkFolders()
	if err != nil {
		return "", false
	}
	for _, folder := range folders {
		if folder.Root == root {
			return folder.Mode, true
		}
	}
	return "", false
}

// LinkEnvelope is the one message shape the link uses in both directions: a
// short, expiring, signed record. V is the schema, Direction separates the two
// flows, ID is single use, and the signature covers every field except itself.
type LinkEnvelope struct {
	V         int    `json:"v"`
	Direction string `json:"direction"`
	MachineID string `json:"machine_id"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func (e LinkEnvelope) signingBytes() []byte {
	return []byte(strings.Join([]string{
		linkProtocolDomain, e.Direction, e.MachineID, e.ID, e.Type,
		strconv.FormatInt(e.IssuedAt, 10), strconv.FormatInt(e.ExpiresAt, 10), e.Payload,
	}, "\n"))
}

// LinkSign signs one outbound message. The id is random, the lifetime is the
// 30 seconds the protocol allows, and the payload is bounded so a hub can
// reject a body without reading all of it.
func LinkSign(key ed25519.PrivateKey, machineID, direction, kind string, payload any, now time.Time) (LinkEnvelope, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return LinkEnvelope{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	return linkSign(key, machineID, direction, kind, payload, now, id)
}

func linkSign(key ed25519.PrivateKey, machineID, direction, kind string, payload any, now time.Time, id string) (LinkEnvelope, error) {
	if len(key) != ed25519.PrivateKeySize {
		return LinkEnvelope{}, fmt.Errorf("link signing key unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return LinkEnvelope{}, err
	}
	if len(body) > linkMaxBody {
		return LinkEnvelope{}, fmt.Errorf("link payload is too large")
	}
	e := LinkEnvelope{
		V:         1,
		Direction: direction,
		MachineID: machineID,
		ID:        id,
		Type:      kind,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(linkTTL).Unix(),
		Payload:   base64.RawURLEncoding.EncodeToString(body),
	}
	e.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, e.signingBytes()))
	return e, nil
}

// LinkVerify checks a message from the other side before anything in it is
// trusted: the schema, the direction, the machine, the clock window, the
// single-use id, the bound on the body and the signature over the whole
// record. Every failure is the same error, so a caller learns nothing from a
// rejected envelope except that it is rejected.
func LinkVerify(key ed25519.PublicKey, e LinkEnvelope, direction string, now time.Time) error {
	if len(key) != ed25519.PublicKeySize ||
		e.V != 1 || e.Direction != direction ||
		!linkIdentifier.MatchString(e.MachineID) || !linkIdentifier.MatchString(e.ID) || len(e.ID) < 20 ||
		strings.ContainsAny(e.Type, "\r\n") || len(e.Type) > 40 ||
		e.IssuedAt > now.Unix()+5 || e.IssuedAt < now.Unix()-linkTTLSec ||
		e.ExpiresAt > now.Unix()+linkTTLSec+linkSkew ||
		e.ExpiresAt <= now.Unix() ||
		e.ExpiresAt <= e.IssuedAt || e.ExpiresAt-e.IssuedAt > linkTTLSec ||
		len(e.Payload) > linkMaxBody {
		return fmt.Errorf("invalid link envelope")
	}
	if _, err := base64.RawURLEncoding.DecodeString(e.Payload); err != nil {
		return fmt.Errorf("invalid link envelope")
	}
	signature, err := base64.RawURLEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(key, e.signingBytes(), signature) {
		return fmt.Errorf("invalid link envelope")
	}
	return nil
}

// LinkDecodeEnvelope parses an envelope as the other side sent it: a bounded
// body, no unknown field and no trailing JSON, so a new envelope field cannot
// ride in unnoticed.
func LinkDecodeEnvelope(raw []byte, target *LinkEnvelope) error {
	if len(raw) > linkMaxBody {
		return fmt.Errorf("link envelope is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

// LinkDecodePayload reads the signed body into a struct, refusing unknown
// fields and trailing JSON so a new field cannot arrive unnoticed.
func LinkDecodePayload(e LinkEnvelope, target any) error {
	body, err := base64.RawURLEncoding.DecodeString(e.Payload)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
