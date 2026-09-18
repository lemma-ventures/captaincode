package captaincode

// The launch check. Every start of Captain Code (brain boot, launcher,
// `captain doctor`, the sidebar) asks the same two questions of the memory
// layer: is the MAIN brain (~/.euclid) filled and current, and does the
// project have a LOCAL brain (<repo>/.euclid) that inherited the doctrine and
// was bootstrapped from its docs. Filesystem only: it must answer with the
// brain down and never block a launch.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BrainCheck is the state of one brain as the operator should hear it.
type BrainCheck struct {
	Kind      string    `json:"kind"`      // main | local
	Root      string    `json:"root"`      // register directory ("" when absent)
	Present   bool      `json:"present"`   // the directory exists
	Writable  bool      `json:"writable"`  // this is where runs are journaled
	Template  []string  `json:"template"`  // registers still holding scaffold placeholders
	Inherits  bool      `json:"inherits"`  // local only: SOUL/WISDOM carry the main brain's doctrine
	Journal   int       `json:"journal"`   // runs journaled since the last distillation (write brain only)
	Distilled time.Time `json:"distilled"` // last consolidation (zero: never)
	Stale     bool      `json:"stale"`     // a built catalog is older than the registers
	Attention string    `json:"attention"` // one short phrase for a panel, "" when all is well
	Fix       string    `json:"fix"`       // the command that repairs it, "" when nothing to do
}

// registersOf lists the registers a check reads for a brain of this kind.
func registersOf(kind string) []string {
	switch kind {
	case "developer":
		return []string{"BRAIN.md", "WISDOM.md", "memory/MEMORIES.md", "memory/FAILURES.md"}
	case "main": // MAP is a per-project register; the main brain has no code to map
		return []string{"SOUL.md", "VISION.md", "BRAIN.md", "WISDOM.md", "memory/MEMORIES.md", "memory/FAILURES.md"}
	}
	return []string{"SOUL.md", "VISION.md", "BRAIN.md", "WISDOM.md", "MAP.md", "memory/MEMORIES.md", "memory/FAILURES.md"}
}

// templateRegisters returns the registers of root that are still placeholders.
func templateRegisters(root, kind string) []string {
	var out []string
	for _, n := range registersOf(kind) {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil {
			continue
		}
		if isTemplateRegister(filepath.Base(n), string(b)) {
			out = append(out, n)
		}
	}
	return out
}

