package captaincode

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// LoadCaptainEnv puts ~/.config/captain/env into this process's environment
// for every variable it does not already have. A bare `captain "task"` from
// a terminal did not see the file, and once the OpenRouter key moved out of
// opencode.jsonc into that file (2026-09-13) every OpenRouter leg of the
// standalone CLI answered 401 "no cookie auth credentials found" (live
// 2026-09-15). Values already in the environment win, as with `set -a; . env`.
// A second file, ~/.config/captain/secrets.env (0600, machine-fleet plan step
// 4), is loaded the same way and a value in it wins over the same name in env.
// Returns the names it set.
func LoadCaptainEnv() []string {
	seen := map[string]bool{}
	var set []string
	set = append(set, assignEnvFile(CaptainEnvPath(), seen, nil)...)
	set = append(set, assignSecretsFile(seen, nil)...)
	return set
}

// LoadBlackboxConfig puts only the CAPTAIN_BLACKBOX_* names of both captain
// files into this process's environment. `captain blackbox` runs before
// LoadCaptainEnv, so a bare terminal resolved an unconfigured authority: status
// printed "not configured" and the pilot review screened the legacy file
// snapshot instead of the operator's signed store. The trust pins are
// configuration, not credentials, so a short-lived screening command reads them
// without putting every provider key into its environment. Values already in
// the environment still win. Returns the names it set.
func LoadBlackboxConfig() []string {
	seen := map[string]bool{}
	keep := func(k string) bool { return strings.HasPrefix(k, "CAPTAIN_BLACKBOX_") }
	var set []string
	set = append(set, assignEnvFile(CaptainEnvPath(), seen, keep)...)
	set = append(set, assignSecretsFile(seen, keep)...)
	return set
}

// CaptainSecretsPath is the separate secrets file `captain secrets split`
// moves credential lines into. It must be 0600; a looser file is refused.
func CaptainSecretsPath() string {
	return filepath.Join(configHome(), "captain", "secrets.env")
}

func assignEnvFile(path string, seen map[string]bool, keep func(string) bool) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return assignEnv(raw, seen, keep)
}

func assignSecretsFile(seen map[string]bool, keep func(string) bool) []string {
	raw, _, err := readPrivateEnvFile(CaptainSecretsPath())
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "captain: cannot safely read secrets.env; refusing to load it")
		}
		return nil
	}
	return assignEnv(raw, seen, keep)
}

func loadEnvFile(path string, seen map[string]bool) []string {
	return assignEnvFile(path, seen, nil)
}

func loadSecretsFile(seen map[string]bool) []string {
	return assignSecretsFile(seen, nil)
}

func readPrivateEnvFile(path string) ([]byte, os.FileMode, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("credential file must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, 0, fmt.Errorf("credential file must have one link and belong to the current user")
	}
	if mode := info.Mode().Perm(); mode != 0o600 && mode != 0o400 {
		return nil, 0, fmt.Errorf("credential file must have mode 0600 or 0400")
	}
	const limit = 1 << 20
	if info.Size() > limit {
		return nil, 0, fmt.Errorf("credential file exceeds 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return nil, 0, fmt.Errorf("cannot read credential file within 1 MiB")
	}
	return raw, info.Mode().Perm(), nil
}

func EnvFileNames(path string) ([]string, os.FileMode, error) {
	raw, mode, err := readPrivateEnvFile(path)
	if err != nil {
		return nil, 0, err
	}
	unique := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		name, _, ok := strings.Cut(line, "=")
		if !ok || name == "" {
			continue
		}
		valid := true
		for i, c := range name {
			if c != '_' && !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') && !(i > 0 && c >= '0' && c <= '9') {
				valid = false
				break
			}
		}
		if valid {
			unique[name] = true
		}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, mode, nil
}

func assignEnv(raw []byte, seen map[string]bool, keep func(string) bool) []string {
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
		if keep != nil && !keep(k) {
			continue
		}
		v = strings.TrimSpace(v)
		if n := len(v); n >= 2 && (v[0] == '"' && v[n-1] == '"' || v[0] == '\'' && v[n-1] == '\'') {
			v = v[1 : n-1]
		}
		if _, present := os.LookupEnv(k); present && !seen[k] {
			continue
		}
		if os.Setenv(k, v) == nil {
			set = append(set, k)
			seen[k] = true
		}
	}
	return set
}

// IsSecretEnvName reports whether an environment variable name looks like a
// credential: it carries KEY, TOKEN, SECRET, PASSWD, PASSWORD or CREDENTIAL.
func IsSecretEnvName(k string) bool {
	u := strings.ToUpper(k)
	for _, marker := range []string{"KEY", "TOKEN", "SECRET", "PASSWD", "PASSWORD", "CREDENTIAL"} {
		if strings.Contains(u, marker) {
			return true
		}
	}
	return false
}
