package captaincode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
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
  if name=='euclid_status': value={'brain':brain,'writable':os.environ.get('EUCLID_ALLOW_WRITES')=='1'}
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
	require.Contains(t, memoryOrientation([]EuclidBrain{brain}, 2500, ""), "Use the accepted rule.")
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
	require.NotContains(t, memoryOrientation([]EuclidBrain{brain}, 2500, ""), "Preserve unread events.")
	event := map[string]any{"id": "outcome:later", "kind": "task_outcome", "task_id": "task:later", "source_revision": "fixture:2", "text": "Five unread inputs remain.", "outcome": "accepted", "evidence": []string{"lesson:batch"}}
	var recorded struct {
		Revision string `json:"revision"`
	}
	require.NoError(t, memoryCall(context.Background(), memoryConn{Config: config}, "euclid_record_event", map[string]any{"event": event}, &recorded))
	require.NoError(t, memoryCall(context.Background(), memoryConn{Config: config}, "euclid_review_lesson", map[string]any{"lesson_id": "lesson:batch", "status": "accepted", "evidence_id": "outcome:later", "expected_revision": recorded.Revision}, nil))
	require.Contains(t, memoryOrientation([]EuclidBrain{brain}, 2500, ""), "Preserve unread events.")
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
	require.Equal(t, config, resolved.Config)

	conn, err := memoryConnection(brain, false)
	require.NoError(t, err)
	require.Equal(t, config, conn.Config)
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

func TestP1aDerivedConnectionCarriesScopeAndWrites(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "mcp.json")
	data, _ := json.Marshal(map[string]any{
		"command": "/bin/sh",
		"args":    []string{"-c", "exit 0"},
		"env":     map[string]string{"BASE_VAR": "hello", "EUCLID_ALLOW_WRITES": "1"},
	})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

	repoDir := filepath.Join(dir, "myrepo")
	dotEuclid := filepath.Join(repoDir, ".euclid")
	devAlice := filepath.Join(dotEuclid, "developers", "alice")
	require.NoError(t, os.MkdirAll(devAlice, 0o755))

	mainRoot := filepath.Join(dir, ".main_euclid")
	require.NoError(t, os.MkdirAll(mainRoot, 0o755))

	// 1. Main brain (writable = true)
	mainBrain := EuclidBrain{Root: mainRoot, Kind: "main", Writable: true, Label: "main"}
	connMain, err := memoryMCPConfig(mainBrain)
	require.NoError(t, err)
	assert.Equal(t, "main", connMain.Env["EUCLID_BRAIN_SCOPE"])
	assert.Equal(t, mainRoot, connMain.Env["EUCLID_ROOT"])
	assert.Equal(t, "1", connMain.Env["EUCLID_ALLOW_WRITES"])
	assert.Equal(t, "hello", connMain.Env["BASE_VAR"])

	// 2. Repo brain (writable = false) - overrides base config's EUCLID_ALLOW_WRITES to "0"
	repoBrain := EuclidBrain{Root: dotEuclid, Kind: "repo", Writable: false, Label: "repo:myrepo"}
	connRepo, err := memoryMCPConfig(repoBrain)
	require.NoError(t, err)
	assert.Equal(t, "shared", connRepo.Env["EUCLID_BRAIN_SCOPE"])
	assert.Equal(t, repoDir, connRepo.Env["EUCLID_ROOT"])
	assert.Equal(t, "0", connRepo.Env["EUCLID_ALLOW_WRITES"])

	// 3. Developer brain (writable = true)
	devBrain := EuclidBrain{Root: devAlice, Kind: "developer", Writable: true, Label: "me@myrepo"}
	connDev, err := memoryMCPConfig(devBrain)
	require.NoError(t, err)
	assert.Equal(t, "developer", connDev.Env["EUCLID_BRAIN_SCOPE"])
	assert.Equal(t, repoDir, connDev.Env["EUCLID_ROOT"])
	assert.Equal(t, "alice", connDev.Env["EUCLID_HANDLE"])
	assert.Equal(t, "1", connDev.Env["EUCLID_ALLOW_WRITES"])
}

