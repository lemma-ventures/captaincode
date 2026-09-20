package captaincode

// What the shelf was worth (ROADMAP M3.9). Stocking a skill costs a worker
// its name and description in every startup context; the only way to know
// whether that purchase was sound is to ask the same judge that already
// grades the work.
//
// So the director's assessment carries a second, smaller verdict: for each
// skill that was on the worker's shelf, did the answer show it being used,
// and was it worth its place. That grade is recorded here beside the plain
// fact of having been stocked, and the two together are the report:
//
//	stocked  - how often selection put this skill in front of a worker
//	used     - how often the director could see it in the output
//	grade    - the mean usefulness, over the runs where it was graded
//
// A skill stocked often and never used is selection's failure, not the
// skill's, and the report says so by keeping the two counts apart. A grade
// with no uses behind it is not reported as quality at all: the director is
// judging a book it did not see opened.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxSkillUses caps the persisted usage log, like the decision and outcome
// logs it sits beside.
const maxSkillUses = 600

// SkillUse is one skill's appearance on one worker's shelf, and the
// director's verdict on it when there was one.
type SkillUse struct {
	At     time.Time `json:"at"`
	TaskID string    `json:"task_id,omitempty"`
	Skill  string    `json:"skill"`
	Leg    Leg       `json:"leg,omitempty"`
	Class  Class     `json:"class,omitempty"`
	Domain Domain    `json:"domain,omitempty"`
	// Used is the director's observation that the answer shows this skill's
	// procedure being followed. Absent a grade it stays false: unseen is not
	// unused, and the counts keep the difference.
	Used bool `json:"used,omitempty"`
	// Usefulness is 0-10, and 0 means UNGRADED, not worthless - the same
	// convention Event.Quality uses.
	Usefulness float64 `json:"usefulness,omitempty"`
	Note       string  `json:"note,omitempty"`
}

// Graded reports whether a director verdict is attached.
func (u SkillUse) Graded() bool { return u.Usefulness > 0 }

// SkillGrade is one skill's line in the director's verdict.
type SkillGrade struct {
	Skill      string  `json:"skill"`
	Used       bool    `json:"used"`
	Usefulness float64 `json:"usefulness"`
	Note       string  `json:"note,omitempty"`
}

// SkillStat is one skill's aggregate, for `captain skills report` and the
// dashboard's panel.
type SkillStat struct {
	Name          string    `json:"name"`
	Stocked       int       `json:"stocked"`
	Used          int       `json:"used"`
	Graded        int       `json:"graded"`
	AvgUsefulness float64   `json:"avg_usefulness"`
	LastAt        time.Time `json:"last_at"`
}

// UseRate is how often a stocked skill was seen being used.
func (s SkillStat) UseRate() float64 {
	if s.Stocked == 0 {
		return 0
	}
	return float64(s.Used) / float64(s.Stocked)
}

// RecordSkillUse appends one shelf entry. A record without a skill name is
// dropped: a usage row nobody can attribute is worse than no row.
func (l *Ledger) RecordSkillUse(u SkillUse) {
	if strings.TrimSpace(u.Skill) == "" {
		return
	}
	if u.At.IsZero() {
		u.At = time.Now()
	}
	if u.Usefulness < 0 {
		u.Usefulness = 0
	}
	if u.Usefulness > 10 {
		u.Usefulness = 10
	}
	l.SkillUses = append(l.SkillUses, u)
	if len(l.SkillUses) > maxSkillUses {
		l.SkillUses = l.SkillUses[len(l.SkillUses)-maxSkillUses:]
	}
}

