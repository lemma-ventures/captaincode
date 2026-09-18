package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The TUI's prompt history is machine-wide; each folder gets its own state
// directory, seeded from the folder's past sessions in opencode's database.

func fakeOpencodeDB(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	sql := `
create table session(id text primary key, directory text, parent_id text, time_created integer);
create table message(id text primary key, session_id text, time_created integer, data text);
create table part(id text primary key, message_id text, time_created integer, data text);
insert into session values('ses_arc','/src/arc',null,1),('ses_dlm','/src/dlm',null,2),('ses_worker','/src/arc','ses_arc',3);
insert into message values
 ('m1','ses_arc',10,'{"role":"user"}'),('m2','ses_arc',20,'{"role":"assistant"}'),('m3','ses_arc',30,'{"role":"user"}'),
 ('m4','ses_dlm',40,'{"role":"user"}'),('m5','ses_worker',50,'{"role":"user"}'),('m6','ses_arc',60,'{"role":"user"}'),('m7','ses_arc',70,'{"role":"user"}');
insert into part values
 ('p1','m1',10,'{"type":"text","text":"/grok review the arc roadmap"}'),
 ('p2','m2',20,'{"type":"text","text":"here is the review"}'),
 ('p3','m3',30,'{"type":"text","text":"/frontier do O01-a"}'),
 ('p4','m4',40,'{"type":"text","text":"finish Phase 1 in DLM"}'),
 ('p5','m5',50,'{"type":"text","text":"worker brief, never a prompt"}'),
 ('p6','m6',60,'{"type":"text","text":"[system]\nYou are opencode… a replayed worker transcript"}'),
 ('p7','m7',70,'{"type":"text","text":"/codex-cli do O01-a"}');`
	cmd := exec.Command("sqlite3", db)
	cmd.Stdin = strings.NewReader(sql)
	require.NoError(t, cmd.Run())
	return db
}

func TestPromptHistoryFromDBIsTheFoldersOwnPromptsOldestFirst(t *testing.T) {
	db := fakeOpencodeDB(t)
	got := PromptHistoryFromDB(db, "/src/arc", 50)
	assert.Equal(t, []string{"/grok review the arc roadmap", "/frontier do O01-a", "/codex-cli do O01-a"}, got,
		"user prompts of the folder's top-level sessions; no DLM prompt, no worker brief, no replayed transcript, no assistant text")
	assert.Equal(t, []string{"/frontier do O01-a", "/codex-cli do O01-a"}, PromptHistoryFromDB(db, "/src/arc", 2), "the last n")
	assert.Empty(t, PromptHistoryFromDB(db, "/src/nowhere", 50))
	assert.Empty(t, PromptHistoryFromDB(filepath.Join(t.TempDir(), "missing.db"), "/src/arc", 50), "no database: empty, never an error")
}

func TestPrepareTuiStateSeedsAFoldersOwnState(t *testing.T) {
	db := fakeOpencodeDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".local", "state", "opencode")
	require.NoError(t, os.MkdirAll(global, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(global, "kv.json"), []byte(`{"thinking_mode":"show"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(global, "prompt-history.jsonl"), []byte(`{"input":"finish Phase 1 in DLM","parts":[],"mode":"normal"}`+"\n"), 0o644))

	stateHome, err := PrepareTuiState("/src/arc", global, db)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stateHome, filepath.Join(home, ".captaincode", "state")), stateHome)
	state := filepath.Join(stateHome, "opencode")
	kv, _ := os.ReadFile(filepath.Join(state, "kv.json"))
	assert.Contains(t, string(kv), "thinking_mode", "preferences carry over")
	hist, _ := os.ReadFile(filepath.Join(state, "prompt-history.jsonl"))
	assert.Contains(t, string(hist), "/frontier do O01-a", "the folder's own prompts are what the arrow keys find")
	assert.NotContains(t, string(hist), "Phase 1 in DLM", "…not another folder's")
	target, err := os.Readlink(filepath.Join(state, "locks"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(global, "locks"), target, "locks stay machine-wide")

	// A second launch leaves the folder's state alone.
	require.NoError(t, os.WriteFile(filepath.Join(state, "prompt-history.jsonl"), []byte("mine\n"), 0o644))
	again, err := PrepareTuiState("/src/arc", global, db)
	require.NoError(t, err)
	assert.Equal(t, stateHome, again)
	hist, _ = os.ReadFile(filepath.Join(state, "prompt-history.jsonl"))
	assert.Equal(t, "mine\n", string(hist))
}

// -c must resume a session of THIS folder, never the project's newest one
// in a subfolder that no longer exists (2026-09-13).
func TestLastSessionIsTheFoldersOwnNewest(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	db := filepath.Join(root, "opencode.db")
	sql := `create table session (id text primary key, directory text, parent_id text, time_updated integer);
insert into session values ('ses_old_here', '` + repo + `', null, 100);
insert into session values ('ses_new_here', '` + repo + `', null, 200);
insert into session values ('ses_gone_sub', '` + repo + `/prototype/gone', null, 300);
insert into session values ('ses_worker', '` + repo + `', 'ses_new_here', 400);`
	require.NoError(t, exec.Command("sqlite3", db, sql).Run())
	assert.Equal(t, "ses_new_here", LastSessionFromDB(db, repo), "the newest top-level session of the folder itself")
	assert.Equal(t, "", LastSessionFromDB(db, filepath.Join(repo, "prototype", "gone")), "a folder that is gone cannot be resumed")
	assert.Equal(t, "", LastSessionFromDB(db, filepath.Join(root, "other")))
}
