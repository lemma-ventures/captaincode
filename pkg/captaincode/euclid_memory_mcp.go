package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/internal/mcpclient"
)

type memoryConn struct {
	Config string            `json:"config"`
	Env    map[string]string `json:"env,omitempty"`
}

type connCacheEntry struct {
	verifiedAt time.Time
}

var (
	connCacheMu sync.Mutex
	connCache   = map[string]connCacheEntry{}

	errorLogMu  sync.Mutex
	errorLogMap = map[string]time.Time{}
)

func connectionCacheKey(brainRoot, config string, env map[string]string, write bool) string {
	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(filepath.Clean(brainRoot))
	b.WriteString("|")
	b.WriteString(config)
	b.WriteString(fmt.Sprintf("|write:%v|", write))
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(env[k])
		b.WriteString(";")
	}
	return b.String()
}

func logMemoryConnectionError(brainLabel string, err error) {
	if err == nil {
		return
	}
	key := brainLabel + ":" + err.Error()
	errorLogMu.Lock()
	defer errorLogMu.Unlock()
	if last, ok := errorLogMap[key]; ok && time.Since(last) < 10*time.Minute {
		return
	}
	errorLogMap[key] = time.Now()
	fmt.Fprintf(os.Stderr, "euclid orientation (%s): %v\n", brainLabel, err)
}

func MemoryMCPEnabled() bool {
	return memoryMCPEnabled()
}

func memoryMCPEnabled() bool {
	return os.Getenv("CAPTAIN_EUCLID_MEMORY_CONFIG") != "" || os.Getenv("CAPTAIN_EUCLID_MCP_CONFIG") != ""
}

func memoryMCPConfig(brain EuclidBrain) (memoryConn, error) {
	if path := os.Getenv("CAPTAIN_EUCLID_MEMORY_CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return memoryConn{}, err
		}
		if len(data) > 65536 {
			return memoryConn{}, fmt.Errorf("memory connection map exceeds 64 KiB")
		}
		var configs map[string]string
		if err := json.Unmarshal(data, &configs); err != nil {
			return memoryConn{}, err
		}
		config := configs[filepath.Clean(brain.Root)]
		if config == "" && brain.Label != "" {
			config = configs[brain.Label]
		}
		if config == "" && brain.Kind != "" {
			config = configs[brain.Kind]
		}
		if !filepath.IsAbs(config) {
			return memoryConn{}, fmt.Errorf("no pinned MCP connection for brain %s", brain.Label)
		}
		return memoryConn{Config: config}, nil // a pinned server knows its brain
	}
	config := os.Getenv("CAPTAIN_EUCLID_MCP_CONFIG")
	if config == "" {
		return memoryConn{}, fmt.Errorf("memory MCP is not configured")
	}
	cfg, err := mcpclient.ReadConfig(config)
	if err != nil {
		return memoryConn{}, err
	}
	// One shared server config serves every brain: captain points it at each
	// one. A config that pins its brain in args would answer for that brain
	// only, and every other brain failed its identity check ("bound to a
	// different brain") on every turn, so workers started without memory.
	for _, arg := range cfg.Args {
		if arg == "--root" || strings.HasPrefix(arg, "--root=") ||
			arg == "--scope" || strings.HasPrefix(arg, "--scope=") ||
			arg == "--handle" || strings.HasPrefix(arg, "--handle=") ||
			arg == "--alias" || strings.HasPrefix(arg, "--alias=") {
			return memoryConn{}, fmt.Errorf("configuration args contain %s; use CAPTAIN_EUCLID_MEMORY_CONFIG for pinned servers", arg)
		}
	}
	overlay := make(map[string]string)
	for k, v := range cfg.Env {
		overlay[k] = v
	}
	switch brain.Kind {
	case "main":
		overlay["EUCLID_BRAIN_SCOPE"] = "main"
		overlay["EUCLID_ROOT"] = brain.Root
	case "repo", "named", "linked":
		repoRoot := filepath.Dir(brain.Root)
		if filepath.Base(brain.Root) != ".euclid" {
			if r := RepoRoot(brain.Root); r != "" {
				repoRoot = r
			}
		}
		overlay["EUCLID_BRAIN_SCOPE"] = "shared"
		overlay["EUCLID_ROOT"] = repoRoot
	case "developer", "developer-other":
		handle := filepath.Base(brain.Root)
		repoRoot := filepath.Dir(brain.Root)
		if filepath.Base(repoRoot) == "developers" {
			dotEuclid := filepath.Dir(repoRoot)
			if filepath.Base(dotEuclid) == ".euclid" {
				repoRoot = filepath.Dir(dotEuclid)
			}
		} else if r := RepoRoot(brain.Root); r != "" {
			repoRoot = r
		}
		overlay["EUCLID_BRAIN_SCOPE"] = "developer"
		overlay["EUCLID_ROOT"] = repoRoot
		overlay["EUCLID_HANDLE"] = handle
	default:
		return memoryConn{}, fmt.Errorf("unknown brain kind %s for %s", brain.Kind, brain.Label)
	}
	if brain.Writable {
		overlay["EUCLID_ALLOW_WRITES"] = "1"
	} else {
		overlay["EUCLID_ALLOW_WRITES"] = "0"
	}
	return memoryConn{Config: config, Env: overlay}, nil
}

