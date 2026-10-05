package captaincode

import (
	"encoding/json"
	"os"
)

var euclidToolCatalog = []map[string]any{
	{"name": "search", "description": "Search this repository's Euclid index - every governed doc and source file (catalog BM25 + full-text + relation-graph walk, fused with CodeIntel AST code intelligence). Journals are excluded by default. Use read_register for persona memory. Use it before re-deriving where something lives or what was decided. Returns ranked hits with paths.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"scope":            map[string]any{"type": "string", "enum": []string{"project", "docs", "config"}, "default": "project", "description": "project: docs and code; docs: project documents; config: tracked config paths only, no values"},
			"include_journals": map[string]any{"type": "boolean", "default": false, "description": "Include journal entries. False excludes journals from every result section."},
			"query":            map[string]any{"type": "string", "description": "search terms"},
			"lane":             map[string]any{"type": "string", "description": "code | doc | both (default: both) - 'code' for code symbols, functions, types; 'doc' for architecture/specs; 'both' for unified search"},
			"limit":            map[string]any{"type": "integer", "description": "max hits per section (default 8)"}},
			"required": []string{"query"}}},
	{"name": "code_search", "description": "Search repository source code declarations (functions, structs, types, methods, implementations) using Euclid fused with CodeIntel AST intelligence. Fast and token-efficient: returns exact file paths, line spans, and declarations without reading entire directories.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "function, type, symbol name, or code pattern to find"},
			"limit": map[string]any{"type": "integer", "description": "max code results (default 10)"}},
			"required": []string{"query"}}},
	{"name": "file_search", "description": "Search project files and paths. Select docs or config with scope. Journals are excluded unless include_journals is true. Config results contain paths only.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"scope":            map[string]any{"type": "string", "enum": []string{"project", "docs", "config"}, "default": "project", "description": "project: docs and code; docs: project documents; config: tracked config paths only, no values"},
			"include_journals": map[string]any{"type": "boolean", "default": false, "description": "Include journal entries. False excludes journals from every result section."},
			"query":            map[string]any{"type": "string", "description": "file name, path fragment, or topic"},
			"limit":            map[string]any{"type": "integer", "description": "max results (default 10)"}},
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

// ToolSchemaUsage measures only Captain's Euclid definitions. It is an
// estimate of offered context, not provider billing or every worker tool.
type ToolSchemaUsage struct {
	Profile         string `json:"profile"`
	Count           int    `json:"count"`
	Bytes           int    `json:"bytes"`
	EstimatedTokens int    `json:"estimated_tokens"`
	Source          string `json:"source"`
}

func EuclidToolProfile() string {
	if os.Getenv("CAPTAIN_EUCLID_TOOL_PROFILE") == "lean" {
		return "lean"
	}
	return "full"
}

func EuclidTools() []map[string]any {
	if EuclidToolProfile() != "lean" {
		return euclidToolCatalog
	}
	var out []map[string]any
	for _, tool := range euclidToolCatalog {
		if tool["name"] == "search" || tool["name"] == "code_search" {
			out = append(out, tool)
		}
	}
	return out
}

func EuclidToolSchemaUsage() *ToolSchemaUsage {
	tools := EuclidTools()
	raw, _ := json.Marshal(tools)
	return &ToolSchemaUsage{EuclidToolProfile(), len(tools), len(raw), (len(raw) + 3) / 4, "euclid_definitions_estimate"}
}
