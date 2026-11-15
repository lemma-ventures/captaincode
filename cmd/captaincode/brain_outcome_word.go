package main

// Q13 (ROADMAP M5.1): outcome words in the TUI. /accept, /reject,
// /correction and /regressed act on the last task this session started.
//
// The plugin sends the word out of band the moment it is typed (the /btw
// pattern), so it never queues behind a running turn. A copy that reaches
// the brain as a turn anyway (an older plugin, an API client) is answered
// in one line by handleOutcomeWord, never run as a task. The reviewer is
// the OS user at this TUI, stored only in the local outcome record.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

var outcomeWordRe = regexp.MustCompile(`(?i)^\s*/(accept|reject|correction|regressed)\b[\s:]*([\s\S]*)$`)

// tuiReviewer names who typed the verdict: the OS user at this TUI.
func tuiReviewer() string {
	who := os.Getenv("USER")
	if who == "" {
		who = "unknown"
	}
	return "tui:" + who
}

// noteTurnTask remembers the newest task a session (its X-Captain-Turn id)
// started, so an outcome word from that session acts on that task.
func (b *brain) noteTurnTask(turn, taskID string) {
	if turn == "" || taskID == "" {
		return
	}
	b.otmu.Lock()
	defer b.otmu.Unlock()
	if b.lastTaskByTurn == nil {
		b.lastTaskByTurn = map[string]string{}
	}
	b.lastTaskByTurn[turn] = taskID
}

// turnTask returns the newest task the session started.
func (b *brain) turnTask(turn string) (string, bool) {
	if turn == "" {
		return "", false
	}
	b.otmu.Lock()
	defer b.otmu.Unlock()
	id := b.lastTaskByTurn[turn]
	return id, id != ""
}

// parseOutcomeWord splits one outcome word into its action and argument.
// ok is false when the text is not an outcome word.
func parseOutcomeWord(raw string) (word, arg string, ok bool) {
	m := outcomeWordRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", false
	}
	return strings.ToLower(m[1]), strings.TrimSpace(m[2]), true
}

// applyOutcomeWord records one outcome word against a task and returns the
// one-line answer. The caller holds b.mu: it guards the ledger.
func (b *brain) applyOutcomeWord(taskID, word, arg string) (string, error) {
	reviewer := tuiReviewer()
	switch word {
	case "accept":
		if err := b.ledger.RecordTaskReview(taskID, captaincode.ReviewAccept, reviewer, arg, false); err != nil {
			return "", err
		}
	case "reject":
		if arg == "" {
			return "", fmt.Errorf("/reject needs a reason: /reject <reason>")
		}
		if err := b.ledger.RecordTaskReview(taskID, captaincode.ReviewReject, reviewer, arg, false); err != nil {
			return "", err
		}
	case "correction":
		fields := strings.Fields(arg)
		if len(fields) == 0 {
			return "", fmt.Errorf("/correction needs minutes: /correction <minutes> [reason]")
		}
		minutes, err := strconv.Atoi(fields[0])
		if err != nil || minutes <= 0 {
			return "", fmt.Errorf("/correction minutes must be a positive number, got %q", fields[0])
		}
		b.ledger.RecordCorrection(taskID, minutes, strings.TrimSpace(strings.TrimPrefix(arg, fields[0])))
	case "regressed":
		if arg == "" {
			return "", fmt.Errorf("/regressed needs a reason: /regressed <reason>")
		}
		b.ledger.RecordRegression(taskID, arg, reviewer)
	default:
		return "", fmt.Errorf("unknown outcome word %q", word)
	}
	notices := b.ledger.TakeOutcomeNotices()
	go captaincode.PublishOutcomeNotices(notices)
	if err := b.ledger.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "captain brain: save outcome word: %v\n", err)
	}
	status := "pending"
	if o := b.ledger.OutcomeFor(taskID); o != nil {
		status = string(o.Status)
	}
	return fmt.Sprintf("%s recorded: task %s is %s", word, taskID, status), nil
}

// outcomeWordHTTP is the plugin's out-of-band call: POST /v1/outcome/word
// with the session's turn id, the word and its argument.
func (b *brain) outcomeWordHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	turn := r.URL.Query().Get("turn")
	if !turnIDPattern.MatchString(turn) {
		writeErr(w, http.StatusBadRequest, "turn required")
		return
	}
	var body struct {
		Word string `json:"word"`
		Text string `json:"text"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "expected word and text")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "expected one request object")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	taskID, ok := b.turnTask(turn)
	if !ok {
		writeErr(w, http.StatusNotFound, "no task started in this session yet")
		return
	}
	msg, err := b.applyOutcomeWord(taskID, strings.ToLower(strings.TrimSpace(body.Word)), strings.TrimSpace(body.Text))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": msg, "task": taskID})
}

// outcomeLastHTTP serves the sidebar its session's last task and outcome
// status: GET /v1/outcome/last?turn=<session id>.
func (b *brain) outcomeLastHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	turn := r.URL.Query().Get("turn")
	if !turnIDPattern.MatchString(turn) {
		writeErr(w, http.StatusBadRequest, "turn required")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	taskID, ok := b.turnTask(turn)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no task in this session yet"})
		return
	}
	out := map[string]any{"task": taskID, "status": "pending"}
	if o := b.ledger.OutcomeFor(taskID); o != nil {
		out["status"] = string(o.Status)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOutcomeWord answers the copy of an outcome word that reaches the
// brain as a turn: it acts on the session's last task and answers in one
// line. An outcome word is never a task, so this always returns true when
// the text is one.
func (b *brain) handleOutcomeWord(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	word, arg, ok := parseOutcomeWord(raw)
	if !ok {
		return false
	}
	emit, _, finish := newCompletionWriter(w, req, "captain")
	defer finish()
	taskID, has := b.turnTask(req.turn)
	if !has {
		emit(fmt.Sprintf("**/%s** - no task started in this session yet, so nothing was recorded.", word))
		finish()
		return true
	}
	b.mu.Lock()
	msg, err := b.applyOutcomeWord(taskID, word, arg)
	b.mu.Unlock()
	if err != nil {
		emit(fmt.Sprintf("**/%s** - %s", word, err.Error()))
		finish()
		return true
	}
	emit(fmt.Sprintf("**/%s** - %s", word, msg))
	finish()
	return true
}
