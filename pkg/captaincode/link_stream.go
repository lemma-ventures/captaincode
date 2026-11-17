package captaincode

// The outbound stream: the third part of the link protocol (roadmap row F6).
// Pairing gave this machine a hub URL, a pinned hub key and a signed frame;
// this file keeps one signed connection open to that hub and reports the
// folders that opted in. The rules from the protocol hold here: the brain
// dials out and never listens, one connection is one short stream the hub
// closes itself, every message travels inside the signed envelope, and no
// payload ever carries a path - a folder is known to the hub only by the
// label the machine's presence configuration gives it.
//
// Three layers:
//
//   - linkStreamSession runs one stream: open, wait for link.ready, keep one
//     snapshot going until the hub closes, goes quiet, or refuses us.
//   - LinkStreamLoop reopens streams with bounded backoff and stops at once
//     on a refusal, which is a failure repeating cannot fix.
//   - LinkStreamSupervisor reads local state between attempts, idles while
//     nothing is paired or reportable, and only retries a refusal when that
//     state changes or its pause has passed, so a revoked machine logs once
//     instead of hammering the hub.
//
// Every hub message is checked before it means anything: pinned key,
// direction, machine, clock window, single-use id and its own type. Unknown
// types fail closed, the way the protocol says they must.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// linkPresenceInterval is the snapshot cadence while a stream is open.
	// The protocol wants a snapshot at least every three seconds; the due
	// time is 2.5 s and the scheduler wakes exactly at it, so no beat can
	// drift past three seconds.
	linkPresenceInterval = 2500 * time.Millisecond
	// linkTickMax bounds how long a quiet stream sleeps before it looks at
	// its own state again, and linkTickMin keeps a short test interval from
	// spinning the loop.
	linkTickMax = time.Second
	linkTickMin = 10 * time.Millisecond
	// linkIdleLimit: the hub pings every two seconds, so six seconds of
	// silence means the connection is dead even though no error arrived.
	linkIdleLimit = 6 * time.Second
	// linkSessionLimit stops one stream on its own even if the hub never
	// closes it. The hub closes each stream at twenty seconds.
	linkSessionLimit = 25 * time.Second
	// linkPostLimit bounds one presence post inside its session.
	linkPostLimit = 10 * time.Second
	// linkPresenceLimit is the hub's folder ceiling for one snapshot.
	linkPresenceLimit = 200
	// linkSeenLimit bounds the one-time message id table of one stream.
	linkSeenLimit = 128
	// linkStreamLineLimit bounds one line read from the event stream.
	linkStreamLineLimit = 256 * 1024
)

// linkHubLabel is the label profile the hub accepts for a folder, a machine
// and a brain version: it starts with a letter or a number, then keeps
// letters, numbers, spaces, dots, underscores and hyphens, for at most 80
// characters. It is the hub's own rule, copied so a snapshot is validated
// before it leaves this machine instead of being refused on arrival.
var linkHubLabel = regexp.MustCompile(`^[\pL\pN][\pL\pN ._-]{0,79}$`)

// LinkPresenceFolder is one linked folder on its way up. Label comes from
// the machine's presence configuration, TUIOpen is the folder's live state,
// and ProjectID is optional and only set when the paired account already
// owns that project. Root never travels: the hub stores no path.
type LinkPresenceFolder struct {
	Label     string `json:"label"`
	TUIOpen   bool   `json:"tui_open"`
	ProjectID string `json:"project_id,omitempty"`
}

// linkPresencePayload is the presence payload, in the hub's own spelling.
type linkPresencePayload struct {
	StreamID     string               `json:"stream_id"`
	Sequence     int64                `json:"sequence"`
	BrainVersion string               `json:"brain_version"`
	Folders      []LinkPresenceFolder `json:"folders"`
}

// LinkPresenceLabel reports whether a folder label may travel to a hub.
func LinkPresenceLabel(label string) bool {
	return linkHubLabel.MatchString(label) && strings.TrimSpace(label) == label && label != ""
}

// LinkDecisionOption is one answer choice on a decision card, in the hub's
// own spelling.
type LinkDecisionOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// LinkDecisionCard is one owner question on its way up. It carries no path
// and no transcript: the machine's own register already masked the text, and
// the record names a file inside the repository, never an absolute path.
type LinkDecisionCard struct {
	DecisionID string               `json:"decision_id"`
	Title      string               `json:"title"`
	Question   string               `json:"question"`
	Context    string               `json:"context,omitempty"`
	Options    []LinkDecisionOption `json:"options"`
	Recommend  string               `json:"recommend,omitempty"`
	Why        string               `json:"why,omitempty"`
	Record     string               `json:"record,omitempty"`
	PostedAt   int64                `json:"posted_at"`
	Status     string               `json:"status"`
	Answer     string               `json:"answer,omitempty"`
}

