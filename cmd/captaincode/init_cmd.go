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
		changed, missing, err := captaincode.EnsureCaptainPlugins(captaincode.OpencodeConfigPath(), captainSourceDir())
		switch {
		case err != nil:
			fmt.Printf("captain init: plugins not registered: %v\n", err)
		case len(missing) > 0:
			// `go install` brings the brain and no checkout, so there is no
			// plugin/ to point opencode at. Silence here looked like a broken
			// captain: the TUI came up unrouted, with no Models panel, and
			// nothing said why.
			fmt.Printf("\n! the opencode plugins were NOT registered - the terminal will run unrouted, with no panels.\n")
			for _, m := range missing {
				fmt.Printf("    not found: %s\n", m)
			}
			fmt.Printf("  These ship in the repository, not in the binary. Clone it and point captain at it:\n")
			fmt.Printf("    git clone https://github.com/lemma-ventures/captaincode\n")
			fmt.Printf("    export CAPTAIN_SRC=/path/to/captaincode   # currently %s\n", captainSourceDir())
			fmt.Printf("    (cd \"$CAPTAIN_SRC/plugin/captain-ui\" && bun install) && captain init\n")
			fmt.Printf("  The brain itself is fine without them: `captain \"…\"` and `captain brain` route as usual.\n\n")
		case changed:
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
