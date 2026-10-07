package main

// `captain send [--reply <token> | --cwd <dir>] [--leg <leg>] [--from <who>] "<prompt>"`
// - hand a prompt to a TUI (brain_inbox.go). The sidebar submits it into the
// session as if typed; --leg forces a leg or pseudo-model the way a /<leg>
// prefix would. --reply (or CAPTAIN_REPLY) is a worker's reply token: the
// prompt goes to the session that worker's turn came from (brain_reply.go).
// --cwd sends to the TUI open in another folder, captain to captain; the
// prompt then says which folder it came from.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	reply := strings.TrimSpace(os.Getenv("CAPTAIN_REPLY"))
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
		case a == "--reply" && i+1 < len(args):
			reply = args[i+1]
			i++
		case a == "-h" || a == "--help":
			fmt.Println("usage: captain send [--reply <token> | --cwd <dir>] [--leg <leg>] [--from <who>] \"<prompt>\"\n\nQueues a prompt for a TUI; its sidebar submits it into the session as if you had typed it.\n--reply  a worker's reply token: the session that worker's turn came from (also CAPTAIN_REPLY)\n--cwd    the TUI open in that folder (default: this folder); from another folder, the prompt says where it came from\n--leg    force a leg (grok, claude, team, frontier…)")
			return
		default:
			words = append(words, a)
		}
	}
	text := strings.TrimSpace(strings.Join(words, " "))
	if text == "" {
		fatal(fmt.Errorf("nothing to send - usage: captain send [--cwd <dir>] [--leg <leg>] \"<prompt>\""))
	}
	if cwd != "" {
		reply = "" // a folder named on purpose wins over an inherited token
	}
	here, _ := filepath.Abs(euclidCwd())
	if real, err := filepath.EvalSymlinks(here); err == nil {
		here = real
	}
	if cwd == "" {
		cwd = here
	}
	cwd, _ = filepath.Abs(cwd)
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	if leg != "" {
		text = "/" + strings.TrimPrefix(leg, "/") + " " + text // the head the plugin forces on
	}
	body, _ := json.Marshal(map[string]string{"text": text, "leg": leg, "from": from, "reply": reply, "sender_dir": here})
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(brainURL()+"/v1/inbox?cwd="+url.QueryEscape(cwd), "application/json", bytes.NewReader(body))
	if err != nil {
		fatal(fmt.Errorf("the brain is not running (%v)", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == 202 {
		// Held by the injection screen (brain_inbox.go): not delivered; the
		// user reviews it. Exit 2 so a script can tell it from success.
		var held struct {
			ID       string `json:"id"`
			Findings []struct {
				Kind string `json:"kind"`
			} `json:"findings"`
		}
		_ = json.Unmarshal(raw, &held)
		kinds := make([]string, 0, len(held.Findings))
		for _, f := range held.Findings {
			kinds = append(kinds, f.Kind)
		}
		fmt.Fprintf(os.Stderr, "captain send: held %s for the user - it reads as possible prompt injection (%s). They review it with `captain inbox`.\n", held.ID, strings.Join(kinds, ", "))
		os.Exit(2)
	}
	if resp.StatusCode != 200 {
		// brainErrorText reads both error shapes: the inbox answers with
		// writeErr's flat one, which left this line without its reason.
		fmt.Fprintf(os.Stderr, "captain send: HTTP %d %s\n", resp.StatusCode, brainErrorText(resp.StatusCode, raw))
		os.Exit(1)
	}
	var out struct {
		OK      bool `json:"ok"`
		Pending int  `json:"pending"`
	}
	_ = json.Unmarshal(raw, &out)
	if reply != "" {
		fmt.Printf("queued as a reply (%s, %d pending) - it is submitted into the session that turn came from\n", reply, out.Pending)
		return
	}
	fmt.Printf("queued for the TUI in %s (%d pending) - it is submitted the next time that TUI's sidebar polls\n", cwd, out.Pending)
}
