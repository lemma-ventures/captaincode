package captaincode

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

type Claim struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Statement string `json:"statement"`
	Path      string `json:"path"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Quote     string `json:"quote"`
}

type ClaimSet struct {
	Version int     `json:"version"`
	Commit  string  `json:"declared_commit"`
	Claims  []Claim `json:"claims"`
}

type ClaimResult struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Citation string `json:"citation,omitempty"`
	Result   string `json:"result"`
	Reason   string `json:"reason,omitempty"`
}

type ClaimCounts struct {
	Present int `json:"present"`
	Dropped int `json:"dropped"`
}

type ClaimCheckLog struct {
	Version        int           `json:"version"`
	DeclaredCommit string        `json:"declared_commit"`
	ArchiveSHA256  string        `json:"archive_sha256"`
	ClaimsSHA256   string        `json:"claims_sha256"`
	FilesCaptured  int           `json:"files_captured"`
	Results        []ClaimResult `json:"results"`
	Counts         ClaimCounts   `json:"counts"`
}

const (
	claimLimit      = 100
	claimTypeFact   = "fact"
	claimTypeInterp = "interpretation"
	claimTypeLimit  = "limitation"
)

var claimIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func claimNormalize(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func claimValidate(c Claim) string {
	if !claimIDPattern.MatchString(c.ID) {
		return "invalid_claim"
	}
	if c.Type != claimTypeFact && c.Type != claimTypeInterp && c.Type != claimTypeLimit {
		return "invalid_claim"
	}
	if n := len(c.Statement); n < 1 || n > 2000 {
		return "invalid_claim"
	}
	if !reviewSafePath(c.Path) || len(c.Path) > 4096 {
		return "invalid_claim"
	}
	if c.Start < 1 || c.End < c.Start {
		return "invalid_claim"
	}
	if n := len(c.Quote); n < 1 || n > 8000 {
		return "invalid_claim"
	}
	return ""
}

func claimPathIndex(files []ReviewFile) map[string]ReviewFile {
	index := map[string]ReviewFile{}
	root := ""
	uniform := len(files) > 0
	for _, f := range files {
		index[f.Path] = f
		first, rest, ok := strings.Cut(f.Path, "/")
		if !ok || rest == "" {
			uniform = false
			continue
		}
		if root == "" {
			root = first
		} else if root != first {
			uniform = false
		}
	}
	if uniform {
		for _, f := range files {
			_, rel, _ := strings.Cut(f.Path, "/")
			if _, clash := index[rel]; !clash {
				index[rel] = f
			}
		}
	}
	return index
}

func CheckClaims(ev ReviewEvidence, raw []byte) (ClaimCheckLog, error) {
	log := ClaimCheckLog{Version: 1, Results: []ClaimResult{}, Counts: ClaimCounts{}}
	if ev.Version != 1 || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(ev.Commit) || len(ev.Files) == 0 {
		return log, errors.New("invalid evidence pack")
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return log, errors.New("claims must be a file of at most 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var set ClaimSet
	if dec.Decode(&set) != nil || dec.Decode(new(any)) != io.EOF {
		return log, errors.New("invalid claim set JSON")
	}
	if set.Version != 1 || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(set.Commit) {
		return log, errors.New("invalid claim set header")
	}
	if set.Commit != ev.Commit {
		return log, errors.New("claim set commit does not match the evidence pack")
	}
	if len(set.Claims) == 0 || len(set.Claims) > claimLimit {
		return log, errors.New("claim count must be between 1 and 100")
	}
	log.DeclaredCommit = ev.Commit
	log.ArchiveSHA256 = ev.ArchiveSHA256
	log.ClaimsSHA256 = reviewDigest(raw)
	log.FilesCaptured = len(ev.Files)
	index := claimPathIndex(ev.Files)
	seen := map[string]bool{}
	for _, c := range set.Claims {
		r := ClaimResult{ID: c.ID, Type: c.Type}
		reason := claimValidate(c)
		if reason == "" {
			if seen[c.ID] {
				return log, errors.New("duplicate claim id")
			}
			seen[c.ID] = true
		}
		if reason == "" {
			r.Citation = strings.Join([]string{c.Path, itoa(c.Start), itoa(c.End)}, ":")
			f, ok := index[c.Path]
			if !ok {
				reason = "unknown_path"
			} else {
				lines := strings.Split(f.Text, "\n")
				if c.End > len(lines) {
					reason = "line_range_invalid"
				} else {
					span := claimNormalize(strings.Join(lines[c.Start-1:c.End], "\n"))
					if !strings.Contains(span, claimNormalize(c.Quote)) {
						reason = "quote_not_found"
					}
				}
			}
		}
		if reason == "" {
			r.Result = "present"
			log.Counts.Present++
		} else {
			r.Result = "dropped"
			r.Reason = reason
			log.Counts.Dropped++
		}
		log.Results = append(log.Results, r)
	}
	return log, nil
}
