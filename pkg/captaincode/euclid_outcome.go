package captaincode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// TaskExposure is one task's assigned lesson arms and the guardrails that
// were rendered into its prompt.
type TaskExposure struct {
	BrainRoot      string          `json:"brain_root,omitempty"`
	BrainKind      string          `json:"brain_kind,omitempty"`
	BrainLabel     string          `json:"brain_label,omitempty"`
	Dir            string          `json:"dir,omitempty"`
	SourceRevision string          `json:"source_revision,omitempty"`
	Entries        []ExposureEntry `json:"entries,omitempty"`
	Guardrails     []string        `json:"guardrails,omitempty"`
	At             time.Time       `json:"at,omitempty"`
}

// OutcomeNotice is one settled outcome ready to send after the ledger lock
// is released.
type OutcomeNotice struct {
	Brain EuclidBrain
	Dir   string
	Event memoryEvent
}

var euclidID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func euclidTaskID(taskID string) string {
	if euclidID.MatchString(taskID) {
		return taskID
	}
	sum := sha256.Sum256([]byte(taskID))
	return "captain:" + hex.EncodeToString(sum[:16])
}

func outcomeEventID(taskID, status string, at time.Time) string {
	id := "captain:outcome:" + taskID + ":" + status + ":" + at.UTC().Format(time.RFC3339)
	if euclidID.MatchString(id) {
		return id
	}
	return "captain:outcome:" + euclidTaskID(taskID) + ":" + status + ":" + at.UTC().Format(time.RFC3339)
}

func boolPtr(v bool) *bool { return &v }

// MergeMemoryExposure unions one render into the task's record. The arm of a
// lesson never changes. A later render may mark a previously dropped lesson
// as rendered.
func (l *Ledger) MergeMemoryExposure(taskID string, rec TaskExposure) {
	if l == nil || taskID == "" {
		return
	}
	rec.Entries = append([]ExposureEntry(nil), rec.Entries...)
	rec.Guardrails = append([]string(nil), rec.Guardrails...)
	if l.MemoryExposure == nil {
		l.MemoryExposure = map[string]TaskExposure{}
	}
	old, ok := l.MemoryExposure[taskID]
	if !ok {
		if rec.At.IsZero() {
			rec.At = time.Now()
		}
		l.MemoryExposure[taskID] = rec
		l.trimExposure()
		return
	}
	if old.BrainRoot == "" {
		old.BrainRoot, old.BrainKind, old.BrainLabel = rec.BrainRoot, rec.BrainKind, rec.BrainLabel
	}
	if old.Dir == "" {
		old.Dir = rec.Dir
	}
	if old.SourceRevision == "" {
		old.SourceRevision = rec.SourceRevision
	}
	byID := map[string]ExposureEntry{}
	var order []string
	for _, entry := range old.Entries {
		byID[entry.Lesson] = entry
		order = append(order, entry.Lesson)
	}
	for _, entry := range rec.Entries {
		prev, exists := byID[entry.Lesson]
		if !exists {
			byID[entry.Lesson] = entry
			order = append(order, entry.Lesson)
			continue
		}
		if prev.Rendered != nil && entry.Rendered != nil && !*prev.Rendered && *entry.Rendered {
			prev.Rendered = entry.Rendered
			byID[entry.Lesson] = prev
		}
	}
	old.Entries = nil
	for _, id := range order {
		old.Entries = append(old.Entries, byID[id])
	}
	seen := map[string]bool{}
	var guards []string
	for _, id := range append(append([]string{}, old.Guardrails...), rec.Guardrails...) {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		guards = append(guards, id)
	}
	old.Guardrails = guards
	l.MemoryExposure[taskID] = old
	l.trimExposure()
}

func (l *Ledger) trimExposure() {
	if len(l.MemoryExposure) <= maxLifecycle {
		return
	}
	type row struct {
		id string
		at time.Time
	}
	rows := make([]row, 0, len(l.MemoryExposure))
	for id, rec := range l.MemoryExposure {
		rows = append(rows, row{id, rec.At})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].at.Equal(rows[j].at) {
			return rows[i].id < rows[j].id
		}
		return rows[i].at.Before(rows[j].at)
	})
	for _, row := range rows[:len(rows)-maxLifecycle] {
		delete(l.MemoryExposure, row.id)
	}
}

// DroppedShown counts shown lessons whose item did not fit the prompt.
func (l *Ledger) DroppedShown() int {
	if l == nil {
		return 0
	}
	n := 0
	for _, rec := range l.MemoryExposure {
		for _, entry := range rec.Entries {
			if entry.Arm == "shown" && entry.Rendered != nil && !*entry.Rendered {
				n++
			}
		}
	}
	return n
}

func (l *Ledger) queueOutcome(o *OutcomeEvidence) {
	if l == nil || o == nil || o.TaskID == "" {
		return
	}
	status := string(o.Status)
	if status != string(AcceptanceAccepted) && status != string(AcceptanceRejected) && status != string(AcceptanceRegressed) {
		return
	}
	outcome := status
	if o.Status == AcceptanceRegressed {
		outcome = string(AcceptanceRejected)
	}
	at := o.SettledAt
	if at.IsZero() {
		at = o.UpdatedAt
	}
	exp := l.MemoryExposure[o.TaskID]
	var evidence []string
	exposure := append([]ExposureEntry(nil), exp.Entries...)
	for _, entry := range exposure {
		if entry.Arm == "shown" {
			evidence = append(evidence, entry.Lesson)
		}
	}
	evidence = append(evidence, exp.Guardrails...)
	rev := exp.SourceRevision
	if rev == "" {
		rev = "unavailable"
	}
	dir := o.Dir
	if dir == "" {
		dir = exp.Dir
	}
	l.outcomeQueue = append(l.outcomeQueue, OutcomeNotice{
		Dir: dir,
		Brain: EuclidBrain{
			Root: exp.BrainRoot, Kind: exp.BrainKind, Label: exp.BrainLabel, Writable: exp.BrainRoot != "",
		},
		Event: memoryEvent{
			ID:             outcomeEventID(o.TaskID, status, at),
			Kind:           "task_outcome",
			TaskID:         euclidTaskID(o.TaskID),
			SourceRevision: rev,
			Text:           fmt.Sprintf("task %s, decided by %s", status, o.DecidedBy),
			Outcome:        outcome,
			Evidence:       evidence,
			Exposure:       exposure,
		},
	})
}

// TakeOutcomeNotices returns the settled outcomes waiting to be sent, and
// clears the queue. The caller sends them without holding the ledger lock.
func (l *Ledger) TakeOutcomeNotices() []OutcomeNotice {
	if l == nil || len(l.outcomeQueue) == 0 {
		return nil
	}
	out := l.outcomeQueue
	l.outcomeQueue = nil
	return out
}

// PublishOutcomeNotices sends settled outcomes to the write brain. A
// transport failure stays on the P1 resend queue inside recordMemoryEvent.
func PublishOutcomeNotices(notices []OutcomeNotice) {
	if len(notices) == 0 || !memoryMCPEnabled() {
		return
	}
	for _, notice := range notices {
		brain := notice.Brain
		if brain.Root == "" && notice.Dir != "" {
			if wb, ok := WriteBrain(notice.Dir); ok {
				brain = wb
			}
		}
		if brain.Root == "" {
			continue
		}
		brain.Writable = true
		_, _ = recordMemoryEvent(brain, notice.Event)
	}
}
