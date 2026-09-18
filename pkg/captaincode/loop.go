package captaincode

import (
	"regexp"
	"strings"
)

// LoopPlan is a compiled iteration derived from the user's prompt: the user
// says "keep fixing until tests pass"; superagent owns the loop, the model
// only does one iteration's work. Until is a shell command whose exit code 0
// stops the loop; empty Until means single-shot.
type LoopPlan struct {
	Task     string
	Until    string // shell condition; exit 0 = done
	MaxIters int
}

var untilRe = regexp.MustCompile(`(?i)\buntil\b(.+)$`)

// knownConditions maps natural-language stop conditions to shell checks.
// Deterministic and boring on purpose: the loop harness must never guess.
var knownConditions = []struct {
	match string
	cmd   string
}{
	{"tests pass", "go test ./..."},
	{"test passes", "go test ./..."},
	{"tests are green", "go test ./..."},
	{"it builds", "go build ./..."},
	{"build passes", "go build ./..."},
	{"lint passes", "golangci-lint run"},
	{"typecheck passes", "npm run typecheck"},
}

// CompileLoop derives a LoopPlan from a prompt. If the prompt contains a
// recognizable "until <condition>" it returns a bounded loop; otherwise a
// single-shot plan. explicitUntil (from --until) always wins.
func CompileLoop(task, explicitUntil string, maxIters int) LoopPlan {
	if maxIters <= 0 {
		maxIters = 5
	}
	plan := LoopPlan{Task: task, MaxIters: maxIters}
	if explicitUntil != "" {
		plan.Until = explicitUntil
		return plan
	}
	m := untilRe.FindStringSubmatch(task)
	if m == nil {
		return plan
	}
	cond := strings.ToLower(strings.TrimSpace(m[1]))
	for _, kc := range knownConditions {
		if strings.Contains(cond, kc.match) {
			plan.Until = kc.cmd
			return plan
		}
	}
	return plan
}
