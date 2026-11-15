package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type hostCertStep struct {
	Name   string
	Status string
	Detail string
}

type hostCertReport struct {
	Steps  []hostCertStep
	Failed int
}

func runHostCertDynamic(client *captaincode.TaskClient, projectRoot string, restart func() error) hostCertReport {
	adapter := captaincode.NewPiAdapter(client)
	cert := captaincode.NewCertChecklist(adapter)
	report := hostCertReport{}
	run := func(name string, check func() error) {
		step := hostCertStep{Name: name, Status: "pass"}
		if err := check(); err != nil {
			step.Status = "fail"
			step.Detail = err.Error()
			report.Failed++
		}
		report.Steps = append(report.Steps, step)
	}
	run("cancel during execution", func() error {
		return cert.CheckCancelDuringExecution("certification cancel", captaincode.SubmitOptions{})
	})
	run("recover after restart", func() error {
		return cert.CheckRecoverAfterRestart("certification recovery", projectRoot, captaincode.SubmitOptions{
			Limits: captaincode.ResourceLimits{MaxAttempts: 2, MaxCostUSD: 0.25, MaxWallMs: 60000},
		}, restart)
	})
	run("conflicting retry", func() error {
		revision, err := adapter.CurrentRevision(projectRoot)
		if err != nil {
			return err
		}
		taskID := captaincode.NewTaskID()
		request := captaincode.SubmitRequest{Prompt: "certification retry", ProjectRoot: projectRoot, BaseRevision: revision}
		if _, taskErr := client.SubmitWithID(taskID, request); taskErr != nil {
			return taskErr
		}
		request.Prompt = "certification retry with different input"
		return cert.CheckRejectConflictingRetry(taskID, request)
	})
	run("root budget", func() error {
		return cert.CheckRootBudget("certification budget", projectRoot, captaincode.SubmitOptions{
			Limits: captaincode.ResourceLimits{MaxAttempts: 3, MaxCostUSD: 0.5, MaxWallMs: 120000},
		})
	})
	run("deny scope widening", func() error {
		return cert.CheckDenyScopeWidening("certification scope", projectRoot, captaincode.SubmitOptions{})
	})
	return report
}

func validateHostCertBrain(raw string, allowLive bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid brain URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("brain URL must use http or https")
	}
	if allowLive || parsed.Port() != "14097" {
		return nil
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "localhost" || net.ParseIP(hostname).IsLoopback() {
		return fmt.Errorf("dynamic certification cancels and restarts work; use a test brain or --allow-live-brain")
	}
	return nil
}

func hostEditorConfigJSON(editor, binary string) ([]byte, error) {
	return captaincode.EditorMCPConfigJSON(editor, binary)
}

func cmdHostEditor(args []string) {
	if len(args) != 2 || args[0] != "config" {
		fatal(fmt.Errorf("usage: captain host editor config <cursor|vscode|zed>"))
	}
	binary, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		fatal(err)
	}
	config, err := hostEditorConfigJSON(args[1], binary)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(config))
}
