package main

// The egress proxy: the last thing between a prompt and a provider's socket.
//
//	http://127.0.0.1:14098/<provider>/...  →  https://<provider api>/...
//
// Every request body is run through the redaction engine (pkg/captaincode
// redact.go): secrets become stable placeholders, the operator's home
// directory and names become stand-ins. Authorization headers pass through
// untouched - that credential is the one meant for the provider. Responses
// come back with the identity stand-ins restored (streaming included, with a
// holdback for a stand-in split across two chunks) so the paths the model
// names are real again on this side; secret placeholders are NOT restored
// here - they turn back into values only at a tool boundary.
//
// Who goes through it: the opencode workers whose provider blocks name it as
// baseURL (captain init writes them: xai, openrouter, nim/nvidia, opencode
// zen, huggingface), claude -p via ANTHROPIC_BASE_URL, and codex exec via a custom
// provider on the chatgpt route (CodexProxyArgs). cursor-agent speaks
// grpc-web+proto to its endpoint - it is the gap, covered only by what its
// tool boundary refuses to read.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const proxyAddrDefault = "127.0.0.1:14098"

// proxyUpstreams maps the first path segment to the provider's origin. The
// rest of the path is forwarded as-is, so "/openrouter/v1/chat/completions"
// reaches "https://openrouter.ai/api/v1/chat/completions".
var proxyUpstreams = map[string]string{
	"anthropic":   "https://api.anthropic.com",
	"openai":      "https://api.openai.com",
	"chatgpt":     "https://chatgpt.com", // codex exec under the ChatGPT login: /backend-api/codex/responses
	"xai":         "https://api.x.ai",
	"openrouter":  "https://openrouter.ai/api",
	"nim":         "https://integrate.api.nvidia.com",
	"nvidia":      "https://integrate.api.nvidia.com",
	"opencode":    "https://opencode.ai/zen",
	"huggingface": "https://router.huggingface.co",
}

func proxyAddr() string {
	if v := os.Getenv("CAPTAIN_PROXY_ADDR"); v != "" {
		return v
	}
	return proxyAddrDefault
}

// ProxyBase is what config points at: "http://127.0.0.1:14098".
func proxyBase() string { return "http://" + proxyAddr() }

// proxyStats is what the sidebar's shield line reads.
type proxyStats struct {
	Requests int64 `json:"requests"`
	Secrets  int64 `json:"secrets"`
	Identity int64 `json:"identity"`
	mu       sync.Mutex
	kinds    map[string]int64
}

var proxyTotals proxyStats

func (s *proxyStats) add(r captaincode.Redaction) {
	atomic.AddInt64(&s.Requests, 1)
	atomic.AddInt64(&s.Secrets, int64(r.Secrets))
	atomic.AddInt64(&s.Identity, int64(r.Identity))
	if len(r.Kinds) > 0 {
		s.mu.Lock()
		if s.kinds == nil {
			s.kinds = map[string]int64{}
		}
		for k, n := range r.Kinds {
			s.kinds[k] += int64(n)
		}
		s.mu.Unlock()
	}
}

func (s *proxyStats) snapshot() map[string]any {
	s.mu.Lock()
	kinds := map[string]int64{}
	for k, n := range s.kinds {
		kinds[k] = n
	}
	s.mu.Unlock()
	return map[string]any{
		"requests": atomic.LoadInt64(&s.Requests), "secrets": atomic.LoadInt64(&s.Secrets),
		"identity": atomic.LoadInt64(&s.Identity), "kinds": kinds, "mode": captaincode.RedactMode(),
		"addr": proxyAddr(),
	}
}