// linkCardLimit bounds the cards one machine pushes, matching the hub's row
// ceiling per machine.
const linkCardLimit = 64

// LinkDecisionValid checks a card against the hub's rules before it is
// signed, so a card this machine builds badly is never sent.
func LinkDecisionValid(c LinkDecisionCard) error {
	if !decisionIDRe.MatchString(c.DecisionID) {
		return fmt.Errorf("a decision card id is outside the id profile")
	}
	if c.Title == "" || len(c.Title) > 200 || c.Question == "" || len(c.Question) > 4000 || len(c.Context) > 4000 || len(c.Why) > 500 {
		return fmt.Errorf("a decision card holds text outside the hub's bounds")
	}
	if len(c.Record) > 256 || strings.HasPrefix(c.Record, "/") || strings.Contains(c.Record, "..") {
		return fmt.Errorf("a decision card record is not a bounded repository-relative name")
	}
	if len(c.Options) < 2 || len(c.Options) > 6 {
		return fmt.Errorf("a decision card holds %d options; the hub takes two to six", len(c.Options))
	}
	seen := make(map[string]bool, len(c.Options))
	for _, o := range c.Options {
		if len(o.ID) > 16 || !linkIdentifier.MatchString(o.ID) || o.Label == "" || len(o.Label) > 200 || len(o.Detail) > 1000 || seen[o.ID] {
			return fmt.Errorf("a decision card option is outside the hub's rules")
		}
		seen[o.ID] = true
	}
	switch c.Status {
	case "open":
		if c.Answer != "" {
			return fmt.Errorf("an open decision card carries an answer")
		}
	case "answered":
		if !seen[c.Answer] {
			return fmt.Errorf("an answered decision card names an option it does not hold")
		}
	case "withdrawn":
	default:
		return fmt.Errorf("a decision card holds a status the hub does not use")
	}
	if c.Recommend != "" && !seen[c.Recommend] {
		return fmt.Errorf("a decision card recommends an option it does not hold")
	}
	return nil
}

// linkCardCanon is the comparable form of a card: a change in any of these
// fields is a change the hub must see again.
func linkCardCanon(c LinkDecisionCard) string {
	var b strings.Builder
	b.WriteString(c.DecisionID)
	b.WriteByte('\n')
	b.WriteString(c.Status)
	b.WriteByte('\n')
	b.WriteString(c.Answer)
	b.WriteByte('\n')
	b.WriteString(c.Title)
	b.WriteByte('\n')
	b.WriteString(c.Question)
	b.WriteByte('\n')
	b.WriteString(c.Recommend)
	return b.String()
}

// linkCardState remembers what one run already pushed, across the streams it
// opens, so a reconnect does not repeat every card. It lives for the run.
type linkCardState struct {
	canon map[string]string
}

func (s *linkCardState) sent(id, canon string) {
	if s.canon == nil {
		s.canon = map[string]string{}
	}
	s.canon[id] = canon
}

func (s *linkCardState) known(id, canon string) bool {
	return s.canon != nil && s.canon[id] == canon
}

// LinkPresenceValid checks a snapshot against the hub's rules before it is
// signed: the folder count, unique labels inside the label profile, and an
// optional project id in the identifier profile. The caller logs what it
// dropped; this function decides only whether the snapshot may be sent.
func LinkPresenceValid(folders []LinkPresenceFolder) error {
	if len(folders) > linkPresenceLimit {
		return fmt.Errorf("a presence snapshot holds at most %d folders, got %d", linkPresenceLimit, len(folders))
	}
	seen := make(map[string]bool, len(folders))
	for _, f := range folders {
		if !LinkPresenceLabel(f.Label) {
			return fmt.Errorf("a folder label is outside the hub's label profile")
		}
		if seen[f.Label] {
			return fmt.Errorf("a presence snapshot repeats a folder label")
		}
		seen[f.Label] = true
		if f.ProjectID != "" && !linkIdentifier.MatchString(f.ProjectID) {
			return fmt.Errorf("a folder carries an unusable project id")
		}
	}
	return nil
}

// linkPresenceCanon is the comparable form of a snapshot: sorted by label,
// one line each. A provider may return folders in any order; a heartbeat is
// only due when this string is unchanged and the interval has passed.
func linkPresenceCanon(folders []LinkPresenceFolder) string {
	sorted := append([]LinkPresenceFolder(nil), folders...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Label < sorted[j].Label })
	var b strings.Builder
	for _, f := range sorted {
		b.WriteString(f.Label)
		b.WriteByte('\n')
		if f.TUIOpen {
			b.WriteString("tui\n")
		} else {
			b.WriteString("brain\n")
		}
		b.WriteString(f.ProjectID)
		b.WriteByte('\n')
	}
	return b.String()
}

// LinkRefusal is a failure no reconnect can fix: a message the hub cannot
// verify or accept, a reply for another machine, a refusable status. It
// stops the loop at once so a revoked or misconfigured machine logs the
// reason once instead of retrying forever.
type LinkRefusal struct{ Reason string }

