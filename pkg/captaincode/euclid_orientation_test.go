package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1c: the task text reaches Euclid, every brain's items merge into one
// block, and no item means no block.

// orientationServer is a stand-in Euclid MCP server. It binds the brain the
// connection names (EUCLID_ROOT, EUCLID_BRAIN_SCOPE), answers
// euclid_orientation with the items in <brain>/fixture-items.json, adds one
// item that echoes the task when <brain>/fixture-echo exists, and appends
// recorded events to <brain>/fixture-events.jsonl.
func orientationServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "server.py")
	require.NoError(t, os.WriteFile(script, []byte(`import json,sys,os,pathlib
root=os.environ.get('EUCLID_ROOT','')
brain=pathlib.Path(root if os.environ.get('EUCLID_BRAIN_SCOPE')=='main' else os.path.join(root,'.euclid'))
for line in sys.stdin:
 req=json.loads(line); mid=req.get('id'); method=req['method']
 if mid is None: continue
 if method=='initialize': result={'protocolVersion':'2025-06-18','capabilities':{'tools':{}}}
 elif method=='tools/list': result={'tools':[{'name':n} for n in ['euclid_status','euclid_orientation','euclid_record_event']]}
 else:
  name=req['params']['name']; args=req['params']['arguments']
  if name=='euclid_status': value={'brain':str(brain),'writable':os.environ.get('EUCLID_ALLOW_WRITES')=='1'}
  elif name=='euclid_orientation':
   f=brain/'fixture-items.json'
   items=json.loads(f.read_text()) if f.exists() else []
   if (brain/'fixture-echo').exists() and args.get('task'):
    items.append({'id':'failure:echo','kind':'guardrail','source':'FAILURES','text':'matched '+args['task'],'score':1.0})
   exposure=[]
   ef=brain/'fixture-exposure.json'
   if args.get('task_id') and ef.exists():
    exposure=json.loads(ef.read_text())
   with open(brain/'fixture-orient.jsonl','a') as out:
    out.write(json.dumps({'task':args.get('task'),'task_id':args.get('task_id')})+'\n')
   value={'version':1,'items':items,'exposure':exposure}
  else:
   with open(brain/'fixture-events.jsonl','a') as out: out.write(json.dumps(args['event'])+'\n')
   value={'id':args['event']['id']}
  result={'structuredContent':value,'content':[{'type':'text','text':json.dumps(value)}]}
 print(json.dumps({'jsonrpc':'2.0','id':mid,'result':result}),flush=True)
`), 0o600))
	config := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{script}})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")
	return config
}

func writeItems(t *testing.T, brainRoot string, items []map[string]any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(brainRoot, 0o755))
	data, _ := json.Marshal(items)
	require.NoError(t, os.WriteFile(filepath.Join(brainRoot, "fixture-items.json"), data, 0o600))
}

func TestMemoryOrientationIsEmptyWhenEveryBrainFails(t *testing.T) {
	_, brain := learnFixture(t)
	other := brain
	other.Root = filepath.Join(t.TempDir(), "elsewhere", ".euclid")
	other.Label = "elsewhere"
	memoryFixture(t, brain) // a server bound to brain: both brains below fail its check
	brain.Root = filepath.Join(t.TempDir(), ".euclid")
	assert.Equal(t, "", memoryOrientation([]EuclidBrain{brain, other}, 2500, "fix the parser"),
		"no rendered item means no <euclid> block")
}

