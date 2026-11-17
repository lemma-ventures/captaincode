package captaincode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const codexAppServerMaxLine = 16 * 1024 * 1024

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *codexRPCError  `json:"error"`
}

type codexRPCAnswer struct {
	result json.RawMessage
	err    error
}

type codexRPCClient struct {
	stdin    io.WriteCloser
	cancel   context.CancelFunc
	notify   func(string, json.RawMessage)
	fatal    chan error
	readDone chan struct{}

	writeMu  sync.Mutex
	mu       sync.Mutex
	nextID   int64
	pending  map[string]chan codexRPCAnswer
	closed   bool
	closeErr error
	threadID string
	turnID   string
}

func newCodexRPCClient(stdin io.WriteCloser, cancel context.CancelFunc, notify func(string, json.RawMessage)) *codexRPCClient {
	return &codexRPCClient{stdin: stdin, cancel: cancel, notify: notify, fatal: make(chan error, 1), readDone: make(chan struct{}), pending: map[string]chan codexRPCAnswer{}}
}

func (c *codexRPCClient) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > codexAppServerMaxLine {
		return errors.New("codex app-server request exceeds 16 MiB")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("codex app-server is closed")
	}
	raw = append(raw, '\n')
	for len(raw) > 0 {
		n, err := c.stdin.Write(raw)
		if err != nil {
			return err
		}
		raw = raw[n:]
	}
	return nil
}

func (c *codexRPCClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("codex app-server is closed")
		}
		return nil, err
	}
	c.nextID++
	id := c.nextID
	key := fmt.Sprint(id)
	answer := make(chan codexRPCAnswer, 1)
	c.pending[key] = answer
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("codex app-server %s: %w", method, err)
	}
	select {
	case got := <-answer:
		return got.result, got.err
	case <-ctx.Done():
		return nil, fmt.Errorf("codex app-server %s: %w", method, ctx.Err())
	}
}

func (c *codexRPCClient) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	pending := c.pending
	c.pending = map[string]chan codexRPCAnswer{}
	c.mu.Unlock()
	_ = c.stdin.Close()
	for _, answer := range pending {
		answer <- codexRPCAnswer{err: err}
	}
	select {
	case c.fatal <- err:
	default:
	}
	c.cancel()
}

func (c *codexRPCClient) shutdown() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[string]chan codexRPCAnswer{}
	c.mu.Unlock()
	_ = c.stdin.Close()
	for _, answer := range pending {
		answer <- codexRPCAnswer{err: errors.New("codex app-server closed")}
	}
}

func (c *codexRPCClient) readLoop(stdout io.Reader) {
	defer close(c.readDone)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), codexAppServerMaxLine)
	for sc.Scan() {
		var msg codexRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			c.fail(fmt.Errorf("codex app-server malformed event: %w", err))
			return
		}
		if len(msg.ID) > 0 && msg.Method == "" {
			key := string(msg.ID)
			var id int64
			if json.Unmarshal(msg.ID, &id) == nil {
				key = fmt.Sprint(id)
			}
			c.mu.Lock()
			answer := c.pending[key]
			c.mu.Unlock()
			if answer != nil {
				if msg.Error != nil {
					answer <- codexRPCAnswer{err: fmt.Errorf("codex app-server refused request: %s", truncateStr(msg.Error.Message, 240))}
				} else {
					answer <- codexRPCAnswer{result: msg.Result}
				}
			}
			continue
		}
		if len(msg.ID) > 0 && msg.Method != "" {
			_ = c.write(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "Captain does not accept app-server requests"}})
			continue
		}
		if msg.Method != "" && c.notify != nil {
			c.notify(msg.Method, msg.Params)
		}
	}
	if err := sc.Err(); err != nil {
		c.fail(fmt.Errorf("codex app-server event stream: %w", err))
		return
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.fail(errors.New("codex app-server event stream closed before turn/completed"))
	}
}

func (c *codexRPCClient) ids() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.threadID, c.turnID
}

func (c *codexRPCClient) steer(text string) error {
	threadID, turnID := c.ids()
	if threadID == "" || turnID == "" {
		return errors.New("codex app-server has no active turn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.call(ctx, "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": turnID,
		"input": []any{map[string]any{"type": "text", "text": SteerLine(text)}},
		// 0.169 rejects a name inside the context ("only supported for
		// untrusted context", live 17 November 2026); the key and value
		// still carry the origin.
		"additionalContext": map[string]any{"captain": map[string]any{
			"kind": "application", "value": "A user note amends the active turn.",
		}},
	})
	return err
}

func (c *codexRPCClient) interrupt() {
	threadID, turnID := c.ids()
	if threadID == "" || turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = c.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
}

func CodexAppServerRequested() bool {
	return strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_TRANSPORT")) == "app-server"
}

