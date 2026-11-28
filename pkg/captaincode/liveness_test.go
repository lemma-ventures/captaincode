package captaincode

import (
	"math"
	"os"
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

// 20-22 Nov: eight claude workers on one Mac stopped at a `find` into
// privacy-guarded home folders. The worker and the find spent no CPU for two
// hours. A tree in which nothing moves counts as frozen; a tree with a busy
// tool or a tool that keeps starting processes does not.
func TestLivenessCountsOnlyAFrozenTree(t *testing.T) {
	saved := livenessEvery
	livenessEvery = time.Millisecond
	t.Cleanup(func() { livenessEvery = saved })

	for _, tc := range []struct {
		name   string
		script string
		frozen bool
	}{
		{"stuck tool", "sleep 60 & wait", true},
		{"busy tool", "(while :; do :; done) & wait", false},
		{"polling tool", "while :; do sleep 0.05; done", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := startProc(t, tc.script)
			l := &liveness{pid: proc.Process.Pid, name: "claude"}
			time.Sleep(200 * time.Millisecond) // let the child start
			for i := 0; i < 4; i++ {
				_, _, ok := l.sample()
				require.True(t, ok)
				time.Sleep(300 * time.Millisecond)
			}
			if tc.frozen {
				assert.GreaterOrEqual(t, l.frozenSamples(), 3)
			} else {
				assert.Zero(t, l.frozenSamples())
			}
		})
	}
}

// A tool can move bytes while its own CPU stays under the frozen line.
// That traffic is not the worker's. The tree is not frozen.
func TestLivenessDoesNotFreezeAChildThatMovesNetwork(t *testing.T) {
	savedEvery := livenessEvery
	savedNet := readNetBytes
	livenessEvery = time.Millisecond
	t.Cleanup(func() {
		livenessEvery = savedEvery
		readNetBytes = savedNet
	})

	proc := startProc(t, "sleep 60 & wait")
	parent := proc.Process.Pid
	var moved int64
	readNetBytes = func(pid int) (int64, bool) {
		if pid == parent {
			return 0, true
		}
		moved += 8192
		return moved, true
	}
	l := &liveness{pid: parent, name: "claude"}
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 4; i++ {
		_, _, ok := l.sample()
		require.True(t, ok)
		time.Sleep(50 * time.Millisecond)
	}
	assert.Zero(t, l.frozenSamples())
	assert.Greater(t, moved, int64(0), "the child was measured")
}

func BenchmarkSampleNet(b *testing.B) {
	saved := readNetBytes
	b.Cleanup(func() { readNetBytes = saved })
	readNetBytes = func(int) (int64, bool) { return 4096, true }
	pids := []int{1, 2}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sampleNet(pids)
	}
}

func BenchmarkSampleNetMissing(b *testing.B) {
	saved := readNetBytes
	b.Cleanup(func() { readNetBytes = saved })
	readNetBytes = func(pid int) (int64, bool) {
		if pid == 2 {
			return 0, false
		}
		return 4096, true
	}
	pids := []int{1, 2, 3}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sampleNet(pids)
	}
}

func BenchmarkSampleNetOverflow(b *testing.B) {
	saved := readNetBytes
	b.Cleanup(func() { readNetBytes = saved })
	readNetBytes = func(int) (int64, bool) { return math.MaxInt64, true }
	pids := []int{1, 2}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sampleNet(pids)
	}
}

func TestSampleNetStopsAtTheTreeCap(t *testing.T) {
	saved := readNetBytes
	t.Cleanup(func() { readNetBytes = saved })
	calls := 0
	readNetBytes = func(int) (int64, bool) {
		calls++
		return 1, true
	}
	_, _, complete := sampleNet(make([]int, frozenTreeCap+1))
	assert.False(t, complete)
	assert.Zero(t, calls)

	sum, ok, complete := sampleNet([]int{1, 2})
	assert.True(t, complete)
	assert.True(t, ok)
	assert.Equal(t, int64(2), sum)
	assert.Equal(t, 2, calls)
}

func TestSampleNetIncompleteWhenCountersDisagree(t *testing.T) {
	saved := readNetBytes
	t.Cleanup(func() { readNetBytes = saved })
	calls := 0
	readNetBytes = func(pid int) (int64, bool) {
		calls++
		if pid == 2 {
			return 0, false
		}
		return 10, true
	}
	_, _, complete := sampleNet([]int{1, 2, 3})
	assert.False(t, complete)
	assert.Equal(t, 2, calls, "a missing counter after a real one stops the sum")

	calls = 0
	readNetBytes = func(pid int) (int64, bool) {
		calls++
		if pid == 1 {
			return 0, false
		}
		return 10, true
	}
	_, _, complete = sampleNet([]int{1, 2, 3})
	assert.False(t, complete)
	assert.Equal(t, 2, calls, "a real counter after a missing one stops the sum")

	calls = 0
	readNetBytes = func(int) (int64, bool) {
		calls++
		return 0, false
	}
	sum, ok, complete := sampleNet([]int{1, 2})
	assert.True(t, complete, "no counter at all stays on CPU")
	assert.False(t, ok)
	assert.Equal(t, int64(0), sum)
	assert.Equal(t, 2, calls)
}

