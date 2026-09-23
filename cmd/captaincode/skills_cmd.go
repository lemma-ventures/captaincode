package main

// `captain skills` - the vetted shelf (ROADMAP M3.9, pkg skills.go).
//
//	captain skills                        what is synced: source, commit, license, scripts
//	captain skills sync [--source anthropics/skills] [--commit <sha>]
//	                    [--only a,b] [--allow-scripts a,b] [--dry-run]
//	                                      fetch a source at a NAMED COMMIT, vet, hash, lock
//	captain skills verify                 recompute every locked hash against disk
//	captain skills select "<task>"        what selection would stock for this task, and why
//	captain skills stage --dir <d> "<task>"
//	                                      stage that shelf by hand (what a worker would hold)
//	captain skills unstage --dir <d>      take a staged shelf back out of a directory
//	captain skills report [--json] [--skill <name>]
//	                                      most stocked, most used, best graded
//
// With nothing synced every one of these says so and changes nothing: no
// catalog, no directory, no listing, and a run byte-for-byte what it is
// today.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const skillsUsage = "usage: captain skills [sync|verify|select|stage|unstage|report] - see `captain skills report --help`"

func cmdSkills(args []string) {
	if len(args) == 0 {
		fmt.Print(captaincode.FormatCatalog(captaincode.Catalog()))
		skillsLockLine()
		return
	}
	switch args[0] {
	case "sync":
		skillsSync(args[1:])
	case "verify":
		skillsVerify()
	case "select":
		skillsSelect(args[1:])
	case "stage":
		skillsStage(args[1:])
	case "unstage":
		skillsUnstage(args[1:])
	case "report":
		skillsReport(args[1:])
	case "list":
		fmt.Print(captaincode.FormatCatalog(captaincode.Catalog()))
		skillsLockLine()
	default:
		fatal(fmt.Errorf("%s", skillsUsage))
	}
}

// skillsLockLine names what the catalog is pinned to. A shelf whose
// provenance is not on the screen is a shelf nobody can audit.
func skillsLockLine() {
	l, err := captaincode.ReadSkillLock()
	if err != nil || len(l.Sources) == 0 {
		return
	}
	for _, s := range l.Sources {
		fmt.Printf("pinned: %s @ %s (%s, synced %s)\n", s.Repo, shortSHA(s.Commit), s.License, s.At.Format("2006-01-02"))
	}
	fmt.Printf("lock: %s\n", captaincode.SkillsLockPath())
}

