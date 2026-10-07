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
	// Findings is what the injection screen saw (captaincode.ScanInjection);
	// Dir is the folder a held item waits for.
	Findings []captaincode.Finding `json:"findings,omitempty"`
	Dir      string                `json:"dir,omitempty"`
	// Screening: the injection judge has not answered yet; the sidebar does
	// not get the item until it has (judgeInbox).
	Screening bool `json:"-"`
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
	// origins remembers who sent each handed text, so the turn it becomes
	// carries its provenance into the worker prompt (sentTurnContract).
	origins map[string]string
	// held are messages the injection screen stopped, waiting for the user
	// (captain inbox release|drop); notify lists the ones a sidebar has not
	// been told about yet.
	held   []inboxItem
	notify map[string][]inboxItem
}

// hold keeps a flagged message for the user instead of delivering it.
func (ib *inbox) hold(it inboxItem) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	ib.held = append(ib.held, it)
	if ib.notify == nil {
		ib.notify = map[string][]inboxItem{}
	}
	ib.notify[it.Dir] = append(ib.notify[it.Dir], it)
}

// heldFor lists held messages (dir "" = every folder).
func (ib *inbox) heldFor(dir string) []inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	var out []inboxItem
	for _, it := range ib.held {
		if dir == "" || it.Dir == dir {
			out = append(out, it)
		}
	}
	return out
}

// takeNotify hands a sidebar the held messages it has not shown yet.
func (ib *inbox) takeNotify(dir string) []inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	out := ib.notify[dir]
	delete(ib.notify, dir)
	return out
}

// unhold removes a held message and returns it.
func (ib *inbox) unhold(id string) (inboxItem, bool) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	for i, it := range ib.held {
		if it.ID == id {
			ib.held = append(ib.held[:i:i], ib.held[i+1:]...)
			return it, true
		}
	}
	return inboxItem{}, false
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
func (ib *inbox) noteTurn(dir, text string) (sentBy string, sent bool) {
	dir = filepath.Clean(dir)
	ib.mu.Lock()
	defer ib.mu.Unlock()
	text = strings.TrimSpace(text)
	for i, h := range ib.handed[dir] {
		if h == text {
			ib.handed[dir] = append(ib.handed[dir][:i:i], ib.handed[dir][i+1:]...)
			sentBy = ib.origins[text]
			delete(ib.origins, text)
			if sentBy == "" {
				sentBy = "captain send (no sender named)"
			}
			return sentBy, true
		}
	}
	delete(ib.sent, dir)
	return "", false
}

const inboxTTL = 24 * time.Hour

func (ib *inbox) push(dir, text, leg, from, session, origin string) inboxItem {
	return ib.pushScreening(dir, text, leg, from, session, origin, false)
}

