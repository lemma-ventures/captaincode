package captaincode

// Worker scope contracts (ROADMAP M3.1 remaining). A worktree isolates Git
// changes, not processes, credentials or network access. Until now every
// worktree was full-access: a worker could read any file, write any path,
// make network calls and spawn subprocesses, and the only constraint was a
// prompt instruction the worker might ignore.
//
// A strict scope request must reject an adapter that cannot enforce it,
// rather than rely on prompt instructions. This file declares what each
// transport CAN enforce, so a task that asks for "write only src/" can
// refuse an adapter whose CLI has no path-level write restriction.
//
// The design follows the capability registry's pattern:
//   - declared per transport, because that is where the enforcement lives
//   - three-valued: yes (the CLI flag exists and constrains), no (the
//     transport has no such flag), unknown (not checked)
//   - checked before dispatch, so a rejection is a decision record's
//     exclusion reason, not a mid-run surprise

// ScopeVersion is the schema version of a ScopeContract.
const ScopeVersion = 1

// ScopeLevel is what a task asks for: how tightly the worker must be
// constrained. Higher levels are subsets of lower ones — a strict request
// implies write-paths and read-only are also satisfied.
type ScopeLevel string

const (
	// ScopeFull means no constraints beyond the worktree's filesystem
	// isolation. The worker can read any file, write any path, make
	// network calls and spawn subprocesses. This is the default and the
	// current behaviour for every adapter.
	ScopeFull ScopeLevel = "full"
	// ScopeWritePaths means the worker's writes are restricted to a set
	// of paths the task declares. Reads may still be unrestricted. An
	// adapter that cannot enforce path-level write restrictions (no
	// --allowed-tools or --sandbox-paths flag) cannot satisfy this.
	ScopeWritePaths ScopeLevel = "write-paths"
	// ScopeStrict means the worker is constrained on reads, writes,
	// network and subprocess execution. An adapter that cannot enforce
	// all four cannot satisfy this. The serialized fallback does not
	// help: serializing a worker that ignores its scope is still a
	// worker that ignored its scope.
	ScopeStrict ScopeLevel = "strict"
)

// ScopeEnforcement is one dimension of scope the adapter may or may not
// be able to control. Three-valued, like Support in capability.go.
type ScopeEnforcement string

const (
	ScopeEnfYes     ScopeEnforcement = "yes"     // the CLI flag exists and constrains
	ScopeEnfNo      ScopeEnforcement = "no"      // the transport has no such flag
	ScopeEnfUnknown ScopeEnforcement = "unknown" // not checked
)

// ScopeDimension is one axis of worker behaviour a scope contract may cover.
type ScopeDimension string

const (
	ScopeDimWritePaths ScopeDimension = "write-paths" // can restrict which paths are writable
	ScopeDimReadPaths  ScopeDimension = "read-paths"  // can restrict which paths are readable
	ScopeDimNetwork    ScopeDimension = "network"     // can block network access
	ScopeDimSubprocess ScopeDimension = "subprocess"  // can block or restrict subprocess execution
	ScopeDimToolSet    ScopeDimension = "tool-set"    // can restrict which tools are available
)

// ScopeDimensions is every dimension in report order.
var ScopeDimensions = []ScopeDimension{ScopeDimWritePaths, ScopeDimReadPaths, ScopeDimNetwork, ScopeDimSubprocess, ScopeDimToolSet}

// ScopeFact is one dimension's enforcement answer with provenance.
type ScopeFact struct {
	Enforcement ScopeEnforcement `json:"enforcement"`
	Flag        string           `json:"flag,omitempty"` // the CLI flag that enforces it
	Note        string           `json:"note,omitempty"`
}

// ScopeContract is what a transport can enforce. Declared per transport,
// the same way transportCaps is, because enforcement lives in the CLI.
type ScopeContract struct {
	Version   int                          `json:"version"`
	Transport Transport                    `json:"transport"`
	Facts     map[ScopeDimension]ScopeFact `json:"facts"`
}

