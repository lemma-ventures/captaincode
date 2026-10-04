package main

// Curating private names from the folders captain works in (pkg
// private_curate.go). Once a day per folder, in the background: a private
// repository's own name is added to ~/.config/captain/private-names on the
// spot, and its package name and README title words are proposed. The
// folder's next turn says what happened; `/private` answers a proposal.
// CAPTAIN_PRIVATE_CURATE=0 turns it off.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// curateEvery is how often one folder is looked at again.
const curateEvery = 24 * time.Hour

// curatePrivateNames runs the pass on one folder and returns what it did,
// in the words the folder's next turn shows; empty when nothing happened.
func (b *brain) curatePrivateNames(dir string) string {
	if dir == "" || os.Getenv("CAPTAIN_PRIVATE_CURATE") == "0" {
		return ""
	}
	b.privMu.Lock()
	if b.privSeen == nil {
		b.privSeen = map[string]time.Time{}
	}
	if time.Since(b.privSeen[dir]) < curateEvery {
		b.privMu.Unlock()
		return ""
	}
	b.privSeen[dir] = time.Now()
	b.privMu.Unlock()

	var auto []captaincode.NameEntry
	var proposed []captaincode.NameCandidate
	for _, c := range captaincode.CurateCandidates(dir) {
		if c.Auto {
			auto = append(auto, captaincode.NameEntry{Name: c.Name, Repo: c.Repo, Source: c.Source, How: captaincode.NameAuto})
			continue
		}
		proposed = append(proposed, c)
	}
	var lines []string
	if len(auto) > 0 {
		if added, err := captaincode.AddPrivateNames(auto); err == nil && len(added) > 0 {
			lines = append(lines, fmt.Sprintf("private names: added %s (a private repository you work in) - commits and pushes that name it are refused · /private to review", strings.Join(added, ", ")))
		}
	}
	if len(proposed) > 0 && captaincode.SuggestNames(proposed) == nil {
		var names []string
		for _, c := range proposed {
			names = append(names, fmt.Sprintf("%s (%s)", c.Name, c.Source))
		}
		lines = append(lines, fmt.Sprintf("private names: suggested %s - `/private add <names>` keeps them private, `/private dismiss <names>` stops the suggestion, and `/private add` takes any other word too", strings.Join(names, ", ")))
	}
	if len(lines) == 0 {
		return ""
	}
	note := strings.Join(lines, "\n")
	b.pushActivity(activity{Dir: dir, Kind: "feed", Leg: "captain", Model: "private-names", Text: note})
	b.privMu.Lock()
	if b.privNotice == nil {
		b.privNotice = map[string]string{}
	}
	b.privNotice[dir] = strings.TrimSpace(b.privNotice[dir] + "\n" + note)
	b.privMu.Unlock()
	return note
}

// privateNotice hands a folder's pending curation notice to its next turn,
// once.
func (b *brain) privateNotice(dir string) string {
	b.privMu.Lock()
	defer b.privMu.Unlock()
	n := b.privNotice[dir]
	delete(b.privNotice, dir)
	if n == "" {
		return ""
	}
	return n + "\n"
}

// handlePrivateNames answers /private: the list's state and the proposals,
// or add / dismiss / remove. Nothing reaches a worker or a model.
func (b *brain) handlePrivateNames(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	f := strings.Fields(strings.TrimSpace(raw))
	if len(f) == 0 || strings.ToLower(f[0]) != "/private" {
		return false
	}
	emit, _, finish := newCompletionWriter(w, req, "captain")
	defer finish()
	emit(privateNamesAnswer(f[1:], req.ws.Dir))
	finish()
	return true
}

// privateNamesAnswer runs one /private (or `captain private-names`) command.
func privateNamesAnswer(args []string, dir string) string {
	verb := "show"
	if len(args) > 0 {
		verb, args = strings.ToLower(args[0]), args[1:]
	}
	repo := ""
	if dir != "" {
		repo = filepath.Base(dir)
	}
	switch verb {
	case "add":
		if len(args) == 0 {
			return "usage: `/private add <name> [name…]` - keeps each name out of public repositories"
		}
		var entries []captaincode.NameEntry
		recs := captaincode.NameRecords()
		for _, n := range args {
			e := captaincode.NameEntry{Name: n, Repo: repo, Source: "typed by the user", How: captaincode.NameUser}
			if r, ok := recs[strings.ToLower(n)]; ok && r.Repo != "" {
				e.Repo, e.Source = r.Repo, r.Source // an accepted suggestion keeps its project
			}
			entries = append(entries, e)
		}
		added, err := captaincode.AddPrivateNames(entries)
		if err != nil {
			return "private names: " + err.Error()
		}
		if len(added) == 0 {
			return "private names: already listed - nothing added"
		}
		return fmt.Sprintf("private names: added %s. Commits, messages and pushes that name them are refused (captain leakcheck).", strings.Join(added, ", "))
	case "dismiss":
		if _, err := captaincode.DismissPrivateNames(args); err != nil {
			return "private names: " + err.Error()
		}
		return fmt.Sprintf("private names: dismissed %s - never suggested again.", strings.Join(args, ", "))
	case "remove":
		removed, err := captaincode.RemovePrivateNames(args)
		if err != nil {
			return "private names: " + err.Error()
		}
		return fmt.Sprintf("private names: removed %s.", strings.Join(removed, ", "))
	}
	var sb strings.Builder
	names := captaincode.PrivateNames()
	fmt.Fprintf(&sb, "**Private names** - %d listed in `%s`, kept out of public repositories by `captain leakcheck` and the git hooks.\n", len(names), captaincode.PrivateNamesPath())
	if pend := captaincode.PendingSuggestions(); len(pend) > 0 {
		sb.WriteString("\nSuggested, waiting for you:\n")
		for _, e := range pend {
			fmt.Fprintf(&sb, "- `%s` - %s of %s\n", e.Name, e.Source, e.Repo)
		}
		sb.WriteString("\n`/private add <names>` keeps them private (any other word too); `/private dismiss <names>` stops a suggestion; `/private remove <names>` takes a name off the list.\n")
	} else {
		sb.WriteString("\nNo suggestion is waiting. `/private add <names>` adds any word; the dashboard lists every name per project.\n")
	}
	return sb.String()
}
