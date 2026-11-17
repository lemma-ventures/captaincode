package captaincode

import (
	"fmt"
	"path/filepath"
	"strings"
)

const ReadOnlyTurnMarker = "[captain] Remote prompt scope: read only."
const ReadOnlyTurnEnv = "CAPTAIN_READ_ONLY_TURN"

func IsReadOnlyTurn(prompt string) bool { return strings.Contains(prompt, ReadOnlyTurnMarker) }

func ReadOnlyTurnDecision(a GateAction) (string, bool) {
	if why := SentTurnRefusal(a); why != "" {
		return why, false
	}
	tool := strings.ToLower(a.Tool)
	switch tool {
	case "read", "notebookread", "grep", "glob", "ls", "list":
		if !readOnlyPathInWorkspace(a.Path, a.Cwd) {
			return "refused: a remote read-only prompt may only inspect its linked repository", false
		}
		return "", false
	case "bash", "shell", "run", "execute", "terminal":
		fields := strings.Fields(strings.TrimSpace(a.Command))
		if len(fields) < 2 || filepath.Base(fields[0]) != "git" || !readOnlyGit[fields[1]] || !plainlyReadOnly(a.Command) || !remoteReadOnlyGit(fields) {
			return "refused: a remote read-only prompt may only read files, search and inspect repository history", false
		}
	default:
		return fmt.Sprintf("refused: a remote read-only prompt may not use the %s tool", a.Tool), false
	}
	if _, _, err := JailCommand(orDot(a.Cwd), "true"); err != nil {
		return "refused: a remote read-only prompt cannot run a shell command outside a sandbox (" + err.Error() + ")", false
	}
	return "", true
}

func remoteReadOnlyGit(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	switch fields[1] {
	case "status", "blame":
		return false
	case "diff", "log", "show":
		noExtDiff, noTextConv := false, false
		for _, arg := range fields[2:] {
			if strings.Contains(arg, "%G") {
				return false
			}
			switch arg {
			case "--no-index", "--show-signature", "--textconv":
				return false
			case "--no-ext-diff":
				noExtDiff = true
			case "--no-textconv":
				noTextConv = true
			}
		}
		return noExtDiff && noTextConv
	default:
		return true
	}
}

func readOnlyPathInWorkspace(path, cwd string) bool {
	if cwd == "" {
		return false
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	target := root
	if path != "" {
		target = path
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
