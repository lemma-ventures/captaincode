package captaincode

// Capability registry (ROADMAP M2.2). Until now a leg's runtime facts were a
// single boolean - Vision - and everything else the router needed to know
// about a worker was implicit in the code that drove it: whether a session
// could be resumed, whether a run in flight could be stopped, whether the
// runtime reported usage at all, whether captain could constrain the worker's
// tools for one run. Those facts decided real behaviour (an accounting row
// marked `unknown`, a cancellation that had to fall back to killing a
// process) and none of them could be asked for, recorded or ranked on.
//
// They are declared here, per TRANSPORT, because that is where they are true:
// a capability belongs to the thing that drives the model, not to the model.
// Swapping grok's pin for another opencode model changes nothing on this
// table; moving it to `claude -p` changes four rows.
//
// Three rules keep the table honest:
//
//   - Support is yes / no / UNKNOWN, and unknown is a real answer. A runtime
//     that might report usage on some models and not others is unknown, not
//     yes - a capability claimed and then absent is worse than one declared
//     doubtful, because the caller planned on it.
//   - Every fact carries its SOURCE. `transport` is this file's contract with
//     the runtime captain actually drives; `registry` is a per-model fact
//     (vision, context) that only the model can answer; `overlay` is the
//     operator's `caps` block in legs.json, which wins because an operator
//     running a patched CLI knows something this build does not.
//   - The set is VERSIONED. A capability answer is part of a decision record
//     (M2.1) and of an evaluation report (M1.3); a reader that does not
//     recognise the version must refuse it rather than read a `no` where a
//     later schema wrote something finer.

import (
	"fmt"
	"sort"
	"strings"
)

// CapabilityVersion is the schema of a CapabilitySet.
const CapabilityVersion = 1

// Capability is one runtime behaviour a task may require of a worker.
type Capability string

const (
	CapTools        Capability = "tools"         // can read/write the repo and run commands, not just emit text
	CapVision       Capability = "vision"        // accepts image input
	CapSessionReuse Capability = "session-reuse" // a follow-up turn can continue the same conversation
	CapCancel       Capability = "cancel"        // a run in flight can be stopped (cooperatively or by killing it - see the note)
	CapUsage        Capability = "usage"         // reports token usage for the turn
	CapCost         Capability = "cost"          // reports the turn's cost in money, not only tokens
	CapPermissions  Capability = "permissions"   // captain can constrain the worker's tool use for one run
)

// Capabilities is every capability in report order.
var Capabilities = []Capability{CapTools, CapVision, CapSessionReuse, CapCancel, CapUsage, CapCost, CapPermissions}

// Support is a three-valued answer. Unknown is never silently read as yes.
type Support string

const (
	SupportYes     Support = "yes"
	SupportNo      Support = "no"
	SupportUnknown Support = "unknown"
)

// Capability fact sources, most to least authoritative on read (overlay wins).
const (
	capSourceTransport = "transport"
	capSourceRegistry  = "registry"
	capSourceOverlay   = "overlay"
)

// CapabilityFact is one answer with its provenance.
type CapabilityFact struct {
	Support Support `json:"support"`
	Source  string  `json:"source"`
	Note    string  `json:"note,omitempty"`
}

// CapabilitySet is everything captain claims to know about how a leg runs.
type CapabilitySet struct {
	Version   int                           `json:"version"`
	Leg       Leg                           `json:"leg"`
	Transport Transport                     `json:"transport"`
	Ctx       int                           `json:"ctx,omitempty"` // context window in tokens; 0 = undeclared
	Facts     map[Capability]CapabilityFact `json:"facts"`
}

// Supports answers one capability. A capability this schema does not define,
// or a leg that is not in the registry, is unknown - never no.
func (c CapabilitySet) Supports(cap Capability) Support {
	f, ok := c.Facts[cap]
	if !ok {
		return SupportUnknown
	}
	return f.Support
}

