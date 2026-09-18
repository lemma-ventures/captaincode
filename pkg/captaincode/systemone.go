package captaincode

// TypeSafe's System One API - the jev leg. Unlike every other leg, jev does not
// generate text and cannot use a tool: it takes a state and typed questions
// and answers each with a calibrated probability distribution, in a few
// hundred milliseconds, billed per INPUT token only ($0.042/M, output free,
// 2026-09-17). That is the shape of captain's own decision points - triage
// classification first - not of a worker, so the registry lists it under its
// own transport (TransportSystemOne) and no task path ever offers it work.
//
//	POST https://api.typesafe.ai/v1/systemone   Authorization: Bearer $TYPESAFE_API_KEY
//	  {state, model, questions{<name>: {type: choice|score|noul, instructions, criteria}}}
//	→ {model, answers{<name>: {type, choice, probabilities, confidence
//	                                | score, legend, probabilities, confidence
//	                                | noul}}, usage{input_tokens, output_tokens}}
//	GET  https://api.typesafe.ai/v1/models → {models: [{name, description, release_date}]}
//
// Errors: 401 bad key, 422 malformed question, 429 rate limit (250k tokens/s,
// 1200 requests/min), 529 overloaded. The vendor asks for exponential backoff
// on the last two; Ask retries them twice and gives up.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SystemOneKeyEnv is the vendor SDK's own variable, so one key serves both;
	// ~/.config/captain/env is loaded into the process (LoadCaptainEnv).
	SystemOneKeyEnv = "TYPESAFE_API_KEY"
	// SystemOneKeyFile is the drop-in for the console's key download (a one-line
	// `API_KEY=…`), read the way aa.env is: ~/.config/captain/jev.env, then
	// $CAPTAIN_SRC/jev.env (gitignored there by *.env), then ./jev.env. The
	// variable wins over the file.
	SystemOneKeyFile = "jev.env"
	// SystemOneURLEnv points the client at a mock or a proxy.
	SystemOneURLEnv = "CAPTAIN_SYSTEMONE_URL"
	systemOneURL    = "https://api.typesafe.ai"
	// systemOneStateMax bounds what a classification sends: a task's head is
	// what carries its class, and the vendor states no context window.
	systemOneStateMax = 1500
	systemOneRetries  = 2
)

// systemOneBackoff is the wait before retry attempt+1 (300ms, 900ms): the
// exponential backoff the vendor asks for on 429/529. A test seam.
var systemOneBackoff = func(attempt int) time.Duration {
	return time.Duration(300*math.Pow(3, float64(attempt))) * time.Millisecond
}

// ErrDecisionLeg: a task reached a leg that answers questions, not tasks.
var ErrDecisionLeg = errors.New("decision leg: it answers typed questions and cannot take a task")

// S1Question is one typed question. Criteria is a map[string]string for a
// choice (option → description), a []string of ordered levels for a score,
// and absent (or {"true": …, "false": …}) for a noul.
type S1Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// S1Answer is one question's answer; which fields are set follows Type.
type S1Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
}

// S1Usage is one call's token consumption; only input is billed.
type S1Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// S1Response is the wire response. Model is the versioned id that served the
// call even when the request named an alias (jev-latest) - the figure to log.
type S1Response struct {
	Model   string              `json:"model"`
	Answers map[string]S1Answer `json:"answers"`
	Usage   S1Usage             `json:"usage"`
}

// S1Model is one entry of GET /v1/models.
type S1Model struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// SystemOneClient calls one System One endpoint with one key.
type SystemOneClient struct {
	BaseURL   string
	APIKey    string
	KeySource string       // where the key came from - the variable's name or a file path, never the key
	Model     string       // alias or versioned id; "" → jev-latest
	HTTP      *http.Client // nil → a 20s-timeout client
}

// SystemOneKey finds the jev key the way aaKey finds the Artificial Analysis
// one: TYPESAFE_API_KEY first, then a jev.env file (`TYPESAFE_API_KEY=…` or
// the console download's `API_KEY=…`) in ~/.config/captain/, the captain
// source dir (CAPTAIN_SRC) or the current directory. It returns where the key
// came from - never the key itself - so doctor and `captain jev` can name
// the source.
func SystemOneKey() (key, source string) {
	if k := strings.TrimSpace(os.Getenv(SystemOneKeyEnv)); k != "" {
		return k, SystemOneKeyEnv
	}
	candidates := []string{filepath.Join(configHome(), "captain", SystemOneKeyFile)}
	if src := os.Getenv("CAPTAIN_SRC"); src != "" {
		candidates = append(candidates, filepath.Join(src, SystemOneKeyFile))
	}
	candidates = append(candidates, SystemOneKeyFile)
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			for _, prefix := range []string{SystemOneKeyEnv + "=", "API_KEY="} {
				if !strings.HasPrefix(line, prefix) {
					continue
				}
				if k := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "\"'"); k != "" {
					return k, p
				}
			}
		}
	}
	return "", ""
}

