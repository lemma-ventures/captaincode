package captaincode

// Liveness: what a quiet CLI worker is actually doing.
//
// A CLI worker (claude -p, codex exec, cursor-agent) is cut when its stream
// stays silent past its window (progress.quiet). Silence alone does not say
// whether the agent is thinking or wedged, and the process can tell: a model
// streaming tokens moves the agent's own CPU time and network bytes, a wedged
// one moves neither. On 2026-10-06 a cursor run finished and deployed its
// work in 7m45s, then went silent for 30 minutes and was cut with "partial
// output"; nothing showed what it was doing meanwhile, and two preview
// servers it had started kept running afterwards, reachable from the LAN.
//
// The sampler reads the worker process once a minute. CPU or network
// movement counts as activity (the run is not quiet); otherwise a status
// line says how long the run has been quiet and that the process is idle.
// At the end, processes the worker started and left running are named,
// not killed: a deliberate watcher armed with `captain send` must survive.

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// livenessEvery is how often a worker process is sampled (a variable so a
// test can sample faster).
var livenessEvery = time.Minute

// Activity thresholds per sample: an idle Node or Python CLI spends a few
// milliseconds of CPU a minute on timers, and keep-alives move a few hundred
// bytes. A model streaming tokens moves far more of both.
const (
	livenessCPU = 300 * time.Millisecond
	livenessNet = 4096
)

type procNode struct {
	PID, PPID int
	CPU       time.Duration
	Cmd       string
}

// psTable is every process on the machine: pid, parent, CPU time, command.
func psTable() map[int]procNode {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,time=,command=").Output()
	if err != nil {
		return nil
	}
	table := map[int]procNode{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		table[pid] = procNode{PID: pid, PPID: ppid, CPU: parseCPUTime(f[2]), Cmd: strings.Join(f[3:], " ")}
	}
	return table
}

// parseCPUTime reads ps's TIME: [dd-][hh:]mm:ss[.xx] (macOS prints
// minutes past 59, "854:09.40").
func parseCPUTime(s string) time.Duration {
	days := 0
	if i := strings.Index(s, "-"); i >= 0 {
		days, _ = strconv.Atoi(s[:i])
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return time.Duration((total + float64(days)*86400) * float64(time.Second))
}

// descendantsOf lists root's descendants in table, nearest first.
func descendantsOf(table map[int]procNode, root int) []procNode {
	children := map[int][]int{}
	for pid, n := range table {
		children[n.PPID] = append(children[n.PPID], pid)
	}
	var out []procNode
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		kids := children[p]
		sort.Ints(kids)
		for _, k := range kids {
			out = append(out, table[k])
			queue = append(queue, k)
		}
	}
	return out
}

// netBytes is the bytes a process has received and sent, where the system
// can say (macOS nettop); ok is false elsewhere.
func netBytes(pid int) (int64, bool) {
	if runtime.GOOS != "darwin" {
		return 0, false
	}
	out, err := exec.Command("nettop", "-P", "-L", "1", "-n", "-x", "-J", "bytes_in,bytes_out", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 || !strings.HasSuffix(f[0], "."+strconv.Itoa(pid)) {
			continue
		}
		in, err1 := strconv.ParseInt(f[1], 10, 64)
		outB, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 == nil && err2 == nil {
			return in + outB, true
		}
	}
	return 0, false
}

// liveness samples one worker process.
type liveness struct {
	mu      sync.Mutex
	pid     int
	name    string
	cpu     time.Duration
	net     int64
	hasNet  bool
	sampled bool
	kids    []procNode // the worker's descendants at the last sample
	note    func(string)
}

// sample reads the process; active reports CPU or network movement since the
// previous sample, and detail says what moved.
func (l *liveness) sample() (active bool, detail string, ok bool) {
	table := psTable()
	self, found := table[l.pid]
	if !found {
		return false, "", false
	}
	net, hasNet := netBytes(l.pid)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kids = descendantsOf(table, l.pid)
	if !l.sampled {
		l.cpu, l.net, l.hasNet, l.sampled = self.CPU, net, hasNet, true
		return false, "", true
	}
	dCPU := self.CPU - l.cpu
	dNet := net - l.net
	l.cpu, l.net = self.CPU, net
	moved := dCPU >= livenessCPU || (hasNet && l.hasNet && dNet >= livenessNet)
	l.hasNet = hasNet
	detail = fmt.Sprintf("%.1fs CPU", dCPU.Seconds())
	if hasNet {
		detail += fmt.Sprintf(", %s on the network", humanBytes(dNet))
	}
	return moved, detail + " in the last minute", true
}

// snapshot records the worker's descendants now: what it may leave behind.
func (l *liveness) snapshot() {
	table := psTable()
	if _, ok := table[l.pid]; !ok {
		return
	}
	kids := descendantsOf(table, l.pid)
	l.mu.Lock()
	l.kids = kids
	l.mu.Unlock()
}

// leftovers are the processes the worker started that are still running
// after it exited, identified by pid and command so a reused pid is not
// mistaken for one of them.
func (l *liveness) leftovers() []procNode {
	l.mu.Lock()
	kids := append([]procNode(nil), l.kids...)
	l.mu.Unlock()
	if len(kids) == 0 {
		return nil
	}
	table := psTable()
	var out []procNode
	for _, k := range kids {
		if n, ok := table[k.PID]; ok && n.Cmd == k.Cmd && syscall.Kill(k.PID, 0) == nil {
			out = append(out, n)
		}
	}
	return out
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// leftoverLine names what a finished worker left running, or "".
func leftoverLine(name string, procs []procNode) string {
	var shown []string
	seen := map[string]bool{}
	for _, p := range procs {
		cmd := p.Cmd
		// A shell wrapper carries its real command after " -- "; the
		// wrapper and the command it runs are one entry.
		if i := strings.LastIndex(cmd, " -- "); i >= 0 {
			cmd = cmd[i+4:]
		}
		if len(cmd) > 60 {
			cmd = cmd[:60] + "…"
		}
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		shown = append(shown, fmt.Sprintf("pid %d %s", p.PID, cmd))
	}
	if len(shown) == 0 {
		return ""
	}
	return fmt.Sprintf("%s left %d process(es) running after it ended: %s - stop them with `kill <pid>` if they were not meant to outlive the turn", name, len(shown), strings.Join(shown, "; "))
}
