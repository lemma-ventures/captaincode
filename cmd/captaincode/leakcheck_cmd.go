package main

// `captain leakcheck` - refuse text that names a private project, paper,
// person or tool listed in ~/.config/captain/private-names (pkg
// leakcheck.go). The repository's git hooks call it on every commit, commit
// message and push; a worker calls it before committing to a public repo.
//
//	captain leakcheck --staged            the staged diff's added lines and new paths
//	captain leakcheck --message-file F    a commit message (the commit-msg hook)
//	captain leakcheck --range A..B        added lines and messages of commits in a range
//	captain leakcheck --pre-push          the pre-push hook: ref lines on stdin
//	captain leakcheck FILE...             files; "-" reads stdin
//
// Exit 3 names each hit; exit 0 is clean. With no list nothing is checked,
// and the command says so on stderr.

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdLeakcheck(args []string) {
	fs := flag.NewFlagSet("leakcheck", flag.ExitOnError)
	staged := fs.Bool("staged", false, "check the staged diff (pre-commit)")
	msgFile := fs.String("message-file", "", "check a commit message file (commit-msg)")
	rng := fs.String("range", "", "check the commits in a git range, e.g. origin/main..HEAD")
	prePush := fs.Bool("pre-push", false, "check what a push sends: the pre-push hook's ref lines on stdin")
	_ = fs.Parse(args)

	names := captaincode.PrivateNames()
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "captain leakcheck: no private-names list at %s - nothing checked\n", captaincode.PrivateNamesPath())
		return
	}
	var hits []string
	check := func(where, text string) {
		for _, l := range captaincode.FindLeaks(text, names) {
			hits = append(hits, fmt.Sprintf("%s:%d names %q: %s", where, l.Line, l.Name, truncate(l.Text, 120)))
		}
	}
	switch {
	case *staged:
		check("staged diff", addedLines(gitIn("diff", "--cached", "-U0", "--no-color")))
		check("staged paths", gitIn("diff", "--cached", "--name-only"))
	case *msgFile != "":
		b, err := os.ReadFile(*msgFile)
		if err != nil {
			fatal(err)
		}
		check("commit message", string(b))
	case *rng != "":
		checkRange(*rng, check)
	case *prePush:
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			f := strings.Fields(sc.Text()) // <local ref> <local sha> <remote ref> <remote sha>
			if len(f) < 4 || strings.Trim(f[1], "0") == "" {
				continue // a deletion sends nothing
			}
			if strings.Trim(f[3], "0") == "" {
				checkRange(f[1]+" --not --remotes", check) // a new branch: what the remote lacks
				continue
			}
			checkRange(f[3]+".."+f[1], check)
		}
	default:
		for _, path := range fs.Args() {
			var b []byte
			var err error
			if path == "-" {
				b, err = io.ReadAll(os.Stdin)
			} else {
				b, err = os.ReadFile(path)
			}
			if err != nil {
				fatal(err)
			}
			check(path, string(b))
		}
	}
	if len(hits) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "captain leakcheck: private names would leave this machine - use a neutral name (a sibling repo, project A, a paper):")
	for _, h := range hits {
		fmt.Fprintln(os.Stderr, "  "+h)
	}
	os.Exit(3)
}

// checkRange checks every commit a range adds: its message and its added
// lines and paths.
func checkRange(rng string, check func(where, text string)) {
	revs := strings.Fields(gitIn(append([]string{"rev-list"}, strings.Fields(rng)...)...))
	for _, c := range revs {
		short := c
		if len(short) > 7 {
			short = short[:7]
		}
		check(short+" message", gitIn("log", "-1", "--format=%B", c))
		check(short+" diff", addedLines(gitIn("show", "--format=", "-U0", "--no-color", c)))
		check(short+" paths", gitIn("show", "--format=", "--name-only", c))
	}
}

// addedLines keeps a diff's added lines, without the "+" and file headers.
func addedLines(diff string) string {
	var sb strings.Builder
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			sb.WriteString(l[1:] + "\n")
		}
	}
	return sb.String()
}

// gitIn runs git in the current repository and stops on failure: a check
// that cannot read what it checks must not pass.
func gitIn(args ...string) string {
	out, err := gitOut(".", args...)
	if err != nil {
		fatal(fmt.Errorf("git %s: %w", strings.Join(args, " "), err))
	}
	return out
}

// cmdPrivateNames is `captain private-names`: the same verbs as /private in
// the TUI, plus `suggest [dir]`, which runs the curation pass on a folder now.
//
//	captain private-names                   the list, and proposals waiting
//	captain private-names add|dismiss|remove NAME...
//	captain private-names suggest [DIR]     look at a folder now (default: here)
func cmdPrivateNames(args []string) {
	if len(args) > 0 && args[0] == "suggest" {
		dir, _ := os.Getwd()
		if len(args) > 1 {
			dir = args[1]
		}
		if n := (&brain{}).curatePrivateNames(dir); n != "" {
			fmt.Println(n)
			return
		}
		fmt.Println("private names: nothing new in this folder")
		return
	}
	dir, _ := os.Getwd()
	fmt.Println(privateNamesAnswer(args, dir))
}
