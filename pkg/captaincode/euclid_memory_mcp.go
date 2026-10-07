package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/internal/mcpclient"
)

func MemoryMCPEnabled() bool {
	return memoryMCPEnabled()
}

func memoryMCPEnabled() bool {
	return os.Getenv("CAPTAIN_EUCLID_MEMORY_CONFIG") != "" || os.Getenv("CAPTAIN_EUCLID_MCP_CONFIG") != ""
}

// memoryConn is one MCP server for one brain: its config, and the
// environment that points the server at that brain.
type memoryConn struct {
	Config string
	Env    map[string]string
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
	env, err := brainServerEnv(brain)
	if err != nil {
		return memoryConn{}, err
	}
	return memoryConn{Config: config, Env: env}, nil
}

// brainServerEnv points the one shared Euclid server config at this brain.
// Without it the server serves the project brain of whatever folder it was
// started in, and every other brain - main, the other repositories, a
// developer's own - failed its identity check ("bound to a different
// brain") on every turn, so workers started without their memory.
func brainServerEnv(brain EuclidBrain) (map[string]string, error) {
	switch brain.Kind {
	case "main":
		return map[string]string{"EUCLID_BRAIN_SCOPE": "main", "EUCLID_ROOT": brain.Root}, nil
	case "repo", "linked":
		return map[string]string{"EUCLID_BRAIN_SCOPE": "shared", "EUCLID_ROOT": repoOfBrain(brain.Root)}, nil
	case "developer", "developer-other":
		// <repo>/.euclid/developers/<handle>
		devs := filepath.Dir(brain.Root)
		if filepath.Base(devs) != "developers" || filepath.Base(filepath.Dir(devs)) != ".euclid" {
			return nil, fmt.Errorf("developer brain %s is not under .euclid/developers", brain.Root)
		}
		return map[string]string{"EUCLID_BRAIN_SCOPE": "developer", "EUCLID_ROOT": filepath.Dir(filepath.Dir(devs)),
			"EUCLID_HANDLE": filepath.Base(brain.Root)}, nil
	}
	return nil, fmt.Errorf("no Euclid server scope for brain kind %q (%s)", brain.Kind, brain.Label)
}

// repoOfBrain is the repository a <repo>/.euclid brain belongs to.
func repoOfBrain(root string) string {
	if filepath.Base(root) == ".euclid" {
		return filepath.Dir(root)
	}
	return root
}

func memoryCall(ctx context.Context, conn memoryConn, tool string, args any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := mcpclient.Call(ctx, conn.Config, tool, args, conn.Env)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	data := result.StructuredContent
	if len(data) == 0 {
		data = []byte(result.Text())
	}
	return json.Unmarshal(data, out)
}

func memoryConnection(brain EuclidBrain, write bool) (memoryConn, error) {
	config, err := memoryMCPConfig(brain)
	if err != nil {
		return memoryConn{}, err
	}
	if config.Env != nil {
		config.Env["EUCLID_ALLOW_WRITES"] = map[bool]string{true: "1", false: "0"}[write]
	}
	var status struct {
		Brain    string `json:"brain"`
		Alias    string `json:"alias"`
		Scope    string `json:"scope"`
		Writable bool   `json:"writable"`
	}
	if err := memoryCall(context.Background(), config, "euclid_status", map[string]any{}, &status); err != nil {
		return memoryConn{}, err
	}
	match := filepath.Clean(status.Brain) == filepath.Clean(brain.Root)
	if !match {
		if r1, err1 := filepath.EvalSymlinks(status.Brain); err1 == nil {
			if r2, err2 := filepath.EvalSymlinks(brain.Root); err2 == nil && filepath.Clean(r1) == filepath.Clean(r2) {
				match = true
			}
		}
	}
	if !match && status.Alias != "" && (status.Alias == brain.Label || status.Alias == brain.Kind) {
		match = true
	}
	if !match && status.Scope != "" && status.Scope == brain.Kind {
		match = true
	}
	if !match {
		return memoryConn{}, fmt.Errorf("MCP connection is bound to a different brain")
	}
	if write && (!brain.Writable || !status.Writable) {
		return memoryConn{}, fmt.Errorf("MCP brain is read-only")
	}
	return config, nil
}

type memoryEvent struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	TaskID         string `json:"task_id"`
	SourceRevision string `json:"source_revision"`
	Text           string `json:"text"`
	Outcome        string `json:"outcome,omitempty"`
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

func recordMemoryEvent(brain EuclidBrain, event memoryEvent) (string, error) {
	config, err := memoryConnection(brain, true)
	if err != nil {
		return "", err
	}
	err = memoryCall(context.Background(), config, "euclid_record_event", map[string]any{"event": event}, nil)
	if err != nil {
		return "", err
	}
	return "euclid:event:" + event.ID, nil
}

func learnMemoryMCP(brain EuclidBrain, max int, apply bool, run func(string) (string, error)) ([]LearnPass, error) {
	config, err := memoryConnection(brain, apply)
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
				if err := memoryCall(context.Background(), config, "euclid_record_event", map[string]any{"event": event}, nil); err != nil {
					return nil, err
				}
			}
		}
	}
	var passes []LearnPass
	for i := 0; i < max; i++ {
		var batch memoryBatch
		if err := memoryCall(context.Background(), config, "euclid_learn_inputs", map[string]any{"consumer": "captain:learn", "limit": 40}, &batch); err != nil {
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
			if err := memoryCall(context.Background(), config, "euclid_learn_commit", args, &result); err != nil {
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

func memoryOrientation(set []EuclidBrain, budget int) string {
	// Same contract as the file path: no brain means no block. An empty
	// <euclid> pair still changes every worker prompt.
	if len(set) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n<euclid>\n")
	for _, brain := range set {
		config, err := memoryConnection(brain, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "euclid orientation (%s): %v\n", brain.Label, err)
			continue
		}
		var result struct {
			Items []struct {
				Source string `json:"source"`
				Text   string `json:"text"`
			} `json:"items"`
		}
		if err := memoryCall(context.Background(), config, "euclid_orientation", map[string]any{"budget": budget}, &result); err != nil {
			fmt.Fprintf(os.Stderr, "euclid orientation (%s): %v\n", brain.Label, err)
			continue
		}
		for _, item := range result.Items {
			line := fmt.Sprintf("<memory source=%q>%s</memory>\n", html.EscapeString(brain.Label+":"+item.Source), html.EscapeString(item.Text))
			if out.Len()+len(line)+10 <= budget {
				out.WriteString(line)
			}
		}
	}
	out.WriteString("</euclid>\n")
	return out.String()
}

func memoryLearnInputs(brain EuclidBrain) LearnInputs {
	config, err := memoryConnection(brain, false)
	if err != nil {
		return LearnInputs{Err: err}
	}
	var batch memoryBatch
	err = memoryCall(context.Background(), config, "euclid_learn_inputs", map[string]any{"consumer": "captain:learn", "limit": 40}, &batch)
	in := LearnInputs{Err: err, PendingJournal: batch.Pending}
	for _, event := range batch.Events {
		in.Journal = append(in.Journal, JournalEntry{Task: event.Text, Kind: event.Kind, Outcome: event.Outcome})
	}
	return in
}
