package main

// `captain redact` - the redaction engine at the tool boundary, for the
// processes that are not the brain: the opencode plugin (tool output on the
// way in, tool arguments on the way back) and Claude Code's PreToolUse hook.
//
//	captain redact                 stdin text → stdout with secrets/identity masked
//	captain redact --restore       stdin text → stdout with placeholders and stand-ins restored
//	captain redact --check <path>  exit 3 (reason on stdout) when a worker must not read the file
//	captain redact --hook          Claude Code PreToolUse hook: restores placeholders in the
//	                               tool input, refuses secret files (hook JSON in, hook JSON out)
//	captain redact --stats         what the tool boundary redacted (for the sidebar)
//
// Every masking call appends one line to ~/.captaincode/redact.log so the
// sidebar's shield line can count what never left the machine.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func redactLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "redact.log")
}

func logRedaction(source string, rep captaincode.Redaction) {
	if rep.Total() == 0 {
		return
	}
	p := redactLogPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	kinds, _ := json.Marshal(rep.Kinds)
	fmt.Fprintf(f, "%s %s secrets=%d identity=%d %s\n", time.Now().Format(time.RFC3339), source, rep.Secrets, rep.Identity, kinds)
}

// boundaryStats sums the log: what the tool boundary masked, by day.
func boundaryStats() map[string]any {
	f, err := os.Open(redactLogPath())
	if err != nil {
		return map[string]any{"secrets": 0, "identity": 0, "today": 0}
	}
	defer f.Close()
	var secrets, identity, today int
	day := time.Now().Format("2006-01-02")
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 {
			continue
		}
		var s, i int
		fmt.Sscanf(fs[2], "secrets=%d", &s)
		fmt.Sscanf(fs[3], "identity=%d", &i)
		secrets += s
		identity += i
		if strings.HasPrefix(fs[0], day) {
			today += s
		}
	}
	return map[string]any{"secrets": secrets, "identity": identity, "today": today}
}

func cmdRedact(args []string) {
	mode := "mask"
	source := "tool"
	var checkPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--restore":
			mode = "restore"
		case "--hook":
			mode = "hook"
		case "--stats":
			mode = "stats"
		case "--check":
			mode = "check"
			if i+1 < len(args) {
				checkPath = args[i+1]
				i++
			}
		case "--source":
			if i+1 < len(args) {
				source = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println("usage: captain redact [--restore | --check <path> | --hook | --stats] [--source name]")
			return
		}
	}
	switch mode {
	case "stats":
		writeStdoutJSON(boundaryStats())
	case "check":
		if deny, why := captaincode.IsSecretFile(checkPath); deny {
			fmt.Printf("captain: %s is a %s - workers do not read it (CAPTAIN_REDACT=%s)\n", checkPath, why, captaincode.RedactMode())
			os.Exit(3)
		}
	case "hook":
		redactHook()
	default:
		in, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal(err)
		}
		if mode == "restore" {
			out, _ := captaincode.Restore(string(in))
			_, _ = os.Stdout.WriteString(out)
			return
		}
		out, rep := captaincode.Redact(string(in))
		logRedaction(source, rep)
		_, _ = os.Stdout.WriteString(out)
	}
}

func writeStdoutJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// redactHook is the Claude Code PreToolUse hook body. Input (stdin):
// {"tool_name":"Write","tool_input":{...}}. It refuses reads of secret files
// and restores placeholders / identity stand-ins in every string of the
// tool input, so a file the model writes with [[secret:…]] in it lands with
// the real value and a command naming /home/captain runs in the real home.
func redactHook() {
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return // not our shape: allow silently
	}
	if in.ToolName == "Read" || in.ToolName == "Grep" || in.ToolName == "Glob" {
		if p, _ := in.ToolInput["file_path"].(string); p != "" {
			if deny, why := captaincode.IsSecretFile(p); deny {
				writeStdoutJSON(map[string]any{"hookSpecificOutput": map[string]any{
					"hookEventName": "PreToolUse", "permissionDecision": "deny",
					"permissionDecisionReason": p + " is a " + why + " - captain workers do not read it",
				}})
				return
			}
		}
	}
	changed := 0
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			out, n := captaincode.Restore(x)
			changed += n
			return out
		case map[string]any:
			for k, c := range x {
				x[k] = walk(c)
			}
			return x
		case []any:
			for i, c := range x {
				x[i] = walk(c)
			}
			return x
		}
		return v
	}
	walk(in.ToolInput)
	if changed == 0 {
		return
	}
	writeStdoutJSON(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "updatedInput": in.ToolInput,
	}})
}