func (ib *inbox) pushScreening(dir, text, leg, from, session, origin string, screening bool) inboxItem {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if ib.items == nil {
		ib.items = map[string][]inboxItem{}
	}
	ib.seq++
	it := inboxItem{ID: fmt.Sprintf("in_%d_%d", time.Now().Unix(), ib.seq), Text: text, Leg: leg, From: from, At: time.Now(),
		Session: session, Origin: origin, Screening: screening, Dir: dir}
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
		if it.Screening {
			ib.items[dir] = append(ib.items[dir], it)
			continue
		}
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
			if ib.origins == nil {
				ib.origins = map[string]string{}
			}
			ib.origins[strings.TrimSpace(it.Text)] = it.Origin
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
			// Only a reply token proves where a message came from; a name
			// and a folder are what the sender says about itself.
			origin = strings.TrimSpace(orDash(req.From) + " in " + filepath.Base(s) + " (another folder, unverified)")
		}
		// The injection screen (captaincode/injection.go) reads the text as
		// sent; what is delivered has hidden characters removed and forged
		// captain markers neutralized. A high finding holds the message for
		// the user; a medium one is delivered with the warning attached.
		findings := captaincode.ScanInjection(req.Text)
		var removed, forged int
		req.Text, removed, forged = captaincode.SanitizeSent(req.Text)
		if removed+forged > 0 {
			captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: "sanitized", From: orDash(origin), Dir: dir,
				Detail: fmt.Sprintf("%d hidden characters removed, %d forged markers neutralized", removed, forged)})
		}
		if captaincode.MaxSeverity(findings) == captaincode.SevHigh && os.Getenv("CAPTAIN_INBOX_HOLD") != "0" {
			b.inbox.mu.Lock()
			b.inbox.seq++
			id := fmt.Sprintf("in_%d_%d", time.Now().Unix(), b.inbox.seq)
			b.inbox.mu.Unlock()
			it := inboxItem{ID: id, Text: req.Text, Leg: leg, From: strings.TrimSpace(req.From), At: time.Now(),
				Session: session, Origin: origin, Findings: findings, Dir: dir}
			b.inbox.hold(it)
			captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: "held", From: orDash(origin), Dir: dir, Findings: findings, Detail: promptPeek(req.Text)})
			fmt.Printf("captain brain: inbox - HELD %s for %s (%s): %s\n", id, filepath.Base(dir), captaincode.FindingsLine(findings), promptPeek(req.Text))
			b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "inbox", Model: "held",
				Text: "held a sent prompt (possible prompt injection: " + captaincode.FindingsLine(findings) + ") - `captain inbox` to review"})
			writeJSON(w, 202, map[string]any{"ok": true, "held": true, "id": id, "findings": findings,
				"note": "held for the user: possible prompt injection. They review it with `captain inbox`."})
			return
		}
		message := req.Text // what the judge reads: cleaned, before the "sent by" footer
		if origin == "" && strings.TrimSpace(req.From) != "" {
			origin = strings.TrimSpace(req.From)
		}
		if len(findings) > 0 {
			kinds := make([]string, 0, len(findings))
			for _, f := range findings {
				kinds = append(kinds, f.Kind)
			}
			origin = strings.TrimSpace(orDash(origin) + " · flagged: " + strings.Join(kinds, ", "))
			captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: "flagged", From: origin, Dir: dir, Findings: findings, Detail: promptPeek(req.Text)})
		}
		if origin != "" {
			req.Text += "\n\n- sent by " + origin
		}
		if !b.inbox.admit(dir) {
			writeErr(w, 429, fmt.Sprintf("%d prompts were sent to %s since the user last typed (CAPTAIN_INBOX_QUOTA); more are accepted once they type", inboxQuota(), filepath.Base(dir)))
			return
		}
		judged := judgeEnabled()
		if !judged {
			it0 := strings.LastIndex(req.Text, "\n\n- sent by ")
			note := " · not screened by a model (CAPTAIN_INBOX_JUDGE=0)"
			if it0 >= 0 {
				req.Text += note
			} else {
				req.Text += "\n\n- sent by " + orDash(origin) + note
			}
		}
		it := b.inbox.pushScreening(dir, req.Text, leg, strings.TrimSpace(req.From), session, origin, judged)
		if judged {
			// A model reads what the patterns passed or only flagged
			// (captaincode/injection_judge.go); the TUI gets the message once
			// it has answered.
			go b.judgeInbox(dir, it.ID, message, findings)
			writeJSON(w, 200, map[string]any{"ok": true, "id": it.ID, "pending": b.inbox.pending(dir), "screening": true})
			return
		}
		fmt.Printf("captain brain: inbox - prompt queued for %s (%s): %s\n", filepath.Base(dir), orDash(leg), promptPeek(req.Text))
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "inbox", Model: orDash(leg), Text: "queued for the TUI: " + promptPeek(req.Text)})
		writeJSON(w, 200, map[string]any{"ok": true, "id": it.ID, "pending": b.inbox.pending(dir)})
	case http.MethodGet:
		items := b.inbox.take(dir, strings.TrimSpace(r.URL.Query().Get("session")))
		if len(items) > 0 {
			fmt.Printf("captain brain: inbox - %d prompt(s) handed to the TUI in %s\n", len(items), filepath.Base(dir))
		}
		out := map[string]any{"items": items}
		if held := b.inbox.takeNotify(dir); len(held) > 0 {
			out["held"] = held // the sidebar says so; held items are never submitted
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "GET or POST")
	}
}

// inboxHeldHTTP: GET /v1/inbox/held[?cwd=] lists held messages; POST
// /v1/inbox/held {"id":"…","action":"release"|"drop"} releases one into its
// folder's inbox (the user's decision: no quota spent) or drops it.
func (b *brain) inboxHeldHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		dir, _ := workspaceFilter(r)
		writeJSON(w, 200, map[string]any{"held": b.inbox.heldFor(dir)})
	case http.MethodPost:
		var req struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "release" && req.Action != "drop" {
			writeErr(w, 400, `action is "release" or "drop"`)
			return
		}
		it, ok := b.inbox.unhold(strings.TrimSpace(req.ID))
		if !ok {
			writeErr(w, 404, "no held message "+req.ID)
			return
		}
		captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: req.Action + "d", From: it.Origin, Dir: it.Dir, Findings: it.Findings, Detail: it.ID})
		if req.Action == "drop" {
			fmt.Printf("captain brain: inbox - dropped held %s\n", it.ID)
			writeJSON(w, 200, map[string]any{"ok": true, "dropped": it.ID})
			return
		}
		text := it.Text + "\n\n- sent by " + orDash(it.Origin) + " · released by the user after the injection screen flagged: " + captaincode.FindingsLine(it.Findings)
		out := b.inbox.push(it.Dir, text, it.Leg, it.From, it.Session, orDash(it.Origin)+" (released by the user)")
		fmt.Printf("captain brain: inbox - released held %s into %s\n", it.ID, filepath.Base(it.Dir))
		writeJSON(w, 200, map[string]any{"ok": true, "released": out.ID, "dir": it.Dir})
	default:
		writeErr(w, 405, "GET or POST")
	}
}

