package captaincode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The opencode TUI keeps its state - the prompt history behind the up arrow,
// the last model picked, its own preferences - in ONE directory for the whole
// machine ($XDG_STATE_HOME/opencode). So the arrow keys in arc scrolled
// through DLM's prompts (live 2026-09-12). The launcher gives each folder its
// own state directory instead, seeded on first use: preferences and model
// copied from the machine-wide state, the prompt history rebuilt from the
// folder's own sessions in opencode's database, so what a folder was asked
// before is what the arrow keys find there.

// PromptHistoryMax mirrors the TUI's MAX_HISTORY_ENTRIES.
const PromptHistoryMax = 50

// TuiStateHome is the XDG_STATE_HOME the launcher exports for a folder's TUI:
// ~/.captaincode/state/<folder slug> (opencode appends its own "opencode").
func TuiStateHome(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	slug := strings.Trim(strings.NewReplacer("/", "-", "\\", "-", " ", "_").Replace(filepath.Clean(dir)), "-")
	if slug == "" {
		slug = "root"
	}
	return filepath.Join(home, ".captaincode", "state", slug)
}

// GlobalStateDir is where stock opencode keeps the machine-wide state:
// $XDG_STATE_HOME/opencode, else ~/.local/state/opencode.
func GlobalStateDir() string {
	if v := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); v != "" {
		return filepath.Join(v, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "opencode")
}

// PrepareTuiState makes the folder's state directory ready and returns the
// XDG_STATE_HOME to export. A directory that already exists is left alone;
// a new one is seeded: kv.json and model.json copied from the global state,
// locks/ shared with it (plugin metadata edits stay mutually excluded across
// folders), and prompt-history.jsonl rebuilt from the folder's sessions.
func PrepareTuiState(dir, global, db string) (string, error) {
	stateHome := TuiStateHome(dir)
	if stateHome == "" {
		return "", fmt.Errorf("no home directory")
	}
	state := filepath.Join(stateHome, "opencode")
	if isDir(state) {
		return stateHome, nil
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return "", err
	}
	for _, f := range []string{"kv.json", "model.json"} {
		if b, err := os.ReadFile(filepath.Join(global, f)); err == nil {
			_ = os.WriteFile(filepath.Join(state, f), b, 0o644)
		}
	}
	if global != "" {
		locks := filepath.Join(global, "locks")
		_ = os.MkdirAll(locks, 0o755)
		_ = os.Symlink(locks, filepath.Join(state, "locks"))
	}
	entries := PromptHistoryFromDB(db, dir, PromptHistoryMax)
	if len(entries) > 0 {
		var sb strings.Builder
		for _, e := range entries {
			b, _ := json.Marshal(map[string]any{"input": e, "parts": []any{}, "mode": "normal"})
			sb.Write(b)
			sb.WriteByte('\n')
		}
		_ = os.WriteFile(filepath.Join(state, "prompt-history.jsonl"), []byte(sb.String()), 0o644)
	}
	return stateHome, nil
}

// PromptHistoryFromDB reads the last n prompts typed in dir, oldest first,
// from opencode's session database: the text of user messages in the
// folder's top-level sessions (workers are children and never count). Replayed
// worker transcripts ("[system]…" pasted into a resumed worker session) and
// oversized pastes are skipped. Best effort: no sqlite3, no database, no rows
// all mean an empty history, never an error.
func PromptHistoryFromDB(db, dir string, n int) []string {
	if db == "" || dir == "" || !isFile(db) {
		return nil
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil
	}
	q := fmt.Sprintf(`select json_extract(p.data,'$.text') from part p
  join message m on p.message_id = m.id
  join session s on m.session_id = s.id
  where s.directory = %s and s.parent_id is null
    and json_extract(m.data,'$.role') = 'user' and json_extract(p.data,'$.type') = 'text'
  order by m.time_created desc, p.time_created asc limit %d`, sqlQuote(dir), n*3)
	out, err := exec.Command(sqlite, "-json", "-readonly", db, q).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	var rows []map[string]any
	if json.Unmarshal(out, &rows) != nil {
		return nil
	}
	var newestFirst []string
	for _, r := range rows {
		text, _ := r[`json_extract(p.data,'$.text')`].(string)
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "[system]") || len(text) > 8000 {
			continue
		}
		if len(newestFirst) > 0 && newestFirst[len(newestFirst)-1] == text {
			continue
		}
		newestFirst = append(newestFirst, text)
		if len(newestFirst) == n {
			break
		}
	}
	out2 := make([]string, 0, len(newestFirst))
	for i := len(newestFirst) - 1; i >= 0; i-- {
		out2 = append(out2, newestFirst[i])
	}
	return out2
}

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// LastSessionFromDB is the newest top-level session opencode holds for
// exactly this folder, or "" - what `-c` should resume. Stock opencode's
// --continue picks the newest session of the PROJECT (the git root), so a
// probe run in a since-deleted subfolder (prototype/opencode-plugin,
// 2026-09-11) was resumed in captaincode two days later and every prompt
// died on FileSystem.realPath ENOENT before reaching the brain
// (2026-09-13). The directory must still exist: a session whose folder is
// gone cannot take a prompt.
func LastSessionFromDB(db, dir string) string {
	if db == "" || dir == "" || !isFile(db) || !isDir(dir) {
		return ""
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return ""
	}
	q := fmt.Sprintf(`select id from session where directory = %s and parent_id is null order by time_updated desc limit 1`, sqlQuote(dir))
	out, err := exec.Command(sqlite, "-readonly", db, q).Output()
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(out))
	if !strings.HasPrefix(id, "ses_") {
		return ""
	}
	return id
}
