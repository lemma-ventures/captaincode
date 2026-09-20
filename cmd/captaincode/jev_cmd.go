package main

// `captain jev` - the decision leg by hand (pkg systemone.go). jev answers
// typed questions, so this is not a chat: it is the live probe doctor cannot
// make without spending, and the same questions the brain's triage asks.
//
//	captain jev                      probe: the models the key reaches + one noul, with latency, tokens and the registry estimate
//	captain jev classify <task…>     the triage questions (class, domain), their probabilities, and whether the gate would take the answer,
//	                                 plus the shadow ones (shape, leg over the open legs) the brain records beside its own choice
//	captain jev shadow [--point p] [--target 0.9] [--min 20]
//	                                 the shadow record read as a calibration: agreement with captain per decision point, by confidence,
//	                                 labelled with the tasks' outcomes, and the bar each point could be gated at (pkg shadow.go)
//	captain jev conform [--json]     does this backend answer captain's questions? a fixed suite whose answers are not in doubt,
//	                                 per capability (triage, route, keep, gate, supervise) - the check an OPEN backend has to
//	                                 pass before a shadow run against it is worth starting (pkg conform.go)
//	captain jev ask --state <text|@file> --questions <json|@file> [--model id]
//	                                 any state, any questions, in the API's own shape; answers as JSON

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const jevUsage = "usage: captain jev | captain jev classify <task…> | captain jev shadow [--point p] [--target 0.9] [--min 20] | captain jev conform [--json] | captain jev ask --state <text|@file> --questions <json|@file> [--model id]"

