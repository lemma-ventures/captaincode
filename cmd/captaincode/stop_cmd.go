package main

// `captain stop` - end the running repeat loop from OUTSIDE the TUI.
//
// While a turn streams, the fork queues typed input, so "/repeat stop" waits
// for the stream to end - which for a watched loop is never (live 2026-09-01:
// "QUEUED /repeat stop is queued"). A loop that streams must remain stoppable,
// so this goes straight to the brain over HTTP: `captain stop` from any
// terminal, or `! captain stop` from the TUI's own shell prefix.

import (
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

func cmdStop(args []string) {
	// This terminal's folder: only its loops stop. `--all` reaches every
	// folder's loops on the machine (one brain serves every TUI).
	q := neturl.Values{"cwd": {defaultWorkspace().Dir}}
	for _, a := range args {
		switch a {
		case "--abort", "abort":
			q.Set("abort", "1")
		case "--finish", "finish", "--wrapup", "wrapup": // the default, named: end after the round in flight
		case "--all", "all":
			q.Del("cwd")
		case "-h", "--help":
			fmt.Println("usage: captain stop [--finish|--abort] [--all]\n\nEnds the running /repeat loops of this folder once their current round completes (--finish, the default).\n--abort cancels the round in flight too (its work is lost).\n--all stops the loops of every folder, not just this one.")
			return
		}
	}
	url := fmt.Sprintf("http://%s/v1/repeat/stop?%s", brainAddr, q.Encode())
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable at %s: %w", brainAddr, err))
	}
	defer resp.Body.Close()
	var out struct{ Result string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Println(out.Result)
}
