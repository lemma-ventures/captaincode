package main

// /parallel typed while a turn runs (2026-11-15). The TUI holds one turn at a
// time and queues whatever is typed while a turn streams, so a /parallel
// typed then reached the brain only when the running turn ended - the one
// moment it was no longer needed ("QUEUED" under a long frontier turn, live
// 2026-11-15). The plugin now sends the word here the moment it is typed,
// as it does /btw, and refuses the queued copy when this endpoint acted.
//
// The run replays the conversation of the folder's latest turn (the one
// still running), with the task as the next user message: the same context
// the queued copy would have had, minus the running turn's answer, which
// does not exist yet.
//
// With no turn running in the folder the endpoint does nothing and says so:
// the plugin then lets the message through as an ordinary turn, which
// handleParallel answers with the exact conversation.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// parallelConv is the conversation of a folder's latest turn.
type parallelConv struct {
	model    string
	messages []oaiMessage
}

// noteConversation keeps the conversation of a user turn for a /parallel
// typed while it runs. Internal rounds and title turns are not the user's
// conversation.
func (b *brain) noteConversation(req oaiChatReq) {
	if req.internal || isTitleTurn(req.Messages) || len(req.Messages) == 0 {
		return
	}
	b.pmu.Lock()
	defer b.pmu.Unlock()
	if b.parallelConvs == nil {
		b.parallelConvs = map[string]parallelConv{}
	}
	b.parallelConvs[filepath.Clean(req.ws.Dir)] = parallelConv{
		model:    req.Model,
		messages: append([]oaiMessage(nil), req.Messages...),
	}
}

// turnRunning reports whether a user turn is in flight in dir.
func (b *brain) turnRunning(dir string) bool {
	dir = filepath.Clean(dir)
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	for s := range b.steers.live {
		if s.Dir == dir {
			return true
		}
	}
	return false
}

// parallelHTTP is POST /v1/parallel?cwd=<dir> {"text": "<task>|status|show [id]|stop [id]"}.
// acted=false means nothing ran here: the message goes through as a turn.
func (b *brain) parallelHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ws := workspaceOf(r)
	if !b.turnRunning(ws.Dir) {
		writeJSON(w, 200, map[string]any{"ok": true, "acted": false,
			"result": "no turn is running in this folder - sent as an ordinary turn"})
		return
	}
	rest := strings.TrimSpace(body.Text)
	low := strings.ToLower(rest)
	var msg string
	switch {
	case rest == "" || low == "status":
		msg = b.parallelStatus(ws.Dir)
	case low == "stop" || strings.HasPrefix(low, "stop "):
		msg = b.parallelStop(ws.Dir, strings.TrimSpace(rest[len("stop"):]))
	case low == "show" || strings.HasPrefix(low, "show "):
		msg = b.parallelShow(ws.Dir, strings.TrimSpace(rest[len("show"):]))
	default:
		b.pmu.Lock()
		conv := b.parallelConvs[filepath.Clean(ws.Dir)]
		b.pmu.Unlock()
		req := oaiChatReq{Model: conv.model, ws: ws,
			Messages: append(append([]oaiMessage(nil), conv.messages...), oaiMessage{Role: "user", Content: jsonString(rest)})}
		msg = b.parallelStart(req, rest)
	}
	fmt.Printf("captain brain: /parallel out of band in %s - %s\n", filepath.Base(ws.Dir), promptPeek(msg))
	writeJSON(w, 200, map[string]any{"ok": true, "acted": true, "result": msg})
}
