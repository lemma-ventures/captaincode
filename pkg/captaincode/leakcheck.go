package captaincode

// Private names stay private (2026-10-04). Workers recording real incidents
// wrote the names of internal projects, papers and tools into code comments,
// test fixtures, commit messages and release notes of this public
// repository. The history had to be rewritten. The list of names that must
// never reach a public repository lives OUTSIDE every repository - a list
// committed here would itself be the leak - in ~/.config/captain/private-names
// (CAPTAIN_PRIVATE_NAMES overrides the path): one name per line, # comments.
// `captain leakcheck` and the repository's git hooks refuse text that names
// one of them.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PrivateNamesPath is where the list lives.
func PrivateNamesPath() string {
	if p := strings.TrimSpace(os.Getenv("CAPTAIN_PRIVATE_NAMES")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "captain", "private-names")
}

// PrivateNames reads the list. A missing file is an empty list.
func PrivateNames() []string {
	f, err := os.Open(PrivateNamesPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Leak is one private name found in a text.
type Leak struct {
	Name string // the listed name that matched
	Line int    // 1-based line in the text
	Text string // the line, trimmed
}

// An entry that starts with "!" is an allowed phrase, not a name: it is
// masked before the names it contains are looked for. "quill" can name a
// private project while "!quill-works" keeps the company's public org name
// from tripping it. An allowed phrase protects only the names it contains,
// so "!quill-works" does not hide "jo@quill.works".
//
// FindLeaks reports every line of text that names a private name, as a whole
// word ("zeta" in "zetabytes" is not a match; in "~/src/zeta" it is). An
// all-lowercase entry matches any case; an entry with capitals matches only
// that spelling, so a project named like an ordinary word ("Beacon") does not
// flag the word ("beacon").
func FindLeaks(text string, names []string) []Leak {
	type rx struct {
		name string
		re   *regexp.Regexp
		mask *regexp.Regexp // the allowed phrases that contain this name; nil when none
	}
	var res []rx
	var allowed []string
	for _, n := range names {
		if a, ok := strings.CutPrefix(n, "!"); ok && a != "" {
			allowed = append(allowed, a)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "!") {
			continue
		}
		flags := ""
		if n == strings.ToLower(n) {
			flags = "(?i)"
		}
		res = append(res, rx{n, regexp.MustCompile(flags + `(^|[^A-Za-z0-9])` + regexp.QuoteMeta(n) + `($|[^A-Za-z0-9])`), maskFor(n, allowed)})
	}
	var out []Leak
	for i, line := range strings.Split(text, "\n") {
		for _, r := range res {
			l := line
			if r.mask != nil {
				l = r.mask.ReplaceAllString(l, " ")
			}
			if r.re.MatchString(l) {
				out = append(out, Leak{Name: r.name, Line: i + 1, Text: strings.TrimSpace(line)})
				break
			}
		}
	}
	return out
}

// maskFor matches the allowed phrases that contain name, case-insensitively;
// nil when none does.
func maskFor(name string, allowed []string) *regexp.Regexp {
	var alts []string
	for _, a := range allowed {
		if strings.Contains(strings.ToLower(a), strings.ToLower(name)) {
			alts = append(alts, regexp.QuoteMeta(a))
		}
	}
	if len(alts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)` + strings.Join(alts, "|"))
}
