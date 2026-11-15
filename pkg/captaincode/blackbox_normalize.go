package captaincode

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

func NormalizeBlackboxAction(host, phase, tool string, raw json.RawMessage) (BlackboxAction, []string) {
	action := BlackboxAction{Schema: 1, Host: strings.ToLower(host), Phase: phase, Tool: tool, ParseStatus: "parsed"}
	gaps := map[string]bool{}
	if kind, known := BlackboxKindForTool(tool); known {
		action.Kind = kind
	} else {
		action.Kind = "unknown"
		gaps["unknown_tool"] = true
	}
	if len(raw) > BlackboxMaxActionBytes {
		action.Truncated = true
		gaps["action_size"] = true
		return action, sortedBlackboxGaps(gaps)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil {
		action.ParseStatus = "unsupported"
		return action, []string{"invalid_arguments"}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		action.ParseStatus = "unsupported"
		return action, []string{"trailing_arguments"}
	}
	state := blackboxNormalizeState{action: &action, gaps: gaps, paths: map[string]bool{}}
	state.walk(input, 0, "")
	if state.nodes > 2048 {
		gaps["argument_depth_or_nodes"] = true
	}
	if action.Kind == "shell" {
		blackboxParseShell(&action, gaps)
	}
	if len(gaps) != 0 {
		action.ParseStatus = "unsupported"
	}
	return action, sortedBlackboxGaps(gaps)
}

type blackboxNormalizeState struct {
	action *BlackboxAction
	gaps   map[string]bool
	paths  map[string]bool
	nodes  int
}

func (state *blackboxNormalizeState) walk(value any, depth int, parent string) {
	state.nodes++
	if depth > 16 || state.nodes > 2048 {
		state.gaps["argument_depth_or_nodes"] = true
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			switch lower {
			case "filepath", "file_path", "notebook_path":
				state.addPath(child)
			case "path":
				if parent == "files" || state.action.Kind == "file" {
					state.addPath(child)
				}
			case "command", "cmd":
				if text, ok := child.(string); ok && state.action.Command == "" {
					state.action.Command = text
				}
			case "url":
				if text, ok := child.(string); ok && len(state.action.URLs) < blackboxMaxTargets {
					state.action.URLs = append(state.action.URLs, text)
				}
			case "content", "newstring", "new_string", "oldstring", "old_string", "text":
				if text, ok := child.(string); ok {
					if len(state.action.Text)+len(text)+1 > blackboxMaxText {
						state.action.Truncated = true
						state.gaps["text_limit"] = true
					} else {
						state.action.Text += text + "\n"
					}
				}
			}
			state.walk(child, depth+1, lower)
		}
	case []any:
		for _, child := range typed {
			state.walk(child, depth+1, parent)
		}
	}
}

func (state *blackboxNormalizeState) addPath(value any) {
	text, ok := value.(string)
	if !ok || text == "" {
		state.gaps["file_target_type"] = true
		return
	}
	if state.paths[text] {
		return
	}
	if len(state.action.Paths) >= blackboxMaxTargets {
		state.action.Truncated = true
		state.gaps["file_target_limit"] = true
		return
	}
	state.paths[text] = true
	state.action.Paths = append(state.action.Paths, text)
}

func blackboxParseShell(action *BlackboxAction, gaps map[string]bool) {
	command := strings.TrimSpace(action.Command)
	if command == "" {
		return
	}
	if strings.ContainsAny(command, "'\"`$|;&<>\n") {
		gaps["unsupported_shell_syntax"] = true
		return
	}
	fields := strings.Fields(command)
	if len(fields) >= 2 && fields[0] == "go" && fields[1] == "get" {
		gaps["unsupported_package_syntax"] = true
		return
	}
	if len(fields) < 3 {
		return
	}
	manager, operation := fields[0], fields[1]
	if manager == "npm" && operation == "i" {
		operation = "install"
	}
	if !((manager == "npm" || manager == "pip" || manager == "pip3") && operation == "install") {
		if operation == "install" || operation == "add" || operation == "get" {
			gaps["unsupported_package_manager"] = true
		}
		return
	}
	for _, field := range fields[2:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		candidate := BlackboxPackage{Ecosystem: manager, Name: field}
		if manager == "pip3" {
			candidate.Ecosystem = "pip"
		}
		if manager == "npm" {
			if at := strings.LastIndex(field, "@"); at > 0 {
				candidate.Name, candidate.Version = field[:at], field[at+1:]
			}
		} else if name, version, found := strings.Cut(field, "=="); found {
			candidate.Name, candidate.Version = name, version
		} else if strings.ContainsAny(field, "<>~^=!*") {
			gaps["unsupported_package_syntax"] = true
			continue
		}
		if candidate.Ecosystem == "pip" {
			candidate.Ecosystem = "pypi"
		}
		if !validBlackboxPackage(candidate.Ecosystem, candidate.Name, candidate.Version, true) {
			gaps["unsupported_package_syntax"] = true
			continue
		}
		if len(action.Packages) >= blackboxMaxTargets {
			action.Truncated = true
			gaps["package_target_limit"] = true
			return
		}
		action.Packages = append(action.Packages, candidate)
	}
}

func sortedBlackboxGaps(gaps map[string]bool) []string {
	result := make([]string, 0, len(gaps))
	for gap := range gaps {
		result = append(result, gap)
	}
	sort.Strings(result)
	return result
}
