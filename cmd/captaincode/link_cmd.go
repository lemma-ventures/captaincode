package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// cmdLink manages the folders this machine offers to a hub: `captain link add`
// opts one in, `captain link off` drops it, `captain link status` shows the
// machine identity and the list. Only the local half exists so far; pairing and
// the message stream follow it (roadmap row F6). Nothing here reaches the
// network.
func cmdLink(args []string) {
	if e := runLink(args); e != nil {
		fatal(e)
	}
}

func linkTerminal() error {
	if os.Getenv(captaincode.SentTurnEnv) == "1" || os.Getenv("CAPTAIN_JAILED") == "1" || os.Getenv(captaincode.GuardEnv) == "1" {
		return fmt.Errorf("only a terminal changes which folders are linked")
	}
	return nil
}

func runLink(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: captain link add <dir> [--queue] | off [<dir>] | status [--json]")
	}
	switch args[0] {
	case "add":
		if e := linkTerminal(); e != nil {
			return e
		}
		var dirs []string
		queue := false
		for _, arg := range args[1:] {
			if arg == "--queue" {
				queue = true
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return fmt.Errorf("usage: captain link add <dir> [--queue] (unknown flag %s)", arg)
			}
			dirs = append(dirs, arg)
		}
		if len(dirs) != 1 {
			return fmt.Errorf("usage: captain link add <dir> [--queue]")
		}
		mode := captaincode.LinkModeActive
		if queue {
			mode = captaincode.LinkModeQueue
		}
		folder, err := captaincode.LinkAdd(dirs[0], mode)
		if err != nil {
			return err
		}
		fmt.Printf("linked %s (%s)\n", filepath.Base(folder.Root), folder.Mode)
		return nil
	case "off":
		if e := linkTerminal(); e != nil {
			return e
		}
		if len(args) > 2 {
			return fmt.Errorf("usage: captain link off [<dir>]")
		}
		dir := ""
		if len(args) == 2 {
			dir = args[1]
		}
		stopped, err := captaincode.LinkOff(dir)
		if err != nil {
			return err
		}
		switch stopped {
		case 0:
			fmt.Println("nothing was linked")
		case 1:
			fmt.Println("1 folder stopped listening")
		default:
			fmt.Printf("%d folders stopped listening\n", stopped)
		}
		return nil
	case "status", "list":
		fs := flag.NewFlagSet("link status", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print a machine-readable record")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: captain link status [--json]")
		}
		return linkStatus(*asJSON)
	default:
		return fmt.Errorf("usage: captain link add <dir> [--queue] | off [<dir>] | status [--json]")
	}
}

type linkStatusFolder struct {
	Label   string `json:"label"`
	Mode    string `json:"mode"`
	AddedAt string `json:"added_at"`
	Root    string `json:"root"`
}

type linkStatusRecord struct {
	Key       string             `json:"key"`
	MachineID string             `json:"machine_id"`
	Linked    int                `json:"linked"`
	Folders   []linkStatusFolder `json:"folders"`
}

// linkStatus is a local report. The root is displayed as itself: a path may
// leave this machine only as a label, and only in a signed message, which
// nothing sends yet.
func linkStatus(asJSON bool) error {
	identity, machineID := "not created", "unconfigured"
	priv, err := captaincode.LoadLinkKey()
	switch {
	case err == nil:
		identity = captaincode.LinkFingerprint(priv.Public().(ed25519.PublicKey))
	case os.IsNotExist(err):
	default:
		return err
	}
	if id, err := captaincode.LinkMachineID(); err == nil {
		machineID = id
	}
	folders, err := captaincode.LinkFolders()
	if err != nil {
		return err
	}
	list := make([]linkStatusFolder, 0, len(folders))
	for _, folder := range folders {
		list = append(list, linkStatusFolder{
			Label:   filepath.Base(folder.Root),
			Mode:    folder.Mode,
			AddedAt: time.Unix(folder.AddedAt, 0).UTC().Format(time.RFC3339),
			Root:    folder.Root,
		})
	}
	if asJSON {
		record := linkStatusRecord{Key: identity, MachineID: machineID, Linked: len(list), Folders: list}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(record)
	}
	fmt.Printf("link key: %s\n", identity)
	fmt.Printf("machine id: %s\n", machineID)
	fmt.Printf("linked folders: %d\n", len(list))
	for _, folder := range list {
		fmt.Printf("  %-24s %-6s %s\n", folder.Label, folder.Mode, folder.Root)
	}
	return nil
}
