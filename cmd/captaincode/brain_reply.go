package main

// Replies: where a worker's later report goes. A worker that backgrounds
// work arms `captain send` to report when it lands (callbackContract). It
// used to name a folder, so the report went to whatever TUI was open there,
// labelled only "from frontier": on 2026-10-05 a watcher's failure landed in
// a session that did not remember starting it, and the worker that read it
// ran in a third folder the text happened to name. Now each worker turn gets
// a reply token bound to the TUI it came from - its folder, and its session
// when the sidebar reported one - and the report says who sent it. Tokens are
// kept on disk, so a watcher that fires after a brain restart still finds its
// way home. Sending to another folder on purpose (captain to captain) stays
// possible with --cwd, and is labelled with the sender's folder.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// replyTTL bounds how long a token stays valid: a watcher that waits longer
// than this reports to its folder instead.
const replyTTL = 14 * 24 * time.Hour

type replyTarget struct {
	Dir     string    `json:"dir"`
	Session string    `json:"session,omitempty"`
	Leg     string    `json:"leg"`
	At      time.Time `json:"at"`
}

type replyBook struct {
	mu      sync.Mutex
	path    string // "" = ~/.captaincode/reply-to.json
	targets map[string]replyTarget
	loaded  bool
}

var replies = &replyBook{}

func (rb *replyBook) file() string {
	if rb.path != "" {
		return rb.path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "reply-to.json")
}

func (rb *replyBook) load() {
	if rb.loaded {
		return
	}
	rb.loaded = true
	rb.targets = map[string]replyTarget{}
	if data, err := os.ReadFile(rb.file()); err == nil {
		_ = json.Unmarshal(data, &rb.targets)
	}
}

// mint records where a reply to this worker turn goes and returns its token.
func (rb *replyBook) mint(t replyTarget) string {
	var raw [5]byte
	_, _ = rand.Read(raw[:])
	id := "rt_" + hex.EncodeToString(raw[:])
	t.At = time.Now()
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.load()
	for k, v := range rb.targets {
		if time.Since(v.At) > replyTTL {
			delete(rb.targets, k)
		}
	}
	rb.targets[id] = t
	if data, err := json.MarshalIndent(rb.targets, "", " "); err == nil {
		_ = os.MkdirAll(filepath.Dir(rb.file()), 0o700)
		_ = os.WriteFile(rb.file(), data, 0o600)
	}
	return id
}

func (rb *replyBook) resolve(id string) (replyTarget, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.load()
	t, ok := rb.targets[strings.TrimSpace(id)]
	if !ok || time.Since(t.At) > replyTTL {
		return replyTarget{}, false
	}
	return t, true
}

// label is how a reply introduces itself: who sent it, from which folder,
// for the turn started when.
func (t replyTarget) label(from string) string {
	if from == "" {
		from = t.Leg
	}
	return fmt.Sprintf("%s, for the %s turn of %s in %s", from, t.Leg, t.At.Format("Jan 2 15:04"), filepath.Base(t.Dir))
}

// sessions remembers which TUI session last sent a message from each folder.
// The sidebar posts it as each message is typed (/v1/session/seen); the
// provider call that follows does not carry it.
type sessionSeen struct {
	mu   sync.Mutex
	last map[string]struct {
		id string
		at time.Time
	}
}

var sessions = &sessionSeen{}

func (s *sessionSeen) note(dir, id string) {
	if dir == "" || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]struct {
			id string
			at time.Time
		}{}
	}
	s.last[filepath.Clean(dir)] = struct {
		id string
		at time.Time
	}{id, time.Now()}
}

// of is the session that last typed in dir, if it did so recently enough to
// be the one this turn belongs to.
func (s *sessionSeen) of(dir string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.last[filepath.Clean(dir)]; ok && time.Since(v.at) < 30*time.Minute {
		return v.id
	}
	return ""
}

// sessionSeenHTTP: POST /v1/session/seen?cwd=<dir> {"session":"…"} - the
// sidebar names the session a message is typed in.
func (b *brain) sessionSeenHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST")
		return
	}
	var req struct {
		Session string `json:"session"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	dir, ok := workspaceFilter(r)
	if !ok {
		writeErr(w, 400, "cwd")
		return
	}
	sessions.note(dir, strings.TrimSpace(req.Session))
	writeJSON(w, 200, map[string]any{"ok": true})
}

// replyTo mints the token a worker of this turn reports back with.
func replyTo(ws captaincode.Workspace, leg captaincode.Leg) string {
	dir := ws.Origin
	if dir == "" {
		dir = ws.Dir
	}
	return replies.mint(replyTarget{Dir: dir, Session: ws.Session, Leg: string(leg)})
}
