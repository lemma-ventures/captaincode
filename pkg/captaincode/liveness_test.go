package captaincode

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPUTime(t *testing.T) {
	assert.Equal(t, 854*time.Minute+9400*time.Millisecond, parseCPUTime("854:09.40"), "macOS: minutes past 59")
	assert.Equal(t, time.Hour+2*time.Minute+3*time.Second, parseCPUTime("01:02:03"), "Linux hh:mm:ss")
	assert.Equal(t, 24*time.Hour+time.Second, parseCPUTime("1-00:00:01"), "days")
	assert.Equal(t, time.Duration(0), parseCPUTime("garbage"))
}

func startProc(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// 2026-10-06: a cursor run silent for 30 minutes was cut with nothing to say
// whether it was thinking or wedged. A silent run whose process is busy is
// working; a silent idle one says so.
func TestLivenessKeepsABusySilentRunAndNamesAnIdleOne(t *testing.T) {
	saved := livenessEvery
	livenessEvery = time.Millisecond
	t.Cleanup(func() { livenessEvery = saved })

	busy := startProc(t, "while :; do :; done")
	idle := startProc(t, "sleep 30")

	var mu sync.Mutex
	var notes []string
	note := func(s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }

	for _, tc := range []struct {
		name    string
		pid     int
		working bool
	}{{"busy", busy.Process.Pid, true}, {"idle", idle.Process.Pid, false}} {
		p := &progress{idle: 30 * time.Minute}
		p.last.Store(time.Now().Add(-10 * time.Minute).UnixNano()) // silent for 10 minutes
		p.watch(tc.pid, tc.name, note)
		var lastSample, lastNote time.Time
		p.checkLive(time.Now(), &lastSample, &lastNote) // baseline
		time.Sleep(1200 * time.Millisecond)
		p.checkLive(time.Now(), &lastSample, &lastNote)
		silent := time.Since(time.Unix(0, p.last.Load()))
		if tc.working {
			assert.Less(t, silent, time.Minute, "a busy process counts as activity")
		} else {
			assert.Greater(t, silent, 9*time.Minute, "an idle process does not")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(notes, "\n")
	assert.Contains(t, joined, "busy silent 10m0s but working")
	assert.Contains(t, joined, "idle quiet 10m0s, process idle")
}

// The preview servers a cursor run started kept running after it was cut,
// reachable from the LAN. A finished worker names what it left running.
func TestLivenessNamesWhatAFinishedWorkerLeftRunning(t *testing.T) {
	worker := exec.Command("sh", "-c", "sleep 37 & sleep 0.5")
	require.NoError(t, worker.Start())
	p := &progress{}
	p.watch(worker.Process.Pid, "cursor", nil)
	time.Sleep(200 * time.Millisecond)
	p.snapshotKids()
	require.NoError(t, worker.Wait())

	left := p.live.Load().leftovers()
	t.Cleanup(func() {
		for _, n := range left {
			_ = exec.Command("kill", strconv.Itoa(n.PID)).Run()
		}
	})
	note := p.leftoverNote()
	assert.Contains(t, note, "cursor left 1 process(es) running after it ended")
	assert.Contains(t, note, "sleep 37")

	// A pid reused by another command is not one of the worker's.
	l := &liveness{kids: []procNode{{PID: left[0].PID, Cmd: "something else"}}}
	assert.Empty(t, l.leftovers())
}