// GradeSkills files one director verdict over a worker's whole shelf: every
// stocked skill gets a row, graded or not. The rows for skills the director
// did not mention are what make "stocked 40 times, used twice" visible;
// recording only the graded ones would report selection as perfect.
func (l *Ledger) GradeSkills(taskID string, leg Leg, class Class, domain Domain, stocked []string, grades []SkillGrade) {
	if len(stocked) == 0 {
		return
	}
	byName := map[string]SkillGrade{}
	for _, g := range grades {
		byName[strings.ToLower(strings.TrimSpace(g.Skill))] = g
	}
	now := time.Now()
	for _, name := range stocked {
		u := SkillUse{At: now, TaskID: taskID, Skill: name, Leg: leg, Class: class, Domain: domain}
		if g, ok := byName[strings.ToLower(strings.TrimSpace(name))]; ok {
			u.Used, u.Usefulness, u.Note = g.Used, g.Usefulness, truncateStr(oneLine(g.Note), 140)
			// A usefulness score with "used": false is the director grading a
			// book it did not see opened. The observation is kept, the number
			// is not - it would otherwise inflate the mean of a skill nothing
			// ever used.
			if !g.Used {
				u.Usefulness = 0
			}
		}
		l.RecordSkillUse(u)
	}
}

// SkillStats aggregates the usage log, most stocked first, ties broken by
// grade then name so the order is stable between rebuilds.
func (l *Ledger) SkillStats() []SkillStat {
	type agg struct {
		stocked, used, graded int
		sum                   float64
		last                  time.Time
	}
	by := map[string]*agg{}
	for _, u := range l.SkillUses {
		a, ok := by[u.Skill]
		if !ok {
			a = &agg{}
			by[u.Skill] = a
		}
		a.stocked++
		if u.Used {
			a.used++
		}
		if u.Graded() {
			a.graded++
			a.sum += u.Usefulness
		}
		if u.At.After(a.last) {
			a.last = u.At
		}
	}
	out := make([]SkillStat, 0, len(by))
	for name, a := range by {
		s := SkillStat{Name: name, Stocked: a.stocked, Used: a.used, Graded: a.graded, LastAt: a.last}
		if a.graded > 0 {
			s.AvgUsefulness = a.sum / float64(a.graded)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stocked != out[j].Stocked {
			return out[i].Stocked > out[j].Stocked
		}
		if out[i].AvgUsefulness != out[j].AvgUsefulness {
			return out[i].AvgUsefulness > out[j].AvgUsefulness
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SkillNotes returns the director's most recent remarks about one skill -
// the sentences behind a number, which is what tells the user whether a low
// grade is the skill's fault or the selection's.
func (l *Ledger) SkillNotes(name string, limit int) []SkillUse {
	var out []SkillUse
	for i := len(l.SkillUses) - 1; i >= 0 && len(out) < limit; i-- {
		u := l.SkillUses[i]
		if !strings.EqualFold(u.Skill, name) || u.Note == "" {
			continue
		}
		out = append(out, u)
	}
	return out
}

// FormatSkillStats renders the report for `captain skills report`.
func FormatSkillStats(rows []SkillStat) string {
	if len(rows) == 0 {
		return "no skill has been stocked yet - selection records a row per skill per task, and the director grades them on the runs it scores\n"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-24s %-8s %-8s %-8s %-8s %s\n", "SKILL", "STOCKED", "USED", "USE%", "GRADE", "LAST")
	ungraded := 0
	for _, r := range rows {
		grade := "-"
		if r.Graded > 0 {
			grade = fmt.Sprintf("%.1f", r.AvgUsefulness)
		} else {
			ungraded++
		}
		last := "-"
		if !r.LastAt.IsZero() {
			last = r.LastAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&sb, "%-24s %-8d %-8d %-8s %-8s %s\n",
			r.Name, r.Stocked, r.Used, fmt.Sprintf("%.0f%%", 100*r.UseRate()), grade, last)
	}
	sb.WriteString("\nSTOCKED is what selection put on a shelf; USED is what the director saw in the output.\n")
	sb.WriteString("A skill stocked often and used rarely is selection's failure, not the skill's.\n")
	if ungraded > 0 {
		fmt.Fprintf(&sb, "%d skill(s) carry no grade yet: only the runs the director scores are graded (CAPTAIN_ASSESS_MIN_SCORED).\n", ungraded)
	}
	return sb.String()
}