func memoryCallWithResult(ctx context.Context, conn memoryConn, tool string, args any, out any) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := mcpclient.Call(ctx, conn.Config, tool, args, conn.Env)
	if err != nil {
		if result.IsError {
			msg := result.Text()
			if msg == "" {
				msg = err.Error()
			}
			return true, fmt.Errorf("MCP tool %s rejected: %s", tool, msg)
		}
		return false, err
	}
	if out != nil {
		data := result.StructuredContent
		if len(data) == 0 {
			data = []byte(result.Text())
		}
		if err := json.Unmarshal(data, out); err != nil {
			return false, err
		}
	}
	return false, nil
}

func memoryCall(ctx context.Context, conn memoryConn, tool string, args any, out any) error {
	_, err := memoryCallWithResult(ctx, conn, tool, args, out)
	return err
}

func memoryConnection(brain EuclidBrain, write bool) (memoryConn, error) {
	if write && !brain.Writable {
		return memoryConn{}, fmt.Errorf("brain is read-only")
	}
	conn, err := memoryMCPConfig(brain)
	if err != nil {
		return memoryConn{}, err
	}
	if !write && conn.Env != nil {
		readEnv := make(map[string]string, len(conn.Env))
		for k, v := range conn.Env {
			readEnv[k] = v
		}
		readEnv["EUCLID_ALLOW_WRITES"] = "0"
		conn.Env = readEnv
	} else if write && conn.Env != nil {
		writeEnv := make(map[string]string, len(conn.Env))
		for k, v := range conn.Env {
			writeEnv[k] = v
		}
		writeEnv["EUCLID_ALLOW_WRITES"] = "1"
		conn.Env = writeEnv
	}

	cacheKey := connectionCacheKey(brain.Root, conn.Config, conn.Env, write)
	connCacheMu.Lock()
	if entry, ok := connCache[cacheKey]; ok && time.Since(entry.verifiedAt) < 10*time.Minute {
		connCacheMu.Unlock()
		return conn, nil
	}
	connCacheMu.Unlock()

	var status struct {
		Brain    string `json:"brain"`
		Alias    string `json:"alias"`
		Scope    string `json:"scope"`
		Writable bool   `json:"writable"`
	}
	if err := memoryCall(context.Background(), conn, "euclid_status", map[string]any{}, &status); err != nil {
		return memoryConn{}, err
	}
	statusBrain := filepath.Clean(status.Brain)
	brainRoot := filepath.Clean(brain.Root)
	match := statusBrain == brainRoot
	if !match {
		if r1, err1 := filepath.EvalSymlinks(statusBrain); err1 == nil {
			if r2, err2 := filepath.EvalSymlinks(brainRoot); err2 == nil && filepath.Clean(r1) == filepath.Clean(r2) {
				match = true
			}
		}
	}
	if !match {
		return memoryConn{}, fmt.Errorf("MCP connection is bound to a different brain")
	}
	if write != status.Writable {
		if write {
			return memoryConn{}, fmt.Errorf("MCP brain is read-only")
		}
		return memoryConn{}, fmt.Errorf("MCP server is writable for read connection")
	}

	connCacheMu.Lock()
	connCache[cacheKey] = connCacheEntry{verifiedAt: time.Now()}
	connCacheMu.Unlock()

	return conn, nil
}

type memoryEvent struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	TaskID         string          `json:"task_id"`
	SourceRevision string          `json:"source_revision"`
	Text           string          `json:"text"`
	Outcome        string          `json:"outcome,omitempty"`
	Category       string          `json:"category,omitempty"` // note events only; accepted since P1b
	Evidence       []string        `json:"evidence,omitempty"`
	Exposure       []ExposureEntry `json:"exposure,omitempty"`
}

