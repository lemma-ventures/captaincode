package captaincode

import "testing"

func TestParseAutoUpgrade(t *testing.T) {
	off := ParseAutoUpgrade("")
	if off.Models || off.CLI || off.Label() != "off" {
		t.Fatalf("default must propose only, got %+v", off)
	}
	for _, v := range []string{"0", "off", "propose", "no", "latest", "maybe"} {
		if m := ParseAutoUpgrade(v); m.Models || m.CLI {
			t.Fatalf("%q must not enable auto-upgrade", v)
		}
	}
	if m := ParseAutoUpgrade("models"); !m.Models || m.CLI || m.Label() != "models" {
		t.Fatalf("models: %+v", m)
	}
	if m := ParseAutoUpgrade("1"); !m.Models || m.CLI {
		t.Fatalf("1: %+v", m)
	}
	if m := ParseAutoUpgrade("cli"); m.Models || !m.CLI || m.Label() != "cli" {
		t.Fatalf("cli: %+v", m)
	}
	if m := ParseAutoUpgrade("all"); !m.Models || !m.CLI || m.Label() != "all" {
		t.Fatalf("all: %+v", m)
	}
}

func TestHoldUpgradeBlocksCodingSpeedCostPrivacyAndEnv(t *testing.T) {
	spec := LegSpec{ID: LegGemini, Transport: TransportOpencode, Provider: "openrouter", Model: "google/gemini-3.7-flash", PriceIn: 0.38, PriceOut: 2}
	cur := AAModel{Slug: "gemini-3-7-flash", IntelligenceIndex: 39, CodingIndex: 40, TokensPerSecond: 100, PriceIn: 0.38, PriceOut: 2}
	next := AAModel{Slug: "gemini-3-8-flash", IntelligenceIndex: 50, CodingIndex: 41, TokensPerSecond: 90, PriceIn: 0.40, PriceOut: 2.2}
	if hold := HoldUpgrade(spec, cur, next); hold != "" {
		t.Fatalf("a small price change must pass, hold %q", hold)
	}

	slow := next
	slow.TokensPerSecond = 40
	if HoldUpgrade(spec, cur, slow) != HoldSpeed {
		t.Fatal("speed drop")
	}
	weak := next
	weak.CodingIndex = 30
	if HoldUpgrade(spec, cur, weak) != HoldCoding {
		t.Fatal("coding drop")
	}
	dear := next
	dear.PriceOut = 4
	if HoldUpgrade(spec, cur, dear) != HoldCost {
		t.Fatal("price jump")
	}
	free := spec
	free.PriceIn, free.PriceOut = 0, 0
	if HoldUpgrade(free, cur, next) != HoldPriced {
		t.Fatal("free pin")
	}
	unknown := next
	unknown.PriceIn, unknown.PriceOut = 0, 0
	if HoldUpgrade(spec, cur, unknown) != HoldUnknownPrice {
		t.Fatal("unknown price")
	}

	cli := spec
	cli.Transport = TransportClaudeCLI
	if HoldUpgrade(cli, cur, next) != HoldNotPin {
		t.Fatal("cli leg")
	}

	t.Setenv("CAPTAIN_GEMINI_MODEL", "google/gemini-3.7-flash")
	if HoldUpgrade(spec, cur, next) != HoldEnv {
		t.Fatal("env pin")
	}
	t.Setenv("CAPTAIN_GEMINI_MODEL", "")

	open := spec
	open.Open = boolp(true)
	openCur := cur
	openCur.Slug = "kolibri-1"
	closed := next
	closed.Slug = "gpt-9"
	if HoldUpgrade(open, openCur, closed) != HoldClosed {
		t.Fatal("open-weights leg")
	}
}
