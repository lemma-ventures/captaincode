package main

import (
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// rulesContract carries the user's standing rules (pkg rules.go) on every
// worker prompt, OpenShell sandboxes included: the text is inline, so a
// sandbox that cannot read the host's folders still gets it. Added for a
// rule that keeps agents from crediting themselves in commits (2026-10-12);
// the worker guard refuses such a commit too. CAPTAIN_WORKER_RULES=0 drops
// the block.
func rulesContract(ws captaincode.Workspace) string {
	if os.Getenv("CAPTAIN_WORKER_RULES") == "0" {
		return ""
	}
	return captaincode.RulesContract(captaincode.LoadRules(ws.Dir))
}
