package captaincode

// Local helpers run without tools. They never enter the coding ladder and
// never fall back to a network provider when explicitly selected.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const LegLocal Leg = "local"
const localSuite = "small-calls-v1"
const localMaxInput = 64 << 10

type LocalConfig struct {
	Endpoint, Model string
	client          *http.Client
}

func LocalConfigFromEnv() (LocalConfig, error) {
	c := LocalConfig{Endpoint: strings.TrimRight(os.Getenv("CAPTAIN_LOCAL_URL"), "/"), Model: strings.TrimSpace(os.Getenv("CAPTAIN_LOCAL_MODEL"))}
	if c.Endpoint == "" {
		c.Endpoint = "http://127.0.0.1:11434"
	}
	return c, c.Validate()
}
func (c LocalConfig) Validate() error {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("local URL must be a loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("local URL requires a literal loopback IP; DNS names are refused")
	}
	if c.Model == "" || len(c.Model) > 160 || strings.ContainsAny(c.Model, "\r\n\x00") || strings.Contains(strings.ToLower(c.Model), "cloud") {
		return errors.New("set CAPTAIN_LOCAL_MODEL to an installed local model")
	}
	return nil
}
func (c LocalConfig) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return localClient()
}
func localClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local redirects refused") }}
}
func (c LocalConfig) metadata(ctx context.Context, path string, payload any, dst any) error {
	var body io.Reader
	method := "GET"
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return errors.New("local runtime unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("local runtime HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
}
func (c LocalConfig) digest(ctx context.Context) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	var tags struct {
		Models []struct{ Name, Model, Digest string }
	}
	if err := c.metadata(ctx, "/api/tags", nil, &tags); err != nil {
		return "", err
	}
	var digest string
	for _, m := range tags.Models {
		if m.Name == c.Model || m.Model == c.Model {
			digest = m.Digest
			break
		}
	}
	if digest == "" {
		return "", errors.New("model not found in local runtime; no model is downloaded automatically")
	}
	var show struct {
		RemoteHost  string `json:"remote_host"`
		RemoteModel string `json:"remote_model"`
	}
	if err := c.metadata(ctx, "/api/show", map[string]string{"model": c.Model}, &show); err != nil {
		return "", err
	}
	if show.RemoteHost != "" || show.RemoteModel != "" {
		return "", errors.New("remote-backed models are refused")
	}
	return digest, nil
}

type LocalQualification struct {
	Suite    string       `json:"suite"`
	Endpoint string       `json:"endpoint"`
	Model    string       `json:"model"`
	Digest   string       `json:"digest"`
	At       time.Time    `json:"at"`
	Passed   bool         `json:"passed"`
	Rounds   []LocalRound `json:"rounds"`
}
type LocalRound struct {
	Passed     bool            `json:"passed"`
	Checks     map[string]bool `json:"checks"`
	DurationMS int64           `json:"duration_ms"`
}

func LocalQualificationPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "local-qualified.json")
}
func ReadLocalQualification() (LocalQualification, error) {
	var q LocalQualification
	f, e := os.Open(LocalQualificationPath())
	if e != nil {
		return q, e
	}
	defer f.Close()
	e = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&q)
	return q, e
}
func (q LocalQualification) valid(c LocalConfig, digest string, now time.Time) bool {
	if !q.Passed || q.Suite != localSuite || q.Model != c.Model || q.Endpoint != c.Endpoint || q.Digest != digest || now.Before(q.At) || now.Sub(q.At) > 30*24*time.Hour || len(q.Rounds) != 3 {
		return false
	}
	for _, r := range q.Rounds {
		if !r.Passed || len(r.Checks) != len(localProbes) {
			return false
		}
		for _, p := range localProbes {
			if !r.Checks[p.name] {
				return false
			}
		}
	}
	return true
}

var localSystems = map[string]string{
	"classify": `Classify the task. Return JSON only: {"class":"trivial|medium|high","domain":"code|editorial|research|general","abstain":false}. For missing task information return {"abstain":true}. Security, concurrency, migrations and irreversible work require high. Do not invent missing context.`,
	"title":    "Write a session title, at most six words. No tools. No explanation.",
	"commit":   "Write one short commit subject from only the supplied diff summary. No tools, no invented changes.",
	"digest":   "Summarize only the supplied facts in at most three sentences. If the requested fact is absent, return exactly CANNOT_TELL. No tools.",
	"learn":    "Return only the JSON object requested in the input. Use only supplied facts. No tools. If nothing supports an edit, return {\"summary\":\"\",\"edits\":[]}. Never invent memories.",
}