// Enforces answers one dimension. A dimension not in the map is unknown.
func (c ScopeContract) Enforces(d ScopeDimension) ScopeEnforcement {
	f, ok := c.Facts[d]
	if !ok {
		return ScopeEnfUnknown
	}
	return f.Enforcement
}

// transportScopes is the contract with each runtime, as observed against the
// pinned versions. A flag that exists but does not actually constrain (e.g.
// --trust on cursor allows everything) is declared no, not yes, because the
// enforcement is the point, not the flag's existence.
var transportScopes = map[Transport]map[ScopeDimension]ScopeFact{
	TransportClaudeCLI: {
		ScopeDimWritePaths: {Enforcement: ScopeEnfYes, Flag: "--allowed-tools + Write/Edit restricted to paths", Note: "permission-mode plan restricts writes; directory allow-lists narrow them further"},
		ScopeDimReadPaths:  {Enforcement: ScopeEnfNo, Note: "no read-path restriction; the worker can read any file in the worktree"},
		ScopeDimNetwork:    {Enforcement: ScopeEnfNo, Note: "no network sandbox; the worker's tools may call out"},
		ScopeDimSubprocess: {Enforcement: ScopeEnfYes, Flag: "--permission-mode", Note: "plan mode requires approval for Bash; ask mode allows it"},
		ScopeDimToolSet:    {Enforcement: ScopeEnfYes, Flag: "--allowed-tools / --disallowed-tools", Note: "tool allow-list restricts which tools the worker can invoke"},
	},
	TransportCodexCLI: {
		ScopeDimWritePaths: {Enforcement: ScopeEnfYes, Flag: "--sandbox", Note: "sandbox mode restricts writes to the project directory; --full-auto widens"},
		ScopeDimReadPaths:  {Enforcement: ScopeEnfNo, Note: "sandbox does not restrict reads"},
		ScopeDimNetwork:    {Enforcement: ScopeEnfNo, Note: "no network sandbox in codex exec"},
		ScopeDimSubprocess: {Enforcement: ScopeEnfYes, Flag: "--sandbox with approval_policy", Note: "sandbox mode gates command execution"},
		ScopeDimToolSet:    {Enforcement: ScopeEnfNo, Note: "no per-tool allow-list; sandbox gates all or none"},
	},
	TransportCursorCLI: {
		ScopeDimWritePaths: {Enforcement: ScopeEnfNo, Note: "--trust and --force control approval, not path scope"},
		ScopeDimReadPaths:  {Enforcement: ScopeEnfNo},
		ScopeDimNetwork:    {Enforcement: ScopeEnfNo},
		ScopeDimSubprocess: {Enforcement: ScopeEnfNo, Note: "--force auto-allows commands but does not restrict them"},
		ScopeDimToolSet:    {Enforcement: ScopeEnfNo},
	},
	TransportOpencode: {
		ScopeDimWritePaths: {Enforcement: ScopeEnfNo, Note: "tool policy is the fork's session config, not a per-run argument"},
		ScopeDimReadPaths:  {Enforcement: ScopeEnfNo},
		ScopeDimNetwork:    {Enforcement: ScopeEnfNo},
		ScopeDimSubprocess: {Enforcement: ScopeEnfNo},
		ScopeDimToolSet:    {Enforcement: ScopeEnfNo, Note: "session config, not per-run"},
	},
}

// ScopeContractFor returns the scope contract for a leg's transport. An
// unregistered leg returns an empty contract (all dimensions unknown).
func ScopeContractFor(l Leg) ScopeContract {
	out := ScopeContract{Version: ScopeVersion, Facts: map[ScopeDimension]ScopeFact{}}
	s, ok := specs[l]
	if !ok {
		return out
	}
	out.Transport = s.Transport
	for d, f := range transportScopes[s.Transport] {
		out.Facts[d] = f
	}
	return out
}

