package main

// Decisions before the next turn (18 Nov). When the user answers an owner
// decision, its record turn ("Record the user's ...") writes the answer into
// the repository. Until it has run, the folder's next prompt and the next
// round of its /repeat loop work from the old record. On 17 Nov a loop kept
// iterating while a signed record waited: the signed queue counted the
// unfinished loop as a busy folder, so the record ran only after the loop
// ended. Now:
//
//   - both queues run a folder's pending records ahead of its other pending
//     prompts, in their own order (planQueue, inbox.take, signedQueueStep);
//   - a /repeat round does not start while a record for its repository is
//     pending in either queue or running (awaitRecords), and the signed
//     queue lets a record run while that loop waits for it;
//   - a watch holding the TUI's session ends when the record the loop waits
//     for sits queued behind it.
//
// A worktree maps to its main checkout, as decisions do (DecisionPlace).

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

var (
	// repeatRecordWait bounds how long a loop waits for a record before its
	// next round. A record turn is one small edit and a commit, usually one
	// to five minutes; a headless one first waits sqHeadlessAfter (1 min)
	// for a TUI, and its worker may queue behind busy legs. Half an hour
	// covers that with room to spare, while a record that never runs (a TUI
	// that never submits it, a hung worker) delays the loop once by a short
	// fraction of a typical 1-4 h round instead of wedging it. The loop then
	// logs it and goes on, and does not wait for that record again.
	repeatRecordWait = 30 * time.Minute
	repeatRecordPoll = 2 * time.Second
	// recordHandedGrace: a record a TUI took and that has not arrived as a
	// turn after this long sits queued behind a busy session (a watch).
	recordHandedGrace = 10 * time.Second
)

// recordRepos caches the repository a folder belongs to: DecisionPlace runs
// git, and the queues ask once a tick.
var (
	recordReposMu sync.Mutex
	recordRepos   = map[string]string{}
)

// recordRepo is the main checkout of dir's repository (a linked worktree
// maps to it), or dir itself outside git.
func recordRepo(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	recordReposMu.Lock()
	repo, ok := recordRepos[dir]
	recordReposMu.Unlock()
	if ok {
		return repo
	}
	repo, _, _ = captaincode.DecisionPlace(dir)
	if repo == "" {
		repo = dir
	}
	recordReposMu.Lock()
	if len(recordRepos) > 512 {
		recordRepos = map[string]string{}
	}
	recordRepos[dir] = repo
	recordReposMu.Unlock()
	return repo
}

func sameRepo(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b) || recordRepo(a) == recordRepo(b)
}

// recordsFirst moves the decision records to the front, each group in its
// own order.
func recordsFirst[T any](xs []T, isRecord func(T) bool) ([]T, bool) {
	out := append([]T(nil), xs...)
	sort.SliceStable(out, func(i, j int) bool { return isRecord(out[i]) && !isRecord(out[j]) })
	moved := false
	for i := range xs {
		if isRecord(xs[i]) != isRecord(out[i]) {
			moved = true
		}
	}
	return out, moved
}

// beginRecordTurn counts a record turn in flight for dir's repository, from
// the moment it reaches the brain (before routing starts a worker) until it
// ends.
func (b *brain) beginRecordTurn(dir string) func() {
	b.recMu.Lock()
	defer b.recMu.Unlock()
	if b.recordTurns == nil {
		b.recordTurns = map[int]string{}
	}
	b.recordSeq++
	id := b.recordSeq
	b.recordTurns[id] = dir
	var once sync.Once
	return func() {
		once.Do(func() {
			b.recMu.Lock()
			delete(b.recordTurns, id)
			b.recMu.Unlock()
		})
	}
}

