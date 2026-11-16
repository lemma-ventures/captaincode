package captaincode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	blackboxStoreFileName     = "store.json"
	blackboxStorePreviousName = "store.prev.json"
	blackboxStoreSchema       = 1
	BlackboxMaxStoreBytes     = BlackboxMaxEnvelopeBytes + (1 << 20)
)

type blackboxStoreFile struct {
	Schema           int              `json:"schema"`
	Generation       uint64           `json:"generation"`
	Sequence         int              `json:"sequence"`
	SnapshotID       string           `json:"snapshot_id"`
	Digest           string           `json:"digest"`
	UpstreamRevision string           `json:"upstream_revision"`
	AcceptedAt       time.Time        `json:"accepted_at"`
	VerifiedAt       time.Time        `json:"verified_at"`
	ExpiresAt        time.Time        `json:"expires_at"`
	Revocations      []string         `json:"revocations"`
	Envelope         BlackboxEnvelope `json:"envelope"`
}

type BlackboxStore struct {
	dir      string
	pins     BlackboxTrustPins
	file     blackboxStoreFile
	snapshot BlackboxSnapshot
	current  bool
}

func OpenBlackboxStore(dir string, pins BlackboxTrustPins) (*BlackboxStore, error) {
	store := &BlackboxStore{dir: dir, pins: pins}
	body, err := os.ReadFile(filepath.Join(dir, blackboxStoreFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read blackbox store: %w", err)
	}
	file, err := decodeBlackboxStoreFile(body)
	if err != nil {
		return nil, err
	}
	snapshot, err := verifyBlackboxEnvelopeStructure(file.Envelope, BlackboxVerifyOptions{Pins: pins})
	if err != nil {
		return nil, fmt.Errorf("blackbox store envelope is not valid: %w", err)
	}
	if !blackboxStoreMatchesEnvelope(file, snapshot) {
		return nil, fmt.Errorf("blackbox store record does not match its envelope")
	}
	store.file = file
	store.snapshot = snapshot
	store.current = true
	return store, nil
}

func (store *BlackboxStore) HasCurrent() bool {
	return store.current
}

func (store *BlackboxStore) Generation() uint64 {
	return store.file.Generation
}

func (store *BlackboxStore) Sequence() int {
	return store.file.Sequence
}

func (store *BlackboxStore) Digest() string {
	return store.snapshot.Digest
}

func (store *BlackboxStore) Snapshot() BlackboxSnapshot {
	return store.snapshot
}

func (store *BlackboxStore) Revocations() []string {
	return append([]string{}, store.file.Revocations...)
}

func (store *BlackboxStore) VerifiedAt() time.Time {
	return store.file.VerifiedAt
}

func (store *BlackboxStore) AcceptedAt() time.Time {
	return store.file.AcceptedAt
}

func (store *BlackboxStore) ExpiresAt() time.Time {
	return store.file.ExpiresAt
}

func (store *BlackboxStore) Expired(now time.Time) bool {
	if !store.HasCurrent() {
		return false
	}
	return !store.file.ExpiresAt.After(now)
}

func (store *BlackboxStore) Accept(envelope BlackboxEnvelope, now time.Time) error {
	snapshot, err := VerifyBlackboxEnvelope(envelope, BlackboxVerifyOptions{
		Pins:           store.pins,
		Now:            now,
		LastGeneration: store.Generation(),
	})
	if err != nil {
		return fmt.Errorf("refuse snapshot: %w", err)
	}
	if err := blackboxEnsureStoreDir(store.dir); err != nil {
		return err
	}
	if store.HasCurrent() {
		current, err := json.Marshal(store.file)
		if err != nil {
			return fmt.Errorf("encode previous store record: %w", err)
		}
		if err := writeBlackboxStoreFile(filepath.Join(store.dir, blackboxStorePreviousName), current); err != nil {
			return fmt.Errorf("write previous store record: %w", err)
		}
	}
	next := blackboxStoreFile{
		Schema:           blackboxStoreSchema,
		Generation:       envelope.Authority.Generation,
		Sequence:         envelope.Manifest.Sequence,
		SnapshotID:       envelope.Rules.ID,
		Digest:           snapshot.Digest,
		UpstreamRevision: envelope.Revision,
		AcceptedAt:       now,
		VerifiedAt:       now,
		ExpiresAt:        envelope.ExpiresAt,
		Revocations:      append([]string{}, envelope.Revocations...),
		Envelope:         envelope,
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode store record: %w", err)
	}
	if err := writeBlackboxStoreFile(filepath.Join(store.dir, blackboxStoreFileName), encoded); err != nil {
		return fmt.Errorf("write store record: %w", err)
	}
	store.file = next
	store.snapshot = snapshot
	store.current = true
	return nil
}

func (store *BlackboxStore) Rollback() error {
	if !store.HasCurrent() {
		return fmt.Errorf("no accepted snapshot to roll back")
	}
	previous, err := os.ReadFile(filepath.Join(store.dir, blackboxStorePreviousName))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no previous snapshot on disk")
	}
	if err != nil {
		return fmt.Errorf("read previous store record: %w", err)
	}
	file, err := decodeBlackboxStoreFile(previous)
	if err != nil {
		return err
	}
	snapshot, err := verifyBlackboxEnvelopeStructure(file.Envelope, BlackboxVerifyOptions{Pins: store.pins})
	if err != nil {
		return fmt.Errorf("previous envelope is not valid: %w", err)
	}
	if !blackboxStoreMatchesEnvelope(file, snapshot) {
		return fmt.Errorf("previous store record does not match its envelope")
	}
	if err := blackboxEnsureStoreDir(store.dir); err != nil {
		return err
	}
	current, err := json.Marshal(store.file)
	if err != nil {
		return fmt.Errorf("encode store record: %w", err)
	}
	if err := writeBlackboxStoreFile(filepath.Join(store.dir, blackboxStorePreviousName), current); err != nil {
		return fmt.Errorf("write previous store record: %w", err)
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return fmt.Errorf("encode rolled back store record: %w", err)
	}
	if err := writeBlackboxStoreFile(filepath.Join(store.dir, blackboxStoreFileName), encoded); err != nil {
		return fmt.Errorf("write store record: %w", err)
	}
	store.file = file
	store.snapshot = snapshot
	return nil
}