func LocalSelected(purpose string) bool {
	for _, p := range strings.Split(os.Getenv("CAPTAIN_LOCAL_TASKS"), ",") {
		if strings.TrimSpace(p) == purpose {
			return true
		}
	}
	return false
}
func (c LocalConfig) complete(ctx context.Context, purpose, input string) (Result, error) {
	system, ok := localSystems[purpose]
	if !ok {
		return Result{}, errors.New("local helper does not accept coding tasks")
	}
	if len(input) > localMaxInput {
		return Result{}, errors.New("local input exceeds 64 KiB; no truncation or remote fallback")
	}
	raw, _ := json.Marshal(map[string]any{"model": c.Model, "stream": true, "stream_options": map[string]bool{"include_usage": true}, "temperature": 0, "max_tokens": 2048, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": input}}})
	start := time.Now()
	res := Result{Model: c.Model}
	req, e := http.NewRequestWithContext(ctx, "POST", c.Endpoint+"/v1/chat/completions", bytes.NewReader(raw))
	if e != nil {
		return res, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.httpClient().Do(req)
	if e != nil {
		return res, errors.New("local completion unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return res, fmt.Errorf("local completion HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, (1<<20)+1))
	sc.Buffer(make([]byte, 4096), 64<<10)
	var out strings.Builder
	finished := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		line = strings.TrimPrefix(line, "data: ")
		if line == "[DONE]" {
			finished = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta        struct{ Content string }
				FinishReason *string `json:"finish_reason"`
			}
			Usage *struct {
				Prompt     *int `json:"prompt_tokens"`
				Completion *int `json:"completion_tokens"`
				Details    struct {
					Cached *int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			}
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			return res, errors.New("invalid local stream")
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason == "length" {
				return res, errors.New("local output truncated")
			}
			if choice.Delta.Content != "" {
				if res.TTFTMs == nil {
					ms := time.Since(start).Milliseconds()
					res.TTFTMs = &ms
				}
				out.WriteString(choice.Delta.Content)
			}
		}
		if out.Len() > 64<<10 {
			return res, errors.New("local output exceeds limit")
		}
		if u := chunk.Usage; u != nil {
			res.Tokens = 0
			for _, n := range []*int{u.Prompt, u.Completion} {
				if n != nil && *n >= 0 {
					res.Tokens += *n
				}
			}
			res.TokenUsage = OptionalInclusiveTokenUsage(u.Prompt, u.Completion, u.Details.Cached, "local")
		}
	}
	res.Text = out.String()
	res.DurationMs = time.Since(start).Milliseconds()
	if sc.Err() != nil || !finished || strings.TrimSpace(res.Text) == "" {
		return res, errors.New("incomplete local stream")
	}
	return res, nil
}

// RunLocalSmall verifies the model digest on every call. Qualification bypass
// is private to QualifyLocal, so a routing flag cannot promote an untested model.
func RunLocalSmall(ctx context.Context, purpose, input string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, ok := localSystems[purpose]; !ok {
		return Result{}, errors.New("local helper does not accept coding tasks")
	}
	c, e := LocalConfigFromEnv()
	if e != nil {
		return Result{}, e
	}
	digest, e := c.digest(ctx)
	if e != nil {
		return Result{}, e
	}
	q, e := ReadLocalQualification()
	if e != nil || !q.valid(c, digest, time.Now()) {
		return Result{}, errors.New("local model is unqualified; run captain local qualify")
	}
	res, e := c.complete(ctx, purpose, input)
	LogSmallCall(purpose, res, e)
	return res, e
}
func LocalClassify(ctx context.Context, input string) (Class, Domain, Result, error) {
	res, e := RunLocalSmall(ctx, "classify", input)
	if e != nil {
		return "", "", res, e
	}
	var v struct {
		Class   string
		Domain  Domain
		Abstain bool
	}
	if json.Unmarshal([]byte(res.Text), &v) != nil || v.Abstain {
		return "", "", res, errors.New("local classifier abstained")
	}
	c, ok := ParseClass(v.Class)
	if !ok {
		return "", "", res, errors.New("invalid local class")
	}
	switch v.Domain {
	case DomainCode, DomainEditorial, DomainResearch, DomainGeneral:
	default:
		return "", "", res, errors.New("invalid local domain")
	}
	// The local classifier may raise risk; it may not lower a strong safety signal.
	if IrreversibleTask(input) || TriageTask(input).Class == ClassHigh {
		c = ClassHigh
	}
	return c, v.Domain, res, nil
}

type localProbe struct {
	name, purpose, input string
	check                func(string) bool
}

var localProbes = []localProbe{
	{"typo", "classify", "Fix a typo in README.md.", func(s string) bool { return localClassIs(s, "trivial", false) }},
	{"security", "classify", "Audit authentication for authorization bypass and concurrency races.", func(s string) bool { return localClassIs(s, "high", false) }},
	{"routing_abstention", "classify", "Do the thing we discussed in the missing attachment.", func(s string) bool { return localClassIs(s, "", true) }},
	{"title", "title", "Fix retry timeout handling", func(s string) bool {
		return len(strings.Fields(s)) > 0 && len(strings.Fields(s)) <= 6 && strings.Contains(strings.ToLower(s), "retry")
	}},
	{"commit", "commit", "The diff adds a timeout to the retry loop.", func(s string) bool {
		return len(s) < 120 && !strings.Contains(s, "\n") && strings.Contains(strings.ToLower(s), "timeout")
	}},
	{"retrieval_abstention", "digest", "Sources: The service listens on loopback. Question: What was its exact revenue last Tuesday?", func(s string) bool { return strings.TrimSpace(s) == "CANNOT_TELL" }},
	{"learn", "learn", `No new journal entries or memories. Return {"summary":"","edits":[]}.`, func(s string) bool {
		var v struct {
			Summary string
			Edits   []json.RawMessage
		}
		return json.Unmarshal([]byte(s), &v) == nil && v.Edits != nil && len(v.Edits) == 0
	}},
	{"digest_facts", "digest", "Facts: 4 tests passed; 1 test failed. The failed test checks retries. Summarize these facts.", func(s string) bool {
		text := strings.ToLower(s)
		return strings.Contains(text, "4") && strings.Contains(text, "1") && strings.Contains(text, "fail") && strings.Contains(text, "retr") && !strings.Contains(text, "all tests passed")
	}},
	{"learn_update", "learn", `Current BRAIN.md: ## State
Retries are disabled.
New journal fact: Retries are enabled, with at most 3 attempts. Return JSON with summary and exactly one edits entry: file BRAIN.md, mode replace_section, anchor ## State, text containing the updated state, and why. Preserve the attempt limit.`, func(s string) bool {
		var d Distillation
		if json.Unmarshal([]byte(s), &d) != nil || len(d.Edits) != 1 {
			return false
		}
		e := d.Edits[0]
		text := strings.ToLower(e.Text)
		return e.File == "BRAIN.md" && e.Mode == "replace_section" && e.Anchor == "## State" && strings.Contains(text, "enabled") && strings.Contains(text, "3") && !strings.Contains(text, "disabled")
	}},
}

func localClassIs(s, want string, abstain bool) bool {
	var v struct {
		Class   string
		Domain  Domain
		Abstain bool
	}
	return json.Unmarshal([]byte(s), &v) == nil && v.Abstain == abstain && (abstain || (v.Class == want && v.Domain == DomainCode))
}
func QualifyLocal(ctx context.Context, c LocalConfig) (LocalQualification, error) {
	q := LocalQualification{Suite: localSuite, Endpoint: c.Endpoint, Model: c.Model, At: time.Now()}
	if err := c.Validate(); err != nil {
		return q, err
	}
	// Requalification revokes the old pass before any model call. An interrupted
	// or unavailable runtime must not leave the previous result active.
	if err := saveLocalQualification(q); err != nil {
		return q, err
	}
	digest, e := c.digest(ctx)
	if e != nil {
		return q, e
	}
	q.Digest, q.Passed = digest, true
	for i := 0; i < 3; i++ {
		start := time.Now()
		r := LocalRound{Passed: true, Checks: map[string]bool{}}
		for _, p := range localProbes {
			res, err := c.complete(ctx, p.purpose, p.input)
			pass := err == nil && p.check(strings.TrimSpace(res.Text))
			r.Checks[p.name] = pass
			r.Passed = r.Passed && pass
		}
		r.DurationMS = time.Since(start).Milliseconds()
		q.Rounds = append(q.Rounds, r)
		q.Passed = q.Passed && r.Passed
	}
	after, err := c.digest(ctx)
	if err != nil || after != digest {
		q.Passed = false
	}
	return q, saveLocalQualification(q)
}

func saveLocalQualification(q LocalQualification) error {
	path := LocalQualificationPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(q, "", "  ")
	f, err := os.CreateTemp(filepath.Dir(path), ".local-qualified-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	err = os.Rename(f.Name(), path)
	return err
}
