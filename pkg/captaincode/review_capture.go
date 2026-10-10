package captaincode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ReviewFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}

type ReviewExclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ReviewEvidence struct {
	Version       int               `json:"version"`
	Commit        string            `json:"declared_commit"`
	ArchiveSHA256 string            `json:"archive_sha256"`
	Scope         []string          `json:"scope"`
	Files         []ReviewFile      `json:"files"`
	Excluded      []ReviewExclusion `json:"excluded"`
}

func reviewDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func reviewReadBounded(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, errors.New("review input read failed")
	}
	if int64(len(b)) > n {
		return nil, errors.New("review size limit exceeded")
	}
	return b, nil
}

func reviewSafePath(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	if len(name) == 0 || len(name) > 4096 || !utf8.ValidString(name) || strings.Contains(name, "\\") || strings.Contains(first, ":") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	n := strings.TrimSuffix(name, "/")
	return n != "" && n != "." && n != ".." && !strings.HasPrefix(n, "../") && path.Clean(n) == n
}

func CaptureReview(r io.Reader, commit string) (ReviewEvidence, error) {
	return CaptureReviewScope(r, commit, nil)
}

func CaptureReviewScope(r io.Reader, commit string, scope []string) (ReviewEvidence, error) {
	var empty ReviewEvidence
	if len(scope) > 100 {
		return empty, errors.New("too many review scope paths")
	}
	for _, name := range scope {
		if !reviewSafePath(name) {
			return empty, errors.New("invalid review scope path")
		}
	}
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) {
		return empty, errors.New("review requires a full lowercase commit SHA")
	}
	b, err := reviewReadBounded(r, 32<<20)
	if err != nil {
		return empty, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return empty, errors.New("invalid gzip archive")
	}
	defer gz.Close()
	raw, err := reviewReadBounded(gz, 64<<20)
	if err != nil {
		return empty, err
	}
	p := ReviewEvidence{Version: 1, Commit: commit, ArchiveSHA256: reviewDigest(b), Scope: append([]string{}, scope...), Files: []ReviewFile{}, Excluded: []ReviewExclusion{}}
	tr := tar.NewReader(bytes.NewReader(raw))
	seen := map[string]bool{}
	total := 0
	root := ""
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return empty, errors.New("invalid tar archive")
		}
		if entries >= 10000 {
			return empty, errors.New("review entry limit exceeded")
		}
		if !reviewSafePath(h.Name) {
			return empty, errors.New("unsafe archive path")
		}
		key := strings.ToLower(strings.TrimSuffix(h.Name, "/"))
		if seen[key] {
			return empty, errors.New("duplicate archive path")
		}
		seen[key] = true
		if h.Typeflag == tar.TypeXGlobalHeader {
			p.Excluded = append(p.Excluded, ReviewExclusion{h.Name, "archive_metadata"})
			continue
		}
		if len(scope) > 0 {
			prefix, rel, ok := strings.Cut(h.Name, "/")
			if (!ok && h.Typeflag != tar.TypeDir) || (root != "" && root != prefix) {
				return empty, errors.New("scoped archive must have one root directory")
			}
			root = prefix
			if h.Typeflag != tar.TypeDir {
				included := false
				for _, name := range scope {
					if rel == name || (strings.HasSuffix(name, "/") && strings.HasPrefix(rel, name)) {
						included = true
					}
				}
				if !included {
					p.Excluded = append(p.Excluded, ReviewExclusion{h.Name, "outside_declared_scope"})
					continue
				}
			}
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			p.Excluded = append(p.Excluded, ReviewExclusion{h.Name, "link_or_special"})
			continue
		}
		if h.Size < 0 || h.Size > 1<<20 {
			return empty, errors.New("review file limit exceeded")
		}
		data, err := reviewReadBounded(tr, 1<<20)
		if err != nil {
			return empty, err
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			p.Excluded = append(p.Excluded, ReviewExclusion{h.Name, "binary_or_invalid_utf8"})
			continue
		}
		if len(p.Files) >= 1000 || total+len(data) > 20<<20 {
			return empty, errors.New("review text limit exceeded")
		}
		total += len(data)
		p.Files = append(p.Files, ReviewFile{h.Name, reviewDigest(data), string(data)})
	}
	if len(p.Files) == 0 {
		return empty, errors.New("archive contains no text files")
	}
	return p, nil
}
