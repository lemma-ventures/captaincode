package captaincode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RetrievalEvent records one search or code retrieval event for telemetry
// and efficiency tracking (performance, security, accuracy, token use).
type RetrievalEvent struct {
	At             time.Time `json:"at"`
	Tool           string    `json:"tool"` // search, code_search, file_search, ask
	Query          string    `json:"query"`
	Lane           string    `json:"lane"` // code, doc, both
	Cwd            string    `json:"cwd,omitempty"`
	DurationMS     float64   `json:"duration_ms"`
	HitsCount      int       `json:"hits_count"`
	TokensReturned int       `json:"tokens_returned"`
	TokensAvoided  int       `json:"tokens_avoided"` // estimated tokens saved vs reading full files
	Safe           bool      `json:"safe"`           // paths contained, bounded payload
	CodeIntelUsed  bool      `json:"codeintel_used"` // CodeIntel AST search utilized
}

// RetrievalStats aggregates efficiency metrics over a time window.
type RetrievalStats struct {
	WindowDays          int     `json:"window_days"`
	TotalQueries        int     `json:"total_queries"`
	CodeQueries         int     `json:"code_queries"`
	DocQueries          int     `json:"doc_queries"`
	MedianLatencyMS     float64 `json:"median_latency_ms"`
	P95LatencyMS        float64 `json:"p95_latency_ms"`
	TotalTokensReturned int     `json:"total_tokens_returned"`
	TotalTokensAvoided  int     `json:"total_tokens_avoided"`
	TokenSavingsPercent float64 `json:"token_savings_percent"`
	SafetyRate          float64 `json:"safety_rate"`
	HitRate             float64 `json:"hit_rate"`
	CodeIntelShare      float64 `json:"codeintel_share"`
}

var retrievalLogMu sync.Mutex

// RetrievalLogPath returns the default path to retrieval.jsonl.
func RetrievalLogPath() string {
	if p := os.Getenv("CAPTAIN_RETRIEVAL_LOG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".captaincode", "retrieval.jsonl")
}

// LogRetrievalEvent records a retrieval operation. Never fails callers.
func LogRetrievalEvent(e RetrievalEvent) {
	if os.Getenv("CAPTAIN_RETRIEVAL_LOG") == "0" || os.Getenv("CAPTAIN_RETRIEVAL_LOG") == "off" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(e.Query) > 200 {
		e.Query = e.Query[:200]
	}

	retrievalLogMu.Lock()
	defer retrievalLogMu.Unlock()

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')

	p := RetrievalLogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(data)
			_ = f.Close()
		}
	}

	// Also record locally to repo brain if available
	if e.Cwd != "" {
		if root := RepoBrainRoot(e.Cwd); root != "" {
			localP := filepath.Join(root, "index", "retrieval-telemetry.jsonl")
			_ = os.MkdirAll(filepath.Dir(localP), 0o755)
			if f, err := os.OpenFile(localP, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = f.Write(data)
				_ = f.Close()
			}
		}
	}
}

// ReadRetrievalEvents reads events newer than since from the log path.
func ReadRetrievalEvents(logPath string, since time.Time) ([]RetrievalEvent, error) {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []RetrievalEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e RetrievalEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if !since.IsZero() && e.At.Before(since) {
			continue
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// ComputeRetrievalStats computes aggregated efficiency metrics for the given events.
func ComputeRetrievalStats(events []RetrievalEvent, windowDays int) RetrievalStats {
	stats := RetrievalStats{
		WindowDays: windowDays,
	}
	if len(events) == 0 {
		stats.SafetyRate = 100.0
		return stats
	}

	stats.TotalQueries = len(events)
	var latencies []float64
	hitsCount := 0
	safeCount := 0
	codeintelCount := 0

	for _, e := range events {
		latencies = append(latencies, e.DurationMS)
		stats.TotalTokensReturned += e.TokensReturned
		stats.TotalTokensAvoided += e.TokensAvoided
		if e.HitsCount > 0 {
			hitsCount++
		}
		if e.Safe {
			safeCount++
		}
		if e.CodeIntelUsed {
			codeintelCount++
		}
		if e.Lane == "code" {
			stats.CodeQueries++
		} else if e.Lane == "doc" {
			stats.DocQueries++
		}
	}

	sort.Float64s(latencies)
	n := len(latencies)
	if n > 0 {
		if n%2 == 1 {
			stats.MedianLatencyMS = latencies[n/2]
		} else {
			stats.MedianLatencyMS = (latencies[n/2-1] + latencies[n/2]) / 2.0
		}
		p95Idx := int(math.Ceil(0.95*float64(n))) - 1
		if p95Idx < 0 {
			p95Idx = 0
		}
		if p95Idx >= n {
			p95Idx = n - 1
		}
		stats.P95LatencyMS = latencies[p95Idx]
	}

	stats.HitRate = (float64(hitsCount) / float64(len(events))) * 100.0
	stats.SafetyRate = (float64(safeCount) / float64(len(events))) * 100.0
	stats.CodeIntelShare = (float64(codeintelCount) / float64(len(events))) * 100.0

	totalEvaluated := stats.TotalTokensReturned + stats.TotalTokensAvoided
	if totalEvaluated > 0 {
		stats.TokenSavingsPercent = (float64(stats.TotalTokensAvoided) / float64(totalEvaluated)) * 100.0
	}

	return stats
}

// RenderRetrievalStats formats metrics for CLI output.
func RenderRetrievalStats(s RetrievalStats) string {
	var sb strings.Builder
	period := "all time"
	if s.WindowDays > 0 {
		period = fmt.Sprintf("last %d days", s.WindowDays)
	}
	fmt.Fprintf(&sb, "Euclid & CodeIntel Retrieval Efficiency (%s)\n", period)
	fmt.Fprintf(&sb, "  Queries:        %d total (%d code, %d doc)\n", s.TotalQueries, s.CodeQueries, s.DocQueries)
	fmt.Fprintf(&sb, "  Performance:    p50 %.2f ms, p95 %.2f ms\n", s.MedianLatencyMS, s.P95LatencyMS)
	fmt.Fprintf(&sb, "  Accuracy:       %.1f%% hit rate, %.1f%% CodeIntel AST coverage\n", s.HitRate, s.CodeIntelShare)
	fmt.Fprintf(&sb, "  Security:       %.1f%% safe containment (bounded, zero traversal)\n", s.SafetyRate)
	fmt.Fprintf(&sb, "  Token Use:      %d tokens returned, ~%d avoided (%.1f%% reduction)\n",
		s.TotalTokensReturned, s.TotalTokensAvoided, s.TokenSavingsPercent)
	return sb.String()
}