// transportCaps is the contract with each runtime captain drives, as observed
// against the versions pinned in toolchain.go. Notes say why, because a bare
// `no` invites someone to "fix" a capability the runtime does not have.
var transportCaps = map[Transport]map[Capability]CapabilityFact{
	TransportOpencode: {
		CapTools:        {Support: SupportYes},
		CapSessionReuse: {Support: SupportYes, Note: "the serve session is addressed by id and reused across turns"},
		CapCancel:       {Support: SupportYes, Note: "POST /session/<id>/abort stops the turn without killing the server"},
		CapUsage:        {Support: SupportYes, Note: "the assistant message carries token counts"},
		CapCost:         {Support: SupportUnknown, Note: "reported per provider; absent for the zen/subscription rosters"},
		CapPermissions:  {Support: SupportNo, Note: "tool policy is the fork's session config, not a per-run argument"},
	},
	TransportClaudeCLI: {
		CapTools:        {Support: SupportYes},
		CapSessionReuse: {Support: SupportNo, Note: "each turn is a fresh `claude -p`; captain does not pass --resume"},
		CapCancel:       {Support: SupportYes, Note: "the process is killed - a stop, but not a cooperative one: partial work is whatever reached the disk"},
		CapUsage:        {Support: SupportYes, Note: "the final stream-json result carries input/output/cache tokens"},
		CapCost:         {Support: SupportYes, Note: "total_cost_usd on the result"},
		CapPermissions:  {Support: SupportYes, Note: "--permission-mode selects the mode per run"},
	},
	TransportCursorCLI: {
		CapTools:        {Support: SupportYes},
		CapSessionReuse: {Support: SupportNo, Note: "each turn is a fresh `cursor-agent -p`"},
		CapCancel:       {Support: SupportYes, Note: "the process is killed - a stop, but not a cooperative one: partial work is whatever reached the disk"},
		CapUsage:        {Support: SupportNo, Note: "stream-json reports no token counts - these runs are charged `unknown`"},
		CapCost:         {Support: SupportNo},
		CapPermissions:  {Support: SupportYes, Note: "--trust alone, or --force to auto-allow commands"},
	},
	TransportCodexCLI: {
		CapTools:        {Support: SupportYes},
		CapSessionReuse: {Support: SupportNo, Note: "each turn is a fresh `codex exec`"},
		CapCancel:       {Support: SupportYes, Note: "the process is killed - a stop, but not a cooperative one: partial work is whatever reached the disk"},
		CapUsage:        {Support: SupportYes, Note: "turn.completed carries input/cached/output tokens"},
		CapCost:         {Support: SupportNo, Note: "ChatGPT-subscription quota, not a per-turn price"},
		CapPermissions:  {Support: SupportYes, Note: "--sandbox with approval_policy=never"},
	},
	TransportSystemOne: {
		CapTools:        {Support: SupportNo, Note: "answers typed questions (choice/score/noul); it never reads or writes the repo - a decision leg, not a worker"},
		CapSessionReuse: {Support: SupportNo, Note: "one HTTP request per decision, no session"},
		CapCancel:       {Support: SupportYes, Note: "the HTTP request ends with its context"},
		CapUsage:        {Support: SupportYes, Note: "the response carries input/output token counts"},
		CapCost:         {Support: SupportNo, Note: "tokens, not dollars: the registry prices them (input only - output is free)"},
		CapPermissions:  {Support: SupportNo, Note: "no tools to constrain"},
	},
}

// CapabilitiesFor assembles a leg's capability set: the transport contract,
// then the per-model facts only the registry can answer, then the operator's
// overlay. An unregistered leg answers unknown to everything rather than no.
func CapabilitiesFor(l Leg) CapabilitySet {
	out := CapabilitySet{Version: CapabilityVersion, Leg: l, Facts: map[Capability]CapabilityFact{}}
	s, ok := specs[l]
	if !ok {
		return out
	}
	out.Transport, out.Ctx = s.Transport, s.Ctx
	for c, f := range transportCaps[s.Transport] {
		f.Source = capSourceTransport
		out.Facts[c] = f
	}
	if _, ok := out.Facts[CapTools]; !ok {
		out.Facts[CapTools] = CapabilityFact{Support: SupportUnknown, Source: capSourceTransport, Note: "transport " + string(s.Transport) + " has no declared contract"}
	}
	// Vision is the model's answer, not the transport's, and CAPTAIN_VISION_LEGS
	// may override it - so it is read through the same gate the router uses.
	vision := CapabilityFact{Support: SupportNo, Source: capSourceRegistry}
	if LegSupportsVision(l) {
		vision.Support = SupportYes
	}
	out.Facts[CapVision] = vision
	for c, sup := range s.Caps {
		out.Facts[c] = CapabilityFact{Support: sup, Source: capSourceOverlay, Note: "declared in legs.json"}
	}
	return out
}