// startProxy listens on the proxy address. Off when CAPTAIN_REDACT=off; a
// port already taken (a second brain, a stale one) is logged, not fatal -
// the workers' provider blocks would then fail to connect, which is loud.
func startProxy() {
	if captaincode.RedactMode() == "off" {
		fmt.Println("captain proxy: off (CAPTAIN_REDACT=off) - provider calls go direct")
		return
	}
	ln, err := net.Listen("tcp", proxyAddr())
	if err != nil {
		fmt.Printf("captain proxy: cannot listen on %s: %v\n", proxyAddr(), err)
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(proxyHandler), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	captaincode.SetProxyBase(proxyBase())
	fmt.Printf("captain proxy: %s (mode %s) - secrets and identity redacted on the wire\n", proxyAddr(), captaincode.RedactMode())
}

// wireProxy points the transports at the proxy when it listens and back at
// the providers when it does not (CAPTAIN_REDACT=off, or the port taken), so
// a config never names a proxy that is not there. The Claude Code hook
// follows the mode, not the port: it is the tool boundary, not the wire.
func wireProxy() {
	listening := captaincode.ProxyBase() != ""
	if changed, notes, err := captaincode.EnsureProxyRouting("", proxyBase(), listening); err != nil {
		fmt.Printf("captain proxy: opencode config: %v\n", err)
	} else if changed {
		fmt.Printf("captain proxy: opencode providers rerouted: %s\n", strings.Join(notes, ", "))
	}
	if changed, note, err := captaincode.EnsureClaudeRedactHook(captaincode.RedactMode() != "off"); err != nil {
		fmt.Printf("captain proxy: claude hook: %v\n", err)
	} else if changed {
		fmt.Println("captain proxy: " + note)
	}
}

var proxyClient = &http.Client{
	Timeout: 0, // streams run for minutes; the worker's own timers bound them
	Transport: &http.Transport{
		MaxIdleConns: 64, IdleConnTimeout: 90 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute, // frontier models think for minutes before the first byte
	},
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	upstream, ok := proxyUpstreams[seg[0]]
	if !ok {
		if r.URL.Path == "/health" || r.URL.Path == "/" {
			writeJSON(w, 200, proxyTotals.snapshot())
			return
		}
		http.Error(w, "captain proxy: unknown upstream "+seg[0], 404)
		return
	}
	rest := ""
	if len(seg) > 1 {
		rest = "/" + seg[1]
	}
	target, err := url.Parse(upstream + rest)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	target.RawQuery = r.URL.RawQuery

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "captain proxy: read body: "+err.Error(), 400)
		return
	}
	var rep captaincode.Redaction
	if len(body) > 0 && isTextBody(r.Header.Get("Content-Type")) {
		var out string
		out, rep = captaincode.Redact(string(body))
		body = []byte(out)
	}
	// /deterministic: a request for a pinned model is routed to the serving
	// tuple ADI measured green, at temperature 0 (brain_pool.go).
	if seg[0] == "openrouter" && len(body) > 0 {
		if pinned, ok := proxyPins.apply(body); ok {
			body = pinned
		}
	}
	proxyTotals.add(rep)
	if rep.Secrets > 0 {
		fmt.Printf("captain proxy: %s - %d secret(s) redacted %v\n", seg[0], rep.Secrets, rep.Kinds)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	for k, vs := range r.Header {
		switch strings.ToLower(k) {
		case "host", "content-length", "accept-encoding", "connection":
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity") // we read the body; no gzip to unpack
	req.ContentLength = int64(len(body))
	resp, err := proxyClient.Do(req)
	if err != nil {
		http.Error(w, "captain proxy: upstream "+seg[0]+": "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue // the restored body may differ in length
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	ct := resp.Header.Get("Content-Type")
	if ct == "" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		ct = "text/event-stream" // chatgpt's codex stream carries no content type (live 2026-09-13)
	}
	if os.Getenv("CAPTAIN_PROXY_DEBUG") != "" {
		fmt.Printf("captain proxy: ← %s %d %s enc=%q\n", seg[0], resp.StatusCode, ct, resp.Header.Get("Content-Encoding"))
	}
	switch {
	case !captaincode.RedactIdentity():
		_, _ = io.Copy(w, resp.Body)
	case strings.HasPrefix(ct, "text/event-stream"):
		restoreSSE(w, resp.Body)
	case isTextBody(ct):
		b, _ := io.ReadAll(resp.Body)
		out, _ := captaincode.RestoreIdentity(string(b))
		_, _ = w.Write([]byte(out))
	default:
		_, _ = io.Copy(w, resp.Body)
	}
}

func isTextBody(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/")
}

// ── streaming restore ────────────────────────────────────────────────────────

// textKeys are the JSON fields that carry model output in the streaming
// formats we proxy: Anthropic (delta.text, delta.partial_json), OpenAI chat
// (delta.content, function.arguments), OpenAI/xAI responses (delta as a
// string). The restore runs on those, with one holdback across events so a
// stand-in split over two chunks ("/home/cap" + "tain/…") is still put back.
var textKeys = map[string]bool{"text": true, "partial_json": true, "content": true, "arguments": true, "delta": true}

type sseRestorer struct {
	w       io.Writer
	flusher http.Flusher
	hold    string
	pending *pendingEvent
	standIn []string
}

type pendingEvent struct {
	prefix string // "data: "
	obj    any
	set    func(string) // writes the field back into obj
	get    func() string
	trail  []string // the lines that followed it (blank separators, event:/id: of the next event), emitted after it in order
}

func restoreSSE(w http.ResponseWriter, body io.Reader) {
	fl, _ := w.(http.Flusher)
	rs := &sseRestorer{w: w, flusher: fl, standIn: captaincode.IdentityStandIns()}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		rs.line(sc.Text())
	}
	rs.finish()
}

func (rs *sseRestorer) emit(s string) {
	_, _ = io.WriteString(rs.w, s)
	_, _ = io.WriteString(rs.w, "\n")
	if rs.flusher != nil {
		rs.flusher.Flush()
	}
}

func (rs *sseRestorer) flushPending() {
	if rs.pending == nil {
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rs.pending.obj); err == nil {
		rs.emit(rs.pending.prefix + strings.TrimRight(buf.String(), "\n"))
	}
	for _, t := range rs.pending.trail {
		rs.emit(t)
	}
	rs.pending = nil
}

// split returns the part of t safe to emit and the suffix to hold: the
// longest tail of t that is a proper prefix of a stand-in.
func (rs *sseRestorer) split(t string) (string, string) {
	// Only the text AFTER the last complete stand-in can be a partial one:
	// "/home/captain" ends with "captain", a prefix of "captain-user", and
	// holding that back split a complete match in two (live 2026-09-13).
	end := 0
	for _, si := range rs.standIn {
		if i := strings.LastIndex(t, si); i >= 0 && i+len(si) > end {
			end = i + len(si)
		}
	}
	tail := t[end:]
	best := 0
	for _, si := range rs.standIn {
		n := len(si) - 1
		if n > len(tail) {
			n = len(tail)
		}
		for ; n > best; n-- {
			if strings.HasSuffix(tail, si[:n]) {
				best = n
				break
			}
		}
	}
	return t[:len(t)-best], t[len(t)-best:]
}

func (rs *sseRestorer) line(l string) {
	if !strings.HasPrefix(l, "data: ") {
		// Separators and event:/id: lines ride behind the pending data line
		// so the stream keeps its shape; they never end a text run.
		if rs.pending != nil {
			rs.pending.trail = append(rs.pending.trail, l)
		} else {
			rs.emit(l)
		}
		return
	}
	if strings.HasPrefix(l, "data: [DONE]") {
		rs.finish()
		rs.emit(l)
		return
	}
	var obj any
	if err := json.Unmarshal([]byte(l[6:]), &obj); err != nil {
		rs.finish()
		rs.emit(l)
		return
	}
	get, set := findTextField(obj)
	if get == nil {
		// A structural event (message_start, content_block_stop, usage…):
		// the text before it is complete, so anything held is real text.
		rs.finish()
		rs.emit(l)
		return
	}
	t := rs.hold + get()
	out, keep := rs.split(t)
	restored, _ := captaincode.RestoreIdentity(out)
	if os.Getenv("CAPTAIN_PROXY_DEBUG") != "" {
		fmt.Printf("captain proxy: delta %q → %q hold=%q\n", t, restored, keep)
	}
	set(restored)
	rs.hold = keep
	rs.flushPending()
	rs.pending = &pendingEvent{prefix: "data: ", obj: obj, set: set, get: get}
}

// finish releases the holdback into the pending event and writes it.
func (rs *sseRestorer) finish() {
	if rs.pending != nil && rs.hold != "" {
		restored, _ := captaincode.RestoreIdentity(rs.hold)
		rs.pending.set(rs.pending.get() + restored)
		rs.hold = ""
	}
	rs.hold = ""
	rs.flushPending()
}

// findTextField walks a decoded event and returns accessors for the first
// string field named like model output (see textKeys), depth-first.
func findTextField(obj any) (get func() string, set func(string)) {
	switch v := obj.(type) {
	case map[string]any:
		for k, child := range v {
			if s, ok := child.(string); ok && textKeys[k] {
				key := k
				_ = s
				return func() string { return v[key].(string) }, func(x string) { v[key] = x }
			}
		}
		for _, child := range v {
			if g, s := findTextField(child); g != nil {
				return g, s
			}
		}
	case []any:
		for _, child := range v {
			if g, s := findTextField(child); g != nil {
				return g, s
			}
		}
	}
	return nil, nil
}

// GET /v1/proxy/stats
func (b *brain) proxyStatsHTTP(w http.ResponseWriter, _ *http.Request) {
	out := proxyTotals.snapshot()
	out["boundary"] = boundaryStats() // what the tool boundary masked (captain redact log)
	writeJSON(w, 200, out)
}

// pinTable holds the OpenRouter request pins of the /deterministic runs in
// flight, by model id: provider_prefs from the ADI feed. Refcounted, so two
// concurrent runs of one model keep the pin until the last one ends.
type pinTable struct {
	mu   sync.Mutex
	pins map[string]*pinEntry
}

type pinEntry struct {
	prefs map[string]any
	n     int
}

var proxyPins = &pinTable{pins: map[string]*pinEntry{}}

// hold registers a pin and returns its release.
func (t *pinTable) hold(model string, prefs map[string]any) func() {
	key := strings.ToLower(model)
	t.mu.Lock()
	e := t.pins[key]
	if e == nil {
		e = &pinEntry{prefs: prefs}
		t.pins[key] = e
	}
	e.n++
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if e := t.pins[key]; e != nil {
			e.n--
			if e.n <= 0 {
				delete(t.pins, key)
			}
		}
	}
}

// apply rewrites a chat-completions body for a pinned model: OpenRouter's
// `provider` routing block and temperature 0. Reports whether it did.
func (t *pinTable) apply(body []byte) ([]byte, bool) {
	t.mu.Lock()
	n := len(t.pins)
	t.mu.Unlock()
	if n == 0 {
		return body, false
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body, false
	}
	model, _ := obj["model"].(string)
	t.mu.Lock()
	e := t.pins[strings.ToLower(model)]
	t.mu.Unlock()
	if e == nil {
		return body, false
	}
	obj["provider"] = e.prefs
	if _, has := obj["temperature"]; !has {
		obj["temperature"] = 0
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body, false
	}
	return out, true
}
