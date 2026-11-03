package captaincode

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// ErrLoginNeeded: an agent CLI's own login is gone (`grok login`, `codex
// login`, `claude /login`, `cursor-agent login`). Only the user can fix it,
// on the machine the brain runs on, so the message says where and how.
var ErrLoginNeeded = errors.New("login needed")

// LoginError is a dead CLI login. It is also ErrProviderDown, so a task the
// brain routed itself still reroutes to another leg; a leg the user named
// does not (Workspace.Named), and a /repeat loop running one stops (2 Nov:
// the message read "provider temporarily unavailable (upstream outage) -
// retry shortly", and the loop kept going).
type LoginError struct {
	Leg    Leg
	Cmd    string // what the user runs, e.g. "grok login"
	Detail string // what the CLI said
}

func (e *LoginError) Error() string {
	s := fmt.Sprintf("%s is not logged in on %s: run `%s` in a terminal on that machine, then send the prompt again", e.Leg, machineName(), e.Cmd)
	if e.Detail != "" {
		s += " (it said: " + truncateStr(strings.Join(strings.Fields(e.Detail), " "), 160) + ")"
	}
	return s
}

// Is makes a LoginError both ErrLoginNeeded and ErrProviderDown.
func (e *LoginError) Is(target error) bool {
	return target == ErrLoginNeeded || target == ErrProviderDown
}

func loginNeeded(leg Leg, cmd, detail string) error {
	return &LoginError{Leg: leg, Cmd: cmd, Detail: detail}
}

// machineName is the name the user knows this machine by: the Mac's own
// name, not the one the network's DHCP server gave it (os.Hostname).
var machineName = sync.OnceValue(func() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "LocalHostName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this machine"
	}
	return strings.TrimSuffix(h, ".local")
})
