package captaincode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func memoryFixture(t *testing.T, brain EuclidBrain) (string, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys,pathlib,os
state=pathlib.Path(sys.argv[1])
brain=sys.argv[2]
for line in sys.stdin:
 req=json.loads(line); mid=req.get('id'); method=req['method']
 if mid is None: continue
 if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':n} for n in ['euclid_status','euclid_orientation','euclid_record_event','euclid_learn_inputs','euclid_learn_commit']]}
 else:
  args=req['params']['arguments']; name=req['params']['name']
  db=json.loads(state.read_text()) if state.exists() else {'events':[], 'cursor':0, 'lessons':[]}
  if name=='euclid_status': value={'brain':brain,'writable':True}
  elif name=='euclid_orientation': value={'items':[{'source':'lesson:accepted','text':'Use the accepted rule.'}]}
  elif name=='euclid_record_event':
   event=args['event']
   if not any(e['id']==event['id'] for e in db['events']): db['events'].append(event)
   state.write_text(json.dumps(db)); value={'id':event['id']}
  elif name=='euclid_learn_inputs': value={'revision':str(len(db['events']))+':'+str(db['cursor']),'events':db['events'][db['cursor']:db['cursor']+40], 'lessons':db['lessons']}
  else:
   if pathlib.Path(str(state)+'.fail').exists():
    print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'isError':True,'content':[]}}),flush=True); continue
   assert args['event_ids']==[e['id'] for e in db['events'][db['cursor']:db['cursor']+len(args['event_ids'])]]
   assert args['expected_revision']==str(len(db['events']))+':'+str(db['cursor'])
   db['cursor']+=len(args['event_ids']); db['lessons']+=args['lessons']
   state.write_text(json.dumps(db)); value={'committed':True}
  result={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0600))
	config := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script, state, brain.Root}})
	require.NoError(t, os.WriteFile(config, data, 0600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")
	return config, state
}

func TestMemoryMCPAuthoritativeBatchAndRetry(t *testing.T) {
	root, brain := learnFixture(t)
	_, state := memoryFixture(t, brain)
	before, err := os.ReadFile(filepath.Join(root, "WISDOM.md"))
	require.NoError(t, err)
	propose := func(prompt string) (string, error) {
		require.Contains(t, prompt, "captain:journal:")
		require.Contains(t, prompt, "captain:memory:")
		return `{"summary":"consumed","lessons":[]}`, nil
	}
	require.NoError(t, os.WriteFile(state+".fail", []byte("fail"), 0600))
	_, err = LearnLoop(brain, 2, true, propose)
	require.Error(t, err)
	require.NoError(t, os.Remove(state+".fail"))
	passes, err := LearnLoop(brain, 2, true, propose)
	require.NoError(t, err)
	require.Len(t, passes, 1)
	passes, err = LearnLoop(brain, 2, true, func(string) (string, error) { t.Fatal("consumed events were replayed"); return "", nil })
	require.NoError(t, err)
	require.Empty(t, passes)
	after, err := os.ReadFile(filepath.Join(root, "WISDOM.md"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = os.Stat(filepath.Join(root, "journal/.learn-state.json"))
	require.True(t, os.IsNotExist(err))
	data, err := os.ReadFile(state)
	require.NoError(t, err)
	var db struct {
		Events []memoryEvent
		Cursor int
	}
	require.NoError(t, json.Unmarshal(data, &db))
	require.Len(t, db.Events, 2)
	require.Equal(t, 2, db.Cursor)
	require.Contains(t, memoryOrientation([]EuclidBrain{brain}, 2500), "Use the accepted rule.")
}

func TestMemoryMCPDryRunAndWrongScope(t *testing.T) {
	_, brain := learnFixture(t)
	_, state := memoryFixture(t, brain)
	_, err := LearnLoop(brain, 1, false, func(string) (string, error) { t.Fatal("no imported events in dry run"); return "", nil })
	require.NoError(t, err)
	_, err = os.Stat(state)
	require.True(t, os.IsNotExist(err))
	brain.Root = filepath.Join(t.TempDir(), "other")
	_, err = LearnLoop(brain, 1, true, func(string) (string, error) { return "", nil })
	require.ErrorContains(t, err, "different brain")
}

func TestMemoryMCPBacklogIncludesEveryEntry(t *testing.T) {
	root, brain := learnFixture(t)
	var lines strings.Builder
	for i := 0; i < 45; i++ {
		data, _ := json.Marshal(JournalEntry{Task: strings.Repeat("x", i+1)})
		lines.Write(data)
		lines.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "journal/activity-2026-10-01.jsonl"), []byte(lines.String()), 0600))
	_, state := memoryFixture(t, brain)
	passes, err := LearnLoop(brain, 3, true, func(string) (string, error) { return `{"summary":"batch","lessons":[]}`, nil })
	require.NoError(t, err)
	require.Len(t, passes, 2)
	require.Equal(t, 40, passes[0].Journal)
	require.Equal(t, 6, passes[1].Journal)
	data, err := os.ReadFile(state)
	require.NoError(t, err)
	var db struct{ Cursor int }
	require.NoError(t, json.Unmarshal(data, &db))
	require.Equal(t, 46, db.Cursor)
}

func TestMemoryMCPLiveLifecycle(t *testing.T) {
	server := os.Getenv("EUCLID_TEST_SERVER")
	if server == "" {
		t.Skip("set EUCLID_TEST_SERVER to an installed Euclid MCP server")
	}
	root, brain := learnFixture(t)
	config := filepath.Join(t.TempDir(), "server.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{server}, "env": map[string]string{"EUCLID_BRAIN_SCOPE": "main", "EUCLID_ROOT": root, "EUCLID_ALLOW_WRITES": "1"}})
	require.NoError(t, os.WriteFile(config, data, 0600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")
	_, err := LearnLoop(brain, 1, true, func(prompt string) (string, error) {
		var batch memoryBatch
		require.NoError(t, json.Unmarshal([]byte(prompt[strings.Index(prompt, "\n")+1:]), &batch))
		reply, _ := json.Marshal(map[string]any{"summary": "candidate", "lessons": []memoryLesson{{ID: "lesson:batch", Text: "Preserve unread events.", Evidence: []string{batch.Events[0].ID}}}})
		return string(reply), nil
	})
	require.NoError(t, err)
	require.NotContains(t, memoryOrientation([]EuclidBrain{brain}, 2500), "Preserve unread events.")
	event := map[string]any{"id": "outcome:later", "kind": "task_outcome", "task_id": "task:later", "source_revision": "fixture:2", "text": "Five unread inputs remain.", "outcome": "accepted", "evidence": []string{"lesson:batch"}}
	var recorded struct {
		Revision string `json:"revision"`
	}
	require.NoError(t, memoryCall(context.Background(), memoryConn{Config: config}, "euclid_record_event", map[string]any{"event": event}, &recorded))
	require.NoError(t, memoryCall(context.Background(), memoryConn{Config: config}, "euclid_review_lesson", map[string]any{"lesson_id": "lesson:batch", "status": "accepted", "evidence_id": "outcome:later", "expected_revision": recorded.Revision}, nil))
	require.Contains(t, memoryOrientation([]EuclidBrain{brain}, 2500), "Preserve unread events.")
}

func TestMemoryMCPBrainAliasAndScopeMapping(t *testing.T) {
	_, brain := learnFixture(t)
	config, _ := memoryFixture(t, brain)
	dir := t.TempDir()
	mapPath := filepath.Join(dir, "memory-map.json")
	mapping := map[string]string{
		"main": config,
	}
	mapData, _ := json.Marshal(mapping)
	require.NoError(t, os.WriteFile(mapPath, mapData, 0o600))

	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", mapPath)
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "")

	resolved, err := memoryMCPConfig(brain)
	require.NoError(t, err)
	require.Equal(t, memoryConn{Config: config}, resolved)

	conn, err := memoryConnection(brain, false)
	require.NoError(t, err)
	require.Equal(t, memoryConn{Config: config}, conn)
}

// One shared server config serves every brain: main, a repository's shared
// brain and a developer's own. The fake server resolves its brain from the
// environment the way Euclid's does (EUCLID_BRAIN_SCOPE, EUCLID_ROOT,
// EUCLID_HANDLE), and from its working folder when nothing is set - which is
// how every brain but one failed "bound to a different brain".
func TestMemoryMCPSharedConfigServesEveryBrain(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys,os,pathlib
scope=os.environ.get('EUCLID_BRAIN_SCOPE','project'); root=os.environ.get('EUCLID_ROOT') or os.getcwd()
brain=root if scope=='main' else os.path.join(root,'.euclid')
if scope=='developer': brain=os.path.join(brain,'developers',os.environ.get('EUCLID_HANDLE','me'))
for line in sys.stdin:
 req=json.loads(line); mid=req.get('id')
 if mid is None: continue
 if req['method']=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif req['method']=='tools/list': result={'tools':[{'name':'euclid_status'},{'name':'euclid_orientation'}]}
 else:
  name=req['params']['name']
  value={'brain':brain,'writable':os.environ.get('EUCLID_ALLOW_WRITES')=='1'} if name=='euclid_status' else {'items':[{'source':'lesson:x','text':'from '+brain}]}
  result={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0o600))
	config := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script}})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

	repo := t.TempDir()
	brains := []EuclidBrain{
		{Root: t.TempDir(), Kind: "main", Label: "main"},
		{Root: filepath.Join(repo, ".euclid"), Kind: "repo", Label: "repo:x"},
		{Root: filepath.Join(repo, ".euclid", "developers", "dev-a"), Kind: "developer", Label: "me@x", Writable: true},
		{Root: filepath.Join(t.TempDir(), ".euclid"), Kind: "linked", Label: "repo:y"},
	}
	for _, b := range brains {
		_, err := memoryConnection(b, false)
		require.NoError(t, err, b.Label)
	}
	_, err := memoryConnection(brains[2], true)
	require.NoError(t, err, "the write brain gets a writable server")
	for _, b := range brains {
		require.Contains(t, memoryOrientation([]EuclidBrain{b}, 2500), "from "+b.Root, b.Label)
	}
	_, err = brainServerEnv(EuclidBrain{Root: "/x/notes", Kind: "developer"})
	require.Error(t, err)
}

