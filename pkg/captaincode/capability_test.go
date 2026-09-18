package captaincode

import (
	"strings"
	"testing"
)

func TestCapabilitiesComeFromTheTransport(t *testing.T) {
	claude := CapabilitiesFor(LegClaude)
	if claude.Version != CapabilityVersion {
		t.Fatalf("version %d, want %d", claude.Version, CapabilityVersion)
	}
	if got := claude.Supports(CapCost); got != SupportYes {
		t.Errorf("claude cost support = %q, want yes (total_cost_usd is on the result)", got)
	}
	if got := CapabilitiesFor(LegCursor).Supports(CapUsage); got != SupportNo {
		t.Errorf("cursor usage support = %q, want no - cursor-agent reports no token counts", got)
	}
	if got := CapabilitiesFor(LegGLM).Supports(CapSessionReuse); got != SupportYes {
		t.Errorf("opencode session reuse = %q, want yes", got)
	}
	// Two legs on one transport answer identically except where the model differs.
	if a, b := CapabilitiesFor(LegGLM).Supports(CapPermissions), CapabilitiesFor(LegQwen).Supports(CapPermissions); a != b {
		t.Errorf("same transport disagreed on permissions: %q vs %q", a, b)
	}
}

func TestVisionIsAModelFactNotATransportOne(t *testing.T) {
	if got := CapabilitiesFor(LegGemini).Supports(CapVision); got != SupportYes {
		t.Errorf("gemini vision = %q, want yes", got)
	}
	if got := CapabilitiesFor(LegQwen).Supports(CapVision); got != SupportNo {
		t.Errorf("qwen vision = %q, want no - same opencode transport as gemini", got)
	}
	if src := CapabilitiesFor(LegGemini).Facts[CapVision].Source; src != capSourceRegistry {
		t.Errorf("vision source = %q, want %q", src, capSourceRegistry)
	}
}

func TestUnknownNeverSatisfiesARequirement(t *testing.T) {
	// opencode reports cost per provider, so it is unknown - and a task that
	// requires a measured price must not be handed to a maybe.
	why := (Requirements{Require: []Capability{CapCost}}).Missing(LegGLM)
	if why == "" {
		t.Fatal("unknown cost support satisfied a cost requirement")
	}
	if !strings.Contains(why, "unknown") {
		t.Errorf("exclusion %q does not say the answer was unknown", why)
	}
	if got := (Requirements{Require: []Capability{CapCost}}).Missing(LegClaude); got != "" {
		t.Errorf("claude excluded from a cost requirement: %s", got)
	}
}

func TestUnregisteredLegAnswersUnknown(t *testing.T) {
	if got := CapabilitiesFor(Leg("nope")).Supports(CapTools); got != SupportUnknown {
		t.Errorf("unregistered leg = %q, want unknown", got)
	}
}

func TestRequirementsExcludeWithAReason(t *testing.T) {
	why := (Requirements{Vision: true}).Missing(LegQwen)
	if !strings.Contains(why, "vision") {
		t.Errorf("vision exclusion = %q", why)
	}
	if got := (Requirements{Vision: true}).Missing(LegGemini); got != "" {
		t.Errorf("gemini excluded from a vision task: %s", got)
	}
	// A context floor is a hard constraint, and names both numbers.
	why = (Requirements{MinCtx: 900_000}).Missing(LegMiniMax)
	if !strings.Contains(why, "128k") || !strings.Contains(why, "878k") {
		t.Errorf("context exclusion %q should name the window and the floor", why)
	}
	if got := (Requirements{MinCtx: 900_000}).Missing(LegGLM); got != "" {
		t.Errorf("glm (1.3M ctx) excluded by a 900k floor: %s", got)
	}
}

func TestEmptyRequirementsExcludeNothing(t *testing.T) {
	r := Requirements{}
	if !r.Empty() {
		t.Fatal("zero requirements not empty")
	}
	if got := FilterCapable(AllLegs, r); len(got) != len(AllLegs) {
		t.Errorf("empty requirements dropped %d legs", len(AllLegs)-len(got))
	}
}

func TestFilterCapableKeepsOrder(t *testing.T) {
	got := FilterCapable([]Leg{LegQwen, LegGemini, LegFree, LegClaude}, Requirements{Vision: true})
	if len(got) != 2 || got[0] != LegGemini || got[1] != LegClaude {
		t.Errorf("FilterCapable = %v, want [gemini claude] in that order", got)
	}
}

func TestFrontierSelectionPrefersCapableCandidates(t *testing.T) {
	// Every frontier leg happens to see, so a vision floor must not narrow it.
	if len(FrontierLegsFor(Requirements{Vision: true})) != len(FrontierLegs()) {
		t.Error("a vision floor narrowed the frontier section; all frontier legs have vision")
	}
	// An impossible floor falls back to the full section rather than to nothing.
	if got := FrontierLegsFor(Requirements{MinCtx: 1 << 40}); len(got) != len(FrontierLegs()) {
		t.Errorf("impossible requirement left /frontier with %d legs, want the unfiltered section", len(got))
	}
}

func TestFrontierChainForPutsCapableLegsFirstAndKeepsTheRest(t *testing.T) {
	chain := FrontierChainFor(LegClaude, Requirements{Vision: true})
	if len(chain) != len(FrontierChain(LegClaude)) {
		t.Fatalf("chain length %d, want %d - no leg may be dropped", len(chain), len(FrontierChain(LegClaude)))
	}
	seenBlind := false
	for _, l := range chain {
		if LegSupportsVision(l) && seenBlind {
			t.Fatalf("%s (vision) ranked after a blind leg", l)
		}
		if !LegSupportsVision(l) {
			seenBlind = true
		}
	}
	for _, l := range chain {
		if l == LegClaude {
			t.Fatal("the failed leg is back on its own failover chain")
		}
	}
}

func TestOverlayCapsWinOverTheTransport(t *testing.T) {
	saved := specs[LegCursor]
	t.Cleanup(func() { specs[LegCursor] = saved })
	spec := saved
	spec.Caps = map[Capability]Support{CapUsage: SupportYes}
	specs[LegCursor] = spec

	caps := CapabilitiesFor(LegCursor)
	if caps.Supports(CapUsage) != SupportYes {
		t.Errorf("overlay did not override the transport's no")
	}
	if src := caps.Facts[CapUsage].Source; src != capSourceOverlay {
		t.Errorf("source = %q, want %q", src, capSourceOverlay)
	}
}

func TestCapabilityTableReportsEveryLeg(t *testing.T) {
	out := CapabilityTable()
	for _, s := range Registry() {
		if !strings.Contains(out, string(s.ID)) {
			t.Errorf("%s missing from the capability table", s.ID)
		}
	}
	if !strings.Contains(out, "unknown") {
		t.Error("the table does not explain what ? means")
	}
}
