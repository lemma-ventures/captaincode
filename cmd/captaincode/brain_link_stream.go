package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// The brain's half of the outbound stream (roadmap row F6, step 3). The
// supervisor lives in the package; this file feeds it from local state: the
// pairing in link.json, the machine identity the presence endpoint already
// reports, and one snapshot per linked folder. It reads nothing from the hub
// before it connects, and it prints a line only when something noteworthy
// changes, so an idle machine is silent.

// linkBrainVersion is what a snapshot calls this brain: the revision the
// binary was built from and whether its tree was clean, in the hub's own
// label profile. A build without a revision is named as such instead of
// inventing a version. It carries no path and no build host.
func linkBrainVersion() string {
	rev, modified, ok := captaincode.GoBuildStamp()
	if captaincode.BuildRevision != "" {
		rev, ok = captaincode.BuildRevision, true
	}
	if !ok || rev == "" {
		return "captain unknown"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified {
		return "captain " + rev + " dirty"
	}
	return "captain " + rev
}

// linkNotices keeps the operator's stream notes bounded: each distinct line
// is printed once, and a machine collects at most a few of them, so a
// folder that never gets a label cannot print a line on every beat.
var linkNotices = struct {
	sync.Mutex
	seen map[string]bool
}{seen: map[string]bool{}}

func linkNotice(msg string) {
	linkNotices.Lock()
	defer linkNotices.Unlock()
	if linkNotices.seen[msg] || len(linkNotices.seen) >= 16 {
		return
	}
	linkNotices.seen[msg] = true
	fmt.Println("captain link: " + msg)
}

// linkPresenceFolders is one beat's snapshot: every linked folder under the
// label the presence configuration gives it, with the live TUI state of that
// folder. Two filters keep a path out of the wire: a folder the presence
// configuration does not name has no label to send, and a label outside the
// hub's profile (a name of sixty characters is legal here and refused there)
// never leaves the machine. Sorted by label, and cut at the hub's ceiling,
// so the hub always sees the same deterministic set.
func (b *brain) linkPresenceFolders() ([]captaincode.LinkPresenceFolder, error) {
	linked, err := captaincode.LinkFolders()
	if err != nil {
		return nil, err
	}
	if len(linked) == 0 {
		return nil, nil
	}
	conf, err := pres.DefaultStore().Read()
	if err != nil {
		return nil, err
	}
	labelOf := make(map[string]string, len(conf.Folders))
	for label, dir := range conf.Folders {
		labelOf[filepath.Clean(dir)] = label
	}
	out := make([]captaincode.LinkPresenceFolder, 0, len(linked))
	unlabelled, oversized := 0, 0
	for _, folder := range linked {
		label, ok := labelOf[filepath.Clean(folder.Root)]
		if !ok {
			unlabelled++
			continue
		}
		if !captaincode.LinkPresenceLabel(label) {
			oversized++
			continue
		}
		out = append(out, captaincode.LinkPresenceFolder{
			Label:   label,
			TUIOpen: b.inbox.sqTUIOpen(folder.Root),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	if unlabelled > 0 {
		linkNotice(fmt.Sprintf("%d linked folder(s) have no presence label and are not reported", unlabelled))
	}
	if oversized > 0 {
		linkNotice(fmt.Sprintf("%d folder label(s) exceed the hub's 80 characters and are not reported", oversized))
	}
	if len(out) > 200 {
		linkNotice(fmt.Sprintf("the hub takes 200 folders; %d linked folder(s) are not reported", len(out)-200))
		out = out[:200]
	}
	return out, nil
}

// linkTruncate cuts a text to the hub's bound and marks the cut, so a long
// register entry still travels instead of being refused whole.
func linkTruncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len("…")
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + "…"
}

// linkDecisionCards maps the decision register to cards the hub takes: the
// register's text is already masked where it was written, no path travels,
// and a decision from a folder that never opted in stays on this machine.
// The newest decisions go first and the hub's per-machine ceiling bounds the
// set; a card that would still be refused is skipped, not sent broken.
func (b *brain) linkDecisionCards() ([]captaincode.LinkDecisionCard, error) {
	linked, err := captaincode.LinkFolders()
	if err != nil {
		return nil, err
	}
	roots := make(map[string]bool, len(linked))
	for _, f := range linked {
		roots[f.Root] = true
	}
	all, err := captaincode.ListDecisions("")
	if err != nil {
		return nil, err
	}
	out := make([]captaincode.LinkDecisionCard, 0, 8)
	for i := len(all) - 1; i >= 0 && len(out) < 64; i-- {
		d := all[i]
		repo, checkout := captaincode.DecisionHome(d)
		if !roots[repo] && (checkout == "" || !roots[checkout]) {
			continue
		}
		card := captaincode.LinkDecisionCard{
			DecisionID: d.ID,
			Title:      linkTruncate(d.Title, 200),
			Question:   linkTruncate(d.Question, 4000),
			Context:    linkTruncate(d.Context, 4000),
			Recommend:  d.Recommend,
			Why:        linkTruncate(d.Why, 500),
			PostedAt:   d.PostedAt.Unix(),
			Status:     d.Status,
		}
		if d.Record != "" && !strings.HasPrefix(d.Record, "/") && !strings.Contains(d.Record, "..") && len(d.Record) <= 256 {
			card.Record = d.Record
		}
		if d.Status == captaincode.DecisionAnswered && d.Answer != nil {
			card.Answer = d.Answer.Option
		}
		kept := 0
		for _, o := range d.Options {
			if kept >= 6 {
				break
			}
			card.Options = append(card.Options, captaincode.LinkDecisionOption{
				ID:     o.ID,
				Label:  linkTruncate(o.Label, 200),
				Detail: linkTruncate(o.Detail, 1000),
			})
			kept++
		}
		// A cut option set can drop the recommended or chosen option;
		// the card must stay valid, so those references go with it.
		holds := func(id string) bool {
			for _, o := range card.Options {
				if o.ID == id {
					return true
				}
			}
			return false
		}
		if card.Recommend != "" && !holds(card.Recommend) {
			card.Recommend = ""
		}
		if card.Answer != "" && !holds(card.Answer) {
			card.Answer = ""
			card.Status = captaincode.DecisionOpen
		}
		if captaincode.LinkDecisionValid(card) != nil {
			continue
		}
		out = append(out, card)
	}
	return out, nil
}

// linkStreamReady reads everything the stream needs from local state, and
// says in words why nothing is reported when that is the case. It never
// contacts the hub: pending pairings, an unreadable key and an empty folder
// list all become an inactive state the supervisor idles on.
func (b *brain) linkStreamReady() (captaincode.LinkStreamReady, error) {
	idle := func(why string) captaincode.LinkStreamReady {
		return captaincode.LinkStreamReady{Active: false, Why: why}
	}
	hub, has, err := captaincode.LinkHubState()
	if err != nil {
		return captaincode.LinkStreamReady{}, err
	}
	if !has {
		return idle("no hub is paired with this machine"), nil
	}
	if hub.ApprovedAt == 0 {
		return idle("the pairing with the hub waits for its approval on the web"), nil
	}
	machineID, err := captaincode.LinkMachineID()
	if err != nil {
		return captaincode.LinkStreamReady{}, fmt.Errorf("the machine identity is not configured: %w", err)
	}
	key, err := captaincode.LoadLinkKey()
	if err != nil {
		return captaincode.LinkStreamReady{}, fmt.Errorf("the link key is unreadable: %w", err)
	}
	folders, err := b.linkPresenceFolders()
	if err != nil {
		return captaincode.LinkStreamReady{}, err
	}
	if len(folders) == 0 {
		return idle("no linked folder can be reported to the hub yet"), nil
	}
	return captaincode.LinkStreamReady{
		Active: true,
		Why:    "",
		Options: captaincode.LinkStreamOptions{
			Hub:          hub,
			MachineID:    machineID,
			Key:          key,
			BrainVersion: linkBrainVersion(),
			Folders:      b.linkPresenceFolders,
			Cards:        b.linkDecisionCards,
		},
	}, nil
}

// linkStreamRun keeps the presence connection until the context ends. It
// returns when the supervisor has fully stopped, so a caller that swaps
// standard output (a test) can wait for the last line before it restores it.
func (b *brain) linkStreamRun(ctx context.Context) {
	_, _ = captaincode.LinkStreamSupervisor(ctx, b.linkStreamReady, captaincode.LinkStreamPolicy{}, func(msg string) {
		fmt.Println("captain link: " + msg)
	})
}

// linkStreamSupervisor keeps the presence connection while the brain runs.
// It stops with the brain's own life context, dials out only, and is silent
// while nothing is paired.
func (b *brain) linkStreamSupervisor(done <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-done
		cancel()
	}()
	b.linkStreamRun(ctx)
}