func CheckCodexAppServer() error {
	dir, err := os.MkdirTemp("", "captain-codex-schema-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "generate-json-schema", "--experimental", "--out", dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex app-server schema generation failed: %w: %s", err, truncateStr(strings.TrimSpace(string(output)), 240))
	}
	required := map[string][]string{
		"v1/InitializeParams.json":                    {"clientInfo"},
		"v2/ThreadStartParams.json":                   {"model", "cwd", "approvalPolicy", "sandbox"},
		"v2/ThreadStartResponse.json":                 {"thread"},
		"v2/TurnStartParams.json":                     {"threadId", "input", "effort", "approvalPolicy", "cwd"},
		"v2/TurnStartResponse.json":                   {"turn"},
		"v2/TurnSteerParams.json":                     {"threadId", "expectedTurnId", "input", "additionalContext"},
		"v2/TurnInterruptParams.json":                 {"threadId", "turnId"},
		"v2/ItemStartedNotification.json":             {"item", "threadId", "turnId"},
		"v2/ItemCompletedNotification.json":           {"item", "threadId", "turnId"},
		"v2/TurnCompletedNotification.json":           {"turn", "threadId"},
		"v2/ThreadTokenUsageUpdatedNotification.json": {"turnId", "threadId", "tokenUsage"},
	}
	for name, fields := range required {
		path := filepath.Join(dir, filepath.FromSlash(name))
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("codex app-server schema %s: %w", name, err)
		}
		if len(raw) > codexAppServerMaxLine {
			return fmt.Errorf("codex app-server schema %s exceeds 16 MiB", name)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			return fmt.Errorf("codex app-server schema %s is malformed: %w", name, err)
		}
		for _, field := range fields {
			if _, ok := schema.Properties[field]; !ok {
				return fmt.Errorf("codex app-server schema %s lacks %s", name, field)
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ClientRequest.json"))
	if err != nil {
		return fmt.Errorf("codex app-server client methods: %w", err)
	}
	for _, method := range []string{"initialize", "thread/start", "turn/start", "turn/steer", "turn/interrupt"} {
		if !bytes.Contains(raw, []byte(method)) {
			return fmt.Errorf("codex app-server schema lacks method %s", method)
		}
	}
	return nil
}

func codexAppServerCmdArgs(dir, task string) []string {
	args := []string{"app-server", "--stdio"}
	if !IsReadOnlyTurn(task) {
		args = append(args, CodexMCPArgs(dir)...)
		args = append(args, SerenaCodexMCP(dir)...)
	}
	return append(args, CodexProxyArgs()...)
}

func codexAppServerSettings(leg Leg, dir, task string, effort Effort) (map[string]any, map[string]any) {
	flag := ""
	if leg == LegCodexCLI {
		flag = strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_CLI_EFFORT"))
		if flag == "" {
			flag = "xhigh"
			if effort != "" {
				flag = effort.CodexFlag()
			}
		}
	} else if effort != "" {
		flag = effort.CodexFlag()
	}
	sandbox := strings.TrimSpace(os.Getenv("CAPTAIN_CODEX_CLI_SANDBOX"))
	if IsSentTurn(task) {
		sandbox = "workspace-write"
		if IsReadOnlyTurn(task) {
			sandbox = "read-only"
		}
	}
	if sandbox == "" || sandbox == "skip" {
		sandbox = "danger-full-access"
	}
	thread := map[string]any{
		"model": codexLegModel(leg, effort), "approvalPolicy": "never", "sandbox": sandbox,
	}
	turn := map[string]any{
		"approvalPolicy": "never",
		"input":          []any{map[string]any{"type": "text", "text": task}},
	}
	if dir != "" {
		thread["cwd"] = dir
		turn["cwd"] = dir
	}
	if flag != "" {
		turn["effort"] = flag
	}
	return thread, turn
}

type codexAppTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexAppStream struct {
	leg         Leg
	start       time.Time
	steer       *Steer
	prog        *progress
	onDelta     func(string)
	onStatus    func(string)
	acc         strings.Builder
	tokens      int
	detail      *TokenUsage
	firstOutput *int64
	threadID    string
	turn        chan codexAppTurn
}

func newCodexAppStream(leg Leg, steer *Steer, prog *progress, onDelta, onStatus func(string)) *codexAppStream {
	return &codexAppStream{leg: leg, start: time.Now(), steer: steer, prog: prog, onDelta: onDelta, onStatus: onStatus, turn: make(chan codexAppTurn, 1)}
}

func codexAppItemType(value string) string {
	switch value {
	case "agentMessage":
		return "agent_message"
	case "commandExecution":
		return "command_execution"
	case "fileChange":
		return "file_change"
	case "mcpToolCall":
		return "mcp_tool_call"
	case "webSearch":
		return "web_search"
	default:
		return value
	}
}

func (s *codexAppStream) item(eventType string, raw json.RawMessage) {
	var wire struct {
		ID               string   `json:"id"`
		Type             string   `json:"type"`
		Text             string   `json:"text"`
		Status           string   `json:"status"`
		ExitCode         *int     `json:"exitCode"`
		AggregatedOutput string   `json:"aggregatedOutput"`
		Summary          []string `json:"summary"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return
	}
	it := codexItem{ID: wire.ID, Type: codexAppItemType(wire.Type), Text: wire.Text, Status: wire.Status, ExitCode: wire.ExitCode, AggregatedOutput: wire.AggregatedOutput}
	if it.Text == "" {
		it.Text = strings.Join(wire.Summary, "\n")
	}
	if codexItemIsTool(it.Type) {
		switch eventType {
		case "item.started":
			s.prog.toolStart()
		case "item.completed":
			s.prog.toolEnd()
		}
	}
	if it.Type == "agent_message" {
		if eventType != "item.completed" || it.Text == "" {
			return
		}
		if s.firstOutput == nil {
			ms := time.Since(s.start).Milliseconds()
			s.firstOutput = &ms
		}
		chunk := it.Text
		if s.acc.Len() > 0 {
			chunk = "\n" + chunk
			if !strings.HasSuffix(s.acc.String(), "\n") {
				chunk = "\n" + chunk
			}
		}
		s.acc.WriteString(chunk)
		if s.onDelta != nil {
			s.onDelta(chunk)
		} else if s.onStatus != nil {
			if text := Narration(it.Text); text != "" {
				s.onStatus(text)
			}
		}
		return
	}
	if it.Type == "reasoning" && eventType == "item.completed" && s.onStatus != nil {
		if text := Narration(it.Text); text != "" {
			s.onStatus(text)
		}
		return
	}
	if eventType == "item.started" && s.onStatus != nil {
		if text := StatusInput(codexItemStatus(it.Type, raw), string(raw)); text != "" {
			s.onStatus(text)
		}
	}
	if eventType == "item.completed" && codexItemIsTool(it.Type) {
		step := Step{Tool: it.Type, Input: unwrapShell(pickDetail(raw, "command")), Output: it.AggregatedOutput}
		if step.Input == "" {
			step.Input = strings.TrimPrefix(codexItemStatus(it.Type, raw), "⚙ ")
		}
		step.Failed = it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
		s.steer.RecordStep(step)
	}
	if eventType == "item.completed" && s.onStatus != nil && it.Type == "command_execution" {
		code := 0
		if it.ExitCode != nil {
			code = *it.ExitCode
		}
		if text := Outcome(it.AggregatedOutput, code, code != 0 || it.Status == "failed"); text != "" {
			s.onStatus(text)
		}
	}
}

func (s *codexAppStream) notification(method string, raw json.RawMessage) {
	s.prog.touch()
	switch method {
	case "item/started", "item/completed":
		var params struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(raw, &params) == nil {
			s.item(strings.Replace(method, "/", ".", 1), params.Item)
		}
	case "thread/tokenUsageUpdated", "thread/tokenUsage/updated":
		var params struct {
			TokenUsage struct {
				Last struct {
					InputTokens       *int `json:"inputTokens"`
					CachedInputTokens *int `json:"cachedInputTokens"`
					OutputTokens      *int `json:"outputTokens"`
				} `json:"last"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return
		}
		usage := params.TokenUsage.Last
		s.tokens = 0
		for _, n := range []*int{usage.InputTokens, usage.OutputTokens} {
			if n != nil && *n >= 0 {
				s.tokens += *n
			}
		}
		s.detail = OptionalInclusiveTokenUsage(usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, "codex")
	case "turn/completed":
		var params struct {
			ThreadID string       `json:"threadId"`
			Turn     codexAppTurn `json:"turn"`
		}
		if json.Unmarshal(raw, &params) == nil {
			s.threadID = params.ThreadID
			select {
			case s.turn <- params.Turn:
			default:
			}
		}
	}
}

