package main

// Saved workflows (R3, PRIME_AGENT_NOTES.md): recurring topologies as named,
// human-editable artifacts. The dogfood data showed the same review workflow
// retyped for days - now:
//
//	/wf save paper-review /grok analyse the paper > /claude audit it
//	/wf run paper-review
//	/wf list
//
// One file per workflow under ~/.captaincode/workflows/<name>.cwl - the raw
// expression on the first non-comment line, gates included. Validated at save
// time so a broken expression can never lie dormant.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

var wfNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func workflowsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".captaincode", "workflows")
}

func saveNamedWorkflow(name, expr string) error {
	if !wfNameRe.MatchString(name) {
		return fmt.Errorf("workflow name must be lowercase letters, digits and dashes (got %q)", name)
	}
	wf, err := captaincode.ParseWorkflow(expr)
	if err != nil {
		return err
	}
	dir := workflowsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".cwl"), []byte(wf.String()+"\n"), 0o644)
}

func loadNamedWorkflow(name string) (captaincode.Workflow, bool) {
	body, err := os.ReadFile(filepath.Join(workflowsDir(), name+".cwl"))
	if err != nil {
		return captaincode.Workflow{}, false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		wf, err := captaincode.ParseWorkflow(line)
		if err != nil {
			return captaincode.Workflow{}, false
		}
		return wf, true
	}
	return captaincode.Workflow{}, false
}

func listNamedWorkflows() []string {
	files, _ := filepath.Glob(filepath.Join(workflowsDir(), "*.cwl"))
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".cwl"))
	}
	sort.Strings(names)
	return names
}

// handleSavedWorkflow serves "/wf save|run|list …". Reports whether handled.
func (b *brain) handleSavedWorkflow(w http.ResponseWriter, req oaiChatReq, prompt, raw string) bool {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) < 2 || (!strings.EqualFold(fields[0], "/wf") && !strings.EqualFold(fields[0], "/workflow")) {
		return false
	}
	sub := strings.ToLower(fields[1])
	reply := func(text string) {
		emit, _, finish := newCompletionWriter(w, req, "workflow")
		defer finish()
		emit(text)
	}
	switch sub {
	case "parse":
		// "/wf parse <expression>" shows how captain reads a program and
		// runs nothing; "/wf parse the logs, then…" is English intent.
		rest := strings.TrimSpace(strings.TrimSpace(raw)[len(fields[0]):])
		rest = strings.TrimSpace(rest[len(fields[1]):])
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "(") {
			return false
		}
		reply(parsePreview(rest))
		return true
	case "list":
		if len(fields) != 2 {
			return false // "/wf list the findings…" is English intent for the compiler
		}
		names := listNamedWorkflows()
		if len(names) == 0 {
			reply("no saved workflows yet - `/wf save <name> <expression>` creates one")
			return true
		}
		var sb strings.Builder
		sb.WriteString("saved workflows:\n")
		for _, n := range names {
			if wf, ok := loadNamedWorkflow(n); ok {
				fmt.Fprintf(&sb, "  %-20s %s · %d runs\n    %s\n", n, wf.Key(), wf.Runs(), wf.String())
			}
		}
		sb.WriteString("\n`/wf run <name>` executes one against the current conversation")
		reply(sb.String())
		return true
	case "save":
		if len(fields) >= 3 && !wfNameRe.MatchString(fields[2]) {
			reply("could not save: workflow name must be lowercase letters, digits and dashes (got " + fields[2] + ")")
			return true
		}
		if len(fields) < 4 || !strings.HasPrefix(fields[3], "/") {
			// No expression follows → not a save command; let the English
			// compiler have it ("/wf save the results to…").
			if len(fields) >= 4 {
				return false
			}
			reply("usage: /wf save <name> <workflow expression>")
			return true
		}
		name := fields[2]
		expr := strings.TrimSpace(strings.SplitN(raw, name, 2)[1])
		if err := saveNamedWorkflow(name, expr); err != nil {
			reply("could not save: " + err.Error())
			return true
		}
		wf, _ := loadNamedWorkflow(name)
		fmt.Printf("captain brain: workflow %q saved (%s)\n", name, wf.Key())
		reply(fmt.Sprintf("saved %q - %s · %d runs + review\n%s\n\n`/wf run %s` executes it", name, wf.Key(), wf.Runs(), wf.String(), name))
		return true
	case "run":
		if len(fields) != 3 || !wfNameRe.MatchString(strings.ToLower(fields[2])) {
			return false // "/wf run the analysis on…" is English intent
		}
		name := strings.ToLower(fields[2])
		wf, ok := loadNamedWorkflow(name)
		if !ok {
			names := listNamedWorkflows()
			reply(fmt.Sprintf("no saved workflow %q - saved: %s", name, strings.Join(names, ", ")))
			return true
		}
		fmt.Printf("captain brain: running saved workflow %q (%s)\n", name, wf.Key())
		b.runWorkflow(w, req, prompt, wf, "wf_"+name)
		return true
	}
	return false
}

// parsePreview says how captain reads an expression - a program's plan and
// turn bound, a workflow, a plain loop or one turn - without running it.
func parsePreview(expr string) string {
	expr = captaincode.HoistLeading(strings.TrimSpace(expr))
	p, ok, err := captaincode.ParseProgram(expr)
	switch {
	case err != nil:
		return "does not parse - nothing would run:\n\n" + err.Error()
	case ok:
		budget := repeatHardCap()
		n := p.MaxTurns(budget)
		bound := fmt.Sprintf("up to %d turns", n)
		if n >= budget {
			bound = fmt.Sprintf("until done, at most %d turns (the turn budget)", budget)
		}
		return fmt.Sprintf("program · %s\n\n```text\n%s\n```\n\ncanonical form: `%s`\n\nNothing ran. Send the expression without `/wf parse` to run it.",
			bound, p.Outline(), p.String())
	}
	if count, rest, isLoop := parseRepeat(expr); isLoop && strings.TrimSpace(rest) != "" {
		rounds := "until stopped"
		if count > 0 {
			rounds = fmt.Sprintf("%d rounds", count)
		}
		return fmt.Sprintf("a /repeat loop · %s of one turn:\n\n    %s\n\nNothing ran.", rounds, rest)
	}
	if wf, err := captaincode.ParseWorkflow(expr); err == nil && (wf.MultiStage() || wf.HasGate()) {
		return fmt.Sprintf("a workflow · %d stage(s) · %d worker run(s) + one director review:\n\n    %s\n\nNothing ran.", len(wf.Stages), wf.Runs(), wf.String())
	} else if err != nil && captaincode.LooksLikeWorkflow(expr) {
		return "does not parse - nothing would run:\n\n" + err.Error()
	}
	return "one turn, routed as typed (no program syntax). Nothing ran."
}
