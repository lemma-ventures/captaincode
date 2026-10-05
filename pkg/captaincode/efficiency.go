package captaincode

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EfficiencyRecord contains counts only. Prompts, answers, paths and digests
// never enter this journal. Prefix reuse is an estimate, not a cache hit.
type EfficiencyRecord struct {
	At                time.Time        `json:"at"`
	Kind              string           `json:"kind"`
	Purpose           string           `json:"purpose,omitempty"`
	Leg               Leg              `json:"leg,omitempty"`
	OK                bool             `json:"ok"`
	DurationMS        int64            `json:"duration_ms,omitempty"`
	Tokens            int              `json:"tokens,omitempty"`
	Usage             *TokenUsage      `json:"token_usage,omitempty"`
	TTFTMs            *int64           `json:"ttft_ms,omitempty"`
	PromptBytes       int              `json:"prompt_bytes,omitempty"`
	PriorBytes        int              `json:"prior_bytes,omitempty"`
	CommonPrefixBytes int              `json:"common_prefix_bytes,omitempty"`
	Comparable        bool             `json:"comparable,omitempty"`
	Schema            *ToolSchemaUsage `json:"tool_schema,omitempty"`
}

var efficiencyMu sync.Mutex

func LogEfficiency(r EfficiencyRecord) {
	if os.Getenv("CAPTAIN_EFFICIENCY_LOG") == "0" {
		return
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".captaincode", "efficiency.jsonl")
	efficiencyMu.Lock()
	defer efficiencyMu.Unlock()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	// Bounded telemetry: rotate once; never grow without a limit.
	if st, e := os.Stat(path); e == nil && st.Size() > 16<<20 {
		_ = os.Rename(path, path+".1")
	}
	r.At = time.Now()
	raw, e := json.Marshal(r)
	if e != nil {
		return
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}
func LogSmallCall(purpose string, res Result, err error) {
	LogEfficiency(EfficiencyRecord{Kind: "local_call", Purpose: purpose, Leg: LegLocal, OK: err == nil, DurationMS: res.DurationMs, Tokens: res.Tokens, Usage: res.TokenUsage, TTFTMs: res.TTFTMs})
}

type promptShape struct {
	Hashes [][32]byte
	Bytes  int
	At     time.Time
}
type PromptReuseTracker struct {
	mu    sync.Mutex
	prior map[[32]byte]promptShape
}

// Observe compares fixed-size hashed blocks at dispatch. Keys and blocks stay
// in bounded process memory. A restart starts a new baseline.
func (t *PromptReuseTracker) Observe(key, prompt string) EfficiencyRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.prior == nil {
		t.prior = map[[32]byte]promptShape{}
	}
	k := sha256.Sum256([]byte(key))
	now := time.Now()
	shape := promptShape{Bytes: len(prompt), At: now}
	for i := 0; i+256 <= len(prompt) && i < 1<<20; i += 256 {
		shape.Hashes = append(shape.Hashes, sha256.Sum256([]byte(prompt[i:i+256])))
	}
	r := EfficiencyRecord{Kind: "prompt_prefix", OK: true, PromptBytes: len(prompt)}
	if old, ok := t.prior[k]; ok && now.Sub(old.At) < 24*time.Hour {
		r.Comparable = true
		r.PriorBytes = old.Bytes
		for i, h := range shape.Hashes {
			if i >= len(old.Hashes) || h != old.Hashes[i] {
				break
			}
			r.CommonPrefixBytes += 256
		}
	}
	if len(t.prior) >= 128 {
		var oldest [32]byte
		var at time.Time
		for k, v := range t.prior {
			if at.IsZero() || v.At.Before(at) {
				oldest = k
				at = v.At
			}
		}
		delete(t.prior, oldest)
	}
	t.prior[k] = shape
	return r
}
