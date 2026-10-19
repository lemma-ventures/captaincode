package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReapOrphanWorkersStopsOnlyADeadBrainsWorkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orphan := exec.Command("sleep", "30")
	mine := exec.Command("sleep", "30")
	for _, cmd := range []*exec.Cmd{orphan, mine} {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	TrackWorker(orphan.Process, "claude")
	untrack := TrackWorker(mine.Process, "codex-cli")
	defer untrack()

	// The orphan's brain is gone: rewrite its record to name a dead brain.
	dir, _ := workerPidDir()
	file := filepath.Join(dir, strconv.Itoa(orphan.Process.Pid)+".json")
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	rec := `{"pid":` + strconv.Itoa(orphan.Process.Pid) + `,"started":"` + processStarted(orphan.Process.Pid) +
		`","brain":` + strconv.Itoa(gone.Process.Pid) + `,"brain_started":"long ago","label":"claude"}`
	if err := os.WriteFile(file, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	reaped := ReapOrphanWorkers()
	if len(reaped) != 1 {
		t.Fatalf("reaped %v, want only the dead brain's worker", reaped)
	}
	done := make(chan error, 1)
	go func() { done <- orphan.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphaned worker is still running")
	}
	if processStarted(mine.Process.Pid) == "" {
		t.Error("a live brain's worker was stopped")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the reaped worker's record stays")
	}
}
