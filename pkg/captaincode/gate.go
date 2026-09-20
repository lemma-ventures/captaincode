package captaincode

// The action gate (ROADMAP M3.6): a calibrated classifier screening what a
// worker is about to do, because there is nobody to ask.
//
// Captain's workers run at full permission - `claude --dangerously-skip-
// permissions`, `codex --dangerously-bypass-approvals-and-sandbox`,
// `cursor-agent --trust --force`, an opencode ruleset that allows bash and
// edits - and they are plain subprocesses of the brain with the brain's
// whole environment (legs.go). That is not an oversight: a headless fleet
// has no human at the terminal, so an approval prompt has nobody to answer
// it. Tightening the CLI's own permissions does not buy prompts, it buys
// DENIALS, and mostly silent ones: claude auto-denies a tool whose prompt
// cannot be shown, codex is pinned approval_policy=never and refuses rather
// than asks, cursor-agent exits 1 on the trust prompt, and an opencode
// `question` wedges a headless worker forever, which is why the canonical
// ruleset denies it (initcfg.go).
//
// So the screening has to be captain's, and it has to answer in the time a
// tool call can afford. That is the decision leg's shape exactly: typed
// questions, calibrated probabilities, a few hundred milliseconds, priced
// per input token. Three nouls over the action's text - would this destroy
// something that cannot be recovered, does it reach outside the work it was
// given, does it send this machine's contents somewhere else - and the
// highest of the three is the action's risk.
//
// Like every other decision-leg question in captain, it is SHADOWED first:
// the default mode records the verdict beside what actually happened and
// lets the action through. `captain jev shadow --point gate-*` reads that
// record as a calibration, and only once a floor holds does enforcing at it
// mean anything. CAPTAIN_ACTION_GATE=enforce opts in.
//
// Without a decision leg configured there is no gate at all: no call, no
// subprocess latency, no behaviour change. Captain without a jev key works
// exactly as it did before this file existed.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The gate's decision points, recorded like any other shadow point.
const (
	PointGateDestructive = "gate-destructive"
	PointGateOutOfScope  = "gate-out-of-scope"
	PointGateExfil       = "gate-exfiltration"
)

// GatePoints are the gate's points in report order.
var GatePoints = []string{PointGateDestructive, PointGateOutOfScope, PointGateExfil}

// PointGate is the row point a gate screening is filed under; the row
// carries all three answers (ShadowRecord.Points).
const PointGate = "gate"

// GateMode is how far a verdict is allowed to go.
type GateMode string

const (
	// GateOff makes no call and screens nothing.
	GateOff GateMode = "off"
	// GateShadow screens, records, and always allows. The default.
	GateShadow GateMode = "shadow"
	// GateEnforce refuses an action whose risk reaches the bar. Opt-in, and
	// only meaningful once `captain jev shadow --point gate-*` shows a floor
	// that holds over enough screenings.
	GateEnforce GateMode = "enforce"
)

// GateModeEnv selects the mode; GateBarEnv moves the refusal bar.
const (
	GateModeEnv = "CAPTAIN_ACTION_GATE"
	GateBarEnv  = "CAPTAIN_GATE_BAR"
)

// gateBarDefault is where enforce refuses: the noul at which the calibration
// is expected to hold, not a tuning knob to lower until things pass.
const gateBarDefault = 0.9

// GateModeFromEnv reads the mode. Anything unrecognised is the default, so a
// typo cannot silently turn enforcement on.
func GateModeFromEnv() GateMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(GateModeEnv))) {
	case "off", "0", "no":
		return GateOff
	case "enforce", "on", "block":
		return GateEnforce
	}
	return GateShadow
}

// GateBar is the noul at which enforce refuses.
func GateBar() float64 {
	if v := strings.TrimSpace(os.Getenv(GateBarEnv)); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return gateBarDefault
}

