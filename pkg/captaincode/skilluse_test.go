package captaincode

import (
	"strings"
	"testing"
)

func TestGradeSkillsRecordsEveryStockedSkill(t *testing.T) {
	l := &Ledger{}
	l.GradeSkills("t1", LegClaude, ClassMedium, DomainCode,
		[]string{"pdf", "xlsx", "gardening"},
		[]SkillGrade{{Skill: "pdf", Used: true, Usefulness: 9, Note: "followed the merge procedure"}})
	if len(l.SkillUses) != 3 {
		t.Fatalf("want a row per stocked skill, got %d", len(l.SkillUses))
	}
	stats := l.SkillStats()
	byName := map[string]SkillStat{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	if got := byName["pdf"]; got.Used != 1 || got.Graded != 1 || got.AvgUsefulness != 9 {
		t.Errorf("pdf = %+v", got)
	}
	if got := byName["xlsx"]; got.Stocked != 1 || got.Used != 0 || got.Graded != 0 {
		t.Errorf("an unmentioned skill must count as stocked-and-unused: %+v", got)
	}
}

func TestGradeSkillsDropsAScoreWithoutAUse(t *testing.T) {
	l := &Ledger{}
	// A usefulness score on a skill the director could not see being used is
	// a grade of a book it did not see opened: the observation is kept, the
	// number is not.
	l.GradeSkills("t1", LegCodex, ClassHigh, DomainCode, []string{"pdf"},
		[]SkillGrade{{Skill: "pdf", Used: false, Usefulness: 8}})
	s := l.SkillStats()[0]
	if s.Graded != 0 || s.AvgUsefulness != 0 {
		t.Fatalf("an ungraded row must not carry a score: %+v", s)
	}
	if s.Stocked != 1 {
		t.Errorf("the stocking itself is still a fact: %+v", s)
	}
}

func TestSkillStatsSeparatesStockedFromUsed(t *testing.T) {
	l := &Ledger{}
	for i := 0; i < 10; i++ {
		l.GradeSkills("t", LegGrok, ClassMedium, DomainCode, []string{"pdf"}, nil)
	}
	l.GradeSkills("t", LegGrok, ClassMedium, DomainCode, []string{"pdf"},
		[]SkillGrade{{Skill: "pdf", Used: true, Usefulness: 7}})
	s := l.SkillStats()[0]
	if s.Stocked != 11 || s.Used != 1 {
		t.Fatalf("stocked/used = %d/%d", s.Stocked, s.Used)
	}
	if rate := s.UseRate(); rate > 0.1 {
		t.Errorf("use rate = %.2f; stocked-often-used-rarely must be visible as such", rate)
	}
	out := FormatSkillStats(l.SkillStats())
	if !strings.Contains(out, "selection's failure") {
		t.Error("the report must say whose failure a low use rate is")
	}
}

func TestSkillUsesAreCapped(t *testing.T) {
	l := &Ledger{}
	for i := 0; i < maxSkillUses+50; i++ {
		l.RecordSkillUse(SkillUse{Skill: "pdf"})
	}
	if len(l.SkillUses) != maxSkillUses {
		t.Fatalf("log = %d rows, cap is %d", len(l.SkillUses), maxSkillUses)
	}
}

func TestKeepStockedGradesRejectsInventedSkills(t *testing.T) {
	refs := []SkillRef{{Name: "pdf", Description: "merge documents"}}
	got := keepStockedGrades([]SkillGrade{
		{Skill: "PDF", Used: true, Usefulness: 12},
		{Skill: "pdf", Used: true, Usefulness: 5},
		{Skill: "imagined", Used: true, Usefulness: 10},
	}, refs)
	if len(got) != 1 {
		t.Fatalf("want one grade for the one stocked skill, got %+v", got)
	}
	if got[0].Skill != "pdf" || got[0].Usefulness != 10 {
		t.Errorf("the first grade wins, clamped to the range: %+v", got[0])
	}
}

func TestSkillBlockIsAbsentWithoutAShelf(t *testing.T) {
	if b := skillBlock(nil); b != "" {
		t.Errorf("no shelf must mean no added prompt, got %q", b)
	}
	if f := skillJSONField(nil); f != "" {
		t.Errorf("no shelf must mean no added JSON field, got %q", f)
	}
	b := skillBlock([]SkillRef{{Name: "pdf", Description: "merge documents"}})
	if !strings.Contains(b, "pdf") || !strings.Contains(b, "usefulness") {
		t.Errorf("the question must name the skill and what is being asked: %q", b)
	}
}

func TestSkillNotesReturnsTheDirectorsWords(t *testing.T) {
	l := &Ledger{}
	l.GradeSkills("t1", LegClaude, ClassMedium, DomainCode, []string{"pdf"},
		[]SkillGrade{{Skill: "pdf", Used: true, Usefulness: 9, Note: "used the flatten step verbatim"}})
	notes := l.SkillNotes("pdf", 5)
	if len(notes) != 1 || !strings.Contains(notes[0].Note, "flatten") {
		t.Fatalf("notes = %+v", notes)
	}
	if len(l.SkillNotes("xlsx", 5)) != 0 {
		t.Error("notes must be per skill")
	}
}