func blackboxStoreMatchesEnvelope(file blackboxStoreFile, snapshot BlackboxSnapshot) bool {
	return file.Generation == file.Envelope.Authority.Generation &&
		file.Sequence == file.Envelope.Manifest.Sequence &&
		file.SnapshotID == file.Envelope.Rules.ID &&
		file.UpstreamRevision == file.Envelope.Revision &&
		file.Digest == snapshot.Digest &&
		file.ExpiresAt.Equal(file.Envelope.ExpiresAt) &&
		slices.Equal(file.Revocations, file.Envelope.Revocations)
}

func blackboxEnsureStoreDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	return nil
}

func writeBlackboxStoreFile(path string, payload []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func decodeBlackboxStoreFile(body []byte) (blackboxStoreFile, error) {
	if len(body) > BlackboxMaxStoreBytes {
		return blackboxStoreFile{}, fmt.Errorf("store record exceeds %d bytes", BlackboxMaxStoreBytes)
	}
	var file blackboxStoreFile
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return blackboxStoreFile{}, fmt.Errorf("decode store record: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return blackboxStoreFile{}, fmt.Errorf("decode store record: trailing data")
	}
	if file.Schema != blackboxStoreSchema {
		return blackboxStoreFile{}, fmt.Errorf("store schema %d is not %d", file.Schema, blackboxStoreSchema)
	}
	if file.Envelope.ID == "" {
		return blackboxStoreFile{}, fmt.Errorf("store record carries no envelope")
	}
	return file, nil
}