func TestP1aDerivedConnectionRefusesFlagsInArgs(t *testing.T) {
	dir := t.TempDir()
	brain := EuclidBrain{Root: filepath.Join(dir, ".euclid"), Kind: "repo"}

	for _, flag := range []string{"--root", "--root=/tmp", "--scope", "--scope=shared", "--handle", "--handle=me", "--alias", "--alias=main"} {
		safeName := strings.NewReplacer("=", "_", "/", "_", "-", "_").Replace(flag)
		config := filepath.Join(dir, "mcp-"+safeName+".json")
		data, _ := json.Marshal(map[string]any{
			"command": "/bin/sh",
			"args":    []string{"server.py", flag},
		})
		require.NoError(t, os.WriteFile(config, data, 0o600))
		t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
		t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

		_, err := memoryMCPConfig(brain)
		require.Error(t, err, "must refuse flag %s", flag)
		assert.Contains(t, err.Error(), "use CAPTAIN_EUCLID_MEMORY_CONFIG for pinned servers")
	}
}

func TestP1aMemoryConnectionRejectsDifferentBrainAndWritableForRead(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys
for line in sys.stdin:
 req=json.loads(line); mid=req.get('id'); method=req['method']
 if mid is None: continue
 if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':'euclid_status'}]}
 else:
  mode=sys.argv[1]
  if mode=='diff_brain':
   value={'brain':'/completely/different/path','alias':'main','writable':False}
  elif mode=='writable_read':
   value={'brain':sys.argv[2],'alias':'main','writable':True}
  result={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0o755))

	brain := EuclidBrain{Root: filepath.Join(dir, ".euclid"), Kind: "main", Label: "main"}
	require.NoError(t, os.MkdirAll(brain.Root, 0o755))

	// 1. Different brain path (even with alias matching)
	configDiff := filepath.Join(dir, "config_diff.json")
	dataDiff, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script, "diff_brain", brain.Root}})
	require.NoError(t, os.WriteFile(configDiff, dataDiff, 0o600))
	mapPath := filepath.Join(dir, "map_diff.json")
	mapData, _ := json.Marshal(map[string]string{brain.Root: configDiff})
	require.NoError(t, os.WriteFile(mapPath, mapData, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", mapPath)
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", "")

	_, err := memoryConnection(brain, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MCP connection is bound to a different brain")

	// 2. Writable server rejected for read connection
	configWritable := filepath.Join(dir, "config_writable.json")
	dataWritable, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script, "writable_read", brain.Root}})
	require.NoError(t, os.WriteFile(configWritable, dataWritable, 0o600))
	mapPath2 := filepath.Join(dir, "map_writable.json")
	mapData2, _ := json.Marshal(map[string]string{brain.Root: configWritable})
	require.NoError(t, os.WriteFile(mapPath2, mapData2, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", mapPath2)

	_, err = memoryConnection(brain, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MCP server is writable for read connection")
}

func TestP1aJournalRunLocalWriteFirstAndSafeResendOnTransportError(t *testing.T) {
	root, brain := learnFixture(t)
	t.Setenv("EUCLID_HOME", root)

	dir := t.TempDir()
	// Fail server that exits immediately (transport error)
	failConfig := filepath.Join(dir, "fail_mcp.json")
	dataFail, _ := json.Marshal(map[string]any{"command": "/bin/sh", "args": []string{"-c", "exit 1"}})
	require.NoError(t, os.WriteFile(failConfig, dataFail, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", failConfig)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

	entry1 := JournalEntry{
		Kind:    "worker",
		Task:    "first task under transport failure",
		Leg:     "claude",
		Outcome: "ok",
		Tokens:  100,
	}
	path1, err := JournalRun(root, entry1)
	require.NoError(t, err, "JournalRun must succeed locally even when MCP fails")
	require.NotEmpty(t, path1)

	// Local files written
	jsonlData, err := os.ReadFile(path1)
	require.NoError(t, err)
	assert.Contains(t, string(jsonlData), "first task under transport failure")

	// Event was queued to .mcp-pending.jsonl
	queueFile := filepath.Join(brain.Root, "journal", ".mcp-pending.jsonl")
	queueData, err := os.ReadFile(queueFile)
	require.NoError(t, err)
	assert.Contains(t, string(queueData), "first task under transport failure")

	// Now switch to a working server
	_, state := memoryFixture(t, brain)

	entry2 := JournalEntry{
		Kind:    "worker",
		Task:    "second task with working MCP",
		Leg:     "claude",
		Outcome: "ok",
		Tokens:  200,
	}
	path2, err := JournalRun(root, entry2)
	require.NoError(t, err)
	require.NotEmpty(t, path2)

	// .mcp-pending.jsonl is now empty / truncated
	queueDataAfter, err := os.ReadFile(queueFile)
	require.NoError(t, err)
	assert.Empty(t, string(queueDataAfter), "queue must be truncated after successful send")

	// Server state received both events
	stateBytes, err := os.ReadFile(state)
	require.NoError(t, err)
	assert.Contains(t, string(stateBytes), "first task under transport failure")
	assert.Contains(t, string(stateBytes), "second task with working MCP")
}

func TestP1aMemoryEventIsErrorLoggedAndNotQueued(t *testing.T) {
	_, brain := learnFixture(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys
while True:
 line = sys.stdin.readline()
 if not line: break
 req=json.loads(line); mid=req.get('id'); method=req['method']
 if mid is None: continue
 if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':'euclid_status'},{'name':'euclid_record_event'}]}
 else:
  name=req['params']['name']
  if name=='euclid_status':
   value={'brain':sys.argv[1],'writable':True}
   res={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
  else:
   res={'isError':True,'content':[{'type':'text','text':'rejected invalid event'}]}
  result = res
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0o755))
	config := filepath.Join(dir, "mcp.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script, brain.Root}})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

	ev := memoryEvent{
		ID:             "captain:note:bad-id",
		Kind:           "note",
		TaskID:         "task:1",
		SourceRevision: "rev:1",
		Text:           "bad note",
	}
	_, err := recordMemoryEvent(brain, ev)
	require.Error(t, err, "must report error when server returns isError")
	assert.Contains(t, err.Error(), "rejected")

	queueFile := filepath.Join(brain.Root, "journal", ".mcp-pending.jsonl")
	if qdata, err := os.ReadFile(queueFile); err == nil {
		assert.Empty(t, string(qdata), "rejected event must not be queued")
	}
}

func TestP1aConcurrentMemoryEventRecordingLosesNoEvent(t *testing.T) {
	_, brain := learnFixture(t)
	_, state := memoryFixture(t, brain)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ev := memoryEvent{
				ID:             fmt.Sprintf("captain:note:concurrent-%d", idx),
				Kind:           "note",
				TaskID:         fmt.Sprintf("task:%d", idx),
				SourceRevision: "rev:1",
				Text:           fmt.Sprintf("concurrent task %d", idx),
			}
			_, errs[idx] = recordMemoryEvent(brain, ev)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "routine %d failed", i)
	}

	stateBytes, err := os.ReadFile(state)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		assert.Contains(t, string(stateBytes), fmt.Sprintf("concurrent task %d", i))
	}
}

func TestP1aCallEuclidMCPQueriesSessionRepository(t *testing.T) {
	dir := t.TempDir()
	repoA := filepath.Join(dir, "repoA")
	repoB := filepath.Join(dir, "repoB")
	require.NoError(t, os.MkdirAll(filepath.Join(repoA, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoA, ".euclid"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoB, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoB, ".euclid"), 0o755))

	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys,os
for line in sys.stdin:
 req=json.loads(line); mid=req.get('id'); method=req['method']
 if mid is None: continue
 if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':'euclid_status'}]}
 else:
  value={
   'root':os.environ.get('EUCLID_ROOT',''),
   'scope':os.environ.get('EUCLID_BRAIN_SCOPE',''),
   'writable':os.environ.get('EUCLID_ALLOW_WRITES','')
  }
  result={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0o755))

	config := filepath.Join(dir, "mcp.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script}})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")

	out, err := callEuclidMCP(repoA, "euclid_status", nil)
	require.NoError(t, err)
	assert.Contains(t, out, repoA)
	assert.Contains(t, out, "shared")
	assert.Contains(t, out, `"0"`)
}
