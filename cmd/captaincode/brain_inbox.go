package main

// The inbox (2026-09-17): a prompt for a folder's OPEN TUI, from outside it.
//
// A watcher that finally sees the GPU it waited two hours for, a cron, a
// script - none of them can type into the terminal. They hand the prompt to
// the brain (`captain send --cwd <dir> [--leg grok] "<prompt>"`, or POST
// /v1/inbox), and the sidebar plugin of the TUI open in that folder, which
// polls the brain every second anyway, submits it into the session exactly
// as if the user had typed it: it shows as a user turn, the forced leg is
// honoured, the answer streams. A folder with no TUI open keeps the prompt
// until one opens; a prompt older than a day is dropped.
//
// Workers can run `captain send` too, and the TUI submits what they send as
// if the user had typed it. Two limits keep that from running without the
// user (formal/CommandSafety/Inbox.lean): a sent prompt may not start a loop
// (a /repeat or a chain), and at most CAPTAIN_INBOX_QUOTA prompts (default 5)
// are accepted between two turns the user types. Before them, a worker could
// send a prompt telling the next worker to send again, without end.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type inboxItem struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	Leg  string    `json:"leg,omitempty"` // forced leg / pseudo-model, "" = the TUI's own model
	From string    `json:"from,omitempty"`
	At   time.Time `json:"at"`
	// Session is the TUI session a reply is for (brain_reply.go); "" = any
	// session open in the folder. Origin says who sent it, from where.
	Session string `json:"session,omitempty"`
	Origin  string `json:"origin,omitempty"`
}

// inboxSessionWait is how long a reply waits for its own session before any
// session open in the folder may take it: a closed session must not strand it.
const inboxSessionWait = 10 * time.Minute

type inbox struct {
	mu    sync.Mutex
	items map[string][]inboxItem // by folder
	seq   int
	// sent counts the prompts accepted per folder since the user last typed;
	// handed holds the texts given to the TUI and not yet seen as a turn, so
	// a submitted prompt is not taken for the user typing.
	sent   map[string]int
	handed map[string][]string
}

// inboxQuota is CAPTAIN_INBOX_QUOTA, default 5.
func inboxQuota() int {
	if n, err := strconv.Atoi(os.Getenv("CAPTAIN_INBOX_QUOTA")); err == nil && n > 0 {
		return n
	}
	return 5
}

// admit spends one unit of dir's quota; false when it is spent.
func (ib *inbox) admit(dir string) bool {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if ib.sent == nil {
		ib.sent = map[string]int{}
	}
	if ib.sent[dir] >= inboxQuota() {
		return false
	}
	ib.sent[dir]++
	return true
}

// noteTurn sees a turn arrive in dir. A prompt the inbox handed over is
// crossed off; anything else was typed by the user, which refills the quota.
func (ib *inbox) noteTurn(dir, text string) {
	dir = filepath.Clean(dir)
	ib.mu.Lock()
	defer ib.mu.Unlock()
	text = strings.TrimSpace(text)
	for i, h := range ib.handed[dir] {
		if h == text {
			ib.handed[dir] = append(ib.handed[dir][:i:i], ib.handed[dir][i+1:]...)
			return
		}
	}
	delete(ib.sent, dir)
}

const inboxTTL = 24 * time.Hour

func (ib *inbox) push(dir, text, leg, from, session, origin string) inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if ib.items == nil {
		ib.items = map[string][]inboxItem{}
	}
	ib.seq++
	it := inboxItem{ID: fmt.Sprintf("in_%d_%d", time.Now().Unix(), ib.seq), Text: text, Leg: leg, From: from, At: time.Now(),
		Session: session, Origin: origin}
	ib.items[dir] = append(ib.items[dir], it)
	return it
}

