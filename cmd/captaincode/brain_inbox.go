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
}

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

func (ib *inbox) push(dir, text, leg, from string) inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if ib.items == nil {
		ib.items = map[string][]inboxItem{}
	}
	ib.seq++
	it := inboxItem{ID: fmt.Sprintf("in_%d_%d", time.Now().Unix(), ib.seq), Text: text, Leg: leg, From: from, At: time.Now()}
	ib.items[dir] = append(ib.items[dir], it)
	return it
}

// take hands over everything queued for dir (the poller submits them in order).
func (ib *inbox) take(dir string) []inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	items := ib.items[dir]
	delete(ib.items, dir)
	var fresh []inboxItem
	for _, it := range items {
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
			Text string `json:"text"`
			Leg  string `json:"leg"`
			From string `json:"from"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			writeErr(w, 400, "empty prompt")
			return
		}
		leg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(req.Leg, "/")))
		if captaincode.StartsLoop(req.Text) || leg == "repeat" {
			writeErr(w, 403, "a sent prompt may not start a /repeat or a chain - only a person types a loop; send the task itself")
			return
		}
		if !b.inbox.admit(dir) {
			writeErr(w, 429, fmt.Sprintf("%d prompts were sent to %s since the user last typed (CAPTAIN_INBOX_QUOTA); more are accepted once they type", inboxQuota(), filepath.Base(dir)))
			return
		}
		it := b.inbox.push(dir, req.Text, leg, strings.TrimSpace(req.From))
		fmt.Printf("captain brain: inbox - prompt queued for %s (%s): %s\n", filepath.Base(dir), orDash(leg), promptPeek(req.Text))
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "inbox", Model: orDash(leg), Text: "queued for the TUI: " + promptPeek(req.Text)})
		writeJSON(w, 200, map[string]any{"ok": true, "id": it.ID, "pending": b.inbox.pending(dir)})
	case http.MethodGet:
		items := b.inbox.take(dir)
		if len(items) > 0 {
			fmt.Printf("captain brain: inbox - %d prompt(s) handed to the TUI in %s\n", len(items), filepath.Base(dir))
		}
		writeJSON(w, 200, map[string]any{"items": items})
	default:
		writeErr(w, 405, "GET or POST")
	}
}