func (e *LinkRefusal) Error() string { return e.Reason }

func linkRefuse(format string, a ...any) error {
	return &LinkRefusal{Reason: fmt.Sprintf(format, a...)}
}

// LinkRefusalOf reports whether an error carries a refusal.
func LinkRefusalOf(err error) bool {
	var ref *LinkRefusal
	return errors.As(err, &ref)
}

// LinkStreamReport is what a run of streams did, for the caller's log and
// for the backoff rule. It says nothing about paths, labels or keys.
type LinkStreamReport struct {
	Sessions       int
	Posted         int
	Acknowledged   int
	Cards          int
	ProviderErrors int
	StreamID       string
	LastSequence   int64
	Reason         string
}

// LinkStreamPolicy bounds the loops. The zero value is the shipped one;
// tests shorten every pause.
type LinkStreamPolicy struct {
	IdlePoll   time.Duration // how often local state is read while nothing streams
	TrustPause time.Duration // after a refusal, before a state-unchanged retry
	RetryBase  time.Duration // first reconnect delay after a failed stream
	RetryMax   time.Duration // ceiling for that delay
}

// DefaultLinkStreamPolicy is the shipped pacing: two seconds between idle
// state reads, five minutes before a refusal is retried with unchanged
// state, and one to thirty seconds between attempts.
func DefaultLinkStreamPolicy() LinkStreamPolicy {
	return LinkStreamPolicy{
		IdlePoll:   2 * time.Second,
		TrustPause: 5 * time.Minute,
		RetryBase:  time.Second,
		RetryMax:   30 * time.Second,
	}
}

func (p LinkStreamPolicy) withDefaults() LinkStreamPolicy {
	d := DefaultLinkStreamPolicy()
	if p.IdlePoll > 0 {
		d.IdlePoll = p.IdlePoll
	}
	if p.TrustPause > 0 {
		d.TrustPause = p.TrustPause
	}
	if p.RetryBase > 0 {
		d.RetryBase = p.RetryBase
	}
	if p.RetryMax > 0 {
		d.RetryMax = p.RetryMax
	}
	if d.RetryMax < d.RetryBase {
		d.RetryMax = d.RetryBase
	}
	return d
}

// LinkStreamOptions configures one run of the presence stream. Nothing here
// may come from a hub: the machine id, the key, the pinned hub and the
// folder provider all come from local state.
type LinkStreamOptions struct {
	Hub          LinkHub
	MachineID    string
	Key          ed25519.PrivateKey
	BrainVersion string
	// Folders reads the current snapshot on demand. An error means the
	// read failed: nothing is sent, never an empty snapshot, because an
	// empty snapshot unlinks every folder on the hub.
	Folders func() ([]LinkPresenceFolder, error)
	// Cards reads the current decision cards on demand. An error means
	// the register could not be read: nothing is sent this beat. A card
	// is pushed when it is new or when its comparable form changed.
	Cards    func() ([]LinkDecisionCard, error)
	cardSent *linkCardState
	Interval time.Duration // defaults to 2.5 seconds
	Policy   LinkStreamPolicy
	// Client overrides the client built from the hub's CA file; the tests
	// set it so a stub hub runs on the system's own verification path.
	Client *http.Client
}

// LinkStreamReady is one read of local state by the supervisor.
type LinkStreamReady struct {
	Options LinkStreamOptions
	Active  bool
	// Why explains an inactive state (no pairing, pending pairing, no
	// reportable folder). It is shown to the operator and contains no path.
	Why string
}

// fingerprint identifies the state a refusal happened under, so the
// supervisor retries at once when anything about it changes - a new pairing,
// a refreshed key or another hub - and only after its pause when nothing did.
func (r LinkStreamReady) fingerprint() string {
	return strings.Join([]string{
		r.Options.MachineID,
		r.Options.Hub.URL,
		r.Options.Hub.PublicKey,
		r.Options.Hub.PairID,
		r.Options.Hub.CAFile,
		fmt.Sprint(r.Options.Hub.ApprovedAt),
		r.Options.BrainVersion,
	}, "|")
}

// linkStreamClient is the pairing client with no whole-request timeout: a
// stream read lasts for as long as its session does, while the header wait
// and the session context still bound every step.
func linkStreamClient(caFile string) (*http.Client, error) {
	client, err := linkHTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	client.Timeout = 0
	return client, nil
}

func (o LinkStreamOptions) interval() time.Duration {
	if o.Interval > 0 {
		return o.Interval
	}
	return linkPresenceInterval
}

func (o LinkStreamOptions) tick() time.Duration {
	t := o.interval() / 4
	if t > linkTickMax {
		t = linkTickMax
	}
	if t < linkTickMin {
		t = linkTickMin
	}
	return t
}