// ExposureEntry is one lesson arm for one task. Rendered is false when the
// arm was shown but the budget dropped the item.
type ExposureEntry struct {
	Lesson   string  `json:"lesson"`
	Arm      string  `json:"arm"`
	P        float64 `json:"p"`
	Rendered *bool   `json:"rendered,omitempty"`
}

type memoryLesson struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

type memoryBatch struct {
	Revision string            `json:"revision"`
	Events   []memoryEvent     `json:"events"`
	Lessons  []json.RawMessage `json:"lessons"`
	Pending  int               `json:"pending"`
}

func memoryEntry(e JournalEntry, key, kind string) memoryEvent {
	data, _ := json.Marshal(e)
	return memoryEvent{ID: "captain:" + kind + ":" + key, Kind: "note", TaskID: fmt.Sprintf("captain:%x", sha256.Sum256([]byte(key))), SourceRevision: "legacy-import:unknown", Text: Scrub(string(data))}
}

const maxPendingQueueBytes = 16 * 1024 * 1024 // 16 MiB

func recordMemoryEvent(brain EuclidBrain, event memoryEvent) (string, error) {
	journalDir := filepath.Join(brain.Root, "journal")
	if err := os.MkdirAll(journalDir, 0o755); err != nil {
		return "", err
	}
	lockPath := filepath.Join(journalDir, ".mcp-pending.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("failed to acquire pending queue lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	sendEvent := func(ev memoryEvent) (bool, error) {
		conn, connErr := memoryConnection(brain, true)
		if connErr != nil {
			return false, connErr
		}
		return memoryCallWithResult(context.Background(), conn, "euclid_record_event", map[string]any{"event": ev}, nil)
	}

	queuePath := filepath.Join(journalDir, ".mcp-pending.jsonl")
	var queuedEvents []memoryEvent
	if qdata, rerr := os.ReadFile(queuePath); rerr == nil && len(qdata) > 0 {
		for _, qline := range strings.Split(string(qdata), "\n") {
			qline = strings.TrimSpace(qline)
			if qline == "" {
				continue
			}
			var qe memoryEvent
			if err := json.Unmarshal([]byte(qline), &qe); err == nil {
				queuedEvents = append(queuedEvents, qe)
			}
		}
	}

	var remainingEvents []memoryEvent
	allQueuedSent := true
	for i, qe := range queuedEvents {
		isToolErr, sendErr := sendEvent(qe)
		if sendErr == nil {
			continue
		}
		if isToolErr {
			fmt.Fprintf(os.Stderr, "euclid journal: MCP event %s rejected (isError), dropping: %v\n", qe.ID, sendErr)
			continue
		}
		remainingEvents = queuedEvents[i:]
		allQueuedSent = false
		break
	}

	if allQueuedSent {
		isToolErr, sendErr := sendEvent(event)
		if sendErr == nil {
			_ = os.Truncate(queuePath, 0)
			return "euclid:event:" + event.ID, nil
		}
		if isToolErr {
			fmt.Fprintf(os.Stderr, "euclid journal: MCP event %s rejected (isError), dropping: %v\n", event.ID, sendErr)
			_ = os.Truncate(queuePath, 0)
			return "", sendErr
		}
		remainingEvents = []memoryEvent{event}
		fmt.Fprintf(os.Stderr, "euclid journal: MCP transport error for event %s (queued for retry): %v\n", event.ID, sendErr)
	} else {
		hasNew := false
		for _, re := range remainingEvents {
			if re.ID == event.ID {
				hasNew = true
				break
			}
		}
		if !hasNew {
			remainingEvents = append(remainingEvents, event)
		}
	}

	var buf strings.Builder
	for _, re := range remainingEvents {
		b, _ := json.Marshal(re)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	dataToWrite := []byte(buf.String())
	if len(dataToWrite) > maxPendingQueueBytes {
		fmt.Fprintf(os.Stderr, "euclid journal: pending MCP queue exceeded 16 MiB; dropping event %s\n", event.ID)
	} else {
		_ = os.WriteFile(queuePath, dataToWrite, 0o600)
	}

	return "", fmt.Errorf("MCP event queued due to transport error: %s", event.ID)
}

func learnMemoryMCP(brain EuclidBrain, max int, apply bool, run func(string) (string, error)) ([]LearnPass, error) {
	conn, err := memoryConnection(brain, apply)
	if err != nil {
		return nil, err
	}
	if max <= 0 {
		max = learnDefaultPassCap
	}
	if apply {
		entries, err := ReadJournal(brain, time.Time{})
		if err != nil {
			return nil, err
		}
		memories, err := readBrainFile(brain.Root, "memory/MEMORIES.md")
		if err != nil {
			return nil, err
		}
		for _, group := range []struct {
			kind    string
			entries []JournalEntry
		}{{"journal", entries}, {"memory", DatedSections(string(memories))}} {
			keys := entryKeys(group.entries)
			for i, entry := range group.entries {
				event := memoryEntry(entry, keys[i], group.kind)
				if err := memoryCall(context.Background(), conn, "euclid_record_event", map[string]any{"event": event}, nil); err != nil {
					return nil, err
				}
			}
		}
	}
	var passes []LearnPass
	for i := 0; i < max; i++ {
		var batch memoryBatch
		if err := memoryCall(context.Background(), conn, "euclid_learn_inputs", map[string]any{"consumer": "captain:learn", "limit": 40}, &batch); err != nil {
			return passes, err
		}
		if batch.Revision == "" {
			return passes, fmt.Errorf("MCP learning revision is missing")
		}
		if len(batch.Events) == 0 {
			break
		}
		data, _ := json.Marshal(batch)
		reply, err := run("Learn candidate lessons from this exact event batch. Do not accept lessons or edit registers. Each evidence ID must name an event in this batch. Return JSON only: {\"summary\":\"...\",\"lessons\":[{\"id\":\"lesson:stable-id\",\"text\":\"...\",\"evidence\":[\"event-id\"]}]}. Use an empty lessons array if no new lesson is justified. Existing lesson IDs cannot be reused.\n" + string(data))
		if err != nil {
			return passes, err
		}
		var proposal struct {
			Summary string         `json:"summary"`
			Lessons []memoryLesson `json:"lessons"`
		}
		decoder := json.NewDecoder(strings.NewReader(reply))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&proposal); err != nil {
			return passes, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return passes, fmt.Errorf("learning reply contains trailing data")
		}
		for i := range proposal.Lessons {
			proposal.Lessons[i].Text = Scrub(proposal.Lessons[i].Text)
		}
		if proposal.Lessons == nil {
			proposal.Lessons = []memoryLesson{}
		}
		ids := make([]string, len(batch.Events))
		for j, e := range batch.Events {
			ids[j] = e.ID
		}
		args := map[string]any{"consumer": "captain:learn", "expected_revision": batch.Revision, "event_ids": ids, "lessons": proposal.Lessons, "request_id": fmt.Sprintf("captain:%x", sha256.Sum256([]byte(batch.Revision+reply)))}
		if apply {
			var result struct {
				Committed bool `json:"committed"`
			}
			if err := memoryCall(context.Background(), conn, "euclid_learn_commit", args, &result); err != nil {
				return passes, err
			}
			if !result.Committed {
				return passes, fmt.Errorf("MCP did not acknowledge the learning batch")
			}
		}
		pass := LearnPass{Journal: len(ids), Summary: proposal.Summary}
		for _, lesson := range proposal.Lessons {
			pass.Edits = append(pass.Edits, lesson.ID)
		}
		passes = append(passes, pass)
		if !apply {
			break
		}
	}
	return passes, nil
}

// orientationItem is one item of euclid_orientation (P1b contract). A server
// older than P1b sends neither kind, id nor score; orientationKind derives
// the kind from the source.
type orientationItem struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Source string   `json:"source"`
	Text   string   `json:"text"`
	Score  *float64 `json:"score"`
}