func TestJournalRunWithMemoryMCPRecordsEventAndAppendsJournalPage(t *testing.T) {
	root, brain := learnFixture(t)
	t.Setenv("EUCLID_HOME", root)
	_, state := memoryFixture(t, brain)

	entry := JournalEntry{
		Kind:    "worker",
		Task:    "implement bounded indexing",
		Leg:     "claude",
		Outcome: "ok",
		Tokens:  1234,
	}
	path, err := JournalRun(root, entry)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(path, ".jsonl"), "returns ledger line path")

	// 1. Verify MCP recorded the event
	stateBytes, err := os.ReadFile(state)
	require.NoError(t, err)
	var db struct {
		Events []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(stateBytes, &db))
	require.NotEmpty(t, db.Events, "event must be recorded in MCP store")
	require.Contains(t, db.Events[0].Text, "implement bounded indexing")

	// 2. Verify local jsonl ledger was written
	jsonlBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(jsonlBytes), "implement bounded indexing")

	// 3. Verify local markdown journal page was written
	pages, err := filepath.Glob(filepath.Join(brain.Root, "journal", "*_claude-*.md"))
	require.NoError(t, err)
	require.Len(t, pages, 1, "markdown journal page written as dashboard clock")
	mdBytes, err := os.ReadFile(pages[0])
	require.NoError(t, err)
	require.Contains(t, string(mdBytes), "**Tokens-Spent**: 1234")
}
