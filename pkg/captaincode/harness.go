package captaincode

// How a leg is NAMED to the user (2026-10-03). A leg is a routing unit, and
// several legs can be one model family behind one provider: grok and
// grok-max are both xAI's Grok through opencode. Printed by leg id, the
// sidebar read as twenty different things and hid what actually ran.
//
// The sidebar lists harness-provider only (claude-cli, codex-cli, grok-xai,
// kimi-nim). Version and effort are not on that list. They appear on a run:
// codex-cli:gpt-6-luna@effort:low. Luna is not its own row. It is gpt-6-luna,
// a version of the codex-cli harness.
//
// Display only: slash commands, CAPTAIN_LEGS, scores and the journal keep the
// leg id.

import (
	"regexp"
	"strings"
)

// Harness names the harness and provenance a leg runs through.
// The shape is harness-provider: claude-cli, codex-cli, grok-xai, kimi-nim.
func Harness(l Leg) string {
	// Luna is gpt-6-luna on the ChatGPT subscription, the same harness as
	// codex exec. It is a version of that row, not a second leg on the list.
	if l == LegLuna {
		return "codex-cli"
	}
	spec, ok := Spec(l)
	if !ok {
		return string(l)
	}
	switch spec.Transport {
	case TransportClaudeCLI:
		return "claude-cli"
	case TransportCodexCLI:
		return "codex-cli"
	case TransportCursorCLI:
		return "cursor-cli"
	case TransportOpencode:
		return modelFamily(spec.Model) + "-" + providerShort(spec.Provider)
	case TransportOpencodeShell:
		return "openshell-nvidia"
	}
	return string(l)
}

// providerShort is a provider as it reads in a harness name.
func providerShort(p string) string {
	switch p {
	case "huggingface":
		return "hf"
	case "nvidia":
		return "nim"
	case "":
		return "api"
	}
	return p
}

var familyRe = regexp.MustCompile(`^[a-z]+`)

// modelFamily is the family a model id belongs to: grok-build-0.1 and
// grok-4.7 are grok, gpt-6-luna is gpt, qwen3.5-397b is qwen.
func modelFamily(model string) string {
	m := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	if strings.HasPrefix(m, "gpt-oss") {
		return "gpt-oss" // open weights; not the gpt family OpenAI serves
	}
	if f := familyRe.FindString(m); f != "" {
		return f
	}
	return m
}

var trailingVersionRe = regexp.MustCompile(`-(\d+)-(\d+)$`)

// ModelShort is a model id as it reads on a run: no provider or publisher
// path, lower case, a dashed version dotted (claude-opus-5-5 → opus-5.5).
func ModelShort(id string) string {
	m := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	m = strings.TrimSuffix(m, "-frontier")
	m = strings.TrimPrefix(m, "claude-")
	return trailingVersionRe.ReplaceAllString(m, "-$1.$2")
}

// ModelAt is the model a leg runs at an effort, as it reads on a run. An
// alias that names no version (claude's "opus", "sonnet") is resolved to the
// newest model of that name in the perf feed, so the user reads opus-5.5,
// not opus - the alias always points at the newest release.
func ModelAt(l Leg, e Effort) string {
	id := ModelIDAt(l, e)
	m := ModelShort(id)
	if strings.ContainsAny(m, "0123456789") {
		return m
	}
	alias := strings.TrimSuffix(strings.ToLower(id[strings.LastIndex(id, "/")+1:]), "-frontier")
	if v := newestRelease(alias); v != "" {
		return ModelShort(v)
	}
	// No feed list loaded: the leg's own perf match, when it versions the alias.
	if p, ok := PerfFor(l); ok && strings.HasPrefix(strings.ToLower(p.Slug), alias+"-") {
		return ModelShort(p.Slug)
	}
	return m
}

// newestRelease is the newest perf-feed slug that versions an alias: for
// "claude-sonnet", the latest claude-sonnet-N-M.
func newestRelease(alias string) string {
	models, _, _ := PerfModels()
	best, bestAt, bestIdx := "", "", -1.0
	for _, am := range models {
		s := strings.ToLower(am.Slug)
		if !strings.HasPrefix(s, alias+"-") || !strings.ContainsAny(s[len(alias):], "0123456789") {
			continue
		}
		if am.Released > bestAt || (am.Released == bestAt && am.IntelligenceIndex > bestIdx) {
			best, bestAt, bestIdx = s, am.Released, am.IntelligenceIndex
		}
	}
	return best
}

// RunLabel is how a run reads: harness:model@effort:x. An empty effort is
// the transport's own default.
func RunLabel(l Leg, e Effort) string {
	eff := string(e)
	if eff == "" {
		eff = "default"
	}
	return Harness(l) + ":" + ModelAt(l, e) + "@effort:" + eff
}

// EffortModel is one model a leg runs and the efforts it runs it at.
type EffortModel struct {
	Model   string   `json:"model"`
	Efforts []Effort `json:"efforts"`
}

// displayEfforts are the efforts a leg can be asked for, lowest first.
var displayEfforts = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// EffortModels lists the models a leg runs across the efforts, each with the
// efforts that select it - what the sidebar prints as grok-4.7@high–max.
func EffortModels(l Leg) []EffortModel {
	var out []EffortModel
	for _, e := range displayEfforts {
		m := ModelAt(l, e)
		if n := len(out); n > 0 && out[n-1].Model == m {
			out[n-1].Efforts = append(out[n-1].Efforts, e)
			continue
		}
		out = append(out, EffortModel{Model: m, Efforts: []Effort{e}})
	}
	return out
}