// ScopeRequest is what a task asks the worker's runtime to enforce.
// Empty/zero values mean "no constraint on this dimension." A request
// with Level = ScopeFull asks for nothing and is always satisfiable.
type ScopeRequest struct {
	Level        ScopeLevel `json:"level"`
	WritePaths   []string   `json:"write_paths,omitempty"`   // paths the worker may write to
	ReadPaths    []string   `json:"read_paths,omitempty"`    // paths the worker may read from
	NoNetwork    bool       `json:"no_network,omitempty"`    // block network access
	NoSubprocess bool       `json:"no_subprocess,omitempty"` // block subprocess execution
}

// ScopeViolation describes why a transport cannot satisfy a scope request.
// A nil violation means the request is satisfiable.
type ScopeViolation struct {
	Dimension ScopeDimension `json:"dimension"`
	Reason    string         `json:"reason"`
}

// CheckScope returns nil when the contract can satisfy the request, or the
// first violation when it cannot. The check is conservative: unknown
// enforcement is treated as "cannot satisfy" for strict and write-paths
// requests, because relying on a constraint the adapter may not enforce is
// worse than refusing the adapter and picking one that can.
func CheckScope(contract ScopeContract, req ScopeRequest) *ScopeViolation {
	if req.Level == "" || req.Level == ScopeFull {
		return nil
	}
	if req.Level == ScopeWritePaths || req.Level == ScopeStrict {
		if v := checkDimension(contract, ScopeDimWritePaths); v != nil {
			return v
		}
		if len(req.WritePaths) == 0 {
			return &ScopeViolation{Dimension: ScopeDimWritePaths, Reason: "write-paths scope requested but no write_paths declared"}
		}
	}
	if req.Level == ScopeStrict {
		for _, d := range []ScopeDimension{ScopeDimReadPaths, ScopeDimNetwork, ScopeDimSubprocess} {
			if v := checkDimension(contract, d); v != nil {
				return v
			}
		}
		if req.NoNetwork && contract.Enforces(ScopeDimNetwork) != ScopeEnfYes {
			return &ScopeViolation{Dimension: ScopeDimNetwork, Reason: "network block requested but transport cannot enforce it"}
		}
		if req.NoSubprocess && contract.Enforces(ScopeDimSubprocess) != ScopeEnfYes {
			return &ScopeViolation{Dimension: ScopeDimSubprocess, Reason: "subprocess block requested but transport cannot enforce it"}
		}
	}
	return nil
}

// checkDimension returns a violation when the transport cannot enforce the
// dimension. Unknown is treated as cannot-enforce, because a constraint that
// might not be enforced is not a constraint.
func checkDimension(contract ScopeContract, d ScopeDimension) *ScopeViolation {
	switch contract.Enforces(d) {
	case ScopeEnfYes:
		return nil
	case ScopeEnfNo:
		return &ScopeViolation{Dimension: d, Reason: string(d) + " enforcement declared no for transport " + string(contract.Transport)}
	default:
		return &ScopeViolation{Dimension: d, Reason: string(d) + " enforcement unknown for transport " + string(contract.Transport)}
	}
}

// FormatScopeContract renders a scope contract for `captain legs scope` or
// `captain doctor`. One line per dimension, with the flag when enforcement
// is yes.
func FormatScopeContract(c ScopeContract) string {
	var out string
	out += string(c.Transport) + ":\n"
	for _, d := range ScopeDimensions {
		f, ok := c.Facts[d]
		if !ok {
			out += "  " + string(d) + ": unknown\n"
			continue
		}
		line := "  " + string(d) + ": " + string(f.Enforcement)
		if f.Flag != "" {
			line += " (" + f.Flag + ")"
		}
		if f.Note != "" {
			line += " — " + f.Note
		}
		out += line + "\n"
	}
	return out
}
