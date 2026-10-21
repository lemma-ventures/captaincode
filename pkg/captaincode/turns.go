package captaincode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Turns a brain has under way, recorded on disk. OpenCode retries a
// provider request that failed with a lost connection or a 5xx, and no
// setting turns that off. When a brain dies during a turn, the retry
// reaches the next brain as a new request for the same prompt. Running it
// would start a second worker for a turn whose first worker may already
// have edited files.

type turnRecord struct {
	Brain        int       `json:"brain"`
	BrainStarted string    `json:"brain_started"`
	At           time.Time `json:"at"`
}

// interruptedTTL bounds how long an interrupted turn is recognised. The
// retries come seconds apart; a prompt sent again hours later is new.
const interruptedTTL = time.Hour

func turnDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".captaincode", "turns"), nil
}

// BeginTurn records that this brain runs the turn named key, until the
// returned func runs.
func BeginTurn(key string) (end func()) {
	dir, err := turnDir()
	if key == "" || err != nil || os.MkdirAll(dir, 0o755) != nil {
		return func() {}
	}
	data, _ := json.Marshal(turnRecord{Brain: os.Getpid(), BrainStarted: processStarted(os.Getpid()), At: time.Now()})
	file := filepath.Join(dir, key+".json")
	if os.WriteFile(file, data, 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(file) }
}

// InterruptedTurn reports whether the turn named key was under way in a
// brain that is gone. It forgets the turn, so the same prompt sent again
// runs.
func InterruptedTurn(key string) bool {
	dir, err := turnDir()
	if key == "" || err != nil {
		return false
	}
	file := filepath.Join(dir, key+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	var rec turnRecord
	if json.Unmarshal(data, &rec) != nil || rec.Brain <= 0 {
		_ = os.Remove(file)
		return false
	}
	if rec.Brain == os.Getpid() || (rec.BrainStarted != "" && processStarted(rec.Brain) == rec.BrainStarted) {
		return false
	}
	_ = os.Remove(file)
	return time.Since(rec.At) < interruptedTTL
}