func TestLivenessDoesNotFreezeWhenAChildCounterIsMissing(t *testing.T) {
	savedEvery := livenessEvery
	savedNet := readNetBytes
	livenessEvery = time.Millisecond
	t.Cleanup(func() {
		livenessEvery = savedEvery
		readNetBytes = savedNet
	})

	proc := startProc(t, "sleep 60 & wait")
	parent := proc.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(descendantsOf(psTable(), parent)) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, descendantsOf(psTable(), parent))
	readNetBytes = func(pid int) (int64, bool) {
		if pid == parent {
			return 0, true
		}
		return 0, false
	}
	l := &liveness{pid: parent, name: "claude"}
	for i := 0; i < 4; i++ {
		_, _, ok := l.sample()
		require.True(t, ok)
		time.Sleep(20 * time.Millisecond)
	}
	assert.Zero(t, l.frozenSamples())
}

func TestLivenessCountsCPUWhenTheNetworkSampleIsIncomplete(t *testing.T) {
	savedEvery := livenessEvery
	savedNet := readNetBytes
	livenessEvery = time.Millisecond
	t.Cleanup(func() {
		livenessEvery = savedEvery
		readNetBytes = savedNet
	})

	proc := startProc(t, "sleep 60 & while :; do :; done")
	parent := proc.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(descendantsOf(psTable(), parent)) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, descendantsOf(psTable(), parent))
	readNetBytes = func(pid int) (int64, bool) {
		if pid == parent {
			return 0, true
		}
		return 0, false
	}
	l := &liveness{pid: parent, name: "claude"}
	active := false
	for i := 0; i < 4; i++ {
		moved, _, ok := l.sample()
		require.True(t, ok)
		active = active || moved
		time.Sleep(400 * time.Millisecond)
	}
	assert.True(t, active, "the worker's own CPU still counts")
	assert.Zero(t, l.frozenSamples())
}

func TestSampleNetRejectsOverflowAndANegativeCounter(t *testing.T) {
	saved := readNetBytes
	t.Cleanup(func() { readNetBytes = saved })
	calls := 0
	readNetBytes = func(int) (int64, bool) {
		calls++
		return math.MaxInt64, true
	}
	_, _, complete := sampleNet([]int{1, 2})
	assert.False(t, complete)
	assert.Equal(t, 2, calls)

	calls = 0
	readNetBytes = func(int) (int64, bool) {
		calls++
		return -1, true
	}
	_, _, complete = sampleNet([]int{1, 2})
	assert.False(t, complete)
	assert.Equal(t, 1, calls, "a negative counter stops the sum")
}

func TestLivenessDoesNotFreezeWhenTheNetworkSumBreaks(t *testing.T) {
	saved := readNetBytes
	t.Cleanup(func() { readNetBytes = saved })
	proc := startProc(t, "sleep 60 & wait")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(descendantsOf(psTable(), proc.Process.Pid)) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, descendantsOf(psTable(), proc.Process.Pid))
	phase := 0
	readNetBytes = func(int) (int64, bool) {
		switch phase {
		case 0:
			return 1000, true
		case 1:
			return math.MaxInt64, true
		default:
			return 0, true
		}
	}
	l := &liveness{pid: proc.Process.Pid, name: "claude"}
	_, _, ok := l.sample()
	require.True(t, ok)
	base := l.net
	require.GreaterOrEqual(t, base, int64(2000))

	phase = 1
	_, _, ok = l.sample()
	require.True(t, ok)
	assert.Zero(t, l.frozenSamples())
	assert.Equal(t, base, l.net, "the last good total stays")

	phase = 2
	_, _, ok = l.sample()
	require.True(t, ok)
	assert.Zero(t, l.frozenSamples(), "a counter that goes backwards is not stillness")
	assert.Equal(t, int64(0), l.net)
}