func TestRenderOrientationIsEmptyForTemplateOnlyBrains(t *testing.T) {
	euclidHome(t)
	root := filepath.Join(t.TempDir(), ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	got := renderOrientation([]EuclidBrain{{Root: root, Kind: "main", Writable: true, Label: "main"}}, 2500)
	assert.Equal(t, "", got, "template files carry no state, so the block is absent")
}

func TestMemoryOrientationMergesBrainsByKindScoreAndReadOrder(t *testing.T) {
	euclidHome(t)
	orientationServer(t)
	main := filepath.Join(t.TempDir(), "main")
	repo := filepath.Join(t.TempDir(), "repo")
	writeItems(t, main, []map[string]any{
		{"id": "wisdom:m1", "kind": "wisdom", "source": "WISDOM", "text": "main wisdom line", "score": 9.0},
		{"id": "brain", "kind": "brain", "source": "BRAIN", "text": "main state", "score": nil},
		{"id": "failure:m1", "kind": "guardrail", "source": "FAILURES", "text": "main guardrail low", "score": 1.5},
		{"id": "failure:same", "kind": "guardrail", "source": "FAILURES", "text": "copied   guardrail", "score": 2.0},
	})
	writeItems(t, filepath.Join(repo, ".euclid"), []map[string]any{
		{"id": "lesson:r1", "kind": "lesson", "source": "lesson:r1", "text": "repo lesson", "score": 3.0},
		{"id": "failure:r1", "kind": "guardrail", "source": "FAILURES", "text": "repo guardrail high", "score": 4.0},
		{"id": "failure:same", "kind": "guardrail", "source": "FAILURES", "text": "copied guardrail", "score": 2.0},
		{"source": "BRAIN", "text": "repo state from an older server"},
	})
	set := []EuclidBrain{
		{Root: filepath.Join(repo, ".euclid"), Kind: "repo", Label: "repo:r"},
		{Root: main, Kind: "main", Label: "main"},
	}
	got := memoryOrientation(set, 2500, "fix the parser")
	order := []string{"repo guardrail high", "copied guardrail", "main guardrail low", "repo lesson", "repo state from an older server", "main state", "main wisdom line"}
	last := -1
	for _, text := range order {
		i := strings.Index(got, text)
		require.Greater(t, i, last, "%q out of order in\n%s", text, got)
		last = i
	}
	assert.Equal(t, 1, strings.Count(got, "copied"), "a bullet copied into two brains is rendered once")
	assert.Contains(t, got, `<memory source="repo:r:FAILURES" kind="guardrail" id="failure:r1">repo guardrail high</memory>`)
	assert.Contains(t, got, `<memory source="repo:r:BRAIN" kind="brain" id="">repo state from an older server</memory>`)
	assert.True(t, strings.HasPrefix(got, "\n<euclid>\n") && strings.HasSuffix(got, "</euclid>\n"))
}

func TestMemoryOrientationSkipsAnItemThatDoesNotFitAndKeepsALaterOne(t *testing.T) {
	euclidHome(t)
	orientationServer(t)
	main := filepath.Join(t.TempDir(), "main")
	writeItems(t, main, []map[string]any{
		{"id": "failure:long", "kind": "guardrail", "source": "FAILURES", "text": strings.Repeat("x", 400), "score": 5.0},
		{"id": "wisdom:short", "kind": "wisdom", "source": "WISDOM", "text": "short line", "score": 1.0},
	})
	got := memoryOrientation([]EuclidBrain{{Root: main, Kind: "main", Label: "main"}}, 300, "")
	assert.NotContains(t, got, "xxxx")
	assert.Contains(t, got, "short line")
	assert.LessOrEqual(t, len(got), 300)
}

func TestOrientationForSendsTheTaskAndKeysTheCacheOnIt(t *testing.T) {
	home := euclidHome(t)
	orientationServer(t)
	root := filepath.Join(home, ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "fixture-echo"), nil, 0o600))
	secret := "sk-ant-api03-" + strings.Repeat("A", 90)
	first := OrientationFor(home, nil, "fix the catalog merge driver "+secret)
	second := OrientationFor(home, nil, "rename the dashboard tab")
	assert.Contains(t, first, "matched fix the catalog merge driver")
	assert.NotContains(t, first, secret, "the task is scrubbed before it leaves Captain")
	assert.Contains(t, second, "matched rename the dashboard tab", "a second task within 30s gets its own block")
	assert.Equal(t, first, OrientationFor(home, nil, "fix the catalog merge driver "+secret))
	assert.Equal(t, "", OrientationFor(home, nil, ""), "no task, no item from this server: no block")
}

func TestOrientationTaskIsClippedOnARuneBoundary(t *testing.T) {
	task := orientationTask(strings.Repeat("é", 3000))
	assert.LessOrEqual(t, len(task), orientationTaskMax)
	assert.True(t, strings.HasSuffix(task, "é"))
}

func TestAppendNoteRecordsTheCategory(t *testing.T) {
	home := euclidHome(t)
	orientationServer(t)
	root := filepath.Join(home, ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	_, err = AppendNote(home, "Failure", "the merge driver dropped a row", "tester")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "fixture-events.jsonl"))
	require.NoError(t, err)
	var ev memoryEvent
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev))
	assert.Equal(t, "failure", ev.Category)
	assert.Equal(t, "the merge driver dropped a row", ev.Text)
}

