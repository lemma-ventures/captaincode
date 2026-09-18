package main

// `captain kill` - stop the worker run in flight.
//
// A worker that is going nowhere (a wedged session, a leg grinding for 17
// minutes) has to be stoppable without restarting the brain, and without the
// TUI - which queues typed input while a turn streams, so no in-TUI command
// can reach it. This kills both kinds of leg: opencode-session legs are
// ABORTED server-side (cancelling the client request would leave the session
// wedged), and CLI legs (claude -p, cursor-agent) are signalled.
//
//	captain kill          # stop whatever is running now
//	captain kill --loops  # also end any /repeat loop
//
// It does not touch the brain, so the turn fails cleanly and any reroute or
// partial-salvage logic still applies.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

func cmdKill(args []string) {
	loops := false
	for _, a := range args {
		switch a {
		case "--loops", "--all":
			loops = true
		case "-h", "--help":
			fmt.Println("usage: captain kill [--loops]\n\nStops the worker run in flight (opencode sessions are aborted server-side;\nclaude -p / cursor-agent are signalled). --loops also ends /repeat threads.")
			return
		}
	}

	c := &http.Client{Timeout: 8 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", opencodePort)

	// 1. opencode-session legs (free/grok/codex/glm/minimax/qwen/gemini/deepseek)
	aborted := 0
	if resp, err := c.Get(base + "/session"); err == nil {
		defer resp.Body.Close()
		var sessions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Time  struct {
				Updated int64 `json:"updated"`
			} `json:"time"`
		}
		if json.NewDecoder(resp.Body).Decode(&sessions) == nil {
			cutoff := time.Now().Add(-30 * time.Minute).UnixMilli()
			for _, s := range sessions {
				// only captain's own workers, and only recently active ones:
				// aborting an idle session is noise, not safety.
				if !strings.HasPrefix(s.Title, "captain-") || s.Time.Updated < cutoff {
					continue
				}
				r, err := c.Post(base+"/session/"+s.ID+"/abort", "application/json", strings.NewReader("{}"))
				if err == nil {
					r.Body.Close()
					aborted++
					fmt.Printf("aborted %s (%s)\n", s.Title, s.ID)
				}
			}
		}
	} else {
		fmt.Printf("opencode not reachable at %s - skipping session legs\n", base)
	}

	// 2. CLI legs - they are subprocesses, not sessions
	signalled := 0
	for _, pat := range []string{"cursor-agent --use-system-ca", "claude -p"} {
		if err := exec.Command("pkill", "-f", pat).Run(); err == nil {
			signalled++
			fmt.Printf("signalled %s\n", pat)
		}
	}

	if loops {
		cmdStop(nil)
	}
	if aborted == 0 && signalled == 0 {
		fmt.Println("nothing was running.")
		return
	}
	fmt.Printf("\n%d session(s) aborted, %d CLI leg(s) signalled. The turn fails cleanly; captain may reroute it.\n", aborted, signalled)
}
