package main

// `captain send [--cwd <dir>] [--leg <leg>] [--from <who>] "<prompt>"` - hand a
// prompt to the TUI open in a folder (brain_inbox.go). The sidebar submits it
// into the session as if typed; --leg forces a leg or pseudo-model the way a
// /<leg> prefix would.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func cmdSend(args []string) {
	cwd := ""
	leg := ""
	from := ""
	var words []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--cwd" && i+1 < len(args):
			cwd = args[i+1]
			i++
		case a == "--leg" && i+1 < len(args):
			leg = args[i+1]
			i++
		case a == "--from" && i+1 < len(args):
			from = args[i+1]
			i++
		case a == "-h" || a == "--help":
			fmt.Println("usage: captain send [--cwd <dir>] [--leg <leg>] [--from <who>] \"<prompt>\"\n\nQueues a prompt for the TUI open in <dir> (default: this folder); its sidebar submits it into the session as if you had typed it. --leg forces a leg (grok, claude, team, frontier…).")
			return
		default:
			words = append(words, a)
		}
	}
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		fatal(fmt.Errorf("nothing to send - usage: captain send [--cwd <dir>] [--leg <leg>] \"<prompt>\""))
	}
	if cwd == "" {
		cwd = euclidCwd()
	}
	cwd, _ = filepath.Abs(cwd)
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	if leg != "" {
		text = "/" + strings.TrimPrefix(leg, "/") + " " + text // the head the plugin forces on
	}
	body, _ := json.Marshal(map[string]string{"text": text, "leg": leg, "from": from})
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(brainURL()+"/v1/inbox?cwd="+url.QueryEscape(cwd), "application/json", bytes.NewReader(body))
	if err != nil {
		fatal(fmt.Errorf("the brain is not running (%v)", err))
	}
	defer resp.Body.Close()
	var out struct {
		OK      bool                     `json:"ok"`
		Pending int                      `json:"pending"`
		Error   struct{ Message string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "captain send: HTTP %d %s\n", resp.StatusCode, out.Error.Message)
		os.Exit(1)
	}
	fmt.Printf("queued for the TUI in %s (%d pending) - it is submitted the next time that TUI's sidebar polls\n", cwd, out.Pending)
}