func runCodexAppServerStreamLeg(leg Leg, dir, task string, base, ceil time.Duration, onDelta, onStatus func(string), effort Effort, steer *Steer) (result Result, runErr error) {
	start := time.Now()
	ctx := context.Background()
	prog := &progress{}
	var capped *atomic.Bool
	if base > 0 {
		var cancel context.CancelFunc
		ctx, cancel, prog, capped = progressCtx(base, ceil)
		defer cancel()
	}
	ctx, stopped, detachStop := interruptible(ctx, steer, leg)
	defer detachStop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := newCodexAppStream(leg, steer, prog, onDelta, onStatus)
	defer func() {
		result.SessionID = stream.threadID
		result.Tokens = stream.tokens
		result.TokenUsage = stream.detail
		result.FirstOutputMs = stream.firstOutput
	}()
	cmd := exec.CommandContext(ctx, "codex", codexAppServerCmdArgs(dir, task)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), GateWorkerEnv(leg, task, steer.Task())...), WorkerGuardEnv(task)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("codex app-server pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("codex app-server stdin: %w", err)
	}
	client := newCodexRPCClient(stdin, cancel, stream.notification)
	cmd.Cancel = func() error {
		client.interrupt()
		_ = stdin.Close()
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Result{}, fmt.Errorf("codex app-server: %w: the Codex CLI is not installed", ErrProviderDown)
		}
		return Result{}, fmt.Errorf("codex app-server start: %w", err)
	}
	prog.watch(cmd.Process.Pid, "codex-app-server", onStatus)
	defer TrackWorker(cmd.Process, "codex-app-server")()
	go client.readLoop(stdout)
	defer client.shutdown()
	clientVersion := BuildRevision
	if clientVersion == "" {
		clientVersion = "dev"
	}
	callCtx, callCancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = client.call(callCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "captaincode", "title": "Captain Code", "version": clientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	callCancel()
	if err == nil {
		err = client.write(map[string]any{"method": "initialized", "params": map[string]any{}})
	}
	var threadID string
	if err == nil {
		thread, _ := codexAppServerSettings(leg, dir, task, effort)
		callCtx, callCancel = context.WithTimeout(ctx, 30*time.Second)
		var raw json.RawMessage
		raw, err = client.call(callCtx, "thread/start", thread)
		callCancel()
		if err == nil {
			var result struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Thread.ID == "" {
				err = errors.New("codex app-server thread/start returned no thread id")
			} else {
				threadID = result.Thread.ID
				stream.threadID = threadID
			}
		}
	}
	if err == nil {
		_, turn := codexAppServerSettings(leg, dir, task, effort)
		turn["threadId"] = threadID
		callCtx, callCancel = context.WithTimeout(ctx, 30*time.Second)
		var raw json.RawMessage
		raw, err = client.call(callCtx, "turn/start", turn)
		callCancel()
		if err == nil {
			var result struct {
				Turn codexAppTurn `json:"turn"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Turn.ID == "" {
				err = errors.New("codex app-server turn/start returned no turn id")
			} else {
				client.mu.Lock()
				client.threadID, client.turnID = threadID, result.Turn.ID
				client.mu.Unlock()
			}
		}
	}
	if err != nil {
		client.shutdown()
		cancel()
		_ = waitBounded(cmd, 3*time.Second)
		<-client.readDone
		return Result{}, err
	}
	detach := steer.Attach(leg, client.steer)
	defer detach()
	var completed codexAppTurn
	select {
	case completed = <-stream.turn:
	case err = <-client.fatal:
	}
	client.shutdown()
	cancel()
	prog.snapshotKids()
	waitErr := waitBounded(cmd, 3*time.Second)
	<-client.readDone
	if completed.Status != "" && errors.Is(waitErr, context.Canceled) {
		waitErr = nil
	}
	reportLeftovers(prog, onStatus)
	partial := strings.TrimSpace(stream.acc.String())
	if stopped.Load() || completed.Status == "interrupted" {
		return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
			fmt.Errorf("codex app-server stopped after %s: %w", time.Since(start).Round(time.Second), ErrInterrupted)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (capped != nil && capped.Load()) {
		if partial != "" {
			return Result{Text: partial, Partial: true, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil},
				fmt.Errorf("codex app-server did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
		}
		return Result{}, fmt.Errorf("codex app-server did not finish within %s: %w", time.Since(start).Round(time.Second), ErrWorkerTimeout)
	}
	if err != nil {
		return Result{Text: partial, Partial: partial != "", DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, err
	}
	if completed.Status == "failed" {
		message := "turn failed"
		if completed.Error != nil && completed.Error.Message != "" {
			message = completed.Error.Message
		}
		failure := classifyCodexCLIFailureLeg(leg, message)
		if errors.Is(failure, ErrRateLimited) {
			return partialResult(stream.acc.String(), message, start, onDelta), failure
		}
		return Result{}, failure
	}
	if waitErr != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return Result{}, fmt.Errorf("codex app-server: %w: %s", waitErr, truncateStr(message, 300))
		}
		return Result{}, fmt.Errorf("codex app-server: %w", waitErr)
	}
	return Result{Text: stream.acc.String(), Tokens: stream.tokens, TokenUsage: stream.detail, FirstOutputMs: stream.firstOutput, SessionID: stream.threadID, DurationMs: time.Since(start).Milliseconds(), Streamed: onDelta != nil}, nil
}
