package captaincode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestInterruptedTurnIsADeadBrainsTurnOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	end := BeginTurn("mine")
	if InterruptedTurn("mine") {
		t.Error("this brain's own turn read as interrupted")
	}
	end()
	if InterruptedTurn("mine") {
		t.Error("a finished turn read as interrupted")
	}

	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dir, _ := turnDir()
	write := func(key string, at time.Time) {
		rec := `{"brain":` + strconv.Itoa(gone.Process.Pid) + `,"brain_started":"long ago","at":"` + at.Format(time.RFC3339Nano) + `"}`
		if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lost", time.Now())
	if !InterruptedTurn("lost") {
		t.Error("a dead brain's turn did not read as interrupted")
	}
	if InterruptedTurn("lost") {
		t.Error("the same prompt sent again still reads as interrupted")
	}
	write("old", time.Now().Add(-2*interruptedTTL))
	if InterruptedTurn("old") {
		t.Error("a turn interrupted hours ago blocks a prompt sent now")
	}
}
