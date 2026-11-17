package captaincode

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamHub stands in for the reviewed hub's two brain paths: GET
// /api/v1/link/stream and POST /api/v1/link/messages. It keeps the hub's own
// checks - verified brain signature, strict payloads, the stream's folder
// rules, strictly rising sequence numbers and the signed links - and each
// test turns one check into a failure.
type streamHub struct {
	hubKey   ed25519.PrivateKey
	brainKey ed25519.PublicKey
	machine  string

	// knobs
	pingEvery       time.Duration
	closeAfterPings int    // 0 keeps the stream open until the test ends
	openStatus      int    // a non-zero status refuses the stream request
	failPosts       int    // refuse this many presence posts with 409
	stickyPost      int    // refuse every presence post with this status
	ackKind         string // ack with another message type
	ackWrong        bool   // ack another message id and sequence
	tamper          func(LinkEnvelope) LinkEnvelope
	sendKind        string // send one extra event of this kind after ready
	answer          *LinkDecisionAnswer
	prompt          *LinkPrompt
	repeatReady     bool   // send the ready frame twice under one id
	eventMachine    string // sign stream events for another machine

	mu        sync.Mutex
	opens     int
	streamIDs []string
	posts     []streamPost
	cards     []LinkDecisionCard
	summaries []LinkTurnSummary
	receipts  []string
	promptAck []string
	current   string
	seen      map[string]bool
	seq       int64
}

// streamPost is one presence snapshot the stub accepted.
type streamPost struct {
	StreamID     string
	Sequence     int64
	BrainVersion string
	Folders      []LinkPresenceFolder
	At           time.Time
}

func newStreamHub(machine string) *streamHub {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return &streamHub{
		hubKey:     key,
		machine:    machine,
		pingEvery:  25 * time.Millisecond,
		seen:       map[string]bool{},
		openStatus: 0,
	}
}

func (h *streamHub) sign(kind string, payload any) LinkEnvelope {
	machine := h.machine
	if h.eventMachine != "" {
		machine = h.eventMachine
	}
	e, err := LinkSign(h.hubKey, machine, linkHubDirection, kind, payload, time.Now())
	if err != nil {
		panic(err)
	}
	if h.tamper != nil {
		e = h.tamper(e)
	}
	return e
}

func (h *streamHub) writeEnvelope(w http.ResponseWriter, status int, e LinkEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

func (h *streamHub) hubError(w http.ResponseWriter, machine string, status int) {
	if machine == "" {
		machine = h.machine
	}
	h.writeEnvelope(w, status, h.sign("link.error", map[string]int{"status": status}))
}

func (h *streamHub) readEnvelope(w http.ResponseWriter, r *http.Request) (LinkEnvelope, bool) {
	var e LinkEnvelope
	if r.Method == http.MethodGet {
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Captain-Link"))
		if err != nil || len(raw) > linkMaxBody {
			h.hubError(w, "", http.StatusBadRequest)
			return e, false
		}
		if LinkDecodeEnvelope(raw, &e) != nil {
			h.hubError(w, "", http.StatusBadRequest)
			return e, false
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, linkMaxBody)
		raw := make([]byte, 0, 4096)
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			raw = append(raw, buf[:n]...)
			if err != nil {
				break
			}
			if len(raw) > linkMaxBody {
				h.hubError(w, "", http.StatusBadRequest)
				return e, false
			}
		}
		if LinkDecodeEnvelope(raw, &e) != nil {
			h.hubError(w, "", http.StatusBadRequest)
			return e, false
		}
	}
	if e.Direction != linkBrainDirection || LinkVerify(h.brainKey, e, linkBrainDirection, time.Now()) != nil {
		h.hubError(w, e.MachineID, http.StatusUnauthorized)
		return e, false
	}
	if e.MachineID != h.machine {
		h.hubError(w, e.MachineID, http.StatusForbidden)
		return e, false
	}
	h.mu.Lock()
	repeat := h.seen[e.ID]
	h.seen[e.ID] = true
	h.mu.Unlock()
	if repeat {
		h.hubError(w, e.MachineID, http.StatusConflict)
		return e, false
	}
	return e, true
}

// signedFrame builds one stream event in the protocol's own shape.
func (h *streamHub) signedFrame(kind string, payload any) string {
	e := h.sign(kind, payload)
	raw, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("event: link\nid: %s\ndata: %s\n\n", e.ID, raw)
}

