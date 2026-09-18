package main

// `captain status` / `captain watch` - the answer to "what is it doing?" from
// any terminal, independent of the TUI's thinking mode (which collapses the
// step tracker by default and cannot show the live step in its title).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type wfStatusResp struct {
	Active    bool   `json:"active"`
	ID        string `json:"id"`
	Key       string `json:"key"`
	Stages    int    `json:"stages"`
	Runs      int    `json:"runs"`
	Elapsed   string `json:"elapsed"`
	Checklist string `json:"checklist"`
}

func fetchWorkflowStatus() (wfStatusResp, error) {
	var out wfStatusResp
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:14097/v1/workflow/status")
	if err != nil {
		return out, fmt.Errorf("brain not reachable on 14097: %w", err)
	}
	defer resp.Body.Close()
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func printWorkflowStatus(st wfStatusResp) {
	if st.ID == "" {
		fmt.Println("no workflow has run yet in this brain")
		return
	}
	state := "DONE"
	if st.Active {
		state = "RUNNING"
	}
	fmt.Printf("workflow %s · %s · %d stages · %d runs · %s [%s]\n\n", st.ID, st.Key, st.Stages, st.Runs, st.Elapsed, state)
	fmt.Print(st.Checklist)
	if p := runFilePath(st.ID, st.Key); p != "" {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("\nfull worker outputs: %s\n", p)
		}
	}
}

// cmdStatus prints the live (or last) workflow once.
func cmdStatus() {
	st, err := fetchWorkflowStatus()
	if err != nil {
		fatal(err)
	}
	printWorkflowStatus(st)
}

// cmdWatch redraws the checklist until the workflow finishes, so a long run has
// a window that is always open.
func cmdWatch() {
	for {
		st, err := fetchWorkflowStatus()
		if err != nil {
			fatal(err)
		}
		fmt.Print("\033[H\033[2J") // home + clear
		printWorkflowStatus(st)
		if !st.Active {
			return
		}
		time.Sleep(2 * time.Second)
	}
}
