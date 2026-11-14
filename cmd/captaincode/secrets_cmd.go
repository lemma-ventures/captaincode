package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// cmdSecrets implements machine-fleet plan step 4: credential names are listed
// without values, and secret-named lines move from env into a separate 0600
// file the env loader also reads. Split is terminal-only: a worker, a sent
// turn or the jail never reorganizes the machine's credentials.
func cmdSecrets(args []string) {
	if e := runSecrets(args); e != nil {
		fatal(e)
	}
}

func secretsTerminal() error {
	if os.Getenv(captaincode.SentTurnEnv) == "1" || os.Getenv("CAPTAIN_JAILED") == "1" || os.Getenv(captaincode.GuardEnv) == "1" {
		return fmt.Errorf("captain secrets split runs from a terminal only")
	}
	return nil
}

func runSecrets(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: captain secrets list|template|split [--dry-run]")
	}
	switch args[0] {
	case "template":
		if len(args) != 1 {
			return fmt.Errorf("usage: captain secrets template")
		}
		return secretsTemplate(os.Stdout)
	case "list":
		return secretsList()
	case "split":
		fs := flag.NewFlagSet("secrets split", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "show what would move without writing")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if e := secretsTerminal(); e != nil {
			return e
		}
		return secretsSplit(*dry)
	default:
		return fmt.Errorf("usage: captain secrets list|template|split [--dry-run]")
	}
}

func secretsList() error {
	for _, path := range []string{captaincode.CaptainEnvPath(), captaincode.CaptainSecretsPath()} {
		names, mode, err := envNames(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("%s: (absent)\n", path)
				continue
			}
			return err
		}
		fmt.Printf("%s (mode %v):\n", path, mode)
		if len(names) == 0 {
			fmt.Println("  (empty)")
		}
		for _, n := range names {
			marker := ""
			if captaincode.IsSecretEnvName(n) {
				marker = "  [secret]"
			}
			fmt.Printf("  %s%s\n", n, marker)
		}
	}
	return nil
}

func envNames(path string) ([]string, os.FileMode, error) {
	return captaincode.EnvFileNames(path)
}

func secretsSplit(dry bool) error {
	envPath := captaincode.CaptainEnvPath()
	secretsPath := captaincode.CaptainSecretsPath()
	if info, err := os.Lstat(secretsPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing", secretsPath)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	var keep, moved []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, _, ok := strings.Cut(trimmed, "=")
		if ok && k != "" && !strings.ContainsAny(k, " \t") && captaincode.IsSecretEnvName(k) {
			moved = append(moved, line)
			continue
		}
		keep = append(keep, line)
	}
	if len(moved) == 0 {
		fmt.Println("nothing to move: no secret-named lines in", envPath)
		return nil
	}
	if dry {
		fmt.Printf("would move %d line(s) to %s:\n", len(moved), secretsPath)
		for _, line := range moved {
			k, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
			fmt.Printf("  %s\n", k)
		}
		return nil
	}
	if len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
		keep = keep[:len(keep)-1]
	}
	existing, _ := os.ReadFile(secretsPath)
	block := "\n# moved by `captain secrets split`\n" + strings.Join(moved, "\n") + "\n"
	newSecrets := strings.TrimRight(string(existing), "\n") + block
	if e := os.MkdirAll(filepath.Dir(secretsPath), 0o700); e != nil {
		return e
	}
	if e := os.WriteFile(secretsPath, []byte(newSecrets), 0o600); e != nil {
		return e
	}
	if e := os.Chmod(secretsPath, 0o600); e != nil {
		return e
	}
	if e := os.WriteFile(envPath, []byte(strings.Join(keep, "\n")+"\n"), 0o600); e != nil {
		return e
	}
	fmt.Printf("moved %d line(s) to %s (0600). Restart the brain to reload.\n", len(moved), secretsPath)
	for _, line := range moved {
		k, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export ")), "=")
		fmt.Printf("  %s\n", k)
	}
	return nil
}
