package main

// `captain legs` - the registry from the shell (MM37 phase 1).
//
//	captain legs                      list active legs: transport, model, price, prior, ctx
//	captain legs add <id> <provider/model> [flags]
//	                                  add (or update) a leg in ~/.captaincode/legs.json; price and
//	                                  context are fetched from OpenRouter's public catalog when the
//	                                  provider is openrouter and not given; then /init adds the TUI
//	                                  entries. Restart the brain + relaunch the TUI to use it.
//	captain legs remove <id>          drop an overlay leg, or disable a compiled one
//
// Flags for add: --prior 7.5 --note "…" --display "…" --aa <slug> --vision
// --ctx N --price-in X --price-out Y --transport opencode|claude-cli|cursor-cli|codex-cli
// --frontier --subscription

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// openRouterCatalogURL is the public (no key) model catalog.
var openRouterCatalogURL = "https://openrouter.ai/api/v1/models"

// openRouterModel is the slice of a catalog row we use.
type openRouterModel struct {
	ID            string
	Name          string
	ContextLength int
	PriceIn       float64 // USD per 1M tokens
	PriceOut      float64
}

// fetchOpenRouterModel looks one model up in the catalog.
func fetchOpenRouterModel(url, id string) (openRouterModel, error) {
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(url)
	if err != nil {
		return openRouterModel{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openRouterModel{}, fmt.Errorf("openrouter catalog: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return openRouterModel{}, fmt.Errorf("decode openrouter catalog: %w", err)
	}
	for _, m := range body.Data {
		if m.ID != id {
			continue
		}
		var pin, pout float64
		fmt.Sscanf(m.Pricing.Prompt, "%g", &pin)
		fmt.Sscanf(m.Pricing.Completion, "%g", &pout)
		return openRouterModel{ID: m.ID, Name: m.Name, ContextLength: m.ContextLength, PriceIn: pin * 1e6, PriceOut: pout * 1e6}, nil
	}
	return openRouterModel{}, fmt.Errorf("model %q not in the openrouter catalog", id)
}

func cmdLegs(args []string) {
	if len(args) == 0 || args[0] == "list" {
		printLegs()
		return
	}
	switch args[0] {
	case "add":
		cmdLegsAdd(args[1:])
	case "caps", "capabilities":
		fmt.Print(captaincode.CapabilityTable())
	case "reopen":
		// Lift a leg's cooldown now (credits topped up, an outage over):
		// the brain clears it, no restart.
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain legs reopen <id>"))
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(brainURL()+"/v1/legs/reopen?leg="+url.QueryEscape(args[1]), "application/json", nil)
		if err != nil {
			fatal(fmt.Errorf("the brain is not running (%v)", err))
		}
		defer resp.Body.Close()
		var out struct {
			Result string                   `json:"result"`
			Error  struct{ Message string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			fatal(fmt.Errorf("%s", out.Error.Message))
		}
		fmt.Println(out.Result)
	case "remove", "rm":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: captain legs remove <id>"))
		}
		if err := captaincode.RemoveLeg("", captaincode.Leg(args[1])); err != nil {
			fatal(err)
		}
		fmt.Printf("removed %s → %s (restart `captain brain` to apply)\n", args[1], captaincode.RegistryOverlayPath())
	default:
		fatal(fmt.Errorf("usage: captain legs [list|caps|add|remove]"))
	}
}

func printLegs() {
	fmt.Printf("%-10s %-11s %-34s %8s %8s %8s %6s  %s\n", "leg", "transport", "model", "$/M in", "$/M out", "ctx", "prior", "flags")
	for _, s := range captaincode.Registry() {
		model := s.Model
		if s.Provider != "" {
			model = s.Provider + "/" + s.Model
		}
		if len(model) > 34 {
			model = model[:33] + "…"
		}
		var flags []string
		if s.Subscription {
			flags = append(flags, "sub")
		}
		if s.Vision {
			flags = append(flags, "vision")
		}
		if s.Frontier {
			flags = append(flags, "frontier")
		}
		if !captaincode.ServesTasks(s.ID) {
			flags = append(flags, "decision")
		}
		ctx := "-"
		if s.Ctx > 0 {
			ctx = fmt.Sprintf("%dk", s.Ctx/1024)
		}
		price := func(v float64) string {
			if v == 0 {
				return "-"
			}
			return fmt.Sprintf("%.2f", v)
		}
		fmt.Printf("%-10s %-11s %-34s %8s %8s %8s %6.1f  %s\n", s.ID, s.Transport, model, price(s.PriceIn), price(s.PriceOut), ctx, captaincode.QualityPrior(s.ID), strings.Join(flags, ","))
	}
	if dp := domainPriorSummary(); dp != "" {
		fmt.Println("\nper-domain priors (synced):")
		fmt.Print(dp)
	}
	fmt.Printf("\noverlay: %s · add: captain legs add <id> <provider/model> [--prior N --note …]\n", captaincode.RegistryOverlayPath())
}

func domainPriorSummary() string {
	var b strings.Builder
	legs := append([]captaincode.Leg(nil), captaincode.AllLegs...)
	sort.Slice(legs, func(i, j int) bool { return captaincode.QualityPrior(legs[i]) > captaincode.QualityPrior(legs[j]) })
	for _, l := range legs {
		code, ed, rs := captaincode.QualityPriorFor(l, captaincode.DomainCode), captaincode.QualityPriorFor(l, captaincode.DomainEditorial), captaincode.QualityPriorFor(l, captaincode.DomainResearch)
		if code == captaincode.QualityPrior(l) && ed == code && rs == code {
			continue
		}
		fmt.Fprintf(&b, "  %-10s code %.1f · editorial %.1f · research %.1f\n", l, code, ed, rs)
	}
	return b.String()
}

func cmdLegsAdd(args []string) {
	fs := flag.NewFlagSet("legs add", flag.ExitOnError)
	prior := fs.Float64("prior", 0, "cold-start quality 0-10 (default: 7.0, or the AA-synced value later)")
	note := fs.String("note", "", "director briefing line")
	display := fs.String("display", "", "TUI display name")
	aa := fs.String("aa", "", "Artificial Analysis slug for priors sync")
	vision := fs.Bool("vision", false, "accepts image input")
	frontier := fs.Bool("frontier", false, "frontier-class semantics (2x budget, never auto-assigned cheaply)")
	sub := fs.Bool("subscription", false, "billed by a quota window, not per token")
	ctx := fs.Int("ctx", 0, "context window (tokens)")
	priceIn := fs.Float64("price-in", 0, "USD per 1M input tokens")
	priceOut := fs.Float64("price-out", 0, "USD per 1M output tokens")
	transport := fs.String("transport", "opencode", "opencode|claude-cli|cursor-cli|codex-cli|system-one")
	noInit := fs.Bool("no-init", false, "do not update opencode.jsonc")
	// Allow flags after the positionals.
	var pos []string
	var flagsOnly []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagsOnly = append(flagsOnly, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.Contains(args[i], "=") && !isBoolFlag(args[i]) {
				flagsOnly = append(flagsOnly, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, args[i])
	}
	_ = fs.Parse(flagsOnly)
	tp := captaincode.Transport(*transport)
	if len(pos) < 1 || ((tp == captaincode.TransportOpencode || tp == captaincode.TransportSystemOne) && len(pos) < 2) {
		fatal(fmt.Errorf("usage: captain legs add <id> <provider/model> [--prior N --note … --aa slug --vision --ctx N --price-in X --price-out Y]"))
	}
	spec := captaincode.LegSpec{ID: captaincode.Leg(pos[0]), Transport: captaincode.Transport(*transport),
		AA: *aa, PriceIn: *priceIn, PriceOut: *priceOut, Ctx: *ctx, Vision: *vision, Frontier: *frontier,
		Subscription: *sub, Prior: *prior, Display: *display, Note: *note}
	if len(pos) >= 2 {
		i := strings.Index(pos[1], "/")
		if i <= 0 {
			fatal(fmt.Errorf("model must be <provider>/<model-id>, e.g. openrouter/meta/muse-spark-1.3"))
		}
		spec.Provider, spec.Model = pos[1][:i], pos[1][i+1:]
	}
	if spec.Provider == "openrouter" && (spec.PriceIn == 0 || spec.Ctx == 0) {
		if m, err := fetchOpenRouterModel(openRouterCatalogURL, spec.Model); err != nil {
			fmt.Fprintf(os.Stderr, "note: %v - price/ctx not filled\n", err)
		} else {
			if spec.PriceIn == 0 {
				spec.PriceIn, spec.PriceOut = m.PriceIn, m.PriceOut
			}
			if spec.Ctx == 0 {
				spec.Ctx = m.ContextLength
			}
			if spec.Display == "" {
				spec.Display = m.Name + " (captain · OpenRouter)"
			}
		}
	}
	if spec.Prior == 0 {
		spec.Prior = 7.0
	}
	if spec.Display == "" {
		spec.Display = string(spec.ID) + " (captain · " + string(spec.Transport) + ")"
	}
	if spec.Note == "" {
		spec.Note = fmt.Sprintf("%s via %s; $%.2f/M in, $%.2f/M out; no local track record yet - exploration will earn it a score", spec.Model, spec.Provider, spec.PriceIn, spec.PriceOut)
	}
	if err := captaincode.AddLeg("", spec); err != nil {
		fatal(err)
	}
	fmt.Printf("added %s → %s\n", spec.ID, captaincode.RegistryOverlayPath())
	if !*noInit {
		if _, notes, err := captaincode.EnsureOpencodeConfig(captaincode.OpencodeConfigPath(), true); err != nil {
			fmt.Fprintf(os.Stderr, "opencode.jsonc: %v\n", err)
		} else {
			for _, n := range notes {
				fmt.Println("  • " + n)
			}
		}
		if spec.Transport == captaincode.TransportOpencode {
			if added, err := captaincode.EnsureProviderModel(captaincode.OpencodeConfigPath(), spec.Provider, spec.Model, spec.Display); err != nil {
				fmt.Fprintf(os.Stderr, "opencode.jsonc provider block: %v\n", err)
			} else if added {
				fmt.Printf("  • listed %s under provider.%s.models (uncatalogued providers need it)\n", spec.Model, spec.Provider)
			}
		}
	}
	if v := os.Getenv("CAPTAIN_LEGS"); v != "" && !containsLeg(v, string(spec.ID)) {
		fmt.Printf("\nCAPTAIN_LEGS is set and does not include %s - add it in ~/.config/captain/env or the leg only runs when forced (/%s).\n", spec.ID, spec.ID)
	}
	fmt.Println("restart `captain brain` and relaunch the TUI to use it (config is read at startup).")
}

func isBoolFlag(name string) bool {
	switch strings.TrimLeft(name, "-") {
	case "vision", "frontier", "subscription", "no-init":
		return true
	}
	return false
}

func containsLeg(list, id string) bool {
	for _, s := range strings.Split(list, ",") {
		if strings.TrimSpace(s) == id {
			return true
		}
	}
	return false
}
