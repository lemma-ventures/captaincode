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

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
