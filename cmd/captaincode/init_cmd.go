package main

// captain init - generate the configs that let prompted agents work headless:
// opencode worker permissions (web search/fetch, edits, bash, out-of-repo
// paths; .env reads denied), the /init TUI command, and the captain env
// scaffold. Idempotent; --check reports without writing.

import (
	"fmt"
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdInit(args []string) {
	apply := true
	for _, a := range args {
		switch a {
		case "--check", "check":
			apply = false
		case "-h", "--help":
			fmt.Println("usage: captain init [--check]\n\nGenerates/normalizes ~/.config/opencode/opencode.jsonc (worker permissions,\ncaptain provider, slash commands) and scaffolds ~/.config/captain/env.\n--check reports what would change without writing.")
			return
		default:
			fatal(fmt.Errorf("captain init: unknown flag %q (try --check)", a))
		}
	}
	report, changed, err := captaincode.RunInit(captaincode.InitOptions{Apply: apply})
	if err != nil {
		fatal(err)
	}
	fmt.Println(report)
	// --check exits 3 when the config has drifted from the registry (a leg
	// added by a release but absent from the TUI's commands and models -
	// grok-max, 2026-09-15), so a launcher can apply it without parsing prose.
	if !apply && changed {
		os.Exit(3)
	}

	// The router and the panels are plugins stock opencode loads; without these
	// entries the TUI runs unrouted, which looks like captain is "not working".
	if apply {
		if changed, err := captaincode.EnsureCaptainPlugins(captaincode.OpencodeConfigPath(), captainSourceDir()); err != nil {
			fmt.Printf("captain init: plugins not registered: %v\n", err)
		} else if changed {
			fmt.Println("registered the captain router + UI plugins (restart the TUI to load them)")
		}
		// The worker's live narration rides the reasoning channel; the TUI
		// hides reasoning by default, which makes every run look silent.
		if changed, err := captaincode.EnsureThinkingShown(); err != nil {
			fmt.Printf("captain init: thinking preference not seeded: %v\n", err)
		} else if changed {
			fmt.Println("set the TUI's worker feed to collapsed (one clickable line per run; toggle with /thinking)")
		}
		// A Claude Code user setting that starves claude workers of every
		// shell command with an expansion (see EnsureClaudeWorkerPermissions).
		if changed, note, err := captaincode.EnsureClaudeWorkerPermissions(); err != nil {
			fmt.Printf("captain init: claude settings: %v\n", err)
		} else if changed {
			fmt.Println(note)
		}
	}
}
