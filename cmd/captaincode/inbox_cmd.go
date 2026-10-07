package main

// `captain inbox` - the sent prompts the injection screen held (brain_inbox.go):
// list them, release one into its folder's session, or drop it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdInbox(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("usage: captain inbox [release <id> | drop <id> | open [folder|--all] | --log]\n\nLists the prompts sent with `captain send` that were held as possible prompt injection, with what the screen found, and the folders whose inbox is closed. `release` delivers one to its folder's session; `drop` deletes it. A sent turn that tries something the sent-turn policy refuses closes its folder's inbox: later sent prompts are held until `open` (default: this folder).")
		return
	}
	if len(args) > 0 && args[0] == "--log" {
		for _, e := range captaincode.ReadInjectionLog(50) {
			kinds := make([]string, 0, len(e.Findings))
			for _, f := range e.Findings {
				kinds = append(kinds, f.Level+" "+f.Kind)
			}
			fmt.Printf("%s  %-7s %-9s %s %s%s%s\n", e.At.Format("Jan 2 15:04"), e.Channel, e.Action, orDash(e.From),
				strings.Join(kinds, ", "), map[bool]string{true: " - " + e.Detail}[e.Detail != ""], map[bool]string{true: " - " + e.Why}[e.Why != ""])
		}
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if len(args) > 0 && args[0] == "open" {
		dir, _ := os.Getwd()
		if len(args) > 1 {
			dir = args[1]
		}
		if dir == "--all" {
			dir = ""
		} else if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		body, _ := json.Marshal(map[string]string{"action": "open", "dir": dir})
		resp, err := client.Post(brainURL()+"/v1/inbox/held", "application/json", bytes.NewReader(body))
		if err != nil {
			fatal(fmt.Errorf("the brain is not running (%v)", err))
		}
		defer resp.Body.Close()
		var out struct {
			Reopened int `json:"reopened"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Reopened == 0 {
			fmt.Println("no closed inbox there")
			return
		}
		fmt.Printf("reopened %d inbox(es); held messages stay held - release them one by one\n", out.Reopened)
		return
	}
	if len(args) == 2 && (args[0] == "release" || args[0] == "drop") {
		body, _ := json.Marshal(map[string]string{"id": args[1], "action": args[0]})
		resp, err := client.Post(brainURL()+"/v1/inbox/held", "application/json", bytes.NewReader(body))
		if err != nil {
			fatal(fmt.Errorf("the brain is not running (%v)", err))
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "captain inbox: HTTP %d %v\n", resp.StatusCode, out["error"])
			os.Exit(1)
		}
		if args[0] == "release" {
			fmt.Printf("released %s - its folder's session receives it on the next poll\n", args[1])
		} else {
			fmt.Printf("dropped %s\n", args[1])
		}
		return
	}
	resp, err := client.Get(brainURL() + "/v1/inbox/held")
	if err != nil {
		fatal(fmt.Errorf("the brain is not running (%v)", err))
	}
	defer resp.Body.Close()
	var out struct {
		Held   []inboxItem       `json:"held"`
		Closed map[string]string `json:"closed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		fatal(fmt.Errorf("captain inbox: %v", err))
	}
	for d, why := range out.Closed {
		fmt.Printf("CLOSED %s - %s\n    captain inbox open %s\n\n", d, why, d)
	}
	if len(out.Held) == 0 {
		fmt.Println("no held messages")
		return
	}
	for _, it := range out.Held {
		fmt.Printf("%s  for %s  from %s  (%s)\n", it.ID, it.Dir, orDash(it.Origin), it.At.Format("Jan 2 15:04"))
		for _, f := range it.Findings {
			fmt.Printf("    %s: %s - %s\n", f.Level, f.Kind, f.Excerpt)
		}
		fmt.Printf("    text: %s\n\n", strings.Join(strings.Fields(promptPeek(it.Text)), " "))
	}
	fmt.Println("captain inbox release <id> delivers one; captain inbox drop <id> deletes it.")
}