// SupportsCap answers one capability for one leg.
func SupportsCap(l Leg, c Capability) Support { return CapabilitiesFor(l).Supports(c) }

// Requirements are what a task needs of whatever runs it. They are HARD
// constraints: a leg that cannot meet them is excluded before any ranking,
// with the reason recorded (M2.1) rather than silently filtered away.
//
// Require is deliberately strict about unknown: a task that says it needs
// cancellation is not served by a runtime that might not have it.
type Requirements struct {
	Vision  bool         `json:"vision,omitempty"`
	MinCtx  int          `json:"min_ctx,omitempty"` // tokens the task must fit in; 0 = no floor
	Require []Capability `json:"require,omitempty"`
}

// Empty reports whether these requirements constrain anything.
func (r Requirements) Empty() bool { return !r.Vision && r.MinCtx == 0 && len(r.Require) == 0 }

// Missing returns why a leg cannot serve these requirements, or "" when it
// can. The sentence is the exclusion reason a decision record carries, so it
// names the capability AND the evidence for the answer.
func (r Requirements) Missing(l Leg) string {
	caps := CapabilitiesFor(l)
	want := r.Require
	if r.Vision {
		want = append(append([]Capability{}, want...), CapVision)
	}
	for _, c := range want {
		switch caps.Supports(c) {
		case SupportYes:
		case SupportNo:
			if c == CapVision {
				return "no vision support and this task carries an image"
			}
			return fmt.Sprintf("no %s support on transport %s", c, caps.Transport)
		default:
			return fmt.Sprintf("%s support unknown on transport %s - not counted as met", c, caps.Transport)
		}
	}
	// An undeclared context window is not a pass: the floor exists because the
	// task does not fit everywhere, and "we don't know" cannot satisfy it.
	if r.MinCtx > 0 {
		if caps.Ctx == 0 {
			return fmt.Sprintf("context window undeclared, task needs %dk", r.MinCtx/1024)
		}
		if caps.Ctx < r.MinCtx {
			return fmt.Sprintf("context %dk below the %dk this task needs", caps.Ctx/1024, r.MinCtx/1024)
		}
	}
	return ""
}

// FilterCapable keeps the legs that meet the requirements, preserving order.
func FilterCapable(order []Leg, r Requirements) []Leg {
	if r.Empty() {
		return order
	}
	out := order[:0:0]
	for _, l := range order {
		if r.Missing(l) == "" {
			out = append(out, l)
		}
	}
	return out
}

// CapabilityTable renders the registry's capabilities for `captain legs caps`.
func CapabilityTable() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s %-11s %9s", "leg", "transport", "ctx")
	for _, c := range Capabilities {
		fmt.Fprintf(&b, " %13s", c)
	}
	b.WriteString("\n")
	mark := func(s Support) string {
		switch s {
		case SupportYes:
			return "yes"
		case SupportNo:
			return "no"
		}
		return "?"
	}
	for _, s := range Registry() {
		caps := CapabilitiesFor(s.ID)
		ctx := "-"
		if caps.Ctx > 0 {
			ctx = fmt.Sprintf("%dk", caps.Ctx/1024)
		}
		fmt.Fprintf(&b, "%-10s %-11s %9s", s.ID, caps.Transport, ctx)
		for _, c := range Capabilities {
			fmt.Fprintf(&b, " %13s", mark(caps.Supports(c)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nschema version " + fmt.Sprint(CapabilityVersion) + "; ? = unknown, which never satisfies a requirement.\n")
	notes := map[string]bool{}
	var lines []string
	for _, s := range Registry() {
		for _, c := range Capabilities {
			f := CapabilitiesFor(s.ID).Facts[c]
			if f.Note == "" {
				continue
			}
			line := fmt.Sprintf("  %-11s %-13s %s", s.Transport, c, f.Note)
			if !notes[line] {
				notes[line] = true
				lines = append(lines, line)
			}
		}
	}
	sort.Strings(lines)
	if len(lines) > 0 {
		b.WriteString("\nwhy:\n" + strings.Join(lines, "\n") + "\n")
	}
	return b.String()
}
