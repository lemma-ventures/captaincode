package main

// `captain euclid mcp` - a stdio MCP server over the session's brains
// (MM38 E3). Multi-root by construction: every call composes the read set
// for the CURRENT project (the one opencode spawned this server for - it
// runs one per project directory, with that directory as cwd), tags each
// hit with its source brain, and never writes anywhere. Registered once, globally,
// in opencode.jsonc by `captain init` / `captain euclid init`; the stock
// opencode serve spawns it and workers see the tools as
// euclid_search / euclid_read_register / euclid_recent_runs / euclid_status.
//
// Transport: newline-delimited JSON-RPC 2.0 on stdin/stdout (MCP stdio).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var euclidTools = []map[string]any{
	{"name": "search", "description": "Search this repository's Euclid index - every governed doc and source file (catalog BM25 + full-text + relation-graph walk) - and the memory registers of the session's brains. Use it before re-deriving where something lives or what was decided. Returns ranked hits with paths.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "search terms"},
			"limit": map[string]any{"type": "integer", "description": "max hits per section (default 8)"}},
			"required": []string{"query"}}},
	{"name": "ask", "description": "Answer a question from the repository's memory in one pass: catalog + full text + multi-hop relation graph + git provenance (who/when/why). Slower than search; use for 'why is X like this', 'what depends on Y', 'when was Z decided'.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"},
			"lane":  map[string]any{"type": "string", "description": "doc | code | git | both (default) | all - 'all' adds a live git history scan"},
			"limit": map[string]any{"type": "integer", "description": "results per section (default 6)"}},
			"required": []string{"query"}}},
	{"name": "recall", "description": "Decision archaeology over git history: ranked commits (who, when, why) for a query, optionally narrowed to a path.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"},
			"path":  map[string]any{"type": "string", "description": "restrict to commits touching this path"},
			"limit": map[string]any{"type": "integer", "description": "default 12"}},
			"required": []string{"query"}}},
	{"name": "note", "description": "Record something in this project's memory while it is fresh: a decision you made (kind=decision), a question you left open (question), a fact worth remembering (memory), or a failure and its cause (failure). Appends one dated line to your write brain's ledger; the next distillation folds it into the registers.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"decision", "question", "memory", "failure"}},
			"text": map[string]any{"type": "string", "description": "one line: what, and why"}},
			"required": []string{"kind", "text"}}},
	{"name": "read_register", "description": "Read a whole register (BRAIN, WISDOM, SOUL, VISION, MAP, MEMORIES, FAILURES, DECISIONS, QUESTIONS) from a brain; source '' = the write brain, else a label from euclid_status.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"name":   map[string]any{"type": "string"},
			"source": map[string]any{"type": "string"}},
			"required": []string{"name"}}},
	{"name": "recent_runs", "description": "The last N journaled runs in this project (task, leg, outcome, files touched, log path).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"limit": map[string]any{"type": "integer", "description": "default 10"}}}},
	{"name": "status", "description": "Which brains this session reads and which one it writes.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
}

// mcpCwd is the project the tools operate on. opencode spawns one MCP server
// per project directory and runs it THERE, so the process cwd is the
// project; the launcher's CAPTAIN_CWD (the TUI's own folder) outranks it. The
// running brain is deliberately not asked: it serves every open TUI at once
// and has no single project (brain_workspace.go).
func mcpCwd() string {
	if d := os.Getenv("CAPTAIN_CWD"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

// euclidToolCall executes one tool and returns MCP content blocks.
func euclidToolCall(name string, args map[string]any, cwd string) (map[string]any, error) {
	text := func(s string) map[string]any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
	}
	intArg := func(k string, def int) int {
		if v, ok := args[k].(float64); ok && v > 0 {
			return int(v)
		}
		return def
	}
	strArg := func(k string) string {
		v, _ := args[k].(string)
		return strings.TrimSpace(v)
	}
	switch name {
	case "status":
		set := captaincode.SearchSet(cwd)
		if len(set) == 0 {
			return text("No Euclid brain for " + cwd + " (run `captain euclid init`)."), nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "project: %s\n", cwd)
		for _, b := range set {
			role := "read"
			if b.Writable {
				role = "write"
			}
			fmt.Fprintf(&sb, "- %s (%s, %s, weight %.1f): %s\n", b.Label, b.Kind, role, b.Weight, b.Root)
		}
		return text(sb.String()), nil
	case "search":
		q := strArg("query")
		if q == "" {
			return nil, fmt.Errorf("query is required")
		}
		var sb strings.Builder
		// The engine first: the repository's own corpus. The registers of
		// every readable brain follow, so memory and code answer together.
		if out, ok := captaincode.EngineSearch(cwd, q, intArg("limit", 8)); ok && out != "" {
			sb.WriteString(out)
			sb.WriteString("\n\n")
		}
		hits := captaincode.EuclidSearch(cwd, q, intArg("limit", 8))
		if len(hits) > 0 {
			sb.WriteString("# Memory registers\n")
			for i, h := range hits {
				fmt.Fprintf(&sb, "%d. [%s] %s:%d (score %.2f)\n%s\n\n", i+1, h.Source, h.File, h.Line, h.Score, h.Text)
			}
		}
		if strings.TrimSpace(sb.String()) == "" {
			return text("no hits in Euclid memory or the repository index for: " + q), nil
		}
		return text(strings.TrimSpace(sb.String())), nil
	case "ask":
		q := strArg("query")
		if q == "" {
			return nil, fmt.Errorf("query is required")
		}
		out, err := captaincode.EngineAsk(cwd, q, strArg("lane"), intArg("limit", 6))
		if err != nil {
			return text("ask unavailable: " + err.Error()), nil
		}
		return text(out), nil
	case "recall":
		q := strArg("query")
		if q == "" {
			return nil, fmt.Errorf("query is required")
		}
		out, err := captaincode.EngineRecall(cwd, q, strArg("path"), intArg("limit", 12))
		if err != nil {
			return text("recall unavailable: " + err.Error()), nil
		}
		return text(out), nil
	case "note":
		p, err := captaincode.AppendNote(cwd, strArg("kind"), strArg("text"), "worker")
		if err != nil {
			return nil, err
		}
		return text("noted in " + p), nil
	case "read_register":
		body, label, ok := captaincode.ReadRegister(cwd, strArg("source"), strArg("name"))
		if !ok {
			return text("no such register in the readable brains (" + strArg("name") + ")"), nil
		}
		return text("[" + label + "] " + strArg("name") + "\n\n" + body), nil
	case "recent_runs":
		runs := captaincode.RecentRuns(cwd, intArg("limit", 10))
		if len(runs) == 0 {
			return text("no journaled runs yet"), nil
		}
		var sb strings.Builder
		for _, e := range runs {
			fmt.Fprintf(&sb, "- %s [%s %s] %s", e.At.Format("2006-01-02 15:04"), e.Leg, e.Outcome, e.Task)
			if len(e.Files) > 0 {
				fmt.Fprintf(&sb, " - files: %s", strings.Join(e.Files, ", "))
			}
			if e.Log != "" {
				fmt.Fprintf(&sb, " - log: %s", e.Log)
			}
			sb.WriteString("\n")
		}
		return text(strings.TrimSpace(sb.String())), nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// serveEuclidMCP runs the JSON-RPC loop; cwdFn is a seam for tests.
func serveEuclidMCP(in io.Reader, out io.Writer, cwdFn func() string) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, err *rpcError) {
		if len(id) == 0 { // notification: no reply
			return
		}
		_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: err})
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(req.ID, map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "captain-euclid", "version": "0.1.0"},
			}, nil)
		case "notifications/initialized", "notifications/cancelled":
			// nothing to do
		case "ping":
			reply(req.ID, map[string]any{}, nil)
		case "tools/list":
			reply(req.ID, map[string]any{"tools": euclidTools}, nil)
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			res, err := euclidToolCall(p.Name, p.Arguments, cwdFn())
			if err != nil {
				reply(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil)
				continue
			}
			reply(req.ID, res, nil)
		default:
			reply(req.ID, nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
}

func cmdEuclidMCP() {
	serveEuclidMCP(os.Stdin, os.Stdout, mcpCwd)
}