// linkStreamMsg is one verified event from the hub's stream, or the reason
// the stream could not be read.
type linkStreamMsg struct {
	ping     bool
	ready    bool
	streamID string
	err      error
}

// linkSeenIDs is the one-time id table of one stream: at most linkSeenLimit
// ids, oldest dropped first, so a long stream cannot grow a map without end.
type linkSeenIDs struct {
	ids   map[string]bool
	order []string
	ready bool
}

func (s *linkSeenIDs) add(id string) bool {
	if s.ids[id] {
		return false
	}
	if !s.ready {
		s.ids = map[string]bool{}
		s.ready = true
	}
	s.ids[id] = true
	s.order = append(s.order, id)
	if len(s.order) > linkSeenLimit {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// linkStreamFrame reads one parsed event and returns the verified message it
// carries, or the refusal the event earned. Everything the hub said is
// checked here, in the order the protocol lists: payload shape, envelope,
// signature, machine, its type, then its own payload.
func linkStreamFrame(frame map[string]string, hasEvent, hasID bool, pub ed25519.PublicKey, machine string, seen *linkSeenIDs) linkStreamMsg {
	if !hasEvent || frame["event"] != "link" {
		return linkStreamMsg{err: linkRefuse("the hub sent an event this machine does not use")}
	}
	data, ok := frame["data"]
	if !ok || data == "" {
		return linkStreamMsg{err: linkRefuse("the hub sent an event with no message in it")}
	}
	if len(data) > linkMaxBody {
		return linkStreamMsg{err: linkRefuse("the hub sent a message that is too large to be a link envelope")}
	}
	var env LinkEnvelope
	if err := LinkDecodeEnvelope([]byte(data), &env); err != nil {
		return linkStreamMsg{err: linkRefuse("the hub sent a message this machine cannot read")}
	}
	if err := LinkVerify(pub, env, linkHubDirection, time.Now()); err != nil {
		return linkStreamMsg{err: linkRefuse("the hub sent a message this machine cannot verify")}
	}
	if env.MachineID != machine {
		return linkStreamMsg{err: linkRefuse("the hub sent a message for another machine")}
	}
	if hasID && frame["id"] != env.ID {
		return linkStreamMsg{err: linkRefuse("the hub labelled an event with another message id")}
	}
	if !seen.add(env.ID) {
		return linkStreamMsg{err: linkRefuse("the hub repeated a message id")}
	}
	var pay struct {
		StreamID string `json:"stream_id"`
	}
	switch env.Type {
	case "link.ready":
	case "link.ping":
	default:
		return linkStreamMsg{err: linkRefuse("the hub sent a message type this machine does not use")}
	}
	if err := LinkDecodePayload(env, &pay); err != nil {
		return linkStreamMsg{err: linkRefuse("the hub sent a stream message this machine cannot read")}
	}
	if pay.StreamID == "" || !linkIdentifier.MatchString(pay.StreamID) {
		return linkStreamMsg{err: linkRefuse("the hub sent a stream id this machine cannot accept")}
	}
	if env.Type == "link.ready" {
		return linkStreamMsg{ready: true, streamID: pay.StreamID}
	}
	return linkStreamMsg{ping: true, streamID: pay.StreamID}
}

// linkStreamRead parses the event stream on its own goroutine. It stops at
// the first refusal and always closes the channel, so the session side only
// ever selects on a channel and its timers.
func linkStreamRead(ctx context.Context, body io.Reader, pub ed25519.PublicKey, machine string, out chan<- linkStreamMsg) {
	defer close(out)
	send := func(m linkStreamMsg) bool {
		select {
		case out <- m:
			return true
		case <-ctx.Done():
			return false
		}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), linkStreamLineLimit)
	seen := &linkSeenIDs{}
	fields := map[string]string{}
	hasEvent, hasID, hasData := false, false, false
	reset := func() {
		fields = map[string]string{}
		hasEvent, hasID, hasData = false, false, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !hasData && !hasEvent && !hasID {
				reset()
				continue
			}
			m := linkStreamFrame(map[string]string{
				"event": fields["event"], "id": fields["id"], "data": fields["data"],
			}, hasEvent, hasID, pub, machine, seen)
			if m.err != nil {
				send(m)
				return
			}
			if !send(m) {
				return
			}
			reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		sep := strings.IndexByte(line, ':')
		if sep < 0 {
			send(linkStreamMsg{err: linkRefuse("the hub sent a stream line this machine does not use")})
			return
		}
		name, value := line[:sep], line[sep+1:]
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch name {
		case "event", "id", "data":
			fields[name] = value
			if len(value) > linkMaxBody {
				send(linkStreamMsg{err: linkRefuse("the hub sent a stream line that is too long")})
				return
			}
			if name == "event" {
				hasEvent = true
			}
			if name == "id" {
				hasID = true
				fields["id"] = value
			}
			if name == "data" {
				hasData = true
			}
		}
	}
	err := scanner.Err()
	if err != nil {
		if ctx.Err() != nil {
			send(linkStreamMsg{err: ctx.Err()})
			return
		}
		send(linkStreamMsg{err: fmt.Errorf("the hub could not be read: %w", err)})
		return
	}
	send(linkStreamMsg{err: io.EOF})
}

// linkStreamStatus reads a non-200 hub answer as the signed link.error the
// protocol defines, when there is one, and classifies it: a refusal for 400,
// 401 and 403, anything else as a failure another attempt may fix.
func linkStreamStatus(pub ed25519.PublicKey, machine string, status int, raw []byte) error {
	var env LinkEnvelope
	if len(raw) > 0 && LinkDecodeEnvelope(raw, &env) == nil &&
		LinkVerify(pub, env, linkHubDirection, time.Now()) == nil &&
		env.MachineID == machine && env.Type == "link.error" {
		var pay struct {
			Status int `json:"status"`
		}
		if LinkDecodePayload(env, &pay) == nil {
			switch pay.Status {
			case 400:
				return linkRefuse("the hub refused the message as malformed")
			case 401:
				return linkRefuse("the hub rejected the signature")
			case 403:
				return linkRefuse("the hub refused this machine: it is not paired, the pairing window closed, or the link was revoked")
			}
			return fmt.Errorf("the hub answered HTTP %d", pay.Status)
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusBadRequest {
		if len(raw) == 0 {
			return fmt.Errorf("the hub answered plain HTTP %d", status)
		}
		return linkRefuse("the hub refused the message without a link error this machine can verify")
	}
	return fmt.Errorf("the hub answered HTTP %d", status)
}

// linkStreamOpen asks for one stream and returns its response, or why the
// hub would not give one.
func linkStreamOpen(ctx context.Context, client *http.Client, opts LinkStreamOptions, pub ed25519.PublicKey) (*http.Response, error) {
	request, err := LinkSign(opts.Key, opts.MachineID, linkBrainDirection, "link.open", struct{}{}, time.Now())
	if err != nil {
		return nil, linkRefuse("the link signing key is missing: %v", err)
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > linkMaxBody {
		return nil, linkRefuse("the stream request this machine builds is unusable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, linkEndpoint(opts.Hub.URL, "/stream"), nil)
	if err != nil {
		return nil, fmt.Errorf("the stream request could not be built: %w", err)
	}
	req.Header.Set("X-Captain-Link", base64.RawURLEncoding.EncodeToString(raw))
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("the hub did not open a stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
		return nil, linkStreamStatus(pub, opts.MachineID, resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		defer resp.Body.Close()
		return nil, fmt.Errorf("the hub answered a stream request with %q", ct)
	}
	return resp, nil
}

// linkStreamPost sends one presence snapshot and checks the acknowledgement
// in full: pinned signature, our machine, the type the protocol names, and
// the id and sequence of the message it answers, byte for byte.
func linkStreamPost(ctx context.Context, client *http.Client, opts LinkStreamOptions, pub ed25519.PublicKey, payload linkPresencePayload) error {
	posted, err := LinkSign(opts.Key, opts.MachineID, linkBrainDirection, "presence", payload, time.Now())
	if err != nil {
		return linkRefuse("the link signing key is missing: %v", err)
	}
	raw, err := json.Marshal(posted)
	if err != nil || len(raw) > linkMaxBody {
		return linkRefuse("the presence snapshot this machine builds is too large")
	}
	reqCtx, cancel := context.WithTimeout(ctx, linkPostLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, linkEndpoint(opts.Hub.URL, "/messages"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("the presence request could not be built: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the presence snapshot could not be delivered: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
	if err != nil {
		return fmt.Errorf("the hub's answer could not be read: %w", err)
	}
	if len(body) > linkMaxBody {
		return linkRefuse("the hub's answer is too large to be a link envelope")
	}
	if resp.StatusCode != http.StatusOK {
		return linkStreamStatus(pub, opts.MachineID, resp.StatusCode, body)
	}
	var reply LinkEnvelope
	if err := LinkDecodeEnvelope(body, &reply); err != nil {
		return linkRefuse("the hub's acknowledgement is not a link envelope")
	}
	if err := LinkVerify(pub, reply, linkHubDirection, time.Now()); err != nil {
		return linkRefuse("the hub's acknowledgement cannot be verified")
	}
	if reply.MachineID != opts.MachineID {
		return linkRefuse("the hub acknowledged another machine's message")
	}
	if reply.Type != "presence.accepted" {
		return linkRefuse("the hub answered a presence snapshot with another message type")
	}
	var pay struct {
		MessageID string `json:"message_id"`
		Sequence  int64  `json:"sequence"`
	}
	if err := LinkDecodePayload(reply, &pay); err != nil {
		return linkRefuse("the hub's acknowledgement cannot be read")
	}
	if pay.MessageID != posted.ID || pay.Sequence != payload.Sequence {
		return linkRefuse("the hub acknowledged a different presence snapshot")
	}
	return nil
}

// linkStreamPostCard sends one decision card and checks the acknowledgement
// in full: pinned signature, our machine, the type the protocol names, and
// the id of the message and of the decision it answers, byte for byte.
func linkStreamPostCard(ctx context.Context, client *http.Client, opts LinkStreamOptions, pub ed25519.PublicKey, card LinkDecisionCard) error {
	posted, err := LinkSign(opts.Key, opts.MachineID, linkBrainDirection, "decision.card", card, time.Now())
	if err != nil {
		return linkRefuse("the link signing key is missing: %v", err)
	}
	raw, err := json.Marshal(posted)
	if err != nil || len(raw) > linkMaxBody {
		return linkRefuse("the decision card this machine builds is too large")
	}
	reqCtx, cancel := context.WithTimeout(ctx, linkPostLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, linkEndpoint(opts.Hub.URL, "/messages"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("the decision card request could not be built: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the decision card could not be delivered: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, linkMaxBody+1))
	if err != nil {
		return fmt.Errorf("the hub's answer could not be read: %w", err)
	}
	if len(body) > linkMaxBody {
		return linkRefuse("the hub's answer is too large to be a link envelope")
	}
	if resp.StatusCode != http.StatusOK {
		return linkStreamStatus(pub, opts.MachineID, resp.StatusCode, body)
	}
	var reply LinkEnvelope
	if err := LinkDecodeEnvelope(body, &reply); err != nil {
		return linkRefuse("the hub's acknowledgement is not a link envelope")
	}
	if err := LinkVerify(pub, reply, linkHubDirection, time.Now()); err != nil {
		return linkRefuse("the hub's acknowledgement cannot be verified")
	}
	if reply.MachineID != opts.MachineID {
		return linkRefuse("the hub acknowledged another machine's message")
	}
	if reply.Type != "decision.accepted" {
		return linkRefuse("the hub answered a decision card with another message type")
	}
	var pay struct {
		MessageID  string `json:"message_id"`
		DecisionID string `json:"decision_id"`
	}
	if err := LinkDecodePayload(reply, &pay); err != nil {
		return linkRefuse("the hub's acknowledgement cannot be read")
	}
	if pay.MessageID != posted.ID || pay.DecisionID != card.DecisionID {
		return linkRefuse("the hub acknowledged a different decision card")
	}
	return nil
}

// errLinkEnd says the session is over because no folder is left to report.
// The supervisor re-reads state when it arrives, so the machine stops its
// traffic instead of streaming an empty set forever.
var errLinkEnd = errors.New("no folder is left to report")

// linkStreamSession runs one stream to its end. It returns a refusal, one
// of the retryable failures, errLinkEnd, or the context's own error when the
// caller stopped it.
func linkStreamSession(ctx context.Context, opts LinkStreamOptions) (LinkStreamReport, error) {
	report := LinkStreamReport{Reason: "not started"}
	if opts.Folders == nil {
		return report, linkRefuse("the folder provider for this machine is missing")
	}
	if len(opts.Key) != ed25519.PrivateKeySize {
		return report, linkRefuse("this machine has no link signing key")
	}
	if opts.MachineID == "" {
		return report, linkRefuse("this machine has no identity to stream with")
	}
	if !LinkPresenceLabel(opts.BrainVersion) {
		return report, linkRefuse("the brain version of this machine is outside the hub's label profile")
	}
	base, err := linkHubBase(opts.Hub.URL)
	if err != nil {
		return report, linkRefuse("the paired hub is unusable: %v", err)
	}
	if opts.Hub.PublicKey == "" {
		return report, linkRefuse("no hub key is pinned for this machine")
	}
	pinned, err := base64.RawURLEncoding.DecodeString(opts.Hub.PublicKey)
	if err != nil || len(pinned) != ed25519.PublicKeySize {
		return report, linkRefuse("the pinned hub key is unreadable; run captain link unpair")
	}
	pub := ed25519.PublicKey(pinned)
	opts.Hub.URL = base

	client := opts.Client
	if client == nil {
		if client, err = linkStreamClient(opts.Hub.CAFile); err != nil {
			return report, linkRefuse("the hub certificate cannot be verified: %v", err)
		}
	}

	sessionCtx, cancel := context.WithTimeout(ctx, linkSessionLimit)
	defer cancel()
	options := opts
	options.Hub.URL = base

	resp, err := linkStreamOpen(sessionCtx, client, options, pub)
	if err != nil {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		report.Reason = err.Error()
		return report, err
	}
	defer resp.Body.Close()

	events := make(chan linkStreamMsg, linkStreamLinkBuffer)
	go linkStreamRead(sessionCtx, resp.Body, pub, opts.MachineID, events)

	tick := opts.tick()
	interval := opts.interval()
	var (
		streamID     string
		seq          int64
		lastCanon    string
		lastSent     time.Time
		lastEvent    = time.Now()
		lastCardRead time.Time
		sent         bool
	)
	report.Reason = "waiting for the hub"
	// snapshot sends one snapshot when one is due and does nothing when
	// none is. A fresh stream sends at once, a changed folder set sends at
	// the next look, and an unchanged one waits exactly for its interval,
	// so no beat drifts past the protocol's three seconds.
	snapshot := func() error {
		if streamID == "" {
			return nil
		}
		if time.Since(lastEvent) > linkIdleLimit {
			report.Reason = "the hub went quiet"
			return fmt.Errorf("%s", report.Reason)
		}
		folders, ferr := opts.Folders()
		if ferr != nil {
			report.ProviderErrors++
			// The read failed, so nothing is sent: an empty snapshot
			// would unlink every folder on the hub.
			return nil
		}
		if err := LinkPresenceValid(folders); err != nil {
			return linkRefuse("a presence snapshot this machine builds would be refused: %v", err)
		}
		canon := linkPresenceCanon(folders)
		if sent && canon == lastCanon && time.Since(lastSent) < interval {
			return nil
		}
		payload := linkPresencePayload{
			StreamID:     streamID,
			Sequence:     seq + 1,
			BrainVersion: opts.BrainVersion,
			Folders:      folders,
		}
		if payload.Folders == nil {
			payload.Folders = []LinkPresenceFolder{}
		}
		if err := linkStreamPost(sessionCtx, client, options, pub, payload); err != nil {
			return err
		}
		seq++
		sent = true
		now := time.Now()
		lastSent = now
		lastEvent = now
		lastCanon = canon
		report.Posted++
		report.Acknowledged++
		report.LastSequence = seq
		report.Reason = "stream open"
		if len(folders) == 0 {
			return errLinkEnd
		}
		return nil
	}
	// cards pushes decision cards when one is due: a fresh stream sends
	// what it has, a changed card sends again, an unchanged one waits for
	// its interval so the register is not reread on every tick.
	cards := func() error {
		if streamID == "" || opts.Cards == nil || opts.cardSent == nil {
			return nil
		}
		if !lastCardRead.IsZero() && time.Since(lastCardRead) < interval {
			return nil
		}
		lastCardRead = time.Now()
		list, err := opts.Cards()
		if err != nil {
			report.ProviderErrors++
			return nil
		}
		if len(list) > linkCardLimit {
			list = list[:linkCardLimit]
		}
		for _, c := range list {
			if err := LinkDecisionValid(c); err != nil {
				return linkRefuse("a decision card this machine builds would be refused: %v", err)
			}
			canon := linkCardCanon(c)
			if opts.cardSent.known(c.DecisionID, canon) {
				continue
			}
			if err := linkStreamPostCard(sessionCtx, client, options, pub, c); err != nil {
				return err
			}
			opts.cardSent.sent(c.DecisionID, canon)
			report.Cards++
			report.Reason = "stream open"
		}
		return nil
	}
	for {
		if streamID == "" && time.Since(lastEvent) > linkIdleLimit {
			report.Reason = "the hub did not open the stream"
			return report, fmt.Errorf("%s", report.Reason)
		}
		if err := snapshot(); err != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			return report, err
		}
		if err := cards(); err != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			return report, err
		}
		wait := tick
		if !lastSent.IsZero() {
			if until := time.Until(lastSent.Add(interval)); until < wait {
				wait = until
			}
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return report, ctx.Err()
		case msg, ok := <-events:
			timer.Stop()
			if !ok {
				report.Reason = "the hub closed the stream"
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				return report, fmt.Errorf("%s", report.Reason)
			}
			if msg.err != nil {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				if errors.Is(msg.err, io.EOF) {
					report.Reason = "the hub closed the stream"
					return report, fmt.Errorf("%s", report.Reason)
				}
				if LinkRefusalOf(msg.err) {
					return report, msg.err
				}
				report.Reason = msg.err.Error()
				return report, msg.err
			}
			lastEvent = time.Now()
			switch {
			case msg.ready:
				if streamID != "" {
					return report, linkRefuse("the hub opened a second stream on one connection")
				}
				streamID = msg.streamID
				report.StreamID = streamID
				report.Reason = "stream open"
			case msg.ping:
				if streamID == "" {
					return report, linkRefuse("the hub pinged a stream it has not opened")
				}
				if msg.streamID != streamID {
					return report, linkRefuse("the hub pinged another stream")
				}
			}
		case <-timer.C:
		}
	}
}

// linkStreamLinkBuffer keeps a little room between the reader goroutine and
// the session loop: a hub that pings every two seconds never fills it, and a
// burst has somewhere to land while a presence post is in flight.
const linkStreamLinkBuffer = 8

// linkStreamDelay grows the reconnect delay from the base to the ceiling and
// jitters it by a fifth, so machines that lost one hub at once do not arrive
// together, and no failure list is retried more than the ceiling allows.
func linkStreamDelay(attempt int, policy LinkStreamPolicy) time.Duration {
	d := policy.RetryBase
	for i := 0; i < attempt && d < policy.RetryMax; i++ {
		d *= 2
	}
	if d > policy.RetryMax {
		d = policy.RetryMax
	}
	if d <= 0 {
		return 0
	}
	jitter := time.Duration(rand.Int63n(int64(d)/5+1)) - d/10
	return d + jitter
}

// linkPause waits for d, or returns false when the context ended first.
func linkPause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

// LinkStreamLoop runs streams to the paired hub until the caller stops it,
// a refusal ends it, or no folder is left. Between streams it waits with
// bounded backoff; a stream that acknowledged at least one snapshot is a
// healthy session, so its delay falls back to the base instead of growing.
func LinkStreamLoop(ctx context.Context, opts LinkStreamOptions) (LinkStreamReport, error) {
	policy := opts.Policy.withDefaults()
	if opts.Cards != nil && opts.cardSent == nil {
		// One card memory for the whole run, so a reconnect does not
		// repeat every card the hub already holds.
		opts.cardSent = &linkCardState{}
	}
	var report LinkStreamReport
	attempt := 0
	for {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		run, err := linkStreamSession(ctx, opts)
		report.Sessions++
		report.Posted += run.Posted
		report.Acknowledged += run.Acknowledged
		report.Cards += run.Cards
		report.ProviderErrors += run.ProviderErrors
		report.StreamID = run.StreamID
		report.LastSequence = run.LastSequence
		report.Reason = run.Reason
		switch {
		case ctx.Err() != nil:
			return report, ctx.Err()
		case errors.Is(err, errLinkEnd):
			return report, err
		case LinkRefusalOf(err):
			return report, err
		}
		if run.Acknowledged > 0 {
			attempt = 0
		}
		delay := linkStreamDelay(attempt, policy)
		attempt++
		if !linkPause(ctx, delay) {
			return report, ctx.Err()
		}
	}
}

// LinkStreamSource reads local state when the supervisor needs it. Active
// means: paired, approved, identified, and holding at least one folder the
// provider can report.
type LinkStreamSource func() (LinkStreamReady, error)

// LinkStreamSupervisor is the brain's own loop: it idles while nothing is
// paired or reportable, streams while it is, and takes a refusal as a
// statement about local state - retried only after its pause, and at once
// when the state that refused it changes. It returns when the context ends.
func LinkStreamSupervisor(ctx context.Context, src LinkStreamSource, policy LinkStreamPolicy, log func(string)) (LinkStreamReport, error) {
	p := policy.withDefaults()
	logOnce := func(msg string) {
		if log != nil {
			log(msg)
		}
	}
	lastWhy := ""
	lastRefusal := ""
	for ctx.Err() == nil {
		state, err := src()
		if err != nil {
			why := "link state is unavailable: " + err.Error()
			if why != lastWhy {
				logOnce(why)
				lastWhy = why
			}
			if !linkPause(ctx, p.IdlePoll) {
				return LinkStreamReport{Reason: "stopped"}, ctx.Err()
			}
			continue
		}
		if !state.Active {
			why := state.Why
			if why == "" {
				why = "the link is idle"
			}
			if why != lastWhy {
				logOnce(why)
				lastWhy = why
			}
			if !linkPause(ctx, p.IdlePoll) {
				return LinkStreamReport{Reason: "stopped"}, ctx.Err()
			}
			continue
		}
		lastWhy = ""
		fingerprint := state.fingerprint()
		run, err := LinkStreamLoop(ctx, state.Options)
		report := run
		report.Reason = run.Reason
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if errors.Is(err, errLinkEnd) {
			// The folder set emptied while streaming. Restarting now
			// would reopen a stream to say it again; the supervisor
			// waits for its poll and the source reports it as idle.
			if !linkPause(ctx, p.IdlePoll) {
				return report, ctx.Err()
			}
			continue
		}
		var ref *LinkRefusal
		if errors.As(err, &ref) {
			if ref.Reason != lastRefusal {
				logOnce("presence stream stopped: " + ref.Reason)
				lastRefusal = ref.Reason
			}
			deadline := time.Now().Add(p.TrustPause)
			for time.Now().Before(deadline) && ctx.Err() == nil {
				if !linkPause(ctx, p.IdlePoll) {
					return report, ctx.Err()
				}
				next, nerr := src()
				if nerr != nil || !next.Active || next.fingerprint() != fingerprint {
					break
				}
			}
			continue
		}
		lastRefusal = ""
		if !linkPause(ctx, p.IdlePoll) {
			return report, ctx.Err()
		}
	}
	return LinkStreamReport{Reason: "stopped"}, ctx.Err()
}