var orientationKindRank = map[string]int{"guardrail": 0, "lesson": 1, "candidate": 2, "brain": 3, "wisdom": 4}

func orientationKind(item orientationItem) string {
	if _, ok := orientationKindRank[item.Kind]; ok {
		return item.Kind
	}
	switch {
	case item.Source == "FAILURES":
		return "guardrail"
	case strings.HasPrefix(item.Source, "lesson:"):
		return "lesson"
	case item.Source == "BRAIN":
		return "brain"
	default:
		return "wisdom"
	}
}

// memoryOrientationItems asks one brain for its orientation items, with the
// full budget: the merge in memoryOrientation decides what fits.
func memoryOrientationItems(brain EuclidBrain, budget int, task, taskID string) ([]orientationItem, []ExposureEntry, error) {
	conn, err := memoryConnection(brain, false)
	if err != nil {
		return nil, nil, err
	}
	args := map[string]any{"budget": budget}
	if strings.TrimSpace(task) != "" {
		args["task"] = task
	}
	if brain.Writable && taskID != "" {
		args["task_id"] = taskID
	}
	var result struct {
		Items    []orientationItem `json:"items"`
		Exposure []ExposureEntry   `json:"exposure"`
	}
	if err := memoryCall(context.Background(), conn, "euclid_orientation", args, &result); err != nil {
		return nil, nil, err
	}
	if !brain.Writable {
		result.Exposure = nil
	}
	return result.Items, result.Exposure, nil
}