// SystemOneFromEnv builds the client the brain and the CLI share, or nil when
// no key is set (SystemOneKey: the variable or a jev.env file). The model is
// the jev leg's registry pin, itself repinned by CAPTAIN_JEV_MODEL like any
// leg's.
func SystemOneFromEnv() *SystemOneClient {
	key, source := SystemOneKey()
	if key == "" {
		return nil
	}
	c := &SystemOneClient{BaseURL: systemOneURL, APIKey: key, KeySource: source, Model: ModelID(LegJev)}
	if s, ok := Spec(LegJev); ok {
		if m := strings.TrimSpace(os.Getenv(s.EnvPrefix() + "_MODEL")); m != "" {
			c.Model = m
		}
	}
	if u := strings.TrimSpace(os.Getenv(SystemOneURLEnv)); u != "" {
		c.BaseURL = strings.TrimRight(u, "/")
	}
	return c
}

func (c *SystemOneClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *SystemOneClient) model() string {
	if c.Model != "" {
		return c.Model
	}
	return "jev-latest"
}

// Ask evaluates the questions against state. The Result beside the answers is
// what the accounting hooks understand: Tokens is the reported total and
// CostUSD stays 0, so the ledger prices the call from the registry and says
// so (estimated, registry:jev) - the API reports tokens, never dollars.
func (c *SystemOneClient) Ask(ctx context.Context, state any, questions map[string]S1Question) (S1Response, Result, error) {
	t0 := time.Now()
	var out S1Response
	if len(questions) == 0 {
		return out, Result{}, errors.New("system one: no questions")
	}
	body, err := json.Marshal(map[string]any{"state": state, "model": c.model(), "questions": questions})
	if err != nil {
		return out, Result{}, err
	}
	var raw []byte
	for attempt := 0; ; attempt++ {
		raw, err = c.do(ctx, http.MethodPost, "/v1/systemone", body)
		var se *systemOneError
		if err == nil || attempt >= systemOneRetries || !errors.As(err, &se) || !se.retryable() {
			break
		}
		select {
		case <-ctx.Done():
			return out, Result{DurationMs: time.Since(t0).Milliseconds()}, ctx.Err()
		case <-time.After(systemOneBackoff(attempt)):
		}
	}
	res := Result{DurationMs: time.Since(t0).Milliseconds()}
	if err != nil {
		return out, res, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, res, fmt.Errorf("system one: decode: %w", err)
	}
	res.Text = string(raw)
	res.Tokens = out.Usage.InputTokens + out.Usage.OutputTokens
	return out, res, nil
}

// Models lists what the key can reach - the probe `captain jev` starts with.
func (c *SystemOneClient) Models(ctx context.Context) ([]S1Model, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []S1Model `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("system one: decode models: %w", err)
	}
	return out.Models, nil
}

func (c *SystemOneClient) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("system one: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("system one: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, &systemOneError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	return raw, nil
}

// systemOneError is a non-2xx answer, with the status the vendor documents.
type systemOneError struct {
	Status int
	Body   string
}

func (e *systemOneError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return fmt.Sprintf("system one: 401 unauthorized - check %s (keys: console.typesafe.ai/settings/keys)", SystemOneKeyEnv)
	case http.StatusTooManyRequests:
		return "system one: 429 rate limited"
	case 529:
		return "system one: 529 overloaded"
	}
	return fmt.Sprintf("system one: HTTP %d: %s", e.Status, truncateStr(e.Body, 300))
}

func (e *systemOneError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == 529 || (e.Status >= 500 && e.Status != http.StatusNotImplemented)
}

// Unwrap maps the vendor's statuses onto the sentinels the brain already
// understands, so a rate limit here is cooled down like any other.
func (e *systemOneError) Unwrap() error {
	switch {
	case e.Status == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.Status == 529 || e.Status >= 500:
		return ErrProviderDown
	}
	return nil
}

// jevClassQuestions are captain's triage questions in System One form: the
// same three classes and four domains the free-leg classify asks for
// (ClassifyWithLLM), as two independent choices scored in one call.
func jevClassQuestions() map[string]S1Question {
	return map[string]S1Question{
		"class": {Type: "choice",
			Instructions: "How much work is this task for a coding agent? Judge the scope of what is asked, not the length of the message.",
			Criteria: map[string]string{
				"trivial": "a one-liner: a typo, a single-sentence edit, a quick question, a rename in one place",
				"medium":  "ordinary focused work: one function, one rewrite, a review of one thing, a contained bug fix",
				"high":    "architecture, security, concurrency, a multi-part audit, formal soundness, or work spanning many files",
			}},
		"domain": {Type: "choice",
			Instructions: "What kind of work is this task?",
			Criteria: map[string]string{
				"code":      "writing, changing, reviewing or debugging software",
				"editorial": "prose for people: writing, rewriting, summarising or proofreading text",
				"research":  "finding out, comparing, analysing or explaining; the deliverable is knowledge",
				"general":   "none of the above, or a mix with no clear centre",
			}},
	}
}

// ClassifyWithJev is triage tier 1 on the decision leg: a task's class and
// domain with the calibrated confidence the caller gates on (the lower of the
// two answers'), in one call. TriageWithJev (shadow.go) is the same call with
// the shadow questions riding along.
func ClassifyWithJev(ctx context.Context, c *SystemOneClient, task string) (TriageResult, Result, error) {
	tr, _, res, err := TriageWithJev(ctx, c, task, JevTriageOptions{})
	return tr, res, err
}
