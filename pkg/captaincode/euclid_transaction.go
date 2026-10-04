package captaincode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type brainTransaction struct {
	Before map[string][]byte `json:"before"`
	After  map[string][]byte `json:"after"`
}

func snapshotBrain(root string, names []string) (map[string][]byte, error) {
	if _, err := os.Lstat(filepath.Join(root, ".pending-edits.json")); err == nil {
		return nil, fmt.Errorf("pending brain transaction; retry after recovery")
	}
	out := map[string][]byte{}
	for _, n := range names {
		b, err := readBrainFile(root, n)
		if err != nil {
			return nil, err
		}
		out[n] = b
	}
	return out, nil
}

func prepareBrainEdits(brain EuclidBrain, edits []RegisterEdit, allowed map[string]bool, base map[string][]byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, e := range edits {
		if !allowed[e.File] {
			continue
		}
		cur, ok := out[e.File]
		if !ok {
			cur = base[e.File]
		}
		text := string(cur)
		if e.Mode == "replace_section" {
			text = replaceSection(text, e.Anchor, e.Text)
		} else {
			text = strings.TrimRight(dropPlaceholders(text), "\n") + "\n\n" + strings.TrimSpace(e.Text) + "\n"
		}
		if len(text) > 8<<20 {
			return nil, fmt.Errorf("register too large")
		}
		out[e.File] = []byte(text)
	}
	return out, nil
}

func atomicBrainFile(root, name string, data []byte) error {
	if _, err := readBrainFile(root, name); err != nil {
		return err
	}
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".euclid-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validTransactionFile(n string) bool {
	return learnFiles[n] || allowedEditFiles[n] || bootstrapFiles[n] || n == "journal/.learn-state.json"
}

func recoverBrainFiles(root string) error {
	data, err := readBrainFile(root, ".pending-edits.json")
	if err != nil || data == nil {
		return err
	}
	var tx brainTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return err
	}
	var names []string
	for n, after := range tx.After {
		if !validTransactionFile(n) {
			return fmt.Errorf("invalid transaction path")
		}
		current, err := readBrainFile(root, n)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, tx.Before[n]) && !bytes.Equal(current, after) {
			return fmt.Errorf("transaction conflicts with %s", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := atomicBrainFile(root, n, tx.After[n]); err != nil {
			return err
		}
	}
	return os.Remove(filepath.Join(root, ".pending-edits.json"))
}

func withBrainLock(root string, run func() error) error {
	f, err := os.OpenFile(filepath.Join(root, ".edits.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("brain writer busy: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return run()
}

func RecoverBrainEdits(brain EuclidBrain) error {
	if !brain.Writable {
		return fmt.Errorf("brain is read-only")
	}
	return withBrainLock(brain.Root, func() error { return recoverBrainFiles(brain.Root) })
}

func commitBrainFiles(root string, base, writes map[string][]byte) error {
	if len(writes) == 0 {
		return nil
	}
	return withBrainLock(root, func() error {
		if err := recoverBrainFiles(root); err != nil {
			return err
		}
		for n, before := range base {
			cur, err := readBrainFile(root, n)
			if err != nil {
				return err
			}
			if !bytes.Equal(cur, before) {
				return fmt.Errorf("brain changed during model call: %s", n)
			}
		}
		for n := range writes {
			if !validTransactionFile(n) {
				return fmt.Errorf("invalid transaction path")
			}
		}
		data, err := json.Marshal(brainTransaction{Before: base, After: writes})
		if err != nil {
			return err
		}
		if len(data) > 8<<20 {
			return fmt.Errorf("transaction too large")
		}
		if err := atomicBrainFile(root, ".pending-edits.json", data); err != nil {
			return err
		}
		return recoverBrainFiles(root)
	})
}
