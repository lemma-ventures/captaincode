package captaincode

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"
)

type OpenShellCheckpoint struct {
	RunDir         string `json:"run_dir"`
	SequenceSHA256 string `json:"sequence_sha256"`
}

func (c OpenShellCheckpoint) Validate() error {
	digest, err := hex.DecodeString(c.SequenceSHA256)
	if err != nil || len(digest) != 32 || !filepath.IsAbs(c.RunDir) || filepath.Clean(c.RunDir) != c.RunDir {
		return errors.New("openshell: invalid task checkpoint")
	}
	return nil
}

type openShellCheckpointKey struct{}

func WithOpenShellCheckpoint(ctx context.Context, save func(OpenShellCheckpoint) error) context.Context {
	return context.WithValue(ctx, openShellCheckpointKey{}, save)
}

func recordOpenShellCheckpoint(ctx context.Context, runDir, digest string) error {
	if save, ok := ctx.Value(openShellCheckpointKey{}).(func(OpenShellCheckpoint) error); ok && save != nil {
		return save(OpenShellCheckpoint{RunDir: runDir, SequenceSHA256: digest})
	}
	return nil
}

func (l *Ledger) RecordOpenShellCheckpoint(attemptID string, checkpoint OpenShellCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	as := l.AttemptStateFor(attemptID)
	if as == nil || as.Leg != LegOpenShell || as.State != StateRunning {
		return errors.New("openshell: checkpoint requires a running sandbox attempt")
	}
	if as.OpenShell != nil && *as.OpenShell != checkpoint {
		return errors.New("openshell: attempt is already bound to a different sequence")
	}
	as.OpenShell = &checkpoint
	as.CheckpointAt, as.UpdatedAt = time.Now(), time.Now()
	return nil
}