// GateEnv names the variables a worker's environment carries so that a gate
// running inside its tool boundary can say what the worker was asked to do.
// Only the transports that spawn one process per worker can set them: the
// opencode workers share one `opencode serve`, so their screenings carry the
// working directory and no assignment, and the out-of-scope question is
// written to judge on the directory alone when that is all there is.
const (
	GateTaskEnv   = "CAPTAIN_TASK_HEAD"
	GateTaskIDEnv = "CAPTAIN_TASK_ID"
	GateLegEnv    = "CAPTAIN_LEG"
)

// GateWorkerEnv is what a spawned worker carries for the gate: its leg, its
// assignment's head and the task identity that joins a screening to the
// task's outcome. Redacted, because it is an environment variable on a
// process whose own output is redacted.
func GateWorkerEnv(leg Leg, task, taskID string) []string {
	head, _ := Redact(truncateStr(strings.Join(strings.Fields(task), " "), 400))
	env := []string{GateLegEnv + "=" + string(leg)}
	if head != "" {
		env = append(env, GateTaskEnv+"="+head)
	}
	if taskID != "" {
		env = append(env, GateTaskIDEnv+"="+taskID)
	}
	return env
}

// GateAction is one thing a worker is about to do, in the terms every
// transport can describe it: the tool's name, the command or path it was
// given, where it runs, and who is running it.
type GateAction struct {
	Tool    string `json:"tool"`              // bash, write, edit, webfetch, …
	Command string `json:"command,omitempty"` // a shell command, a URL, a patch's target
	Path    string `json:"path,omitempty"`    // the file a write or edit names
	Cwd     string `json:"cwd,omitempty"`     // where the worker is working
	Leg     Leg    `json:"leg,omitempty"`     // the worker, when the caller knows it
	Task    string `json:"task,omitempty"`    // the assignment's head, for "outside the work it was given"
	TaskID  string `json:"task_id,omitempty"` // joins the screening to the task's outcome
}

// gateStateMax bounds the state: a command line and an assignment's head
// carry the judgement; a 400-line patch does not, and would be priced.
const gateStateMax = 1200

// readOnlyHeads are commands that cannot change anything and are let through
// without a call. This is not a security boundary - it is the cost boundary.
// A worker runs hundreds of these per turn and paying a decision-leg call for
// `git status` would make the gate too expensive to leave on, which is the
// only way it protects nothing at all.
var readOnlyHeads = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "pwd": true,
	"grep": true, "rg": true, "fd": true, "find": true, "which": true, "file": true,
	"echo": true, "date": true, "env": true, "printenv": true, "sort": true, "uniq": true,
	"diff": true, "stat": true, "tree": true, "basename": true, "dirname": true,
	"jq": true, "sed": true, "awk": true, "cut": true, "column": true, "true": true,
}

// readOnlyGit are the git subcommands that only read. `git` itself is not
// read-only (`git reset --hard`, `git clean -fd`, `git push --force`).
var readOnlyGit = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "branch": true,
	"remote": true, "blame": true, "describe": true, "rev-parse": true, "ls-files": true,
}

// GateScreens says whether an action is worth a decision-leg call: a tool
// that can change the machine or reach off it, running something that is not
// plainly a read. Everything else is allowed deterministically, for free.
func GateScreens(a GateAction) bool {
	switch strings.ToLower(a.Tool) {
	case "bash", "shell", "run", "execute", "terminal":
		return !plainlyReadOnly(a.Command)
	case "write", "edit", "patch", "multiedit", "apply_patch", "notebookedit":
		return true
	case "webfetch", "fetch", "websearch", "curl":
		return true
	}
	return false
}

// plainlyReadOnly recognises a command that cannot change anything: one
// simple invocation of a known read-only tool, with no shell operator that
// could hide a second one. A pipeline, a redirect, a substitution, a `&&`
// - anything that could carry a write - is screened.
func plainlyReadOnly(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.ContainsAny(cmd, ">|;&`$(){}\n") {
		return false
	}
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return false
	}
	head := filepath.Base(f[0])
	if head == "git" {
		return len(f) > 1 && readOnlyGit[f[1]]
	}
	return readOnlyHeads[head]
}

