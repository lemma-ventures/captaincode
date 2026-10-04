package captaincode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type learnCursor struct {
	Version  int             `json:"version"`
	Journal  map[string]bool `json:"journal"`
	Memories map[string]bool `json:"memories"`
}

func entryKeys(entries []JournalEntry) []string {
	counts := map[string]int{}
	keys := make([]string, len(entries))
	for i, e := range entries {
		b, _ := json.Marshal(e)
		h := fmt.Sprintf("%x", sha256.Sum256(b))
		counts[h]++
		keys[i] = fmt.Sprintf("%s:%d", h, counts[h])
	}
	return keys
}

func readLearnInputs(brain EuclidBrain) (LearnInputs, error) {
	in := LearnInputs{cursor: learnCursor{Version: 1, Journal: map[string]bool{}, Memories: map[string]bool{}}}
	var err error
	in.base, err = snapshotBrain(brain.Root, append(append([]string{}, learnRegisterNames...), "journal/.learn-state.json"))
	if err != nil {
		return in, err
	}
	all, err := ReadJournal(brain, time.Time{})
	if err != nil {
		return in, err
	}
	mem := DatedSections(string(in.base["memory/MEMORIES.md"]))
	jk, mk := entryKeys(all), entryKeys(mem)
	if data := in.base["journal/.learn-state.json"]; data != nil {
		if err := json.Unmarshal(data, &in.cursor); err != nil {
			return in, err
		}
		if in.cursor.Version != 1 || in.cursor.Journal == nil || in.cursor.Memories == nil {
			return in, fmt.Errorf("invalid learning cursor")
		}
	}

	for i, e := range all {
		if !in.cursor.Journal[jk[i]] {
			in.PendingJournal++
			if len(in.Journal) >= learnJournalWindow {
				continue
			}
			in.Journal = append(in.Journal, e)
			in.journalKeys = append(in.journalKeys, jk[i])
		}
	}
	for i, e := range mem {
		if !in.cursor.Memories[mk[i]] {
			in.PendingMemories++
			if len(in.Memories) >= learnMemoryWindow {
				continue
			}
			in.Memories = append(in.Memories, e)
			in.memoryKeys = append(in.memoryKeys, mk[i])
		}
	}
	return in, nil
}

func ApplyLearnInputs(brain EuclidBrain, in LearnInputs, edits []RegisterEdit) ([]string, error) {
	if memoryMCPEnabled() {
		return nil, fmt.Errorf("use LearnLoop for MCP learning; local register writes are disabled")
	}
	if in.Err != nil {
		return nil, in.Err
	}
	if in.base == nil || in.cursor.Version != 1 {
		return nil, fmt.Errorf("learning input snapshot required")
	}
	if !brain.Writable {
		return nil, fmt.Errorf("brain is read-only")
	}
	writes, err := prepareBrainEdits(brain, edits, learnFiles, in.base)
	if err != nil {
		return nil, err
	}
	for _, key := range in.journalKeys[:len(in.Journal)] {
		in.cursor.Journal[key] = true
	}
	for _, key := range in.memoryKeys[:len(in.Memories)] {
		in.cursor.Memories[key] = true
	}
	if len(in.Journal)+len(in.Memories) > 0 {
		data, err := json.Marshal(in.cursor)
		if err != nil {
			return nil, err
		}
		writes["journal/.learn-state.json"] = append(data, '\n')
	}
	if err := commitBrainFiles(brain.Root, in.base, writes); err != nil {
		return nil, err
	}
	if len(in.Journal) > 0 {
		latest := LastDistilledAt(brain)
		for _, e := range in.Journal {
			if e.At.After(latest) {
				latest = e.At
			}
		}
		if err := MarkDistilled(brain, latest); err != nil {
			return nil, err
		}
	}
	if len(in.Memories) > 0 {
		if err := MarkCrystallized(brain, len(in.cursor.Memories)); err != nil {
			return nil, err
		}
	}
	var touched []string
	for _, e := range edits {
		if learnFiles[e.File] {
			touched = append(touched, filepath.Join(brain.Root, e.File))
		}
	}
	return touched, nil
}

func readBrainFile(root, name string) ([]byte, error) {
	p := root
	parts := []string{name}
	if filepath.Dir(name) != "." {
		parts = []string{filepath.Dir(name), filepath.Base(name)}
	}
	for _, part := range parts {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in brain path: %s", name)
		}
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, fmt.Errorf("invalid brain file: %s", name)
	}
	return os.ReadFile(p)
}
