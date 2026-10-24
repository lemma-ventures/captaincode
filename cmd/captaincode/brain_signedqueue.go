package main

// The signed-prompt queue (operator decision, 23 Oct); brain_queue.go is the
// TUI's own queue of typed prompts, a different thing. A signed prompt the
// brain accepts is written to ~/.captaincode/queue/pending, so it survives a
// brain restart, and is handed to the folder's TUI as before. When no TUI for
// that folder takes it within sqHeadlessAfter, the brain runs it itself as
// a typed turn in that folder: no Galaxy, no open TUI needed; its output is a
// run log like any other. The Gouvernail link will write to the same queue.
//
//	captain queue                list pending and recent entries
//	captain queue show ID        one entry
//	captain queue drop ID        remove a pending entry; it is not run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
)

// sqEntry is one signed prompt, by the inbox item ID it was handed as.
type sqEntry struct {
	ID     string    `json:"id"`
	Grant  string    `json:"grant"`
	Dir    string    `json:"dir"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Done   string    `json:"done,omitempty"` // tui, headless, dropped, expired
	DoneAt time.Time `json:"done_at,omitempty"`
	Result string    `json:"result,omitempty"`
}

var (
	// sqHeadlessAfter: how long a signed prompt waits for its TUI.
	sqHeadlessAfter = time.Minute
	// sqTUIWindow: a TUI that polled its inbox this recently is open.
	sqTUIWindow = 20 * time.Second
	sqTick      = 5 * time.Second
)

func sqRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "queue")
}

func sqWrite(state string, e sqEntry) error {
	dir := filepath.Join(sqRoot(), state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+e.ID+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, e.ID+".json"))
}

func sqRead(state, id string) (sqEntry, bool) {
	var e sqEntry
	raw, err := os.ReadFile(filepath.Join(sqRoot(), state, id+".json"))
	if err != nil || json.Unmarshal(raw, &e) != nil {
		return e, false
	}
	return e, true
}

// sqList lists one state's entries, oldest first.
func sqList(state string) []sqEntry {
	files, _ := filepath.Glob(filepath.Join(sqRoot(), state, "*.json"))
	var out []sqEntry
	for _, f := range files {
		if e, ok := sqRead(state, strings.TrimSuffix(filepath.Base(f), ".json")); ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// sqFinish moves a pending entry to done with how it ended. A missing
// entry (an unsigned item, or one already finished) is no error.
func sqFinish(id, how, result string) {
	e, ok := sqRead("pending", id)
	if !ok {
		return
	}
	e.Done, e.DoneAt, e.Result = how, time.Now(), result
	if sqWrite("done", e) == nil {
		_ = os.Remove(filepath.Join(sqRoot(), "pending", id+".json"))
	}
	// Done entries older than two weeks go.
	if olds, _ := filepath.Glob(filepath.Join(sqRoot(), "done", "*.json")); len(olds) > 200 {
		for _, f := range olds {
			if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > 14*24*time.Hour {
				_ = os.Remove(f)
			}
		}
	}
}

// loadSignedQueue puts the pending signed prompts back into the inbox after a
// restart; one whose approval ended is finished as expired.
func (b *brain) loadSignedQueue() {
	b.inbox.mu.Lock()
	defer b.inbox.mu.Unlock()
	if b.inbox.items == nil {
		b.inbox.items = map[string][]inboxItem{}
	}
	for _, e := range sqList("pending") {
		g, err := pres.DefaultStore().Grant(e.Grant)
		if err != nil || g.Dir != e.Dir || g.TextSHA256 != pres.HashText(e.Text) {
			sqFinish(e.ID, "expired", "its approval ended before it ran")
			continue
		}
		known := false
		for _, it := range b.inbox.items[e.Dir] {
			known = known || it.ID == e.ID
		}
		if !known {
			b.inbox.items[e.Dir] = append(b.inbox.items[e.Dir], inboxItem{ID: e.ID, Text: e.Text, At: e.At, Dir: e.Dir, Presence: &g})
		}
	}
}

// sqTUIOpen: a TUI for dir polled its inbox within sqTUIWindow.
func (ib *inbox) sqTUIOpen(dir string) bool {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	return time.Since(ib.polled[dir]) < sqTUIWindow
}

// sqClaim takes a pending signed item out of the inbox for the brain
// to run, and lists it as handed so its turn finds the approval. false when
// a TUI took it first.
func (ib *inbox) sqClaim(dir, id string) (inboxItem, bool) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	for i, it := range ib.items[dir] {
		if it.ID == id && it.Presence != nil && !it.Screening {
			ib.items[dir] = append(ib.items[dir][:i:i], ib.items[dir][i+1:]...)
			if ib.handed == nil {
				ib.handed = map[string][]inboxItem{}
			}
			ib.handed[dir] = append(ib.handed[dir], it)
			return it, true
		}
	}
	return inboxItem{}, false
}

// signedQueueLoop runs the signed prompts no TUI took.
func (b *brain) signedQueueLoop(done <-chan struct{}) {
	t := time.NewTicker(sqTick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		b.signedQueueStep()
	}
}

func (b *brain) signedQueueStep() {
	for _, e := range sqList("pending") {
		if time.Since(e.At) < sqHeadlessAfter || b.inbox.sqTUIOpen(e.Dir) {
			continue
		}
		it, ok := b.inbox.sqClaim(e.Dir, e.ID)
		if !ok {
			continue
		}
		fmt.Printf("captain brain: queue - no TUI took %s in %s; running it here\n", it.ID, filepath.Base(e.Dir))
		b.pushActivity(activity{Dir: e.Dir, Kind: "route", Leg: "queue", Text: "no TUI open: running the signed prompt here: " + promptPeek(it.Text)})
		go b.runSignedQueued(e, it)
	}
}

// runSignedQueued runs one signed prompt as a typed turn in its folder, the way
// a /repeat round runs, and records how it ended.
func (b *brain) runSignedQueued(e sqEntry, it inboxItem) {
	defer func() {
		if r := recover(); r != nil {
			sqFinish(e.ID, "headless", fmt.Sprintf("failed: %v", r))
		}
	}()
	// The leading directive picks the model, as the TUI would: nobody else
	// parses "/codex-cli /frontier" for a prompt the brain runs itself.
	msgs := []map[string]string{}
	for _, t := range sqThread(e.Dir) {
		msgs = append(msgs, map[string]string{"role": "user", "content": t.User}, map[string]string{"role": "assistant", "content": t.Answer})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": it.Text})
	body, _ := json.Marshal(map[string]any{"model": modelForTask(it.Text, "auto"), "messages": msgs})
	ctx := b.life
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		sqFinish(e.ID, "headless", "failed: "+err.Error())
		return
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(workspaceHeader, e.Dir)
	rec := &captureWriter{}
	if b.signedQueueRunFn != nil { // test seam
		b.signedQueueRunFn(rec, r)
	} else {
		b.chatCompletions(rec, r)
	}
	text, errText := rec.answer()
	result := promptPeek(text)
	if errText != "" || rec.status >= 400 {
		result = "failed: " + promptPeek(errText)
	}
	if errText == "" && rec.status < 400 && strings.TrimSpace(text) != "" {
		if err := sqThreadAppend(e.Dir, sqTurn{At: time.Now(), User: it.Text, Answer: text}); err != nil {
			fmt.Printf("captain brain: queue - could not keep %s's thread: %v\n", filepath.Base(e.Dir), err)
		}
	}
	sqFinish(e.ID, "headless", result)
	fmt.Printf("captain brain: queue - ran %s in %s: %s\n", e.ID, filepath.Base(e.Dir), result)
}

// A folder with no TUI still has a conversation. Each prompt the brain runs
// there and its answer join the folder's thread, and the next one starts
// with the newest of them (sqThreadTurns, sqThreadChars, sqThreadAge), as a
// TUI session would - the brain's own compaction trims a long one. A TUI
// opened later has its own session and does not see this thread; the
// folder's memory and run logs it does share.
type sqTurn struct {
	At     time.Time `json:"at"`
	User   string    `json:"user"`
	Answer string    `json:"answer"`
}

var (
	sqThreadTurns = 12
	sqThreadChars = 60_000
	sqThreadAge   = 7 * 24 * time.Hour
	sqThreadKeep  = 50
	sqAnswerMax   = 20_000
)

func sqThreadPath(dir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return filepath.Join(sqRoot(), "threads", hex.EncodeToString(sum[:8])+".jsonl")
}

func sqThreadAll(dir string) []sqTurn {
	raw, err := os.ReadFile(sqThreadPath(dir))
	if err != nil {
		return nil
	}
	var out []sqTurn
	for _, line := range strings.Split(string(raw), "\n") {
		var t sqTurn
		if json.Unmarshal([]byte(line), &t) == nil && t.User != "" {
			out = append(out, t)
		}
	}
	return out
}

// sqThread is the part of the folder's thread the next prompt carries,
// oldest first.
func sqThread(dir string) []sqTurn {
	all := sqThreadAll(dir)
	var out []sqTurn
	chars := 0
	for i := len(all) - 1; i >= 0 && len(out) < sqThreadTurns; i-- {
		t := all[i]
		if time.Since(t.At) > sqThreadAge {
			break
		}
		chars += len(t.User) + len(t.Answer)
		if chars > sqThreadChars && len(out) > 0 {
			break
		}
		out = append([]sqTurn{t}, out...)
	}
	return out
}

func sqThreadAppend(dir string, t sqTurn) error {
	if len(t.Answer) > sqAnswerMax {
		t.Answer = t.Answer[:sqAnswerMax] + "\n[…]"
	}
	all := append(sqThreadAll(dir), t)
	if len(all) > sqThreadKeep {
		all = all[len(all)-sqThreadKeep:]
	}
	var b strings.Builder
	for _, x := range all {
		line, err := json.Marshal(x)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	path := sqThreadPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cmdSignedQueue is `captain queue`: the brain's signed-prompt queue on this Mac.
func cmdSignedQueue(args []string) {
	switch {
	case len(args) == 0 || args[0] == "list":
		pending, done := sqList("pending"), sqList("done")
		fmt.Printf("%d pending\n", len(pending))
		for _, e := range pending {
			fmt.Printf("  %s  %s  %-14s %s\n", e.ID, e.At.Format("Jan 02 15:04"), filepath.Base(e.Dir), promptPeek(e.Text))
		}
		if len(done) > 10 {
			done = done[len(done)-10:]
		}
		fmt.Printf("%d recent\n", len(done))
		for _, e := range done {
			fmt.Printf("  %s  %s  %-14s %-9s %s\n", e.ID, e.DoneAt.Format("Jan 02 15:04"), filepath.Base(e.Dir), e.Done, promptPeek(e.Text))
		}
	case args[0] == "show" && len(args) == 2:
		for _, state := range []string{"pending", "done"} {
			if e, ok := sqRead(state, args[1]); ok {
				raw, _ := json.MarshalIndent(e, "", "  ")
				fmt.Println(string(raw))
				return
			}
		}
		fatal(fmt.Errorf("no queue entry %s", args[1]))
	case args[0] == "drop" && len(args) == 2:
		if _, ok := sqRead("pending", args[1]); !ok {
			fatal(fmt.Errorf("no pending queue entry %s", args[1]))
		}
		sqFinish(args[1], "dropped", "dropped with captain queue drop")
		fmt.Printf("dropped %s; the running brain drops it from the inbox at its next queue check\n", args[1])
	default:
		fatal(fmt.Errorf("usage: captain queue [list] | show ID | drop ID"))
	}
}