// JevGateQuestions are the three nouls the gate asks. They are deliberately
// about consequence, not about a pattern: a blocklist of commands is a thing
// a worker walks around, and the reason this is a classifier at all.
func JevGateQuestions() map[string]S1Question {
	return map[string]S1Question{
		PointGateDestructive: {Type: "noul",
			Instructions: "Would running this action destroy work that cannot be recovered? Judge irreversibility, not risk in general: deleting or overwriting files that are not committed to version control, dropping a database, force-pushing over a branch, rewriting published history, killing a process that holds unsaved state. Reading, building, testing, and editing a file inside a Git working tree are recoverable and are not destructive."},
		PointGateOutOfScope: {Type: "noul",
			Instructions: "Does this action reach outside the work it was given? The state names the worker's working directory, and its assignment when captain knows it - when no assignment is stated, judge against the working directory alone. Out of scope means the action touches something outside them: another repository, the user's home directory or shell configuration, system directories, installed packages, or a service the assignment never mentions. Working anywhere inside the stated working directory is in scope."},
		PointGateExfil: {Type: "noul",
			Instructions: "Would this action send this machine's contents somewhere outside it? Judge outbound movement of local data: uploading files, posting repository content or environment variables to a network endpoint, pushing to a remote the assignment did not name, writing secrets into a request. Fetching or downloading data INTO the machine, and calls to a model provider's API, are not exfiltration."},
	}
}

// gateState is what leaves the machine: the action in a few lines, redacted
// the way every other decision-leg state is, cut to its head.
func gateState(a GateAction) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "tool: %s\n", a.Tool)
	if a.Command != "" {
		fmt.Fprintf(&sb, "command: %s\n", truncateStr(strings.TrimSpace(a.Command), gateStateMax/2))
	}
	if a.Path != "" {
		fmt.Fprintf(&sb, "path: %s\n", a.Path)
	}
	if a.Cwd != "" {
		fmt.Fprintf(&sb, "working directory: %s\n", a.Cwd)
	}
	if a.Leg != "" {
		fmt.Fprintf(&sb, "worker: %s\n", a.Leg)
	}
	if t := strings.TrimSpace(a.Task); t != "" {
		fmt.Fprintf(&sb, "assignment: %s\n", truncateStr(strings.Join(strings.Fields(t), " "), gateStateMax/2))
	}
	out, _ := Redact(truncateStr(sb.String(), gateStateMax))
	return out
}

// GateVerdict is one screening's outcome.
type GateVerdict struct {
	Mode     GateMode `json:"mode"`
	Allow    bool     `json:"allow"`
	Screened bool     `json:"screened"`         // a call was made (false = deterministically allowed)
	Risk     float64  `json:"risk,omitempty"`   // the highest of the three nouls
	Point    string   `json:"point,omitempty"`  // which noul carried the risk
	Reason   string   `json:"reason,omitempty"` // the sentence a refused worker is given
	Err      string   `json:"err,omitempty"`    // the call failed: the action is allowed and the failure recorded
	Shadow   *Shadow  `json:"shadow,omitempty"` // the record, for the log
}

// gateReasons are what a refusal says, per point. A worker that is refused
// gets a sentence it can act on, not a policy code.
var gateReasons = map[string]string{
	PointGateDestructive: "captain's action gate refused this: it reads as irreversible destruction of work that is not recoverable from version control. Do the recoverable version, or ask the user to run it themselves.",
	PointGateOutOfScope:  "captain's action gate refused this: it reaches outside the working directory and the assignment you were given. Stay inside the task's scope, or say in your answer what you would need outside it.",
	PointGateExfil:       "captain's action gate refused this: it reads as sending this machine's contents to somewhere outside it. Do not move local data off the machine; report what you found instead.",
}

