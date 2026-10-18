package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const turnHeader = "X-Captain-Turn"

var turnIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (b *brain) bindTurn(id string, s *captaincode.Steer) bool {
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	if !turnIDPattern.MatchString(id) || b.steers.turns[id] != nil {
		return false
	}
	if b.steers.turns == nil {
		b.steers.turns = map[string]*captaincode.Steer{}
	}
	b.steers.turns[id] = s
	return true
}

func (b *brain) stopTurnHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	id := r.URL.Query().Get("turn")
	dir, ok := workspaceFilter(r)
	if !turnIDPattern.MatchString(id) || !ok || !filepath.IsAbs(dir) {
		writeErr(w, http.StatusBadRequest, "turn and absolute workspace required")
		return
	}
	var body struct {
		Stop   bool    `json:"stop"`
		Reason *string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || (!body.Stop && body.Reason == nil) || (body.Stop && body.Reason != nil) {
		writeErr(w, http.StatusBadRequest, "expected stop: true or reason")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "expected one request object")
		return
	}
	b.steers.mu.Lock()
	turn := b.steers.turns[id]
	b.steers.mu.Unlock()
	if turn == nil || filepath.Clean(turn.Dir) != dir {
		writeErr(w, http.StatusNotFound, "active turn not found")
		return
	}
	if body.Reason != nil {
		asked, stopped, withdrawn, msg := b.interruptDeliverTurns(dir, strings.TrimSpace(*body.Reason), []*captaincode.Steer{turn}, false)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "turn": id, "asked": asked, "stopped": stopped, "withdrawn": withdrawn, "result": msg})
		return
	}
	stopped := turn.Abort()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "turn": id, "stopped": stopped})
}

func (b *brain) noteTurnHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	id := r.URL.Query().Get("turn")
	dir, ok := workspaceFilter(r)
	if !turnIDPattern.MatchString(id) || !ok || !filepath.IsAbs(dir) {
		writeErr(w, http.StatusBadRequest, "turn and absolute workspace required")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "expected nonempty text")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "expected one request object")
		return
	}
	b.steers.mu.Lock()
	turn := b.steers.turns[id]
	b.steers.mu.Unlock()
	if turn == nil || filepath.Clean(turn.Dir) != dir {
		writeErr(w, http.StatusNotFound, "active turn not found")
		return
	}
	took, deaf, msg := b.btwDeliverTurns(dir, strings.TrimSpace(body.Text), []*captaincode.Steer{turn}, false)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "turn": id, "delivered": len(took) > 0, "took": took, "running": deaf, "result": msg})
}

func (b *brain) isScopedTurn(s *captaincode.Steer) bool {
	if s == nil {
		return false
	}
	b.steers.mu.Lock()
	defer b.steers.mu.Unlock()
	for _, turn := range b.steers.turns {
		if turn == s {
			return true
		}
	}
	return false
}