// catalogStale: a built catalog older than the newest register means the
// dashboard and search lag the brain.
func catalogStale(root string) bool {
	st, err := os.Stat(filepath.Join(root, "index", "catalog.jsonl"))
	if err != nil {
		return false
	}
	newest := time.Time{}
	for _, n := range registersOf("") {
		if fi, err := os.Stat(filepath.Join(root, n)); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest.After(st.ModTime())
}

// CheckBrains inspects the main brain and the local brain of cwd, in that order.
func CheckBrains(cwd string) []BrainCheck {
	handle := DeveloperHandle()
	set := ReadSet(cwd)
	writable := ""
	for _, b := range set {
		if b.Writable {
			writable = b.Root
		}
	}

	main := BrainCheck{Kind: "main", Root: MainBrainPath()}
	if isDir(main.Root) {
		main.Present = true
		main.Template = templateRegisters(main.Root, "main")
		main.Writable = writable == main.Root
		main.Distilled = LastDistilledAt(EuclidBrain{Root: main.Root})
		if main.Writable {
			entries, _ := ReadJournal(EuclidBrain{Root: main.Root, Kind: "main", Writable: true, Label: "main"}, main.Distilled)
			main.Journal = len(entries)
		}
		main.Stale = catalogStale(main.Root)
		switch {
		case len(main.Template) >= 3:
			main.Attention = "template, nothing recorded yet"
			main.Fix = "fill " + filepath.Join(main.Root, "SOUL.md") + " and VISION.md, or let `captain euclid distill --apply` grow it"
		case len(main.Template) > 0:
			main.Attention = strings.TrimSuffix(strings.Join(main.Template, ", "), ".md") + " template"
		case main.Stale:
			main.Attention = "catalog stale"
			main.Fix = "click the memory link in the sidebar, or EUCLID_ROOT=$HOME build-catalog.py"
		}
	} else {
		main.Attention = "missing"
		main.Fix = "captain euclid init"
	}

	local := BrainCheck{Kind: "local"}
	repo := RepoRoot(cwd)
	shared := ""
	if repo != "" {
		shared = filepath.Join(repo, ".euclid")
	}
	if shared != "" && isDir(shared) {
		local.Present = true
		local.Root = shared
		local.Template = templateRegisters(shared, "repo")
		dev := filepath.Join(shared, "developers", handle)
		local.Writable = isDir(dev) && writable == dev
		local.Inherits = inheritsDoctrine(shared)
		local.Stale = catalogStale(shared)
		if local.Writable {
			local.Distilled = LastDistilledAt(EuclidBrain{Root: dev})
			entries, _ := ReadJournal(EuclidBrain{Root: dev, Kind: "developer", Writable: true, Label: "me@" + filepath.Base(repo)}, local.Distilled)
			local.Journal = len(entries)
		}
		var attn []string
		needsBootstrap := false
		for _, n := range local.Template {
			if bootstrapFiles[n] {
				needsBootstrap = true
			}
		}
		if needsBootstrap {
			attn = append(attn, "not bootstrapped from the repo docs")
			local.Fix = "captain euclid bootstrap --apply"
		}
		if !isDir(dev) {
			attn = append(attn, "no developers/"+handle+" subtree, runs go to the main brain")
			if local.Fix == "" {
				local.Fix = "captain euclid init --repo"
			}
		}
		if !local.Inherits {
			attn = append(attn, "doctrine not inherited")
		}
		if local.Stale && len(attn) == 0 {
			attn = append(attn, "catalog stale")
		}
		local.Attention = strings.Join(attn, "; ")
	} else {
		local.Attention = "none"
		if repo != "" {
			local.Fix = "captain euclid init --repo"
		} else {
			local.Attention = "none (not a git repository)"
		}
	}
	return []BrainCheck{main, local}
}

// inheritsDoctrine: the repo brain's SOUL and WISDOM are neither placeholders
// nor absent, so a worker in this repo reads the same rules as everywhere else.
func inheritsDoctrine(root string) bool {
	for _, n := range []string{"SOUL.md", "WISDOM.md"} {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil || isTemplateRegister(n, string(b)) {
			return false
		}
	}
	return true
}

// RenderBrainChecks is the two-line report printed at launch.
func RenderBrainChecks(checks []BrainCheck) string {
	var sb strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&sb, "memory  %-5s %s\n", c.Kind, describeCheck(c))
	}
	return sb.String()
}

func describeCheck(c BrainCheck) string {
	if !c.Present {
		s := c.Attention
		if c.Fix != "" {
			s += " → " + c.Fix
		}
		return s
	}
	parts := []string{shortHome(c.Root)}
	if c.Writable {
		n := "no runs"
		if c.Journal == 1 {
			n = "1 run"
		} else if c.Journal > 1 {
			n = fmt.Sprintf("%d runs", c.Journal)
		}
		since := "never distilled"
		if !c.Distilled.IsZero() {
			since = "distilled " + humanAgo(time.Since(c.Distilled))
		}
		parts = append(parts, n+" to distill", since)
	} else {
		parts = append(parts, "read")
	}
	if c.Kind == "local" && c.Inherits {
		parts = append(parts, "doctrine inherited")
	}
	if c.Attention != "" {
		parts = append(parts, c.Attention)
	}
	if c.Stale && !strings.Contains(c.Attention, "catalog stale") {
		parts = append(parts, "catalog stale")
	}
	s := strings.Join(parts, " · ")
	if c.Fix != "" {
		s += " → " + c.Fix
	}
	return s
}

func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