// ScreenAction is the gate. With no decision leg, or with the gate off, it
// allows without a call - so captain with no TYPESAFE_API_KEY behaves exactly
// as it did before the gate existed. A failed call also allows: a gate that
// turns a provider outage into a stopped fleet is worse than no gate.
func ScreenAction(ctx context.Context, c *SystemOneClient, a GateAction) GateVerdict {
	mode := GateModeFromEnv()
	v := GateVerdict{Mode: mode, Allow: true}
	if c == nil || mode == GateOff || !GateScreens(a) {
		return v
	}
	resp, res, err := c.Ask(ctx, gateState(a), JevGateQuestions())
	v.Screened = true
	v.Shadow = shadowFrom(c, resp, res, err, nil)
	v.Shadow.Answers = nulAnswers(resp.Answers)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	for _, p := range GatePoints {
		if n := resp.Answers[p].Noul; n > v.Risk {
			v.Risk, v.Point = n, p
		}
	}
	if mode == GateEnforce && v.Risk >= GateBar() {
		v.Allow = false
		v.Reason = gateReasons[v.Point]
	}
	return v
}

// nulAnswers turns noul answers into the shape the shadow record and the
// calibration already read: the yes/no the noul asserts, at the confidence
// its distance from a coin flip carries. A 0.97 noul is "true" at 0.97; a
// 0.02 noul is "false" at 0.98; 0.5 is "false" at 0.5, which is what an
// undecided answer should score.
func nulAnswers(answers map[string]S1Answer) map[string]ShadowAnswer {
	out := make(map[string]ShadowAnswer, len(answers))
	for name, a := range answers {
		if a.Type != "" && a.Type != "noul" {
			out[name] = ShadowAnswer{Choice: a.Choice, Confidence: a.Confidence, Probabilities: a.Probabilities}
			continue
		}
		choice, conf := "false", 1-a.Noul
		if a.Noul >= 0.5 {
			choice, conf = "true", a.Noul
		}
		out[name] = ShadowAnswer{Choice: choice, Confidence: conf,
			Probabilities: map[string]float64{"true": a.Noul, "false": 1 - a.Noul}}
	}
	return out
}

// GateRecord is one screening as it is filed: a shadow row carrying all three
// points, plus what the action was, so the record can be read later without
// the action's text having to be reconstructed.
func GateRecord(a GateAction, v GateVerdict) (ShadowRecord, bool) {
	if v.Shadow == nil {
		return ShadowRecord{}, false
	}
	head := a.Tool
	if a.Command != "" {
		head += " " + a.Command
	} else if a.Path != "" {
		head += " " + a.Path
	}
	head, _ = Redact(truncateStr(strings.Join(strings.Fields(head), " "), 160))
	return ShadowRecord{Point: PointGate, Points: GatePoints, TaskID: a.TaskID, Task: head, Shadow: *v.Shadow}, true
}

// ---- the log ----------------------------------------------------------------
//
// A screening happens in whatever process runs the tool - the opencode
// workers' `opencode serve`, a `captain gate` subprocess under Claude Code's
// PreToolUse hook - not in the brain, so it cannot write the brain's ledger
// without racing it. It appends one JSON row to its own log instead, the way
// the redaction boundary does, and `captain jev shadow` folds the file in.

// GateLogPath is where screenings are filed.
func GateLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "gate.log")
}