func memoryOrientation(set []EuclidBrain, budget int, task string) string {
	return memoryOrientationTask(set, budget, task, "").Text
}

// memoryOrientationTask renders one block from every brain of the read set.
// Merge order is guardrail, lesson, candidate, brain, wisdom, then score,
// then read-set order. taskID goes only to the write brain. A shown lesson
// the budget drops stays in the exposure with rendered false.
func memoryOrientationTask(set []EuclidBrain, budget int, task, taskID string) OrientationView {
	type ranked struct {
		item  orientationItem
		kind  string
		label string
		brain int
		pos   int
	}
	var all []ranked
	var assigned []ExposureEntry
	var write EuclidBrain
	for i, brain := range set {
		items, exposure, err := memoryOrientationItems(brain, budget, task, taskID)
		if err != nil {
			logMemoryConnectionError(brain.Label, err)
			continue
		}
		if brain.Writable && taskID != "" {
			assigned = exposure
			write = brain
		}
		for j, item := range items {
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			all = append(all, ranked{item: item, kind: orientationKind(item), label: brain.Label, brain: i, pos: j})
		}
	}
	score := func(r ranked) float64 {
		if r.item.Score == nil {
			return math.Inf(-1)
		}
		return *r.item.Score
	}
	sort.SliceStable(all, func(a, b int) bool {
		x, y := all[a], all[b]
		if orientationKindRank[x.kind] != orientationKindRank[y.kind] {
			return orientationKindRank[x.kind] < orientationKindRank[y.kind]
		}
		if score(x) != score(y) {
			return score(x) > score(y)
		}
		if x.brain != y.brain {
			return x.brain < y.brain
		}
		return x.pos < y.pos
	})
	const open = "\n<euclid>\nItems with kind=\"candidate\" are unproven lessons. Check one before you rely on it.\n"
	const closing = "</euclid>\n"
	var body strings.Builder
	used := len(open) + len(closing)
	seen := map[string]bool{}
	rendered := map[string]bool{}
	var guardrails []string
	for _, r := range all {
		dup := r.kind + "\x00" + strings.Join(strings.Fields(r.item.Text), " ")
		if seen[dup] {
			continue
		}
		line := fmt.Sprintf("<memory source=%q kind=%q id=%q>%s</memory>\n",
			html.EscapeString(r.label+":"+r.item.Source), html.EscapeString(r.kind), html.EscapeString(r.item.ID), html.EscapeString(r.item.Text))
		if used+len(line) > budget {
			continue
		}
		seen[dup] = true
		rendered[r.item.ID] = true
		if r.kind == "guardrail" && r.item.ID != "" {
			guardrails = append(guardrails, r.item.ID)
		}
		body.WriteString(line)
		used += len(line)
	}
	entries := make([]ExposureEntry, len(assigned))
	for i, entry := range assigned {
		flag := rendered[entry.Lesson]
		entry.Rendered = &flag
		entries[i] = entry
	}
	view := OrientationView{Rec: TaskExposure{
		BrainRoot: write.Root, BrainKind: write.Kind, BrainLabel: write.Label,
		Entries: entries, Guardrails: guardrails,
	}}
	if body.Len() == 0 {
		return view
	}
	view.Text = open + body.String() + closing
	return view
}

func memoryLearnInputs(brain EuclidBrain) LearnInputs {
	conn, err := memoryConnection(brain, false)
	if err != nil {
		return LearnInputs{Err: err}
	}
	var batch memoryBatch
	err = memoryCall(context.Background(), conn, "euclid_learn_inputs", map[string]any{"consumer": "captain:learn", "limit": 40}, &batch)
	in := LearnInputs{Err: err, PendingJournal: batch.Pending}
	for _, event := range batch.Events {
		in.Journal = append(in.Journal, JournalEntry{Task: event.Text, Kind: event.Kind, Outcome: event.Outcome})
	}
	return in
}
