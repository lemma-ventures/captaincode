package captaincode

// Bootstrapping a new repo brain from the repository's own documentation.
//
// Distillation deliberately never writes VISION, MAP or BRAIN's framing: they
// are strategy, and strategy is a human's to state. But a freshly scaffolded
// repo brain knowing nothing about its repo helps nobody, and the repo already
// says what it is: README, docs/, CLAUDE.md, AGENTS.md. So a new brain is
// bootstrapped from those ONCE - the strategy registers may be written by a
// model only while they are still the untouched template. A register that a
// human has since curated is never overwritten.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bootstrapFiles are the strategy registers bootstrap may fill.
var bootstrapFiles = map[string]bool{"VISION.md": true, "MAP.md": true, "BRAIN.md": true}

// bootstrapDocBudget caps what is read: enough for a README and a docs folder,
// not enough to ship a book to the model.
const bootstrapDocBudget = 60_000

// bootstrapDocs lists the repository's self-description: README*, CLAUDE.md,
// AGENTS.md, CONTRIBUTING.md at the root, and *.md directly under docs/.
// Dotfiles and dot-directories are never read (that is where secrets live).
func bootstrapDocs(repo string) []string {
	var out []string
	roots, _ := filepath.Glob(filepath.Join(repo, "*.md"))
	for _, p := range roots {
		base := strings.ToLower(filepath.Base(p))
		if strings.HasPrefix(base, "readme") || base == "claude.md" || base == "agents.md" || base == "contributing.md" || base == "architecture.md" {
			out = append(out, p)
		}
	}
	docs, _ := filepath.Glob(filepath.Join(repo, "docs", "*.md"))
	out = append(out, docs...)
	sort.Strings(out)
	return out
}

// BootstrapPrompt builds the one-time prompt and returns the docs it used.
func BootstrapPrompt(brain EuclidBrain, repo string) (string, []string) {
	files := bootstrapDocs(repo)
	var sb strings.Builder
	sb.WriteString(distillMarker + " bootstrap\n")
	sb.WriteString("You are filling the strategy registers of a fresh Euclid brain for the repository " + filepath.Base(repo) + " from the repository's OWN documentation below. ")
	sb.WriteString("Only what the documents state; no speculation; no secrets. Prefer few, dense lines.\n")
	sb.WriteString("VISION.md: what the project is, who it serves, what \"done\" looks like. MAP.md: table rows `| area | path | what lives there |` for the main folders named in the docs. BRAIN.md: replace the `## Current state` section with the active front the docs describe.\n")
	sb.WriteString("Respond with JSON only: {\"summary\": \"one paragraph\", \"edits\": [{\"file\": \"VISION.md|MAP.md|BRAIN.md\", \"mode\": \"append|replace_section\", \"anchor\": \"## heading (replace_section only)\", \"text\": \"markdown\", \"why\": \"one line\"}]}\n\n")
	used := 0
	var read []string
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(repo, p)
		text := string(b)
		if used+len(text) > bootstrapDocBudget {
			text = text[:max(0, bootstrapDocBudget-used)]
		}
		if text == "" {
			break
		}
		fmt.Fprintf(&sb, "### %s\n%s\n\n", rel, text)
		used += len(text)
		read = append(read, p)
	}
	return sb.String(), read
}

// ApplyBootstrap writes the strategy registers, and only those that are still
// the template. SOUL and WISDOM are doctrine and off limits even here.
func ApplyBootstrap(brain EuclidBrain, edits []RegisterEdit) ([]string, error) {
	var kept []RegisterEdit
	for _, e := range edits {
		if !bootstrapFiles[e.File] {
			continue
		}
		cur, _ := os.ReadFile(filepath.Join(brain.Root, e.File))
		if !isTemplateRegister(e.File, string(cur)) {
			continue // curated since: not ours to overwrite
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		return nil, nil
	}
	b := brain
	b.Writable = true // the write-set rule is about sessions; bootstrap targets this brain by name
	paths, err := applyEditsTo(b, kept, bootstrapFiles)
	var touched []string
	for _, p := range paths {
		touched = append(touched, filepath.Base(p))
	}
	return touched, err
}

// isTemplateRegister reports whether a register still reads as its template.
// Templates come from more than one place (the built-ins, the Euclid repo's
// template dir), so this looks at content rather than bytes: a register is
// filled once any bullet carries text past its label, or any table row exists
// past the header. "- What the project is:" and "(add the first…)" are
// placeholders; "- What the project is: a billing engine" is content.
func isTemplateRegister(name, content string) bool {
	for _, raw := range strings.Split(content, "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(l, "- "):
			body := strings.TrimSpace(l[2:])
			if body == "" || strings.HasSuffix(body, ":") || strings.HasPrefix(body, "(") || strings.HasPrefix(body, "[ ] (") {
				continue
			}
			return false
		case strings.HasPrefix(l, "|"):
			if strings.HasPrefix(l, "|--") || strings.HasPrefix(l, "|-") || strings.Contains(strings.ToLower(l), "| area |") || strings.Contains(strings.ToLower(l), "| date |") {
				continue
			}
			// The Euclid template's MAP ships rows that all point inside .euclid/
			// itself. A MAP is filled once a row points at the repo's own code.
			if name == "MAP.md" && strings.Contains(l, ".euclid") {
				continue
			}
			return false
		case strings.HasPrefix(l, "## ") && !strings.Contains(l, "(") && !strings.EqualFold(l, "## Current state") && !strings.EqualFold(l, "## Active guardrails") && !strings.EqualFold(l, "## Identity") && !strings.EqualFold(l, "## Doctrine"):
			// a dated or named section a human/distiller added
			return false
		}
	}
	return true
}