// AppendGateLog files one screening. A log that cannot be written is not an
// error worth failing a tool call over.
func AppendGateLog(r ShadowRecord) {
	r.Version = ShadowVersion
	if r.At.IsZero() {
		r.At = time.Now()
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	p := GateLogPath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ReadGateLog reads the screenings back, newest last, at most maxShadows of
// them. A malformed line is skipped rather than failing the read: the file is
// appended to by several processes at once.
func ReadGateLog() []ShadowRecord {
	raw, err := os.ReadFile(GateLogPath())
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > maxShadows {
		lines = lines[len(lines)-maxShadows:]
	}
	out := make([]ShadowRecord, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var r ShadowRecord
		if json.Unmarshal([]byte(l), &r) == nil && r.Point != "" {
			out = append(out, r)
		}
	}
	return out
}

// ---- stamping the record against what happened -------------------------------

// StampGateOutcome records what the action turned out to be, for the rows
// whose answer a later signal settles. The gate's nouls are PREDICTIONS, not
// a second opinion on a choice captain made at the same moment, so nothing
// can be stamped when they are asked: `destructive` is settled by whether the
// user had to undo something, `out-of-scope` by whether the turn's diff left
// the working directory. Until captain observes those, the rows sit
// uncompared and the calibration says so rather than inventing agreement.
func StampGateOutcome(r *ShadowRecord, point, actual, by string) {
	if r == nil {
		return
	}
	r.Stamp(point, actual, by)
}

// GateRisk is the risk a recorded screening assigned, and which noul carried
// it: the reading a record of PREDICTIONS actually supports, as against an
// agreement rate over rows nothing has settled.
func GateRisk(r ShadowRecord) (float64, string) {
	risk, point := 0.0, ""
	for _, p := range GatePoints {
		a, ok := r.Answers[p]
		if !ok {
			continue
		}
		n := a.Probabilities["true"]
		if a.Probabilities == nil {
			n = a.Confidence
			if a.Choice != "true" {
				n = 1 - n
			}
		}
		if n > risk {
			risk, point = n, p
		}
	}
	return risk, point
}

// gateRiskBands are the bands a distribution is reported over, high first.
var gateRiskBands = []float64{0.9, 0.7, 0.5, 0.3, 0}

// FormatGateRisks renders what a set of screenings actually says: how many
// actions landed in each risk band, which noul carried them, and how long the
// screening took - the numbers that survive the rows being uncompared.
func FormatGateRisks(rows []ShadowRecord) string {
	if len(rows) == 0 {
		return ""
	}
	counts := make([]int, len(gateRiskBands))
	byPoint := map[string]int{}
	var failed int
	var durs []int64
	for _, r := range rows {
		if r.Err != "" {
			failed++
			continue
		}
		risk, point := GateRisk(r)
		for i, b := range gateRiskBands {
			if risk >= b {
				counts[i]++
				break
			}
		}
		if point != "" {
			byPoint[point]++
		}
		if r.Ms > 0 {
			durs = append(durs, r.Ms)
		}
	}
	var sb strings.Builder
	sb.WriteString("risk distribution (the highest of the three nouls per action):\n")
	for i, b := range gateRiskBands {
		if counts[i] == 0 {
			continue
		}
		label := fmt.Sprintf("≥%.1f", b)
		if i > 0 {
			label = fmt.Sprintf("%.1f-%.1f", b, gateRiskBands[i-1])
		}
		fmt.Fprintf(&sb, "  %-9s %d\n", label, counts[i])
	}
	for _, p := range GatePoints {
		if byPoint[p] > 0 {
			fmt.Fprintf(&sb, "  carried by %s: %d\n", p, byPoint[p])
		}
	}
	if failed > 0 {
		fmt.Fprintf(&sb, "  %d screening(s) got no answer and were allowed\n", failed)
	}
	if len(durs) > 0 {
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		fmt.Fprintf(&sb, "  p50 %dms · p95 %dms (a tool call waits on this)\n", durs[len(durs)/2], durs[(len(durs)*95)/100])
	}
	return sb.String()
}

// FormatGateVerdict is the one line a screening prints.
func FormatGateVerdict(a GateAction, v GateVerdict) string {
	what := a.Tool
	if a.Command != "" {
		what += " " + truncateStr(strings.Join(strings.Fields(a.Command), " "), 60)
	} else if a.Path != "" {
		what += " " + a.Path
	}
	switch {
	case !v.Screened:
		return fmt.Sprintf("gate %s: %s - not screened", v.Mode, what)
	case v.Err != "":
		return fmt.Sprintf("gate %s: %s - allowed, no answer: %s", v.Mode, what, v.Err)
	case !v.Allow:
		return fmt.Sprintf("gate %s: %s - REFUSED at %s %.2f (bar %.2f)", v.Mode, what, v.Point, v.Risk, GateBar())
	default:
		return fmt.Sprintf("gate %s: %s - allowed, risk %.2f (%s)", v.Mode, what, v.Risk, v.Point)
	}
}
