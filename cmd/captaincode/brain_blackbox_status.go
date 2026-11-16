package main

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const blackboxWorkspaceRecentLimit = 20

type blackboxWorkspaceStatus struct {
	Workspace          string                               `json:"workspace"`
	Mode               string                               `json:"mode"`
	ModeValid          bool                                 `json:"mode_valid"`
	ClaudeHook         bool                                 `json:"claude_hook"`
	SnapshotID         string                               `json:"snapshot_id,omitempty"`
	SnapshotAgeSeconds int64                                `json:"snapshot_age_seconds"`
	SnapshotOK         bool                                 `json:"snapshot_ok"`
	Total              int                                  `json:"total"`
	Dispositions       map[string]int                       `json:"dispositions"`
	Hosts              map[string]int                       `json:"hosts"`
	Uncovered          []string                             `json:"uncovered"`
	Recent             []captaincode.BlackboxSecurityRecord `json:"recent"`
}

func collectBlackboxWorkspaceStatus(cwd string) blackboxWorkspaceStatus {
	status := collectBlackboxStatus()
	report := blackboxWorkspaceStatus{
		Workspace:          captaincode.BlackboxWorkspaceID(cwd),
		Mode:               status.Mode,
		ModeValid:          status.ModeValid,
		ClaudeHook:         status.ClaudeHook,
		SnapshotID:         status.SnapshotID,
		SnapshotAgeSeconds: int64(status.SnapshotAge.Seconds()),
		SnapshotOK:         status.SnapshotOK,
		Dispositions:       map[string]int{},
		Hosts:              map[string]int{},
		Uncovered:          append([]string(nil), blackboxUncoveredTransports...),
		Recent:             []captaincode.BlackboxSecurityRecord{},
	}
	for _, name := range blackboxDispositions {
		report.Dispositions[name] = 0
	}
	for _, record := range status.Records {
		if record.WorkspaceID != report.Workspace {
			continue
		}
		report.Total++
		host := record.Host
		if host == "" {
			host = "unknown"
		}
		report.Hosts[host]++
		if record.Disposition != "" {
			report.Dispositions[record.Disposition]++
		}
		if hasNotCovered(record) {
			report.Dispositions["not_covered"]++
		}
		report.Recent = append(report.Recent, record)
	}
	if len(report.Recent) > blackboxWorkspaceRecentLimit {
		report.Recent = report.Recent[len(report.Recent)-blackboxWorkspaceRecentLimit:]
	}
	for left, right := 0, len(report.Recent)-1; left < right; left, right = left+1, right-1 {
		report.Recent[left], report.Recent[right] = report.Recent[right], report.Recent[left]
	}
	return report
}

func (b *brain) blackboxStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	dir, ok := workspaceFilter(r)
	if !ok || !filepath.IsAbs(dir) {
		writeErr(w, http.StatusBadRequest, "absolute cwd required")
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "cwd must be an existing directory")
		return
	}
	writeJSON(w, http.StatusOK, collectBlackboxWorkspaceStatus(filepath.Clean(dir)))
}
