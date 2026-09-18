package captaincode

import (
	"os"
	"strings"
)

// LoadCaptainEnv puts ~/.config/captain/env into this process's environment
// for every variable it does not already have. The launcher sources the file
// before starting the brain, so the brain and its serve see the provider
// keys; a bare `captain "task"` from a terminal did not, and once the
// OpenRouter key moved out of opencode.jsonc into that file (2026-09-13)
// every OpenRouter leg of the standalone CLI answered 401 "no cookie auth
// credentials found" (live 2026-09-15). Values already in the environment
// win, as with `set -a; . env`. Returns the names it set.
func LoadCaptainEnv() []string {
	raw, err := os.ReadFile(CaptainEnvPath())
	if err != nil {
		return nil
	}
	var set []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		v = strings.TrimSpace(v)
		if n := len(v); n >= 2 && (v[0] == '"' && v[n-1] == '"' || v[0] == '\'' && v[n-1] == '\'') {
			v = v[1 : n-1]
		}
		if _, present := os.LookupEnv(k); present {
			continue
		}
		if os.Setenv(k, v) == nil {
			set = append(set, k)
		}
	}
	return set
}