// take hands over what is queued for dir and this session (the poller
// submits them in order). A reply for another session stays, until it has
// waited inboxSessionWait; session "" (an older sidebar) takes everything.
func (ib *inbox) take(dir, session string) []inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	items := ib.items[dir]
	delete(ib.items, dir)
	var fresh []inboxItem
	for _, it := range items {
		if it.Session != "" && session != "" && it.Session != session && time.Since(it.At) < inboxSessionWait {
			ib.items[dir] = append(ib.items[dir], it)
			continue
		}
		if time.Since(it.At) < inboxTTL {
			fresh = append(fresh, it)
			if ib.handed == nil {
				ib.handed = map[string][]string{}
			}
			ib.handed[dir] = append(ib.handed[dir], strings.TrimSpace(it.Text))
		}
	}
	return fresh
}

func (ib *inbox) pending(dir string) int {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	return len(ib.items[dir])
}

// inboxHTTP: POST /v1/inbox?cwd=<dir> {"text":"…","leg":"grok","from":"a100 watcher"} queues;
// GET /v1/inbox?cwd=<dir> takes what is queued (the sidebar's poll).
func (b *brain) inboxHTTP(w http.ResponseWriter, r *http.Request) {
	dir, ok := workspaceFilter(r)
	if !ok {
		dir = defaultWorkspace().Dir
	}
	dir = filepath.Clean(dir)
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Text      string `json:"text"`
			Leg       string `json:"leg"`
			From      string `json:"from"`
			Reply     string `json:"reply"`      // a worker's reply token (brain_reply.go)
			SenderDir string `json:"sender_dir"` // the folder `captain send` ran in
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			writeErr(w, 400, "empty prompt")
			return
		}
		leg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(req.Leg, "/")))
		if captaincode.StartsLoop(req.Text) || leg == "repeat" {
			// A loop or a program must be typed (WORKFLOW_LANGUAGE §11): a
			// watcher, a cron or a worker that queued one would start a run
			// nobody saw start.
			writeErr(w, 403, "captain send cannot start a loop or a program (/repeat, ( … ), ||, until:) - type it in the TUI, or send the task without them")
			return
		}
		// A reply goes to the folder and session its token names; a send
		// to a folder says where it came from when that is another folder.
		session, origin := "", strings.TrimSpace(req.From)
		if req.Reply != "" {
			t, ok := replies.resolve(req.Reply)
			if !ok {
				writeErr(w, 404, "unknown or expired reply token "+req.Reply+" - send with --cwd <folder> instead")
				return
			}
			dir, session, origin = filepath.Clean(t.Dir), t.Session, t.label(req.From)
		} else if s := strings.TrimSpace(req.SenderDir); s != "" && filepath.Clean(s) != dir {
			origin = strings.TrimSpace(orDash(req.From) + " in " + filepath.Base(s) + " (another folder)")
		}
		if origin != "" {
			req.Text += "\n\n- sent by " + origin
		}
		if !b.inbox.admit(dir) {
			writeErr(w, 429, fmt.Sprintf("%d prompts were sent to %s since the user last typed (CAPTAIN_INBOX_QUOTA); more are accepted once they type", inboxQuota(), filepath.Base(dir)))
			return
		}
		it := b.inbox.push(dir, req.Text, leg, strings.TrimSpace(req.From), session, origin)
		fmt.Printf("captain brain: inbox - prompt queued for %s (%s): %s\n", filepath.Base(dir), orDash(leg), promptPeek(req.Text))
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "inbox", Model: orDash(leg), Text: "queued for the TUI: " + promptPeek(req.Text)})
		writeJSON(w, 200, map[string]any{"ok": true, "id": it.ID, "pending": b.inbox.pending(dir)})
	case http.MethodGet:
		items := b.inbox.take(dir, strings.TrimSpace(r.URL.Query().Get("session")))
		if len(items) > 0 {
			fmt.Printf("captain brain: inbox - %d prompt(s) handed to the TUI in %s\n", len(items), filepath.Base(dir))
		}
		writeJSON(w, 200, map[string]any{"items": items})
	default:
		writeErr(w, 405, "GET or POST")
	}
}
