package captaincode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Worker processes a brain started, recorded on disk. A brain killed with
// SIGKILL runs no cleanup: its worker CLIs are reparented and keep working,
// editing files for a turn nobody waits on. The next brain stops them.

type workerRecord struct {
	PID          int    `json:"pid"`
	Started      string `json:"started"`
	Brain        int    `json:"brain"`
	BrainStarted string `json:"brain_started"`
	Label        string `json:"label"`
}

func workerPidDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".captaincode", "workers"), nil
}

// psStarted is processStarted through ps, for platforms without a native
// reading. A sandbox that refuses setuid programs refuses ps too.
func psStarted(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

// TrackWorker records a started worker process until the returned func runs.
func TrackWorker(proc *os.Process, label string) (untrack func()) {
	dir, err := workerPidDir()
	if proc == nil || err != nil || os.MkdirAll(dir, 0o755) != nil {
		return func() {}
	}
	rec := workerRecord{
		PID:          proc.Pid,
		Started:      processStarted(proc.Pid),
		Brain:        os.Getpid(),
		BrainStarted: processStarted(os.Getpid()),
		Label:        label,
	}
	data, _ := json.Marshal(rec)
	file := filepath.Join(dir, strconv.Itoa(proc.Pid)+".json")
	if os.WriteFile(file, data, 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(file) }
}

// ReapOrphanWorkers stops the recorded workers whose brain is gone and
// reports them. A worker whose brain still runs belongs to that brain.
func ReapOrphanWorkers() []string {
	dir, err := workerPidDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var reaped []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		var rec workerRecord
		if err != nil || json.Unmarshal(data, &rec) != nil || rec.PID <= 0 {
			_ = os.Remove(file)
			continue
		}
		if rec.Brain == os.Getpid() || (rec.BrainStarted != "" && processStarted(rec.Brain) == rec.BrainStarted) {
			continue
		}
		if rec.Started != "" && processStarted(rec.PID) == rec.Started {
			if syscall.Kill(rec.PID, syscall.SIGKILL) == nil {
				reaped = append(reaped, fmt.Sprintf("%s (pid %d)", rec.Label, rec.PID))
			}
		}
		_ = os.Remove(file)
	}
	return reaped
}