func cmdJev(args []string) {
	if len(args) > 0 && args[0] == "shadow" { // reads the ledger; needs no key
		jevShadow(args[1:])
		return
	}
	c := captaincode.SystemOneFromEnv()
	if c == nil {
		fatal(fmt.Errorf("no decision leg: set %s (add it to %s, or the console's API_KEY=… line to ~/.config/captain/%s - keys: console.typesafe.ai/settings/keys), or point %s at a System One-shaped endpoint that needs no key. Captain runs without one: triage falls back to the heuristics and the free-leg classify, and nothing else changes",
			captaincode.SystemOneKeyEnv, captaincode.CaptainEnvPath(), captaincode.SystemOneKeyFile, captaincode.SystemOneURLEnv))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if len(args) == 0 {
		jevProbe(ctx, c)
		return
	}
	switch args[0] {
	case "classify":
		task := strings.TrimSpace(strings.Join(args[1:], " "))
		if task == "" {
			fatal(errors.New(jevUsage))
		}
		jevClassify(ctx, c, task)
	case "conform":
		jevConform(ctx, c, args[1:])
	case "ask":
		jevAsk(ctx, c, args[1:])
	default:
		fatal(errors.New(jevUsage))
	}
}

func jevProbe(ctx context.Context, c *captaincode.SystemOneClient) {
	models, err := c.Models(ctx)
	if err != nil {
		fatal(err)
	}
	how := "key from " + c.KeySource
	if c.Keyless() {
		how = "keyless (" + c.KeySource + ")"
	}
	fmt.Printf("%s reaches %d model(s) at %s [backend %s]:\n", how, len(models), c.BaseURL, c.Backend())
	for _, m := range models {
		fmt.Printf("  %-12s %s (%s)\n", m.Name, m.Description, m.ReleaseDate)
	}
	resp, res, err := c.Ask(ctx, "captain is checking that the System One API answers before routing decisions through it.",
		map[string]captaincode.S1Question{"probe": {Type: "noul", Instructions: "Is this text a connectivity check?"}})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("\nprobe: noul %.3f (1 = yes) · served by %s · %dms · %d tokens (%d in, %d out) · ~$%.6f at the registry price\n",
		resp.Answers["probe"].Noul, resp.Model, res.DurationMs, res.Tokens, resp.Usage.InputTokens, resp.Usage.OutputTokens,
		captaincode.EstimateCost(captaincode.LegJev, res.Tokens))
	fmt.Println("ready: triage tier 1 asks jev first (CAPTAIN_TRIAGE_JEV=0 turns that off); a brain started before the key was set needs a restart to see it")
}

func jevClassify(ctx context.Context, c *captaincode.SystemOneClient, task string) {
	h := captaincode.TriageTask(task)
	// The shadow questions ride along, over the legs open right now - the
	// menu the director would be handed - exactly as the brain asks them.
	menu := captaincode.Rungs
	if l, err := captaincode.LoadLedger(); err == nil {
		menu = captaincode.Pick(len(captaincode.Rungs)-1, l.Cooldowns, time.Now())
	}
	tr, sh, res, err := captaincode.TriageWithJev(ctx, c, task, captaincode.JevTriageOptions{Shadow: jevShadowEnabled(), Menu: menu, Domain: h.Domain})
	if err != nil {
		fatal(err)
	}
	var resp captaincode.S1Response
	_ = json.Unmarshal([]byte(res.Text), &resp)
	fmt.Printf("class   %-9s %s  confidence %.2f\n", tr.Class, probLine(resp.Answers["class"].Probabilities), resp.Answers["class"].Confidence)
	fmt.Printf("domain  %-9s %s  confidence %.2f\n", tr.Domain, probLine(resp.Answers["domain"].Probabilities), resp.Answers["domain"].Confidence)
	bar := jevConfidence()
	verdict := "jev decides"
	if tr.Confidence < bar {
		verdict = "below the bar - the free-leg classify would decide"
	}
	fmt.Printf("%s · %dms · %d tokens · ~$%.6f · gate ≥%.2f on the weaker answer (%.2f) → %s\n",
		resp.Model, res.DurationMs, res.Tokens, captaincode.EstimateCost(captaincode.LegJev, res.Tokens), bar, tr.Confidence, verdict)
	fmt.Printf("tier 0 (heuristics) said %s/%s (conf %.2f): %s\n", h.Class, h.Domain, h.Confidence, h.Why)
	if sh == nil {
		return
	}
	if a, ok := sh.Answers[captaincode.PointShape]; ok {
		fmt.Printf("shape   %-9s %s  confidence %.2f  (shadow: recorded beside the director's plan, never acted on)\n", a.Choice, probLine(a.Probabilities), a.Confidence)
	}
	if a, ok := sh.Answers[captaincode.PointLeg]; ok {
		fmt.Printf("leg     %-9s %s  confidence %.2f  (shadow, over %s)\n", a.Choice, probLine(a.Probabilities), a.Confidence, legList(sh.Menu))
	}
}

// jevShadow reads the shadow record - the decision leg's answers beside
// captain's own choices - as a calibration per decision point.
func jevShadow(args []string) {
	fs := flag.NewFlagSet("jev shadow", flag.ExitOnError)
	target := fs.Float64("target", 0.9, "agreement a confidence floor must reach to be suggested as the bar")
	minN := fs.Int("min", 20, "comparisons a floor needs before it can be suggested")
	point := fs.String("point", "", "one decision point only: class, domain, shape, leg, note-route, a gate-* or a supervisor point")
	_ = fs.Parse(args)
	l, err := captaincode.LoadLedger()
	if err != nil {
		fatal(err)
	}
	// The gate screens in whichever process runs the tool, not in the brain,
	// so its rows live in their own append-only log (gate.go). They are the
	// same shape and belong in the same reading.
	shadows := append(append([]captaincode.ShadowRecord(nil), l.Shadows...), captaincode.ReadGateLog()...)
	cal := captaincode.ShadowCalibration(l.Decisions, shadows, l.Outcomes)
	if *point != "" {
		kept := cal[:0]
		for _, p := range cal {
			if p.Point == *point {
				kept = append(kept, p)
			}
		}
		cal = kept
	}
	fmt.Print(captaincode.FormatShadowCalibration(cal, *target, *minN))
}

// probLine renders a choice's distribution, most likely first.
func probLine(p map[string]float64) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if p[keys[i]] != p[keys[j]] {
			return p[keys[i]] > p[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.2f", k, p[k]))
	}
	return "(" + strings.Join(parts, " · ") + ")"
}

// jevConform runs the fixture suite against whatever backend is configured.
// It exits non-zero when a capability is not usable, so it can gate a script
// that is about to point captain at an open backend.
func jevConform(ctx context.Context, c *captaincode.SystemOneClient, args []string) {
	fs := flag.NewFlagSet("jev conform", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "the whole report as JSON")
	_ = fs.Parse(args)
	rep := captaincode.RunConform(ctx, c, captaincode.ConformSuite())
	if *asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Print(captaincode.FormatConformReport(rep))
	}
	for _, capability := range captaincode.ConformCaps {
		if !rep.Usable(capability) {
			os.Exit(1)
		}
	}
}

func jevAsk(ctx context.Context, c *captaincode.SystemOneClient, args []string) {
	fs := flag.NewFlagSet("jev ask", flag.ExitOnError)
	state := fs.String("state", "", "the state to judge: text, or @file")
	questions := fs.String("questions", "", `the questions in the API's shape: {"<name>": {"type": "choice|score|noul", "instructions": "…", "criteria": …}}, or @file`)
	model := fs.String("model", "", "model id (default: the jev leg's pin, jev-latest)")
	_ = fs.Parse(args)
	if *state == "" || *questions == "" {
		fatal(errors.New(jevUsage))
	}
	var q map[string]captaincode.S1Question
	if err := json.Unmarshal([]byte(readArg(*questions)), &q); err != nil {
		fatal(fmt.Errorf("--questions: %w", err))
	}
	if *model != "" {
		c.Model = *model
	}
	resp, res, err := c.Ask(ctx, readArg(*state), q)
	if err != nil {
		fatal(err)
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "%dms · %d tokens · ~$%.6f at the registry price\n", res.DurationMs, res.Tokens, captaincode.EstimateCost(captaincode.LegJev, res.Tokens))
}

// readArg returns v, or the file it names when it starts with @.
func readArg(v string) string {
	if !strings.HasPrefix(v, "@") {
		return v
	}
	b, err := os.ReadFile(v[1:])
	if err != nil {
		fatal(err)
	}
	return string(b)
}
