package captaincode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type OpenShellCheckpoint struct {
	RunDir         string `json:"run_dir"`
	SequenceSHA256 string `json:"sequence_sha256"`
	VerifiedStages int    `json:"verified_stages"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

func (c OpenShellCheckpoint) Validate() error {
	digest, err := hex.DecodeString(c.SequenceSHA256)
	if err != nil || len(digest) != 32 || !filepath.IsAbs(c.RunDir) || filepath.Clean(c.RunDir) != c.RunDir {
		return errors.New("openshell: invalid task checkpoint")
	}
	digest, err = hex.DecodeString(c.EvidenceSHA256)
	if err != nil || len(digest) != 32 || c.VerifiedStages < 0 || c.VerifiedStages > MaxWorkflowStages {
		return errors.New("openshell: task checkpoint lacks valid verified-stage evidence; inspect the run before explicit run-directory recovery")
	}
	return nil
}

type openShellCheckpointKey struct{}

func WithOpenShellCheckpoint(ctx context.Context, save func(OpenShellCheckpoint) error) context.Context {
	return context.WithValue(ctx, openShellCheckpointKey{}, save)
}

func recordOpenShellCheckpoint(ctx context.Context, runDir string, run *OpenShellRun, verified int) error {
	if save, ok := ctx.Value(openShellCheckpointKey{}).(func(OpenShellCheckpoint) error); ok && save != nil {
		checkpoint, err := openShellCheckpoint(runDir, run, verified)
		if err != nil {
			return err
		}
		return save(checkpoint)
	}
	return nil
}

func openShellCheckpoint(runDir string, run *OpenShellRun, verified int) (OpenShellCheckpoint, error) {
	checkpoint := OpenShellCheckpoint{RunDir: runDir, SequenceSHA256: run.SequenceSHA256, VerifiedStages: verified}
	if verified < 0 || verified > MaxWorkflowStages || verified > len(run.Stages) {
		return checkpoint, errors.New("openshell: invalid verified-stage evidence count")
	}
	type stageEvidence struct {
		Record OpenShellStageRecord `json:"record"`
		SHA256 string               `json:"sha256"`
	}
	evidence := struct {
		Version        int                 `json:"version"`
		RunDir         string              `json:"run_dir"`
		SequenceSHA256 string              `json:"sequence_sha256"`
		StartedAt      time.Time           `json:"started_at"`
		Stages         []stageEvidence     `json:"stages"`
		SetAside       []OpenShellSetAside `json:"set_aside"`
	}{Version: 1, RunDir: runDir, SequenceSHA256: run.SequenceSHA256, StartedAt: run.StartedAt}
	for i, record := range run.Stages[:verified] {
		dir := openShellStageDir(runDir, i+1, run.reruns(i+1))
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil || resolved != dir || record.Stage != i+1 || record.Verdict != "pass" ||
			record.RunRecord != filepath.Join(dir, "run.json") {
			return checkpoint, errors.New("openshell: invalid verified-stage evidence path or verdict")
		}
		data, err := readOpenShellFile(record.RunRecord, openShellReportLimit)
		if err != nil {
			return checkpoint, fmt.Errorf("openshell: read verified-stage evidence: %w", err)
		}
		evidence.Stages = append(evidence.Stages, stageEvidence{Record: record, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	for _, aside := range run.SetAside {
		if aside.Stage <= verified {
			evidence.SetAside = append(evidence.SetAside, aside)
		}
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return checkpoint, err
	}
	checkpoint.EvidenceSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	return checkpoint, checkpoint.Validate()
}

func (l *Ledger) RecordOpenShellCheckpoint(attemptID string, checkpoint OpenShellCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	as := l.AttemptStateFor(attemptID)
	if as == nil || as.Leg != LegOpenShell || as.State != StateRunning {
		return errors.New("openshell: checkpoint requires a running sandbox attempt")
	}
	if previous := as.OpenShell; previous != nil {
		if previous.RunDir != checkpoint.RunDir || previous.SequenceSHA256 != checkpoint.SequenceSHA256 {
			return errors.New("openshell: attempt is already bound to a different sequence")
		}
		if checkpoint.VerifiedStages < previous.VerifiedStages || checkpoint.VerifiedStages > previous.VerifiedStages+1 ||
			(checkpoint.VerifiedStages == previous.VerifiedStages && checkpoint.EvidenceSHA256 != previous.EvidenceSHA256) {
			return errors.New("openshell: verified-stage evidence cannot be replaced, rolled back or skipped")
		}
	}
	as.OpenShell = &checkpoint
	as.CheckpointAt, as.UpdatedAt = time.Now(), time.Now()
	return nil
}