// judgeEnabled: CAPTAIN_INBOX_JUDGE=0 turns the model's second opinion off.
func judgeEnabled() bool { return os.Getenv("CAPTAIN_INBOX_JUDGE") != "0" }

// judgeLegs is the panel: CAPTAIN_INBOX_JUDGE_LEGS (comma-separated), else
// CAPTAIN_INBOX_JUDGE_LEG or the compaction leg, plus a leg of another
// family (captaincode.JudgePanel).
func judgeLegs() []captaincode.Leg {
	first := compactLeg()
	if l := strings.TrimSpace(os.Getenv("CAPTAIN_INBOX_JUDGE_LEG")); l != "" {
		first = captaincode.Leg(l)
	}
	return captaincode.JudgePanel(first, os.Getenv("CAPTAIN_INBOX_JUDGE_LEGS"))
}

// runJudge asks one judge about one message; ok is false when it could not
// answer (an error, a timeout, or a reply that is not the verdict).
func (b *brain) runJudge(leg captaincode.Leg, message string) (captaincode.JudgeVerdict, bool) {
	if b.judgeFn != nil {
		return b.judgeFn(leg, message)
	}
	key := captaincode.NewJudgeNonce()
	d := captaincode.NewDispatcher(opencodePort)
	d.Title = "injection-judge"
	d.NoTools = true
	d.Timeout = 45 * time.Second
	res, err := d.Run(leg, captaincode.JudgePrompt(message, captaincode.NewJudgeNonce(), key))
	if err != nil {
		return captaincode.JudgeVerdict{}, false
	}
	return captaincode.ParseJudgeVerdict(res.Text, key)
}

// judgePanel asks every judge at once and combines their answers
// (captaincode.DecideJudges: fail-closed).
func (b *brain) judgePanel(message string) captaincode.JudgeOutcome {
	legs := judgeLegs()
	verdicts := make([]captaincode.JudgeVerdict, len(legs))
	answered := make([]bool, len(legs))
	var wg sync.WaitGroup
	for i, l := range legs {
		wg.Add(1)
		go func(i int, l captaincode.Leg) {
			defer wg.Done()
			verdicts[i], answered[i] = b.runJudge(l, message)
		}(i, l)
	}
	wg.Wait()
	return captaincode.DecideJudges(verdicts, answered)
}

// judgeInbox settles a screening message: held when the panel says so - an
// injection vote, a judge steered by the message, or no judge able to answer
// - else released to the sidebar.
func (b *brain) judgeInbox(dir, id, message string, findings []captaincode.Finding) {
	o := b.judgePanel(message)
	it, found := b.inbox.settle(dir, id, o.Hold)
	if !found {
		return
	}
	if o.Hold {
		it.Findings = append(it.Findings, captaincode.Finding{Kind: "model judge", Severity: captaincode.SevHigh, Level: "high", Excerpt: o.Why})
		b.inbox.hold(it)
		captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: "held", From: it.Origin, Dir: dir, Findings: it.Findings, Detail: promptPeek(message)})
		fmt.Printf("captain brain: inbox - HELD %s after screening (%s)\n", id, o.Why)
		b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "inbox", Model: "held",
			Text: "held a sent prompt (" + o.Why + ") - `captain inbox` to review"})
		return
	}
	captaincode.AppendInjectionLog(captaincode.InjectionEvent{Channel: "inbox", Action: "screened", From: it.Origin, Dir: dir, Findings: findings,
		Detail: fmt.Sprintf("judged benign by %d of %d judges", o.Answered, o.Asked)})
}

// settle ends a message's screening: hold removes it from the queue and
// returns it, otherwise it becomes deliverable where it is.
func (ib *inbox) settle(dir, id string, hold bool) (inboxItem, bool) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	for i, it := range ib.items[dir] {
		if it.ID != id {
			continue
		}
		if hold {
			ib.items[dir] = append(ib.items[dir][:i:i], ib.items[dir][i+1:]...)
			return it, true
		}
		ib.items[dir][i].Screening = false
		return it, true
	}
	return inboxItem{}, false
}