func TestTreeStill(t *testing.T) {
	a := map[int]procNode{10: {PID: 10, Cmd: "find ~/Downloads", CPU: time.Second}}
	assert.True(t, treeStill(a, map[int]procNode{10: {PID: 10, Cmd: "find ~/Downloads", CPU: time.Second + 10*time.Millisecond}}))
	assert.False(t, treeStill(a, map[int]procNode{10: {PID: 10, Cmd: "find ~/Downloads", CPU: 2 * time.Second}}), "the tool worked")
	assert.False(t, treeStill(a, map[int]procNode{10: {PID: 10, Cmd: "other", CPU: time.Second}}), "a reused pid")
	assert.False(t, treeStill(a, map[int]procNode{11: {PID: 11, Cmd: "find ~/Downloads", CPU: time.Second}}), "a new process")
	assert.False(t, treeStill(a, map[int]procNode{}), "the tool ended")
	assert.True(t, treeStill(map[int]procNode{}, map[int]procNode{}))
	assert.False(t, treeStill(a, map[int]procNode{10: {PID: 10, Cmd: "find ~/Downloads", CPU: 0}}), "the tool counter went backwards")
}

// A frozen claude run is cut after its frozen window, not after the 2h tool
// window, and says why. A run that keeps working is not cut.
func TestProgressCutsAFrozenRunAndKeepsABusyOne(t *testing.T) {
	saved := livenessEvery
	livenessEvery = 5 * time.Millisecond
	t.Cleanup(func() { livenessEvery = saved })

	stuck := startProc(t, "sleep 60 & wait")
	ctx, cancel, prog, capped := progressCtx(10*time.Millisecond, 0)
	defer cancel()
	prog.watch(stuck.Process.Pid, "claude", nil)
	prog.setFrozen(20 * time.Millisecond)
	prog.toolStart()
	select {
	case <-ctx.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("a frozen run with a tool in flight was not cut")
	}
	assert.True(t, capped.Load())
	assert.Contains(t, prog.frozeNote(), "frozen for")
	assert.Contains(t, prog.frozeNote(), "spent no CPU")

	busy := startProc(t, "while :; do :; done")
	ctx2, cancel2, prog2, capped2 := progressCtx(10*time.Millisecond, 0)
	defer cancel2()
	prog2.watch(busy.Process.Pid, "claude", nil)
	prog2.setFrozen(20 * time.Millisecond)
	prog2.toolStart()
	select {
	case <-ctx2.Done():
		t.Fatal("a busy run was cut")
	case <-time.After(1500 * time.Millisecond):
	}
	assert.False(t, capped2.Load())
	assert.Empty(t, prog2.frozeNote())
}

func TestClaudeFrozenTimeoutSetting(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_FROZEN_TIMEOUT", "")
	assert.Equal(t, 15*time.Minute, claudeFrozenTimeout())
	t.Setenv("CAPTAIN_WORKER_FROZEN_TIMEOUT", "40m")
	assert.Equal(t, 40*time.Minute, claudeFrozenTimeout())
	t.Setenv("CAPTAIN_WORKER_FROZEN_TIMEOUT", "0")
	assert.Equal(t, time.Duration(0), claudeFrozenTimeout(), "0 turns it off")
	t.Setenv("CAPTAIN_WORKER_FROZEN_TIMEOUT", "garbage")
	assert.Equal(t, 15*time.Minute, claudeFrozenTimeout())

	var p *progress
	assert.False(t, p.isFrozen(), "nil-safe")
	assert.Empty(t, p.frozeNote())
	assert.Empty(t, p.frozeSuffix())
	off := &progress{}
	assert.False(t, off.isFrozen(), "no window, never frozen")
	assert.Empty(t, off.frozeSuffix())

	t.Setenv("CAPTAIN_WORKER_FROZEN_TIMEOUT", "40m")
	armed := &progress{}
	armed.armFrozen()
	assert.Equal(t, 40*time.Minute, armed.frozenWindow())
	armed.froze.Store(true)
	assert.Equal(t, " (frozen for 40m0s: the worker and its tools spent no CPU and started no process)", armed.frozeSuffix())
}

// Every sampled CLI worker arms the same frozen window. A tool in flight
// on cursor, codex or grok must not wait out the 2h tool window while the
// process tree is frozen.
func TestCLIWorkersArmTheFrozenWindow(t *testing.T) {
	files := []string{"legs.go", "grokcli.go", "codexcli.go", "codexappserver.go"}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		watches := strings.Count(text, "prog.watch(")
		arms := strings.Count(text, "prog.armFrozen()")
		if watches == 0 {
			t.Fatalf("%s has no worker watch", name)
		}
		if arms != watches {
			t.Fatalf("%s arms the frozen window %d times for %d watches", name, arms, watches)
		}
	}
}

func BenchmarkFrozenSuffix(b *testing.B) {
	p := &progress{}
	p.setFrozen(15 * time.Minute)
	p.froze.Store(true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if p.frozeSuffix() == "" {
			b.Fatal("empty")
		}
	}
}