// recordPending reports a decision record for dir's repository that is
// pending or running: in the signed queue, in the inbox, handed to a TUI and
// not yet arrived, or in flight as a turn or worker. why describes the first
// one not in skip ("" when there is none); keys names every one found.
func (b *brain) recordPending(dir string, skip map[string]bool) (why string, keys []string) {
	if dir == "" {
		return "", nil
	}
	note := func(key, desc string) {
		keys = append(keys, key)
		if why == "" && !skip[key] {
			why = desc
		}
	}
	seen := map[string]bool{}
	for _, e := range sqList("pending") {
		if isRecordTask(e.Text) && time.Since(e.At) < inboxTTL && sameRepo(e.Dir, dir) {
			seen[e.ID] = true
			note("id:"+e.ID, "signed, queued: "+promptPeek(e.Text))
		}
	}
	b.inbox.mu.Lock()
	type held struct {
		it     inboxItem
		handed bool
	}
	var items []held
	for _, list := range b.inbox.items {
		for _, it := range list {
			items = append(items, held{it, false})
		}
	}
	for _, list := range b.inbox.handed {
		for _, it := range list {
			items = append(items, held{it, true})
		}
	}
	b.inbox.mu.Unlock()
	for _, h := range items {
		if !isRecordTask(h.it.Text) || seen[h.it.ID] || !sameRepo(h.it.Dir, dir) {
			continue
		}
		if h.handed {
			// One the brain runs itself is its pending entry until it ends,
			// and one a TUI took but never submitted is not waited for
			// forever.
			if !h.it.toTUI || time.Since(h.it.handedAt) > repeatRecordWait {
				continue
			}
			note("id:"+h.it.ID, "handed to the TUI: "+promptPeek(h.it.Text))
			continue
		}
		note("id:"+h.it.ID, "in the inbox: "+promptPeek(h.it.Text))
	}
	b.recMu.Lock()
	turns := map[int]string{}
	for id, d := range b.recordTurns {
		turns[id] = d
	}
	b.recMu.Unlock()
	ids := make([]int, 0, len(turns))
	for id := range turns {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if sameRepo(turns[id], dir) {
			note(fmt.Sprintf("turn:%d", id), "running")
		}
	}
	for _, run := range b.active.list() {
		if isRecordTask(run.task) && sameRepo(run.dir, dir) {
			note(fmt.Sprintf("run:%s:%d", run.leg, run.started.UnixNano()), "running on "+string(run.leg))
		}
	}
	return why, keys
}

// handedRecordStuck reports a decision record for dir's repository that a
// TUI took more than recordHandedGrace ago and that has not arrived as a
// turn: it is queued behind that TUI's running turn.
func (ib *inbox) handedRecordStuck(dir string) bool {
	ib.mu.Lock()
	var stuck []inboxItem
	for _, list := range ib.handed {
		for _, it := range list {
			if it.toTUI && isRecordTask(it.Text) && time.Since(it.handedAt) >= recordHandedGrace {
				stuck = append(stuck, it)
			}
		}
	}
	ib.mu.Unlock()
	for _, it := range stuck {
		if sameRepo(it.Dir, dir) {
			return true
		}
	}
	return false
}

// awaitRecords holds the loop's next round while a decision record for its
// repository is pending or running, so the round reads the answer. It
// returns false when the loop was stopped or cancelled meanwhile. A record
// still pending after repeatRecordWait is logged and added to skip: the loop
// goes on and does not wait for it again.
func (b *brain) awaitRecords(ctx context.Context, th *repeatThread, dir string, skip map[string]bool) bool {
	why, _ := b.recordPending(dir, skip)
	if why == "" {
		return true
	}
	start := time.Now()
	b.rmu.Lock()
	next := th.done + 1
	b.rmu.Unlock()
	say := func(line string) {
		b.rmu.Lock()
		th.live = append(th.live, line)
		th.liveSeq++
		if len(th.live) > repeatLiveKeep {
			th.live = th.live[len(th.live)-repeatLiveKeep:]
		}
		b.rmu.Unlock()
	}
	set := func(w string) {
		b.rmu.Lock()
		th.awaitRecord = w
		b.rmu.Unlock()
	}
	set(why)
	defer set("")
	line := fmt.Sprintf("waiting for the decision record before %s %d (%s)", th.unit(), next, why)
	say(line)
	fmt.Printf("captain brain: repeat %s %s\n", th.id, line)
	b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "repeat", Model: "repeat", Text: th.id + " " + line})
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(repeatRecordPoll):
		}
		b.rmu.Lock()
		halted := th.stopped
		b.rmu.Unlock()
		if halted {
			return false
		}
		w, keys := b.recordPending(dir, skip)
		if w == "" {
			line := fmt.Sprintf("the decision record is in; %s %d starts (waited %s)", th.unit(), next, time.Since(start).Round(time.Second))
			say(line)
			fmt.Printf("captain brain: repeat %s %s\n", th.id, line)
			return true
		}
		if time.Since(start) >= repeatRecordWait {
			for _, k := range keys {
				skip[k] = true
			}
			line := fmt.Sprintf("the decision record did not finish within %s; %s %d starts without it (%s)", repeatRecordWait, th.unit(), next, w)
			say(line)
			fmt.Printf("captain brain: repeat %s %s\n", th.id, line)
			b.pushActivity(activity{Dir: dir, Kind: "route", Leg: "repeat", Model: "repeat", Text: th.id + " " + line})
			return true
		}
		if w != why {
			why = w
			set(w)
		}
	}
}
