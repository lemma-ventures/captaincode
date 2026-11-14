package main

// `captain decision` - the register of decisions waiting on the user (pkg
// decisions.go). A worker posts what only the user can settle; a cockpit
// lists it, and the user answers by signing the record prompt.
//
//	captain decision post --title T --question Q --option a="Label" --option b="Label :: detail" \
//	    [--recommend a --why W] [--ref D10] [--context C] [--record docs/decisions.md] \
//	    [--github owner/repo#12] [--needed-before "M1 run"] [--cwd DIR] [--log NAME]
//	captain decision list [--all] [--json]
//	captain decision show ID [--json]
//	captain decision prompt ID --option a [--note N] [--json]    the text the user signs
//	captain decision bundle < [{"id":ID,"option":"a","note":N},...]   one prompt for several, same repository
//	captain decision answered ID --option a [--note N] [--via V]
//	captain decision withdraw ID --reason R
//
// post, answered and withdraw each append an entry to .captaincode/decisions.md
// in the checkout the question was posted from (decisions_log.go); the worker
// commits it with its work, and the record worker with the answer.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

type decisionOptions []captaincode.DecisionOption

func (o *decisionOptions) String() string { return fmt.Sprint(len(*o)) }
func (o *decisionOptions) Set(v string) error {
	id, rest, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("option %q: want id=\"Label\" or id=\"Label :: detail\"", v)
	}
	label, detail, _ := strings.Cut(rest, "::")
	*o = append(*o, captaincode.DecisionOption{ID: strings.TrimSpace(id), Label: strings.TrimSpace(label), Detail: strings.TrimSpace(detail)})
	return nil
}

func cmdDecision(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: captain decision post|list|show|prompt|answered|withdraw ... (see `captain decision post -h`)")
		os.Exit(2)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "post":
		decisionPost(args)
	case "list":
		fs := flag.NewFlagSet("decision list", flag.ExitOnError)
		all := fs.Bool("all", false, "include answered and withdrawn decisions")
		asJSON := fs.Bool("json", false, "print JSON")
		_ = fs.Parse(args)
		status := captaincode.DecisionOpen
		if *all {
			status = ""
		}
		list, err := captaincode.ListDecisions(status)
		if err != nil {
			fatal(err)
		}
		if *asJSON {
			if list == nil {
				list = []captaincode.OwnerDecision{}
			}
			printJSON(list)
			return
		}
		if len(list) == 0 {
			fmt.Println("no decisions waiting")
		}
		for _, d := range list {
			ref := d.Ref
			if ref == "" {
				ref = "-"
			}
			fmt.Printf("%s  %-8s %-10s %s  (%s, %s ago)\n", d.ID, ref, d.Status, d.Title, filepath.Base(d.Repo), time.Since(d.PostedAt).Round(time.Minute))
		}
	case "show":
		fs := flag.NewFlagSet("decision show", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		id := decisionID(fs, args)
		d, err := captaincode.GetDecision(id)
		if err != nil {
			fatal(err)
		}
		if *asJSON {
			printJSON(d)
			return
		}
		fmt.Printf("%s %s [%s] %s\nrepo: %s\nquestion: %s\n", d.ID, d.Ref, d.Status, d.Title, d.Repo, d.Question)
		if d.Context != "" {
			fmt.Printf("context: %s\n", d.Context)
		}
		for _, o := range d.Options {
			mark := " "
			if o.ID == d.Recommend {
				mark = "*"
			}
			fmt.Printf(" %s %s) %s", mark, o.ID, o.Label)
			if o.Detail != "" {
				fmt.Printf(" - %s", o.Detail)
			}
			fmt.Println()
		}
		if d.Why != "" {
			fmt.Printf("recommended (%s): %s\n", d.Recommend, d.Why)
		}
		if d.PostedBy.Log != "" {
			fmt.Printf("log: %s\n", filepath.Join(workerLogsDir(), d.PostedBy.Log))
		}
	case "prompt":
		fs := flag.NewFlagSet("decision prompt", flag.ExitOnError)
		option := fs.String("option", "", "the chosen option id")
		note := fs.String("note", "", "the user's note")
		asJSON := fs.Bool("json", false, "print JSON with the folder, text and its SHA-256")
		id := decisionID(fs, args)
		d, err := captaincode.GetDecision(id)
		if err != nil {
			fatal(err)
		}
		if d.Status != captaincode.DecisionOpen {
			fatal(fmt.Errorf("decision %s is %s", d.ID, d.Status))
		}
		text, err := captaincode.DecisionRecordPrompt(d, *option, *note)
		if err != nil {
			fatal(err)
		}
		if *asJSON {
			printJSON(map[string]string{"id": d.ID, "repo": d.Repo, "text": text, "text_sha256": captaincode.SHA256Hex(text)})
			return
		}
		fmt.Println(text)
	case "bundle":
		var choices []captaincode.DecisionChoice
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&choices); err != nil {
			fatal(fmt.Errorf("decision bundle: read a JSON list of {id, option, note} on stdin: %w", err))
		}
		text, err := captaincode.DecisionBundlePrompt(choices)
		if err != nil {
			fatal(err)
		}
		first, _ := captaincode.GetDecision(choices[0].ID)
		printJSON(map[string]string{"repo": first.Repo, "text": text, "text_sha256": captaincode.SHA256Hex(text)})
	case "answered":
		fs := flag.NewFlagSet("decision answered", flag.ExitOnError)
		option := fs.String("option", "", "the chosen option id")
		note := fs.String("note", "", "the user's note")
		via := fs.String("via", "", "where the answer came from (galaxy, tui, record worker)")
		sha := fs.String("prompt-sha256", "", "SHA-256 of the signed record prompt")
		id := decisionID(fs, args)
		before, err := captaincode.GetDecision(id)
		if err != nil {
			fatal(err)
		}
		d, err := captaincode.AnswerDecision(id, *option, *note, *sha, *via)
		if err != nil {
			fatal(err)
		}
		logged := ""
		if before.Status == captaincode.DecisionOpen {
			logged = loggedIn(logDecision(d, captaincode.DecisionAnswered))
		}
		fmt.Printf("%s answered: option %s%s\n", d.ID, d.Answer.Option, logged)
	case "withdraw":
		fs := flag.NewFlagSet("decision withdraw", flag.ExitOnError)
		reason := fs.String("reason", "", "why the question no longer needs the user")
		id := decisionID(fs, args)
		d, err := captaincode.WithdrawDecision(id, *reason)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%s withdrawn%s\n", id, loggedIn(logDecision(d, captaincode.DecisionWithdrawn)))
	default:
		fmt.Fprintf(os.Stderr, "captain decision: unknown subcommand %q\n", sub)
		os.Exit(2)
	}
}

