package captaincode

import (
	"encoding/json"
	"strings"
)

// Progress reporting: the "what is it doing right now" channel.
//
// The wrapper forwards a worker's ANSWER text and nothing else, so a leg that
// thinks for two minutes or runs a long tool chain produced a completely
// silent stream - the TUI looked wedged while the worker was healthy (live
// 2026-07-29). Every runner now also reports tool activity on a separate
// status channel, which the brain streams as OpenAI `reasoning_content`
// deltas: visible in the TUI's thinking block, structurally outside the
// deliverable, and never scored.

// statusMaxDetail bounds a status line's target so it stays one TUI line.
const statusMaxDetail = 80

// workerStatus renders one progress line: the tool plus a short target.
func workerStatus(tool, detail string) string {
	tool = strings.Join(strings.Fields(tool), " ")
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > statusMaxDetail {
		detail = strings.TrimSpace(detail[:statusMaxDetail]) + "…"
	}
	switch {
	case tool == "" && detail == "":
		return ""
	case detail == "":
		return "⚙ " + tool
	case tool == "":
		return "⚙ " + detail
	}
	return "⚙ " + tool + " " + detail
}

// pickDetail returns the first non-empty string field of a tool's argument
// object, in the caller's preference order.
func pickDetail(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	for _, k := range keys {
		v, ok := args[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// claudeToolDetail summarizes a claude tool_use input: the path being read,
// the command being run, the URL being fetched. The command beats its own
// description - "go test ./..." says more than "run the tests".
func claudeToolDetail(raw json.RawMessage) string {
	return pickDetail(raw, "file_path", "command", "pattern", "url", "path",
		"notebook_path", "query", "description", "prompt")
}

// cursorToolStatus renders a cursor-agent `tool_call` object. Cursor wraps
// each call in a per-tool key - {"shellToolCall":{"args":{…}}} - and its args
// carry a written description, which reads better than the raw command
// (captured live 2026-07-29 from cursor-agent --output-format stream-json).
func cursorToolStatus(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var call map[string]json.RawMessage
	if json.Unmarshal(raw, &call) != nil {
		return ""
	}
	for key, val := range call {
		name := strings.TrimSuffix(key, "ToolCall")
		if name == key || name == "" {
			continue // not the tool member (e.g. "description", "toolCallId")
		}
		var inner struct {
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(val, &inner) != nil {
			continue
		}
		return workerStatus(name, pickDetail(inner.Args,
			"description", "command", "path", "file_path", "url", "query", "pattern"))
	}
	return ""
}

// A status feed of tool starts and "still working (1h20m)" heartbeats says
// the worker is alive, not what it is doing (2026-09-13). Two more kinds of
// line make it readable: the worker's own words between tool calls, and
// what a command came back with.

// Narration renders a worker's prose (a thought, a plan line, a finding) as
// one status line: the first sentence, bounded. "" when there is nothing
// worth a line (a fragment, a bare code fence, a one-word ack).
func Narration(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	// First non-empty line that is prose, not markup or a captain marker.
	for _, l := range strings.Split(t, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "#>*-• "))
		if l == "" || strings.HasPrefix(l, "```") || strings.HasPrefix(l, "|") || strings.HasPrefix(l, "[captain") {
			continue
		}
		if i := strings.IndexAny(l, ".!?"); i > 40 && i+1 < len(l) {
			l = l[:i+1]
		}
		if len(l) < 20 {
			continue
		}
		if len(l) > 160 {
			l = strings.TrimSpace(l[:160]) + "…"
		}
		return "💬 " + l
	}
	return ""
}

// Outcome renders what a command returned, as a line under its start line:
// the exit status when it failed, then the most telling line of output (the
// last non-empty one - "test result: ok. 40 passed" - or the first when the
// output is one line). "" for silent success.
func Outcome(output string, exitCode int, failed bool) string {
	lines := []string{}
	for _, l := range strings.Split(strings.TrimSpace(output), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	// A verdict line among the last few, strongest signal first. Without
	// one, a successful command has nothing to say: the last line of a `cat`
	// or a `ps` ("}", "mod tests;", a process row) is noise, not an outcome
	// (2026-09-13). A failure shows its last line when no verdict exists.
	peek := ""
	tail := lines
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
verdict:
	for _, sig := range []string{"test result", "error", "failed", "passed", "warning:"} {
		for i := len(tail) - 1; i >= 0; i-- {
			if strings.Contains(strings.ToLower(tail[i]), sig) {
				peek = tail[i]
				break verdict
			}
		}
	}
	if peek == "" && failed && len(lines) > 0 {
		peek = lines[len(lines)-1]
	}
	if len(peek) > 120 {
		peek = strings.TrimSpace(peek[:120]) + "…"
	}
	switch {
	case failed && peek != "":
		return "↳ ✗ exit " + itoa(exitCode) + ": " + peek
	case failed:
		return "↳ ✗ exit " + itoa(exitCode)
	case peek != "":
		return "↳ " + peek
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// toolResultText flattens a tool_result body (a string, or text blocks).
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}
	return ""
}
