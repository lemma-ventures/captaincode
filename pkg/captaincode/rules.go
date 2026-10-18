package captaincode

// Rules: standing instructions the user keeps as Markdown files in a
// .captaincode/rules folder. Every worker prompt carries them, like the
// security and plain-writing lines, so one rule reaches every leg - Claude
// Code, Codex, Cursor, opencode and OpenShell sandboxes - without a copy in
// each harness's own instruction file. Two folders, read in this order:
//
//	~/.captaincode/rules/*.md             every folder
//	<workspace>/.captaincode/rules/*.md   that folder only
//
// A workspace file with the same name replaces the user's file. Rules go
// into the prompt in name order.
//
// A rule's text goes to a model provider, and a workspace folder holds what
// the repository's authors put there. So the reader takes regular files
// only, and stays inside the folder it reads: a rule that is a symlink, or a
// rules folder that links out of the repository, is skipped. Else a cloned
// repository could point a "rule" at ~/.ssh or a private note, and captain
// would send it.

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const (
	ruleFileMax = 8 << 10  // bytes kept from one rule
	rulesMax    = 32 << 10 // bytes kept from all rules together
)

// rulesRel is the rules folder, relative to the home or workspace folder.
const rulesRel = ".captaincode/rules"

// Rule is one rule file.
type Rule struct {
	Name   string // file name without ".md"
	Source string // "~/.captaincode/rules/<name>.md" or ".captaincode/rules/<name>.md"
	Text   string
}

// LoadRules returns the rules for a worker in dir ("" = the user's rules
// only), and how many it left out because all rules together passed
// rulesMax.
func LoadRules(dir string) (rules []Rule, dropped int) {
	byName := map[string]Rule{}
	if home, err := os.UserHomeDir(); err == nil {
		for _, r := range readRules(home, "~/") {
			byName[r.Name] = r
		}
	}
	if dir != "" {
		for _, r := range readRules(dir, "") {
			byName[r.Name] = r
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	total := 0
	for _, n := range names {
		r := byName[n]
		if total+len(r.Text) > rulesMax {
			dropped++
			continue
		}
		total += len(r.Text)
		rules = append(rules, r)
	}
	return rules, dropped
}

// readRules reads base/.captaincode/rules/*.md through an os.Root, so no
// path or symlink takes the read outside base.
func readRules(base, label string) []Rule {
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil
	}
	defer root.Close()
	d, err := root.Open(rulesRel)
	if err != nil {
		return nil
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil
	}
	var out []Rule
	for _, e := range entries {
		name := e.Name()
		// ReadDir reports a symlink as one, so this skips links and FIFOs
		// before anything is opened.
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") || !e.Type().IsRegular() {
			continue
		}
		rel := path.Join(rulesRel, name)
		f, err := root.Open(rel)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			f.Close()
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, ruleFileMax+1))
		f.Close()
		if err != nil {
			continue
		}
		text := strings.TrimSpace(CutHead(string(raw), ruleFileMax))
		if text == "" {
			continue
		}
		if len(raw) > ruleFileMax {
			text += fmt.Sprintf("\n[captain: rule cut at %d KiB]", ruleFileMax>>10)
		}
		out = append(out, Rule{Name: strings.TrimSuffix(name, ".md"), Source: label + rel, Text: text})
	}
	return out
}

// RulesContract is the worker-prompt block for rules ("" when there are
// none).
func RulesContract(rules []Rule, dropped int) string {
	if len(rules) == 0 && dropped == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[captain] Standing rules: the user keeps these in .captaincode/rules. Follow them in this turn unless the user's own turn says otherwise.")
	for _, r := range rules {
		fmt.Fprintf(&b, "\n\n--- rule %s (%s) ---\n%s", r.Name, r.Source, r.Text)
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n\n[captain: %d more rules left out - all rules together passed %d KiB]", dropped, rulesMax>>10)
	}
	return b.String()
}
