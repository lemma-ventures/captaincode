package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type mcpProcess struct {
	PID   int
	RSSKB int64
	CWD   string
}

func listEuclidMCPProcesses() ([]mcpProcess, error) {
	out, err := exec.Command("ps", "-axo", "pid=,rss=,args=").Output()
	if err != nil {
		return nil, err
	}
	var processes []mcpProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[3] != "euclid" || fields[4] != "mcp" {
			continue
		}
		switch filepath.Base(fields[2]) {
		case "captain", "captain-next":
		default:
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		rss, rssErr := strconv.ParseInt(fields[1], 10, 64)
		if pidErr == nil && rssErr == nil && pid > 0 && rss >= 0 {
			processes = append(processes, mcpProcess{PID: pid, RSSKB: rss})
		}
	}
	cwds := processCwdsByPID()
	for i := range processes {
		processes[i].CWD = cwds[processes[i].PID]
	}
	return processes, nil
}

func processCwdsByPID() map[int]string {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "pn").Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	cwds := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if value, ok := strings.CutPrefix(line, "p"); ok {
			pid, _ = strconv.Atoi(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "n"); ok && pid > 0 && value != "" {
			cwds[pid] = value
		}
	}
	return cwds
}

func doctorMCPLine(list func() ([]mcpProcess, error)) string {
	if list == nil {
		list = listEuclidMCPProcesses
	}
	processes, err := list()
	if err != nil {
		return fmt.Sprintf("mcp        unavailable (%v)", err)
	}
	var rss int64
	missing, unknown := 0, 0
	for _, process := range processes {
		rss += process.RSSKB
		if process.CWD == "" {
			unknown++
			continue
		}
		if _, err := os.Stat(process.CWD); err != nil {
			if os.IsNotExist(err) {
				missing++
			} else {
				unknown++
			}
		}
	}
	processWord := "processes"
	if len(processes) == 1 {
		processWord = "process"
	}
	state := fmt.Sprintf("%d in missing folders", missing)
	if missing == 1 {
		state = "1 in a missing folder"
	}
	line := fmt.Sprintf("mcp        %d %s, %.1f MiB resident, %s", len(processes), processWord, float64(rss)/1024, state)
	if unknown > 0 {
		line += fmt.Sprintf(", folder unknown for %d", unknown)
	}
	return line
}