func shortSHA(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func skillsSync(args []string) {
	fs := flag.NewFlagSet("skills sync", flag.ExitOnError)
	source := fs.String("source", "anthropics/skills", "catalog to sync ("+strings.Join(skillSourceNames(), " | ")+")")
	commit := fs.String("commit", "", "the commit to pin; default is the source's current head, recorded in the lock")
	only := fs.String("only", "", "comma-separated skill names to sync (default: every skill that passes vetting)")
	scripts := fs.String("allow-scripts", "", "comma-separated skills whose scripts/ directory ships too - arbitrary code, allowlisted by name")
	dry := fs.Bool("dry-run", false, "fetch and vet, write nothing")
	_ = fs.Parse(args)

	src, ok := captaincode.FindSkillSource(*source)
	if !ok {
		fatal(fmt.Errorf("unknown source %q - captain reads first-party catalogs only (%s); community directories are not a supply captain can stand behind", *source, strings.Join(skillSourceNames(), ", ")))
	}
	if *commit != "" {
		src.Commit = *commit
	}
	fmt.Printf("fetching %s at %s…\n", src.Repo, orHead(src.Commit))
	res, err := captaincode.SyncSkills(context.Background(), captaincode.SyncOptions{
		Source:       src,
		Only:         splitList(*only),
		AllowScripts: splitList(*scripts),
		DryRun:       *dry,
	})
	if err != nil {
		fatal(err)
	}
	fmt.Print(captaincode.FormatSyncResult(res, *dry))
	if !*dry {
		skillsLockLine()
	}
}

// skillSourceNames lists the catalogs `--source` accepts, from the one table
// that defines them, so the help and the refusal cannot drift from it.
func skillSourceNames() []string {
	var out []string
	for _, s := range captaincode.SkillSources() {
		out = append(out, s.Repo)
	}
	return out
}

func orHead(c string) string {
	if c == "" {
		return "its current head"
	}
	return c
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func skillsVerify() {
	cat := captaincode.Catalog()
	if len(cat) == 0 {
		fmt.Println("nothing synced - nothing to verify")
		return
	}
	drift := captaincode.VerifySkills()
	if len(drift) == 0 {
		fmt.Printf("%d skill(s) match the lock\n", len(cat))
		return
	}
	fmt.Printf("%d file(s) no longer match the lock:\n", len(drift))
	for _, d := range drift {
		fmt.Printf("  ✗ %-20s %-40s %s\n", d.Skill, d.File, d.Reason)
	}
	fmt.Println("a shelf captain cannot attest to is worse than none - re-run `captain skills sync`")
	os.Exit(3)
}

func skillsSelect(args []string) {
	task := strings.TrimSpace(strings.Join(args, " "))
	if task == "" {
		fatal(fmt.Errorf(`usage: captain skills select "<task>"`))
	}
	tr := captaincode.TriageTask(task)
	picks := captaincode.SelectSkills(captaincode.Catalog(), task, tr.Class, tr.Domain, captaincode.SkillCap())
	fmt.Printf("task class=%s domain=%s\n", tr.Class, tr.Domain)
	if len(picks) == 0 {
		fmt.Println("nothing would be stocked: no synced skill's description matches this task")
		return
	}
	for _, p := range picks {
		if p.Always {
			fmt.Printf("  %-24s always  %s\n", p.Skill.Name, p.Why)
			continue
		}
		fmt.Printf("  %-24s %.2f  matched: %s\n", p.Skill.Name, p.Score, p.Why)
	}
	fmt.Printf("%d of %d synced skill(s) would be stocked (cap %d)\n", len(picks), len(captaincode.Catalog()), captaincode.SkillCap())
}

func skillsStage(args []string) {
	fs := flag.NewFlagSet("skills stage", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to stage the shelf into (default: this one)")
	_ = fs.Parse(args)
	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		fatal(fmt.Errorf(`usage: captain skills stage [--dir d] "<task>"`))
	}
	d := *dir
	if d == "" {
		d, _ = os.Getwd()
	}
	tr := captaincode.TriageTask(task)
	picks := captaincode.SelectSkills(captaincode.Catalog(), task, tr.Class, tr.Domain, captaincode.SkillCap())
	sh, err := captaincode.StageSkills(d, picks)
	if err != nil {
		fatal(err)
	}
	if sh == nil {
		fmt.Println("nothing stocked - no synced skill matched this task")
		return
	}
	fmt.Printf("staged %s in %s\n", strings.Join(sh.Names(), ", "), d)
	fmt.Printf("  %s\n  %s (symlinks into the tree above)\n",
		filepath.Join(d, ".agents", "skills"), filepath.Join(d, ".claude", "skills"))
	fmt.Println("`captain skills unstage --dir " + d + "` takes it back out")
}

// skillsUnstage removes a hand-staged shelf. A brain-staged one goes with
// the turn that made it; this is for the `stage` above, and for residue a
// crash left behind.
func skillsUnstage(args []string) {
	fs := flag.NewFlagSet("skills unstage", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to clear (default: this one)")
	_ = fs.Parse(args)
	d := *dir
	if d == "" {
		d, _ = os.Getwd()
	}
	n := captaincode.UnstageSkills(d)
	fmt.Printf("removed %d staged skill(s) from %s\n", n, d)
	// What stayed under a catalog name is not captain's to judge: the user's
	// own copy, or the residue of a brain from before the marker existed.
	for _, s := range captaincode.Catalog() {
		for _, rel := range []string{filepath.Join(".agents", "skills", s.Name), filepath.Join(".claude", "skills", s.Name)} {
			if _, err := os.Lstat(filepath.Join(d, rel)); err == nil {
				fmt.Printf("  left %s: captain did not stage it - remove it by hand only if it is not yours\n", rel)
			}
		}
	}
}

func skillsReport(args []string) {
	fs := flag.NewFlagSet("skills report", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "the aggregate as JSON")
	skill := fs.String("skill", "", "the director's recent notes on one skill")
	_ = fs.Parse(args)

	rows := skillStatsFromBrain()
	if *skill != "" {
		skillNotes(*skill)
		return
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(raw))
		return
	}
	fmt.Print(captaincode.FormatSkillStats(rows))
}

// skillStatsFromBrain prefers the running brain's ledger (it holds the
// unsaved tail of this session) and falls back to the file, so the report
// works with the brain down.
func skillStatsFromBrain() []captaincode.SkillStat {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://127.0.0.1:14097/v1/stats")
	if err == nil {
		defer resp.Body.Close()
		var out struct {
			Skills []captaincode.SkillStat `json:"skills"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) == nil {
			return out.Skills
		}
	}
	l, lerr := captaincode.LoadLedger()
	if lerr != nil {
		fatal(lerr)
	}
	return l.SkillStats()
}

func skillNotes(name string) {
	l, err := captaincode.LoadLedger()
	if err != nil {
		fatal(err)
	}
	notes := l.SkillNotes(name, 12)
	if len(notes) == 0 {
		fmt.Printf("no director note mentions %s yet\n", name)
		return
	}
	for _, u := range notes {
		used := "unused"
		if u.Used {
			used = fmt.Sprintf("used, %.0f/10", u.Usefulness)
		}
		fmt.Printf("%s  %-10s %-8s %s\n", u.At.Format("2006-01-02 15:04"), u.Leg, used, u.Note)
	}
}
