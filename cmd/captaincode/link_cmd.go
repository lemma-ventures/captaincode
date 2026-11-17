package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// cmdLink manages the connection to one hub: `captain link add` offers a
// folder, `captain link pair` pairs this machine with a hub and waits for the
// code the user typed on the web, `captain link off` and `captain link unpair`
// take each back, and `captain link status` shows the local state. Pairing
// needs the network and nothing else does; the brain still opens outbound
// connections only (roadmap row F6).
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

const linkUsage = "usage: captain link add <dir> [--queue] | off [<dir>] | pair [--ca <file>] [--wait <duration>] <hub-url> | unpair | status [--json]"

func runLink(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", linkUsage)
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
	case "pair":
		if e := linkTerminal(); e != nil {
			return e
		}
		hubURL, caFile, wait, err := linkPairArgs(args[1:])
		if err != nil {
			return err
		}
		return linkPair(hubURL, caFile, wait)
	case "unpair":
		if e := linkTerminal(); e != nil {
			return e
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: captain link unpair")
		}
		cleared, err := captaincode.LinkUnpair()
		if err != nil {
			return err
		}
		if cleared {
			fmt.Println("hub pin removed; the folders this machine offers are unchanged")
		} else {
			fmt.Println("no hub is paired with this machine")
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
		return fmt.Errorf("%s", linkUsage)
	}
}

// linkPairArgs reads the pairing flags in any order, then the one hub url.
// --ca adds a local certificate authority, which is how a test server or a
// hub on a private network is trusted without turning off verification, and
// --wait bounds how long the command polls for the approval.
func linkPairArgs(args []string) (hubURL, caFile string, wait time.Duration, err error) {
	wait = 4 * time.Minute
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value := arg, ""
		if cut := strings.Index(arg, "="); strings.HasPrefix(arg, "--") && cut > 2 {
			name, value = arg[:cut], arg[cut+1:]
		}
		switch name {
		case "--ca":
			if value == "" {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "-") {
					return "", "", 0, fmt.Errorf("usage: captain link pair --ca expects a file")
				}
				value = args[i]
			}
			caFile = value
		case "--wait":
			if value == "" {
				i++
				if i >= len(args) {
					return "", "", 0, fmt.Errorf("usage: captain link pair --wait takes a duration such as 30s or 4m")
				}
				value = args[i]
			}
			parsed, parseErr := time.ParseDuration(value)
			if parseErr != nil || parsed < 0 {
				return "", "", 0, fmt.Errorf("usage: captain link pair --wait takes a duration such as 30s or 4m")
			}
			wait = parsed
		case "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return "", "", 0, fmt.Errorf("usage: captain link pair [--ca <file>] [--wait <duration>] <hub-url> (unknown flag %s)", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return "", "", 0, fmt.Errorf("usage: captain link pair [--ca <file>] [--wait <duration>] <hub-url>")
	}
	return positional[0], caFile, wait, nil
}

// linkPair pairs this machine, prints the code to approve on the web, and
// waits. A deadline or a Ctrl-C stops the waiting, not the pairing: the code
// stays valid until its own window closes.
func linkPair(hubURL, caFile string, wait time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hub, reused, err := captaincode.LinkPair(ctx, hubURL, caFile)
	if err != nil {
		return err
	}
	if reused {
		fmt.Printf("keeping the live pairing for %s\n", hub.URL)
	}
	fmt.Printf("hub: %s\n", hub.URL)
	fmt.Printf("pairing code: %s\n", hub.Code)
	if page, err := captaincode.LinkApprovalURL(hub.URL); err == nil {
		fmt.Printf("approve it at: %s\n", page)
	}
	fmt.Printf("window closes: %s\n", time.Unix(hub.ExpiresAt, 0).UTC().Format(time.RFC3339))
	if wait > 0 {
		fmt.Println("waiting for approval; press Ctrl-C to stop")
	}
	status, err := captaincode.LinkPairWait(ctx, caFile, wait, nil)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("stopped; the pairing is still live until its window closes")
			return nil
		}
		return err
	}
	switch status {
	case captaincode.LinkPairApproved:
		fmt.Println("paired")
	case captaincode.LinkPairExpired:
		return fmt.Errorf("the pairing window closed; run captain link pair again")
	default:
		fmt.Println("still pending; run captain link pair again to keep waiting, or captain link status to look it up")
	}
	return nil
}

type linkStatusFolder struct {
	Label   string `json:"label"`
	Mode    string `json:"mode"`
	AddedAt string `json:"added_at"`
	Root    string `json:"root"`
}

type linkStatusHub struct {
	URL          string `json:"url"`
	Key          string `json:"key"`
	State        string `json:"state"`
	Code         string `json:"code,omitempty"`
	WindowCloses string `json:"window_closes,omitempty"`
	ApprovedAt   string `json:"approved_at,omitempty"`
	ApproveAt    string `json:"approve_at,omitempty"`
}

type linkStatusRecord struct {
	Key       string             `json:"key"`
	MachineID string             `json:"machine_id"`
	Hub       *linkStatusHub     `json:"hub,omitempty"`
	Linked    int                `json:"linked"`
	Folders   []linkStatusFolder `json:"folders"`
}

// linkHubView is the local hub record as a status line: the pinned key, the
// pairing state and, while the code is live, where to type it. The state is
// decided here from the stored times; status never calls the hub.
func linkHubView() (*linkStatusHub, error) {
	hub, has, err := captaincode.LinkHubState()
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	view := &linkStatusHub{URL: hub.URL, Code: hub.Code}
	if fingerprint, err := captaincode.LinkHubFingerprint(hub.PublicKey); err == nil {
		view.Key = fingerprint
	} else {
		view.Key = "unreadable"
	}
	switch {
	case hub.ApprovedAt > 0:
		view.State = captaincode.LinkPairApproved
		view.ApprovedAt = time.Unix(hub.ApprovedAt, 0).UTC().Format(time.RFC3339)
		view.Code = ""
	case time.Now().Unix() > hub.ExpiresAt:
		view.State = captaincode.LinkPairExpired
		view.Code = ""
	default:
		view.State = captaincode.LinkPairPending
		view.WindowCloses = time.Unix(hub.ExpiresAt, 0).UTC().Format(time.RFC3339)
		if page, err := captaincode.LinkApprovalURL(hub.URL); err == nil {
			view.ApproveAt = page
		}
	}
	return view, nil
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
	hub, err := linkHubView()
	if err != nil {
		return err
	}
	if asJSON {
		record := linkStatusRecord{Key: identity, MachineID: machineID, Hub: hub, Linked: len(list), Folders: list}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(record)
	}
	fmt.Printf("link key: %s\n", identity)
	fmt.Printf("machine id: %s\n", machineID)
	if hub != nil {
		fmt.Printf("hub: %s (%s)\n", hub.URL, hub.State)
		fmt.Printf("hub key: %s\n", hub.Key)
		switch hub.State {
		case captaincode.LinkPairPending:
			fmt.Printf("pairing code: %s, valid until %s\n", hub.Code, hub.WindowCloses)
			fmt.Printf("approve it at: %s\n", hub.ApproveAt)
		case captaincode.LinkPairExpired:
			fmt.Printf("the pairing window closed; run captain link pair %s again\n", hub.URL)
		default:
			fmt.Printf("paired since %s\n", hub.ApprovedAt)
		}
	}
	fmt.Printf("linked folders: %d\n", len(list))
	for _, folder := range list {
		fmt.Printf("  %-24s %-6s %s\n", folder.Label, folder.Mode, folder.Root)
	}
	return nil
}
