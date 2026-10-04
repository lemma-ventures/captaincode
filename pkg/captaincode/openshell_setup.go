package captaincode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// confirmOpenShellSetup accepts a file list and a test argv that the user
// wrote on this prompt. Environment values already set are the operator's
// choice and skip this step. Anything else returns the proposal and does
// not start a sandbox. PREPARED and PILOT are never taken from the prompt.
func confirmOpenShellSetup(ctx context.Context, dir, task string, allowed, verify []string) (string, []string, []string, error) {
	rest, parsedAllowed, parsedVerify, confirmed, err := parseOpenShellConfirm(task)
	if err != nil {
		return "", nil, nil, err
	}
	if confirmed {
		if err := checkOpenShellSetup(ctx, dir, parsedAllowed, parsedVerify); err != nil {
			return "", nil, nil, err
		}
		return rest, parsedAllowed, parsedVerify, nil
	}
	if len(allowed) == 0 {
		allowed = pathsCitedInOpenShellTask(ctx, dir, task)
	}
	if len(verify) == 0 {
		verify = detectedOpenShellVerify(dir)
	}
	return "", nil, nil, fmt.Errorf("openshell: confirm the file list and the test command before the sandbox starts.\nCAPTAIN_OPENSHELL_ALLOWED: %s\nCAPTAIN_OPENSHELL_VERIFY: %s\n%s\nCAPTAIN_OPENSHELL_PREPARED and CAPTAIN_OPENSHELL_PILOT stay machine settings. A model does not add paths",
		formatOpenShellAllowed(allowed), formatOpenShellVerify(verify), openShellConfirmLine(allowed, verify))
}

func parseOpenShellConfirm(task string) (rest string, allowed, verify []string, ok bool, err error) {
	task = strings.TrimSpace(task)
	if !strings.HasPrefix(task, "confirm ") {
		return task, nil, nil, false, nil
	}
	body := strings.TrimSpace(strings.TrimPrefix(task, "confirm "))
	if !strings.HasPrefix(body, "allowed=") {
		return "", nil, nil, false, errors.New("openshell: confirm requires allowed=<paths> verify=<json argv> before the task")
	}
	body = strings.TrimPrefix(body, "allowed=")
	idx := strings.Index(body, " verify=")
	if idx <= 0 {
		return "", nil, nil, false, errors.New("openshell: confirm requires allowed=<paths> verify=<json argv> before the task")
	}
	for _, p := range strings.Split(body[:idx], ",") {
		if p = strings.TrimSpace(p); p != "" {
			allowed = append(allowed, p)
		}
	}
	body = body[idx+len(" verify="):]
	dec := json.NewDecoder(strings.NewReader(body))
	if err = dec.Decode(&verify); err != nil {
		return "", nil, nil, false, fmt.Errorf("openshell: confirm verify: %w", err)
	}
	rest = strings.TrimSpace(body[dec.InputOffset():])
	if rest == "" {
		return "", nil, nil, false, errors.New("openshell: confirm requires the task after the file list and the test command")
	}
	return rest, allowed, verify, true, nil
}

func checkOpenShellSetup(ctx context.Context, dir string, allowed, verify []string) error {
	if len(allowed) == 0 || len(allowed) > 64 {
		return errors.New("openshell: confirm allowed= must name 1-64 files")
	}
	seen := map[string]bool{}
	for _, p := range allowed {
		if err := checkOpenShellPath(p); err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("openshell: confirm lists %s twice", p)
		}
		seen[p] = true
		kind, err := gitOutput(ctx, dir, nil, nil, "cat-file", "-t", "HEAD:"+p)
		if err != nil || strings.TrimSpace(string(kind)) != "blob" {
			return fmt.Errorf("openshell: %s is not a file in the pinned commit", p)
		}
	}
	if err := checkOpenShellArgv(verify); err != nil {
		return fmt.Errorf("openshell: confirm verify: %w", err)
	}
	return nil
}

func pathsCitedInOpenShellTask(ctx context.Context, dir, task string) []string {
	var out []string
	seen := map[string]bool{}
	for _, word := range strings.Fields(task) {
		p := strings.Trim(word, "`\"',:;()[]")
		if checkOpenShellPath(p) != nil || seen[p] {
			continue
		}
		kind, err := gitOutput(ctx, dir, nil, nil, "cat-file", "-t", "HEAD:"+p)
		if err != nil || strings.TrimSpace(string(kind)) != "blob" {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == 64 {
			break
		}
	}
	return out
}

func detectedOpenShellVerify(dir string) []string {
	command, ok := detectTestCommand(dir)
	if !ok {
		return nil
	}
	switch {
	case strings.HasPrefix(command, "go test"):
		return []string{"go", "test", "./..."}
	case strings.HasPrefix(command, "npm test"):
		return []string{"npm", "test"}
	case strings.HasPrefix(command, "python -m pytest"):
		return []string{"python", "-m", "pytest"}
	case strings.HasPrefix(command, "cargo test"):
		return []string{"cargo", "test"}
	case strings.HasPrefix(command, "make test"):
		return []string{"make", "test"}
	default:
		return nil
	}
}

func formatOpenShellAllowed(paths []string) string {
	if len(paths) == 0 {
		return "(none: name 1-64 files that exist in the pinned commit)"
	}
	return strings.Join(paths, ",")
}

func formatOpenShellVerify(argv []string) string {
	if len(argv) == 0 {
		return "(none: there is no default; pass a JSON argv)"
	}
	b, err := json.Marshal(argv)
	if err != nil {
		return "(unreadable)"
	}
	return string(b)
}

func openShellConfirmLine(allowed, verify []string) string {
	if len(allowed) == 0 || len(verify) == 0 {
		return "The confirm line needs both a file list and a test argv."
	}
	return "Start the sandbox with: confirm allowed=" + strings.Join(allowed, ",") + " verify=" + formatOpenShellVerify(verify) + " <task>"
}