func TestOrientationProbeCountsItemsPerBrain(t *testing.T) {
	home := euclidHome(t)
	orientationServer(t)
	root := filepath.Join(home, ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	writeItems(t, root, []map[string]any{
		{"id": "brain", "kind": "brain", "source": "BRAIN", "text": "state"},
		{"id": "wisdom", "kind": "wisdom", "source": "WISDOM", "text": "head"},
	})
	counts, errs := OrientationProbe(home)
	assert.Empty(t, errs)
	assert.Equal(t, map[string]int{"main": 2}, counts)
}

// Live, with EUCLID_TEST_SERVER (mcp/euclid_mcp.py of a Euclid checkout): one
// CAPTAIN_EUCLID_MCP_CONFIG serves a repository brain and the main brain, and
// the block holds the matched guardrail of each, with its id.
func TestOrientationLiveMatchesGuardrailsInEveryBrain(t *testing.T) {
	server := os.Getenv("EUCLID_TEST_SERVER")
	if server == "" {
		t.Skip("set EUCLID_TEST_SERVER to an installed Euclid MCP server")
	}
	home := euclidHome(t)
	repo := gitRepo(t)
	main := filepath.Join(home, ".euclid")
	_, err := Scaffold(main, "main")
	require.NoError(t, err)
	shared := filepath.Join(repo, ".euclid")
	_, err = Scaffold(shared, "repo")
	require.NoError(t, err)
	failures := map[string]string{
		main:   "- Catalog regeneration broke after a merge driver change in the main brain.\n- The dashboard tab order is fixed.\n",
		shared: "- Catalog regeneration failed after a merge in this repository.\n- Release notes need a date.\n",
	}
	for root, text := range failures {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "memory"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "memory", "FAILURES.md"), []byte("# FAILURES\n\n"+text), 0o644))
	}
	config := filepath.Join(t.TempDir(), "server.json")
	data, _ := json.Marshal(map[string]any{"command": "/usr/bin/python3", "args": []string{server}})
	require.NoError(t, os.WriteFile(config, data, 0o600))
	t.Setenv("CAPTAIN_EUCLID_MCP_CONFIG", config)
	t.Setenv("CAPTAIN_EUCLID_MEMORY_CONFIG", "")
	set := []EuclidBrain{
		{Root: shared, Kind: "repo", Label: "repo"},
		{Root: main, Kind: "main", Label: "main"},
	}
	got := memoryOrientation(set, 2500, "catalog regeneration fails after a merge")
	assert.Contains(t, got, "in this repository.")
	assert.Contains(t, got, "in the main brain.")
	assert.NotContains(t, got, "Release notes")
	assert.NotContains(t, got, "tab order")
	assert.Equal(t, 2, strings.Count(got, `kind="guardrail" id="failure:`), got)
}

func TestExposureMergeKeepsTheArmAndMarksADroppedShownLesson(t *testing.T) {
	euclidHome(t)
	orientCache.reset()
	orientationServer(t)
	main := filepath.Join(t.TempDir(), "main")
	writeItems(t, main, []map[string]any{
		{"id": "lesson-long", "kind": "candidate", "source": "lesson:lesson-long", "text": strings.Repeat("x", 400), "score": 5.0},
		{"id": "wisdom:short", "kind": "wisdom", "source": "WISDOM", "text": "short line", "score": 1.0},
	})
	require.NoError(t, os.WriteFile(filepath.Join(main, "fixture-exposure.json"), []byte(
		`[{"lesson":"lesson-long","arm":"shown","p":0.5},{"lesson":"lesson-kept","arm":"withheld","p":0.5}]`), 0o600))
	view := memoryOrientationTask([]EuclidBrain{{Root: main, Kind: "main", Label: "main", Writable: true}}, 300, "fix the parser", "task-1")
	require.Len(t, view.Rec.Entries, 2)
	assert.Equal(t, "shown", view.Rec.Entries[0].Arm)
	require.NotNil(t, view.Rec.Entries[0].Rendered)
	assert.False(t, *view.Rec.Entries[0].Rendered, "a shown lesson the budget drops stays shown")
	assert.NotContains(t, view.Text, "xxxx")
	assert.Contains(t, view.Text, "short line")
	assert.Contains(t, view.Text, `kind="candidate" are unproven`)
	assert.NotContains(t, view.Text, "lesson-kept")

	ledger := &Ledger{}
	ledger.MergeMemoryExposure("task-1", view.Rec)
	assert.Equal(t, 1, ledger.DroppedShown())
	again := view.Rec
	again.Entries[0].Arm = "withheld"
	again.Entries[0].Rendered = boolPtr(true)
	ledger.MergeMemoryExposure("task-1", again)
	got := ledger.MemoryExposure["task-1"]
	assert.Equal(t, "shown", got.Entries[0].Arm, "the arm of a lesson never changes for a task")
	require.NotNil(t, got.Entries[0].Rendered)
	assert.True(t, *got.Entries[0].Rendered)
	assert.Equal(t, 0, ledger.DroppedShown())
	assert.Equal(t, "withheld", got.Entries[1].Arm)
}

func TestOrientationCacheSeparatesTasksWithTheSameText(t *testing.T) {
	home := euclidHome(t)
	orientCache.reset()
	orientationServer(t)
	root := filepath.Join(home, ".euclid")
	_, err := Scaffold(root, "main")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "fixture-echo"), nil, 0o600))
	OrientationForTask(home, nil, "fix the parser", "task-a")
	OrientationForTask(home, nil, "fix the parser", "task-b")
	OrientationForTask(home, nil, "fix the parser", "task-a")
	data, err := os.ReadFile(filepath.Join(root, "fixture-orient.jsonl"))
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, `"task_id": "task-a"`)
	assert.Contains(t, text, `"task_id": "task-b"`)
	assert.Equal(t, 2, strings.Count(text, "\n"), "the second render of task-a is served from the cache")
}