// decisionID parses flags that may come before or after the id.
func decisionID(fs *flag.FlagSet, args []string) string {
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	_ = fs.Parse(args)
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		fatal(fmt.Errorf("a decision id is required"))
	}
	return id
}

func decisionPost(args []string) {
	fs := flag.NewFlagSet("decision post", flag.ExitOnError)
	var d captaincode.OwnerDecision
	var opts decisionOptions
	fs.StringVar(&d.Title, "title", "", "short title")
	fs.StringVar(&d.Question, "question", "", "the question the user answers")
	fs.StringVar(&d.Context, "context", "", "what the user needs to know to answer, in a few sentences")
	fs.Var(&opts, "option", "id=\"Label\" or id=\"Label :: detail\" (at least two)")
	fs.StringVar(&d.Recommend, "recommend", "", "the option id you recommend")
	fs.StringVar(&d.Why, "why", "", "one line: why you recommend it")
	fs.StringVar(&d.Ref, "ref", "", "the repository's own id for it (OD-52, D10)")
	fs.StringVar(&d.Record, "record", "", "the repository file the answer belongs in")
	fs.StringVar(&d.GitHub, "github", "", "issue or pull request that gets a note: owner/repo#n")
	fs.StringVar(&d.NeededBefore, "needed-before", "", "what waits for it")
	cwd := fs.String("cwd", "", "the checkout you commit in (default: this folder's); a worktree is filed under its main checkout")
	logName := fs.String("log", "", "the worker log name (default: found by folder and leg)")
	asJSON := fs.Bool("json", false, "print the stored decision as JSON")
	_ = fs.Parse(args)
	d.Options = opts
	dir := *cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir, _ = filepath.Abs(dir)
	// A worker in a linked worktree files the question under the main
	// checkout, the folder its TUI runs in and a signature binds; the log
	// entry goes in the worktree, where the worker commits.
	d.Repo, d.Checkout, d.Branch = captaincode.DecisionPlace(dir)
	d.PostedBy.Leg = os.Getenv(captaincode.GateLegEnv)
	d.PostedBy.Log = *logName
	if d.PostedBy.Log == "" && d.PostedBy.Leg != "" {
		for _, at := range []string{d.Repo, d.Checkout} {
			if d.PostedBy.Log == "" && at != "" {
				d.PostedBy.Log = captaincode.FindRunLog(workerLogsDir(), at, d.PostedBy.Leg)
			}
		}
	}
	if d.Title == "" {
		d.Title = d.Question
	}
	stored, err := captaincode.PostDecision(d)
	if err != nil {
		fatal(err)
	}
	logged := logDecision(stored, captaincode.DecisionOpen)
	commit := ""
	if logged != "" {
		commit = "added to " + logged + ": commit that file with your work"
	}
	if *asJSON {
		printJSON(stored)
		if commit != "" {
			fmt.Fprintln(os.Stderr, "captain decision: "+commit)
		}
		return
	}
	fmt.Printf("posted %s - the user answers it in the cockpit or with `captain decision prompt %s --option <id>`\n", stored.ID, stored.ID)
	if commit != "" {
		fmt.Println(commit)
	}
}

// logDecision appends d's event to its repository's decision log and returns
// the log's path, or "" when there is no checkout to log in. A failed write is
// reported but does not fail the command: the register entry stands.
func logDecision(d captaincode.OwnerDecision, event string) string {
	dir := captaincode.DecisionCheckout(d)
	if dir == "" {
		return ""
	}
	path, err := captaincode.AppendDecisionLog(dir, d, event)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain decision: %s was not updated: %v\n", captaincode.DecisionLogRel, err)
		return ""
	}
	return path
}

func loggedIn(path string) string {
	if path == "" {
		return ""
	}
	return " (logged in " + path + ")"
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