// frame writes one signed stream event.
func (h *streamHub) frame(w http.ResponseWriter, flusher http.Flusher, kind string, payload any) bool {
	if _, err := io.WriteString(w, h.signedFrame(kind, payload)); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (h *streamHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/link/stream":
		h.serveStream(w, r)
	case "/api/v1/link/messages":
		h.serveMessages(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *streamHub) serveStream(w http.ResponseWriter, r *http.Request) {
	e, ok := h.readEnvelope(w, r)
	if !ok {
		return
	}
	if e.Type != "link.open" || e.Payload != "e30" {
		h.hubError(w, e.MachineID, http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	if h.openStatus != 0 {
		status := h.openStatus
		h.mu.Unlock()
		h.hubError(w, e.MachineID, status)
		return
	}
	streamID := streamRandomID()
	h.opens++
	h.current = streamID
	h.streamIDs = append(h.streamIDs, streamID)
	h.seq = 0
	pings := 0
	h.mu.Unlock()

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.hubError(w, e.MachineID, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ready := h.signedFrame("link.ready", map[string]string{"stream_id": streamID})
	if _, err := io.WriteString(w, ready); err != nil {
		return
	}
	flusher.Flush()
	if h.repeatReady {
		// The identical frame again: one message id on two events, which
		// the brain must read as a replay.
		if _, err := io.WriteString(w, ready); err != nil {
			return
		}
		flusher.Flush()
	}
	if h.sendKind != "" {
		if _, err := io.WriteString(w, h.signedFrame(h.sendKind, map[string]string{"stream_id": streamID})); err != nil {
			return
		}
		flusher.Flush()
	}
	if h.answer != nil {
		answer := *h.answer
		answer.StreamID = streamID
		if _, err := io.WriteString(w, h.signedFrame("decision.answer", answer)); err != nil {
			return
		}
		flusher.Flush()
	}
	if h.prompt != nil {
		prompt := *h.prompt
		prompt.StreamID = streamID
		if _, err := io.WriteString(w, h.signedFrame("prompt", prompt)); err != nil {
			return
		}
		flusher.Flush()
	}
	ticker := time.NewTicker(h.pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			pings++
			if h.closeAfterPings > 0 && pings > h.closeAfterPings {
				return
			}
			if !h.frame(w, flusher, "link.ping", map[string]string{"stream_id": streamID}) {
				return
			}
		}
	}
}

// serveMessages is the presence and card path with the hub's own checks.
func (h *streamHub) serveMessages(w http.ResponseWriter, r *http.Request) {
	e, ok := h.readEnvelope(w, r)
	if !ok {
		return
	}
	if e.Type == "decision.card" {
		h.serveCard(w, e)
		return
	}
	if e.Type == "decision.receipt" {
		var receipt struct {
			AnswerID   string `json:"answer_id"`
			DecisionID string `json:"decision_id"`
		}
		if LinkDecodePayload(e, &receipt) != nil || h.answer == nil || receipt.AnswerID != h.answer.AnswerID || receipt.DecisionID != h.answer.DecisionID {
			h.hubError(w, e.MachineID, http.StatusConflict)
			return
		}
		h.mu.Lock()
		h.receipts = append(h.receipts, receipt.AnswerID)
		h.mu.Unlock()
		h.writeEnvelope(w, http.StatusOK, h.sign("decision.answered", receipt))
		return
	}
	if e.Type == "prompt.receipt" {
		var receipt struct {
			PromptID string `json:"prompt_id"`
			Status   string `json:"status"`
			Code     string `json:"code"`
		}
		if LinkDecodePayload(e, &receipt) != nil || h.prompt == nil || receipt.PromptID != h.prompt.PromptID || (receipt.Status != "accepted" && receipt.Status != "refused") {
			h.hubError(w, e.MachineID, http.StatusConflict)
			return
		}
		h.mu.Lock()
		h.promptAck = append(h.promptAck, receipt.PromptID+":"+receipt.Status+":"+receipt.Code)
		h.mu.Unlock()
		h.writeEnvelope(w, http.StatusOK, h.sign("prompt.acknowledged", map[string]string{"prompt_id": receipt.PromptID, "status": receipt.Status}))
		return
	}
	if e.Type == "turn.summary" {
		var summary LinkTurnSummary
		if LinkDecodePayload(e, &summary) != nil || LinkTurnSummaryValid(summary) != nil {
			h.hubError(w, e.MachineID, http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.summaries = append(h.summaries, summary)
		h.mu.Unlock()
		h.writeEnvelope(w, http.StatusOK, h.sign("turn.accepted", map[string]string{"message_id": e.ID, "prompt_id": summary.PromptID}))
		return
	}
	if e.Type != "presence" {
		h.hubError(w, e.MachineID, http.StatusBadRequest)
		return
	}
	var p struct {
		StreamID     string               `json:"stream_id"`
		Sequence     int64                `json:"sequence"`
		BrainVersion string               `json:"brain_version"`
		Folders      []LinkPresenceFolder `json:"folders"`
	}
	if err := LinkDecodePayload(e, &p); err != nil ||
		!linkIdentifier.MatchString(p.StreamID) ||
		p.Sequence <= 0 || len(p.Folders) > linkPresenceLimit ||
		!LinkPresenceLabel(p.BrainVersion) {
		h.hubError(w, e.MachineID, http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, f := range p.Folders {
		if !LinkPresenceLabel(f.Label) || seen[f.Label] ||
			(f.ProjectID != "" && !linkIdentifier.MatchString(f.ProjectID)) {
			h.hubError(w, e.MachineID, http.StatusBadRequest)
			return
		}
		seen[f.Label] = true
	}
	h.mu.Lock()
	if h.stickyPost != 0 {
		status := h.stickyPost
		h.mu.Unlock()
		h.hubError(w, e.MachineID, status)
		return
	}
	if h.failPosts > 0 {
		h.failPosts--
		h.mu.Unlock()
		h.hubError(w, e.MachineID, http.StatusConflict)
		return
	}
	if p.StreamID != h.current || p.Sequence <= h.seq {
		h.mu.Unlock()
		h.hubError(w, e.MachineID, http.StatusConflict)
		return
	}
	h.seq = p.Sequence
	h.posts = append(h.posts, streamPost{
		StreamID:     p.StreamID,
		Sequence:     p.Sequence,
		BrainVersion: p.BrainVersion,
		Folders:      append([]LinkPresenceFolder(nil), p.Folders...),
		At:           time.Now(),
	})
	kind := "presence.accepted"
	if h.ackKind != "" {
		kind = h.ackKind
	}
	ack := map[string]any{"message_id": e.ID, "sequence": p.Sequence}
	if h.ackWrong {
		ack["message_id"] = streamRandomID()
		ack["sequence"] = p.Sequence + 7
	}
	h.mu.Unlock()
	h.writeEnvelope(w, http.StatusOK, h.sign(kind, ack))
}

func streamRandomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

// serveCard is the decision-card path with the hub's own checks: a strict
// payload, the card rules, and a signed acknowledgement that names the
// message and the decision.
func (h *streamHub) serveCard(w http.ResponseWriter, e LinkEnvelope) {
	var c LinkDecisionCard
	if err := LinkDecodePayload(e, &c); err != nil || LinkDecisionValid(c) != nil ||
		c.PostedAt < 946684800 || c.PostedAt > time.Now().Add(5*time.Minute).Unix() {
		h.hubError(w, e.MachineID, http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.cards = append(h.cards, c)
	ack := map[string]any{"message_id": e.ID, "decision_id": c.DecisionID}
	if h.ackKind == "card.wrong" {
		ack["decision_id"] = "dq-261117-000000"
	}
	h.mu.Unlock()
	h.writeEnvelope(w, http.StatusOK, h.sign("decision.accepted", ack))
}

// streamCards is a card provider the tests change while a stream runs.
type streamCards struct {
	mu    sync.Mutex
	cards []LinkDecisionCard
	err   error
	calls int
}

func (c *streamCards) set(cards ...LinkDecisionCard) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cards = append([]LinkDecisionCard(nil), cards...)
}

func (c *streamCards) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func (c *streamCards) read() ([]LinkDecisionCard, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return append([]LinkDecisionCard(nil), c.cards...), nil
}

func (c *streamCards) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// testCard is one valid card.
func testCard(id string) LinkDecisionCard {
	return LinkDecisionCard{
		DecisionID: id,
		Title:      "Trial host for the first cloud worker",
		Question:   "Where should the first Linux host run, and at what spend cap?",
		Options: []LinkDecisionOption{
			{ID: "a", Label: "AWS"},
			{ID: "b", Label: "A second provider"},
		},
		Recommend: "a",
		Record:    "docs/fleet.md",
		PostedAt:  time.Now().Unix(),
		Status:    "open",
	}
}

// streamFolders is a folder provider the tests change while a stream runs.
type streamFolders struct {
	mu      sync.Mutex
	folders []LinkPresenceFolder
	err     error
	calls   int
}

func (f *streamFolders) set(folders ...LinkPresenceFolder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders = append([]LinkPresenceFolder(nil), folders...)
}

func (f *streamFolders) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *streamFolders) read() ([]LinkPresenceFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]LinkPresenceFolder(nil), f.folders...), nil
}

func (f *streamFolders) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// streamFixture gives a test a machine, a stub hub over verified TLS and the
// options the brain would build from local state. The client is the shipped
// one: system roots plus the hub's own certificate file, no switch to turn
// verification off.
type streamFixture struct {
	hub      *streamHub
	server   *httptest.Server
	ca       string
	key      ed25519.PrivateKey
	machine  string
	folders  *streamFolders
	logs     []string
	logMu    sync.Mutex
	interval time.Duration
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	fx := &streamFixture{
		hub:      newStreamHub("machine-stream-test"),
		key:      key,
		machine:  "machine-stream-test",
		folders:  &streamFolders{},
		interval: 150 * time.Millisecond,
	}
	fx.hub.brainKey = key.Public().(ed25519.PublicKey)
	fx.server = httptest.NewTLSServer(fx.hub)
	t.Cleanup(fx.server.Close)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fx.server.Certificate().Raw})
	fx.ca = filepath.Join(t.TempDir(), "hub-ca.pem")
	if err := os.WriteFile(fx.ca, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	fx.folders.set(
		LinkPresenceFolder{Label: "oak", TUIOpen: true},
		LinkPresenceFolder{Label: "elm"},
	)
	return fx
}

func (fx *streamFixture) options() LinkStreamOptions {
	return LinkStreamOptions{
		Hub: LinkHub{
			URL:        fx.server.URL,
			PublicKey:  base64.RawURLEncoding.EncodeToString(fx.hub.hubKey.Public().(ed25519.PublicKey)),
			CAFile:     fx.ca,
			ApprovedAt: time.Now().Unix(),
		},
		MachineID:    fx.machine,
		Key:          fx.key,
		BrainVersion: "captain 1234abc",
		Folders:      fx.folders.read,
		Interval:     fx.interval,
		Policy:       LinkStreamPolicy{IdlePoll: 20 * time.Millisecond, TrustPause: 60 * time.Millisecond, RetryBase: 10 * time.Millisecond, RetryMax: 60 * time.Millisecond},
	}
}

func (fx *streamFixture) logLines() []string {
	fx.logMu.Lock()
	defer fx.logMu.Unlock()
	return append([]string(nil), fx.logs...)
}

// runLoop starts the loop and hands back a channel with its report.
func (fx *streamFixture) runLoop(ctx context.Context) <-chan struct {
	report LinkStreamReport
	err    error
} {
	out := make(chan struct {
		report LinkStreamReport
		err    error
	}, 1)
	opts := fx.options()
	go func() {
		report, err := LinkStreamLoop(ctx, opts)
		out <- struct {
			report LinkStreamReport
			err    error
		}{report, err}
	}()
	return out
}

func (fx *streamFixture) posts() []streamPost {
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	return append([]streamPost(nil), fx.hub.posts...)
}

func (fx *streamFixture) opens() int {
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	return fx.hub.opens
}

func waitForPosts(t *testing.T, fx *streamFixture, n int, d time.Duration) []streamPost {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		posts := fx.posts()
		if len(posts) >= n {
			return posts
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d presence snapshots in %s, got %d", n, d, len(posts))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForOpens(t *testing.T, fx *streamFixture, n int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		opens := fx.opens()
		if opens >= n {
			return opens
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d stream opens in %s, got %d", n, d, opens)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stopLoop(t *testing.T, fx *streamFixture, cancel context.CancelFunc, result <-chan struct {
	report LinkStreamReport
	err    error
}) (LinkStreamReport, error) {
	t.Helper()
	cancel()
	select {
	case got := <-result:
		return got.report, got.err
	case <-time.After(3 * time.Second):
		t.Fatal("the stream loop did not stop with its context")
		return LinkStreamReport{}, nil
	}
}

func TestLinkStreamReportsEveryLinkedFolder(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	posts := waitForPosts(t, fx, 1, 2*time.Second)
	stopLoop(t, fx, cancel, result)

	first := posts[0]
	if first.StreamID == "" || first.Sequence != 1 || first.BrainVersion != "captain 1234abc" {
		t.Fatalf("first snapshot = %+v", first)
	}
	got := map[string]bool{}
	for _, f := range first.Folders {
		got[f.Label] = f.TUIOpen
	}
	if len(got) != 2 || !got["oak"] || got["elm"] {
		t.Fatalf("folders = %+v", first.Folders)
	}
	if strings.Contains(first.StreamID, "/") {
		t.Fatalf("stream id %q looks like a path", first.StreamID)
	}
}

func TestLinkStreamSnapshotsOnChangeAndOnInterval(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	posts := waitForPosts(t, fx, 1, 2*time.Second)
	sentAt := posts[0].At

	// A changed folder set goes up on its own, before the interval.
	fx.folders.set(LinkPresenceFolder{Label: "oak", TUIOpen: false}, LinkPresenceFolder{Label: "elm"})
	changed := waitForPosts(t, fx, 2, time.Second)
	if changed[1].At.After(sentAt.Add(fx.interval)) {
		t.Fatalf("a changed snapshot waited %s; interval is %s", changed[1].At.Sub(sentAt), fx.interval)
	}
	secondAt := changed[1].At

	// An unchanged set waits for its interval instead of beating early.
	heartbeats := waitForPosts(t, fx, 3, 2*time.Second)
	gap := heartbeats[2].At.Sub(secondAt)
	if gap < fx.interval/2 {
		t.Fatalf("the heartbeat arrived after %s; the interval is %s", gap, fx.interval)
	}
	stopLoop(t, fx, cancel, result)

	sequence := int64(0)
	stream := ""
	for i, p := range fx.posts() {
		if p.Sequence <= sequence {
			t.Fatalf("post %d repeated the sequence %d", i, p.Sequence)
		}
		if stream != "" && p.StreamID != stream {
			t.Fatalf("post %d changed the stream mid-session", i)
		}
		sequence, stream = p.Sequence, p.StreamID
	}
}

func TestLinkStreamFolderlessSnapshotEndsTheRun(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	waitForPosts(t, fx, 1, 2*time.Second)
	fx.folders.set()
	posts := waitForPosts(t, fx, 2, 2*time.Second)
	if len(posts[1].Folders) != 0 {
		t.Fatalf("the empty snapshot carried %+v", posts[1].Folders)
	}
	select {
	case got := <-result:
		if !strings.Contains(fmt.Sprint(got.err), "no folder is left") {
			t.Fatalf("wait for the folderless end, got err=%v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run did not end after the folderless snapshot")
	}
}

func TestLinkStreamProviderErrorKeepsHubState(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	waitForPosts(t, fx, 1, 2*time.Second)

	// The registry read fails while the hub waits: nothing is sent, and
	// in particular nothing that would unlink the folders.
	fx.folders.fail(fmt.Errorf("the presence store is unavailable"))
	time.Sleep(fx.interval * 3)
	if got := len(fx.posts()); got != 1 {
		t.Fatalf("a failed folder read posted %d snapshots; only the first one is allowed", got)
	}

	fx.folders.fail(nil)
	posts := waitForPosts(t, fx, 2, 2*time.Second)
	stopLoop(t, fx, cancel, result)
	if len(posts[1].Folders) != 2 {
		t.Fatalf("recovery posted %+v", posts[1].Folders)
	}
}

func TestLinkStreamRefusesMessageItCannotVerify(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.tamper = func(e LinkEnvelope) LinkEnvelope {
		e.Signature = e.Signature[:len(e.Signature)-2] + "AA"
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) {
			t.Fatalf("want a refusal, got %v", got.err)
		}
		if !strings.Contains(got.err.Error(), "verif") {
			t.Fatalf("reason = %q", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a message the hub could not verify did not stop the run")
	}
	if fx.opens() != 1 {
		t.Fatalf("a refusal was retried %d times", fx.opens())
	}
}

func TestLinkStreamRefusesUnknownHubMessageType(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.sendKind = "turn.summary"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "message type") {
			t.Fatalf("want a refusal for the unknown type, got %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unknown message type did not stop the run")
	}
}

func TestLinkStreamRefusesRepeatedMessageID(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.repeatReady = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "repeated") {
			t.Fatalf("want a refusal for the repeated id, got %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a repeated message id did not stop the run")
	}
}

func TestLinkStreamRefusesAnotherMachinesMessage(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.eventMachine = "machine-somebody-else"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "another machine") {
			t.Fatalf("want a refusal for another machine, got %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a message for another machine did not stop the run")
	}
}

func TestLinkStreamRefusesWrongAcknowledgement(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.ackWrong = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "different presence") {
			t.Fatalf("want a refusal for the mismatched acknowledgement, got %v", got.err)
		}
		if got.report.Acknowledged != 0 {
			t.Fatalf("a snapshot with the wrong acknowledgement counted: %+v", got.report)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a mismatched acknowledgement did not stop the run")
	}
}

func TestLinkStreamRefusesFolderLabelOutsideTheHubProfile(t *testing.T) {
	for name, label := range map[string]string{
		"path":      "oak/nested",
		"too long":  strings.Repeat("a", 81),
		"traversal": "..",
		"beginning": "-oak",
	} {
		t.Run(name, func(t *testing.T) {
			fx := newStreamFixture(t)
			fx.folders.set(LinkPresenceFolder{Label: label})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := fx.runLoop(ctx)
			select {
			case got := <-result:
				if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "would be refused") {
					t.Fatalf("want a refusal before sending, got %v", got.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("an invalid label did not stop the run")
			}
			if len(fx.posts()) != 0 {
				t.Fatalf("an invalid label reached the hub: %+v", fx.posts())
			}
		})
	}
}

func TestLinkStreamStopsInsteadOfHammeringOnRefusal(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.stickyPost = http.StatusForbidden
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fx.runLoop(ctx)
	select {
	case got := <-result:
		if !LinkRefusalOf(got.err) || !strings.Contains(got.err.Error(), "revoked") {
			t.Fatalf("want the hub's own refusal, got %v", got.err)
		}
		if got.report.Posted != 0 {
			t.Fatalf("a refused snapshot counted: %d", got.report.Posted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a hub refusal did not stop the run")
	}
	if fx.opens() != 1 {
		t.Fatalf("the run reopened the stream %d times after a refusal", fx.opens())
	}
}

func TestLinkStreamReconnectsAfterConflictOnTrack(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.failPosts = 1
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	waitForPosts(t, fx, 1, 3*time.Second)
	waitForOpens(t, fx, 2, 3*time.Second)
	stopLoop(t, fx, cancel, result)

	posts := fx.posts()
	if len(posts) != 1 {
		t.Fatalf("the hub accepted %d snapshots, want only the retry", len(posts))
	}
	fx.hub.mu.Lock()
	streams := append([]string(nil), fx.hub.streamIDs...)
	fx.hub.mu.Unlock()
	if len(streams) < 2 || posts[0].StreamID == streams[0] {
		t.Fatalf("the accepted snapshot did not ride a fresh stream: %v", streams)
	}
	if posts[0].Sequence != 1 {
		t.Fatalf("the fresh stream posted sequence %d", posts[0].Sequence)
	}
}

func TestLinkStreamReconnectsAfterTheHubCloses(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.closeAfterPings = 1
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	waitForOpens(t, fx, 2, 3*time.Second)
	waitForPosts(t, fx, 2, 3*time.Second)
	report, _ := stopLoop(t, fx, cancel, result)
	if report.Sessions < 2 {
		t.Fatalf("report counted %d sessions", report.Sessions)
	}
}

func TestLinkStreamLoopStopsWithItsContext(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := fx.runLoop(ctx)
	waitForPosts(t, fx, 1, 2*time.Second)
	report, err := stopLoop(t, fx, cancel, result)
	if err != context.Canceled {
		t.Fatalf("stop error = %v, want context.Canceled", err)
	}
	if report.Posted < 1 {
		t.Fatalf("the report lost the snapshot it posted: %+v", report)
	}
}

func TestLinkStreamSupervisorIdlesWithoutAnActiveState(t *testing.T) {
	fx := newStreamFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	calls := 0
	src := func() (LinkStreamReady, error) {
		calls++
		return LinkStreamReady{
			Options: fx.options(),
			Active:  false,
			Why:     "no linked folder can be reported yet",
		}, nil
	}
	_, err := LinkStreamSupervisor(ctx, src, LinkStreamPolicy{IdlePoll: 20 * time.Millisecond, TrustPause: 60 * time.Millisecond}, func(msg string) {
		fx.logMu.Lock()
		defer fx.logMu.Unlock()
		fx.logs = append(fx.logs, msg)
	})
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("supervisor error = %v", err)
	}
	if fx.opens() != 0 {
		t.Fatalf("an inactive state opened %d streams", fx.opens())
	}
	if calls < 2 {
		t.Fatalf("the supervisor read state %d times", calls)
	}
	lines := fx.logLines()
	if len(lines) != 1 || lines[0] != "no linked folder can be reported yet" {
		t.Fatalf("logs = %q", lines)
	}
}

func TestLinkStreamSupervisorPausesAndLogsUnchangedRefusalsOnce(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.stickyPost = http.StatusForbidden
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	state := LinkStreamReady{
		Options: fx.options(),
		Active:  true,
		Why:     "streaming",
	}
	var flagMu sync.Mutex
	inactive := false
	setInactive := func(on bool) {
		flagMu.Lock()
		inactive = on
		flagMu.Unlock()
	}
	src := func() (LinkStreamReady, error) {
		flagMu.Lock()
		off := inactive
		flagMu.Unlock()
		if off {
			return LinkStreamReady{Options: fx.options(), Active: false, Why: "the link is off"}, nil
		}
		return state, nil
	}
	_, err := LinkStreamSupervisor(ctx, src, LinkStreamPolicy{IdlePoll: 20 * time.Millisecond, TrustPause: 60 * time.Millisecond, RetryBase: 10 * time.Millisecond, RetryMax: 60 * time.Millisecond}, func(msg string) {
		fx.logMu.Lock()
		defer fx.logMu.Unlock()
		fx.logs = append(fx.logs, msg)
	})
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("supervisor error = %v", err)
	}
	if fx.opens() < 2 {
		t.Fatalf("one refusal should be paused, not abandoned: %d opens", fx.opens())
	}
	if lines := fx.logLines(); len(lines) != 1 || !strings.Contains(lines[0], "presence stream stopped") {
		t.Fatalf("one refusal logged once, got %q", lines)
	}

	// A changed state is worth an immediate retry and its own line.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	state.Options.Hub.ApprovedAt = time.Now().Unix() + 1
	go func() {
		time.Sleep(60 * time.Millisecond)
		setInactive(true)
	}()
	_, _ = LinkStreamSupervisor(ctx2, src, LinkStreamPolicy{IdlePoll: 20 * time.Millisecond, TrustPause: 60 * time.Millisecond}, func(msg string) {
		fx.logMu.Lock()
		defer fx.logMu.Unlock()
		fx.logs = append(fx.logs, msg)
	})
	lines := fx.logLines()
	if len(lines) < 3 || lines[len(lines)-1] != "the link is off" {
		t.Fatalf("the state change was not reported: %q", lines)
	}
}

func TestLinkPresenceValid(t *testing.T) {
	accept := func(err error) bool { return err == nil }
	if !accept(LinkPresenceValid(nil)) || !accept(LinkPresenceValid([]LinkPresenceFolder{{Label: "oak", ProjectID: "a1b2c3d4e5f6a7b8"}})) {
		t.Fatal("a valid snapshot was refused")
	}
	folders := make([]LinkPresenceFolder, linkPresenceLimit+1)
	for i := range folders {
		folders[i] = LinkPresenceFolder{Label: fmt.Sprintf("f%d", i)}
	}
	if accept(LinkPresenceValid(folders)) {
		t.Fatal("a snapshot past the hub's folder ceiling was accepted")
	}
	cases := map[string][]LinkPresenceFolder{
		"duplicate":   {{Label: "oak"}, {Label: "oak"}},
		"empty label": {{Label: ""}},
		"space edges": {{Label: " oak"}},
		"project id":  {{Label: "oak", ProjectID: "../../etc"}},
	}
	for name, set := range cases {
		if accept(LinkPresenceValid(set)) {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestLinkPresenceLabelProfile(t *testing.T) {
	valid := []string{"oak", "Oak Tree", "a.b_c-1", strings.Repeat("a", 80), "École", "1"}
	invalid := []string{"", ".", strings.Repeat("a", 81), " oak", "oak ", "oak/nested", ".."}
	for _, s := range valid {
		if !LinkPresenceLabel(s) {
			t.Fatalf("%q was refused", s)
		}
	}
	for _, s := range invalid {
		if LinkPresenceLabel(s) {
			t.Fatalf("%q was accepted", s)
		}
	}
}

func TestLinkStreamDelayIsBounded(t *testing.T) {
	policy := LinkStreamPolicy{RetryBase: time.Second, RetryMax: 30 * time.Second}
	var max time.Duration
	for attempt := 0; attempt < 12; attempt++ {
		for i := 0; i < 200; i++ {
			d := linkStreamDelay(attempt, policy)
			if d > policy.RetryMax+policy.RetryMax/5 {
				t.Fatalf("attempt %d delayed %s past the ceiling", attempt, d)
			}
			if d < policy.RetryBase-policy.RetryBase/5 {
				t.Fatalf("attempt %d delayed %s below the base", attempt, d)
			}
			if d > max {
				max = d
			}
		}
	}
}

// BenchmarkLinkPresenceSign measures what one due snapshot costs: the
// comparable form, the hub's own validation and the signed envelope.
func BenchmarkLinkPresenceSign(b *testing.B) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	folders := make([]LinkPresenceFolder, 40)
	for i := range folders {
		folders[i] = LinkPresenceFolder{Label: fmt.Sprintf("project-%02d", i), TUIOpen: i%3 == 0}
	}
	payload := linkPresencePayload{
		StreamID: "AAAAAAAAAAAAAAAAAAAAAA", Sequence: 1,
		BrainVersion: "captain 1234abc", Folders: folders,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = linkPresenceCanon(folders)
		if err := LinkPresenceValid(folders); err != nil {
			b.Fatal(err)
		}
		if _, err := LinkSign(key, "machine-stream-test", linkBrainDirection, "presence", payload, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLinkStreamFrame measures verifying one event off the hub's stream.
func BenchmarkLinkStreamFrame(b *testing.B) {
	hub := newStreamHub("machine-stream-test")
	env, err := LinkSign(hub.hubKey, hub.machine, linkHubDirection, "link.ready", map[string]string{"stream_id": "AAAAAAAAAAAAAAAAAAAAAA"}, time.Now())
	if err != nil {
		b.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		b.Fatal(err)
	}
	frame := map[string]string{"event": "link", "id": env.ID, "data": string(raw)}
	pub := hub.hubKey.Public().(ed25519.PublicKey)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg := linkStreamFrame(frame, true, true, pub, hub.machine, &linkSeenIDs{})
		if msg.err != nil {
			b.Fatal(msg.err)
		}
	}
}

func TestLinkStreamPushesDecisionCards(t *testing.T) {
	fx := newStreamFixture(t)
	cards := &streamCards{}
	cards.set(testCard("dq-261117-0a1b2c"))
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := LinkStreamLoop(ctx, opts)
	if ctx.Err() == nil {
		t.Fatalf("the loop should run until its context ends, got %v (%v)", report.Reason, err)
	}
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	if len(fx.hub.cards) != 1 {
		t.Fatalf("the hub should hold one card, got %d", len(fx.hub.cards))
	}
	if fx.hub.cards[0].DecisionID != "dq-261117-0a1b2c" || fx.hub.cards[0].Status != "open" {
		t.Fatalf("the hub held the wrong card: %+v", fx.hub.cards[0])
	}
	if report.Cards < 1 {
		t.Fatalf("the report should count pushed cards, got %d", report.Cards)
	}
}

func TestLinkStreamDoesNotRepeatAnUnchangedCard(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.closeAfterPings = 2
	cards := &streamCards{}
	cards.set(testCard("dq-261117-0a1b2c"))
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = LinkStreamLoop(ctx, opts)
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	if len(fx.hub.cards) != 1 {
		t.Fatalf("one unchanged card should be pushed once across reconnects, got %d", len(fx.hub.cards))
	}
	if fx.hub.opens < 2 {
		t.Fatalf("the test should have reconnected at least once, opened %d streams", fx.hub.opens)
	}
}

func TestLinkStreamRepostsAChangedCard(t *testing.T) {
	fx := newStreamFixture(t)
	cards := &streamCards{}
	card := testCard("dq-261117-0a1b2c")
	cards.set(card)
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(500 * time.Millisecond)
		card.Status = "answered"
		card.Answer = "a"
		cards.set(card)
	}()
	report, err := LinkStreamLoop(ctx, opts)
	if ctx.Err() == nil {
		t.Fatalf("the loop should run until its context ends, got %v (%v)", report.Reason, err)
	}
	fx.hub.mu.Lock()
	defer fx.hub.mu.Unlock()
	answered := 0
	for _, c := range fx.hub.cards {
		if c.Status == "answered" && c.Answer == "a" {
			answered++
		}
	}
	if answered != 1 {
		t.Fatalf("the changed card should be pushed again, answered cards: %d of %d", answered, len(fx.hub.cards))
	}
}

func TestLinkStreamCardProviderErrorKeepsTheStream(t *testing.T) {
	fx := newStreamFixture(t)
	cards := &streamCards{}
	cards.set(testCard("dq-261117-0a1b2c"))
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(300 * time.Millisecond)
		cards.fail(fmt.Errorf("the register is being rewritten"))
		time.Sleep(300 * time.Millisecond)
		cards.fail(nil)
	}()
	report, err := LinkStreamLoop(ctx, opts)
	if ctx.Err() == nil {
		t.Fatalf("a failed register read should not end the run, got %v (%v)", report.Reason, err)
	}
	if report.ProviderErrors == 0 {
		t.Fatal("the report should count the failed register read")
	}
}

func TestLinkStreamRefusesCardAckForAnotherDecision(t *testing.T) {
	fx := newStreamFixture(t)
	fx.hub.ackKind = "card.wrong"
	cards := &streamCards{}
	cards.set(testCard("dq-261117-0a1b2c"))
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := LinkStreamLoop(ctx, opts)
	if !LinkRefusalOf(err) {
		t.Fatalf("an acknowledgement for another decision should be a refusal, got %v", err)
	}
}

func TestLinkStreamRefusesACardItCannotBuild(t *testing.T) {
	fx := newStreamFixture(t)
	bad := testCard("dq-261117-0a1b2c")
	bad.Options = nil
	cards := &streamCards{}
	cards.set(bad)
	opts := fx.options()
	opts.Cards = cards.read
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := LinkStreamLoop(ctx, opts)
	if !LinkRefusalOf(err) {
		t.Fatalf("an invalid card should stop the run as a refusal, got %v", err)
	}
}

func TestLinkDecisionValid(t *testing.T) {
	base := testCard("dq-261117-0a1b2c")
	if err := LinkDecisionValid(base); err != nil {
		t.Fatalf("a well-formed card should pass: %v", err)
	}
	bad := base
	bad.DecisionID = "not-an-id"
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("a card with a bad id should be refused")
	}
	bad = base
	bad.Status = "answered"
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("an answered card without an answer should be refused")
	}
	bad = base
	bad.Answer = "a"
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("an open card with an answer should be refused")
	}
	bad = base
	bad.Record = "/etc/passwd"
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("an absolute record path should be refused")
	}
	bad = base
	bad.Options = append(bad.Options, LinkDecisionOption{ID: "a", Label: "Duplicate"})
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("a repeated option id should be refused")
	}
	bad = base
	bad.Title = strings.Repeat("x", 201)
	if err := LinkDecisionValid(bad); err == nil {
		t.Fatal("an over-long title should be refused")
	}
}

func BenchmarkLinkDecisionCardChecks(b *testing.B) {
	card := testCard("dq-261117-0a1b2c")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := LinkDecisionValid(card); err != nil {
			b.Fatal(err)
		}
		_ = linkCardCanon(card)
	}
}

// BenchmarkLinkDecisionCardSign measures building one decision card the wire
// takes: the checks, the comparable form, and the signed envelope.
func BenchmarkLinkDecisionCardSign(b *testing.B) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatal(err)
	}
	card := testCard("dq-261117-0a1b2c")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = linkCardCanon(card)
		if err := LinkDecisionValid(card); err != nil {
			b.Fatal(err)
		}
		if _, err := LinkSign(key, "machine-stream-test", linkBrainDirection, "decision.card", card, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
