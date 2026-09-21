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
//	captain jev conform [--json] [--for caps]
//	                                 does this backend answer captain's questions? a fixed suite whose answers are not in doubt,
//	                                 per capability (triage, route, keep, gate, supervise) - the check an OPEN backend has to
//	                                 pass before a shadow run against it is worth starting (pkg conform.go)
//	captain jev ask --state <text|@file> --questions <json|@file> [--model id]
//	                                 any state, any questions, in the API's own shape; answers as JSON
//
// --open asks the sidecar named by CAPTAIN_SYSTEMONE_OPEN_URL instead of the
// primary backend (pkg systemone_open.go) - the same client, with the same
// context ceiling and timeout, that the brain would use for it, so what
// conform measures here is what would answer there.

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

const jevUsage = "usage: captain jev [--open] | captain jev classify [--open] <task…> | captain jev shadow [--point p] [--backend name] [--target 0.9] [--min 20] | captain jev conform [--open] [--json] [--for caps] | captain jev ask [--open] --state <text|@file> --questions <json|@file> [--model id]"

func cmdJev(args []string) {
	if len(args) > 0 && args[0] == "shadow" { // reads the ledger; needs no key
		jevShadow(args[1:])
		return
	}
	open, args := takeFlag(args, "--open")
	b := captaincode.SystemOneBackendsFromEnv()
	// A promotion that did not happen is how an operator comes to believe a
	// backend was tested, so it is said out loud wherever the pool is built.
	for _, r := range b.Refused {
		fmt.Fprintln(os.Stderr, captaincode.SystemOneOpenForEnv+" refused "+r)
	}
	c := b.Primary
	if open {
		if c = b.Open; c == nil {
			fatal(fmt.Errorf("no open sidecar: set %s to a System One-shaped endpoint on loopback (sidecars/laya runs one). Without --open this asks the primary backend", captaincode.SystemOneOpenURLEnv))
		}
	}
	if c == nil {
		fatal(fmt.Errorf("no decision leg: set %s (add it to %s, or the console's API_KEY=… line to ~/.config/captain/%s - keys: console.typesafe.ai/settings/keys), or point %s at a System One-shaped endpoint that needs no key. Captain runs without one: triage falls back to the heuristics and the free-leg classify, and nothing else changes",
			captaincode.SystemOneKeyEnv, captaincode.CaptainEnvPath(), captaincode.SystemOneKeyFile, captaincode.SystemOneURLEnv))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if len(args) == 0 {
		jevProbe(ctx, b, c)
		return
	}
	switch args[0] {
	case "classify":
		task := strings.TrimSpace(strings.Join(args[1:], " "))
		if task == "" {
			fatal(errors.New(jevUsage))
		}
		jevClassify(ctx, b, c, task)
	case "conform":
		jevConform(ctx, c, open, args[1:])
	case "ask":
		jevAsk(ctx, c, args[1:])
	default:
		fatal(errors.New(jevUsage))
	}
}

func jevProbe(ctx context.Context, b captaincode.SystemOneBackends, c *captaincode.SystemOneClient) {
	models, err := c.Models(ctx)
	if err != nil {
		fatal(err)
	}
	how := "key from " + c.KeySource
	if c.Keyless() {
		how = "keyless (" + c.KeySource + ")"
	}
	fmt.Printf("%s reaches %d model(s) at %s [backend %s, holds %d tokens per call]:\n", how, len(models), c.BaseURL, c.Backend(), c.ContextTokens)
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
	fmt.Println(jevReady(b, c))
}

// jevReady says what asking this backend by hand implies for the brain, which
// is a different sentence for a sidecar: the primary answers triage as tier 1
// the moment it is configured, a sidecar answers nothing until it is promoted
// on its own rows.
func jevReady(b captaincode.SystemOneBackends, c *captaincode.SystemOneClient) string {
	if c == b.Open {
		if bar, ok := b.Promoted(captaincode.CapTriage); ok {
			return fmt.Sprintf("ready: this sidecar decides triage at its own bar %.2f (%s); everything else is the primary backend's", bar, captaincode.SystemOneOpenForEnv)
		}
		return "shadow only: it answers beside captain's own choices and decides nothing. `captain jev shadow --backend " +
			c.Backend() + "` reads its rows; the bar it prints is what promotes it (" + captaincode.SystemOneOpenForEnv + ")"
	}
	return "ready: triage tier 1 asks jev first (CAPTAIN_TRIAGE_JEV=0 turns that off); a brain started before the key was set needs a restart to see it"
}

func jevClassify(ctx context.Context, b captaincode.SystemOneBackends, c *captaincode.SystemOneClient, task string) {
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
	// The bar belongs to the backend that just answered. Printing the
	// primary's tuned number beside a sidecar's answer is the exact mistake
	// this whole path exists to prevent.
	bar, verdict := jevConfidence(), "jev decides"
	if c == b.Open {
		promoted, ok := b.Promoted(captaincode.CapTriage)
		bar = promoted
		if !ok {
			fmt.Printf("%s is SHADOW ONLY: it is not promoted for triage, so nothing it says here decides anything. Promote it with %s=triage=<the bar you read off `captain jev shadow --backend %s`>\n",
				c.Backend(), captaincode.SystemOneOpenForEnv, c.Backend())
			bar = 1.01 // nothing clears it: the report must not read as a decision
		}
	}
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
	backend := fs.String("backend", "", "one backend's rows only: typesafe, or a sidecar's host - the reading a bar can be taken from")
	_ = fs.Parse(args)
	l, err := captaincode.LoadLedger()
	if err != nil {
		fatal(err)
	}
	// The gate screens in whichever process runs the tool, not in the brain,
	// so its rows live in their own append-only log (gate.go). They are the
	// same shape and belong in the same reading.
	shadows := append(append([]captaincode.ShadowRecord(nil), l.Shadows...), captaincode.ReadGateLog()...)
	// A gate row is settled by the task's clean acceptance, not by anything
	// captain decided beside it - derived here, never written back to the
	// append-only log (settle.go).
	captaincode.SettleGateRows(shadows, l.Outcomes)
	decisions := l.Decisions
	// One backend at a time is the only reading a bar can be taken from. With
	// a sidecar running beside the primary that is no longer an accident to
	// warn about - both are supposed to be answering - so the report names
	// who answered and how to read each one alone.
	counts := captaincode.ShadowBackends(decisions, shadows)
	if *backend != "" {
		decisions = captaincode.DecisionsFromBackend(decisions, *backend)
		shadows = captaincode.ShadowsFromBackend(shadows, *backend)
		kept := len(decisions) + len(shadows)
		if kept == 0 && len(counts) > 0 {
			// An empty reading under a name nobody answered under reads as
			// "nothing has been recorded", which is a different and much
			// more alarming fact than "not that name".
			fmt.Printf("no rows from %s. These backends answered: %s\n", *backend, backendCounts(counts))
			return
		}
		fmt.Printf("backend %s only: %d of %d rows\n\n", *backend, kept, countRows(counts))
	} else if len(counts) > 1 {
		fmt.Printf("%d backends answered these rows: %s\n", len(counts), backendCounts(counts))
		fmt.Printf("A bar is a property of ONE of them. Read each alone: captain jev shadow --backend <name>\n\n")
	}
	cal := captaincode.ShadowCalibration(decisions, shadows, l.Outcomes)
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
//
// Every capability is always RUN and always reported. What --for changes is
// which ones the exit code speaks for, and --open defaults it to the ones a
// sidecar can be promoted to at all (SystemOneOpenPromotable): a 512-token
// backend fails the gate and supervisor cases on state size, and exiting 1
// for a capability captain will never send it would make the check useless
// for the thing it is actually gating.
func jevConform(ctx context.Context, c *captaincode.SystemOneClient, open bool, args []string) {
	fs := flag.NewFlagSet("jev conform", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "the whole report as JSON")
	def := strings.Join(captaincode.ConformCaps, ",")
	if open {
		def = strings.Join(captaincode.SystemOneOpenPromotable, ",")
	}
	only := fs.String("for", def, "the capabilities the exit code speaks for; every one is run and reported either way")
	_ = fs.Parse(args)
	rep := captaincode.RunConform(ctx, c, captaincode.ConformSuite())
	gating := strings.Split(*only, ",")
	if *asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Print(captaincode.FormatConformReport(rep))
		fmt.Printf("\nexit code speaks for: %s\n", strings.Join(gating, ", "))
	}
	for _, capability := range gating {
		if capability = strings.TrimSpace(capability); capability != "" && !rep.Usable(capability) {
			os.Exit(1)
		}
	}
}

// takeFlag pulls a bare flag out of an argument list wherever it sits, so it
// can precede or follow a subcommand and its free text.
func takeFlag(args []string, name string) (bool, []string) {
	found, rest := false, make([]string, 0, len(args))
	for _, a := range args {
		if a == name {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return found, rest
}

// backendCounts names who answered, most rows first.
func backendCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s (%d)", name, counts[name]))
	}
	return strings.Join(parts, ", ")
}

// countRows totals a backend count.
func countRows(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
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
