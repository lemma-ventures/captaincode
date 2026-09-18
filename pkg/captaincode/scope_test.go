package captaincode

import (
	"strings"
	"testing"
)

func TestScopeContractFor_ClaudeHasWritePathsAndSubprocess(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	if c.Enforces(ScopeDimWritePaths) != ScopeEnfYes {
		t.Fatalf("claude write-paths: got %s, want yes", c.Enforces(ScopeDimWritePaths))
	}
	if c.Enforces(ScopeDimSubprocess) != ScopeEnfYes {
		t.Fatalf("claude subprocess: got %s, want yes", c.Enforces(ScopeDimSubprocess))
	}
	if c.Enforces(ScopeDimNetwork) != ScopeEnfNo {
		t.Fatalf("claude network: got %s, want no", c.Enforces(ScopeDimNetwork))
	}
}

func TestScopeContractFor_CursorEnforcesNothing(t *testing.T) {
	c := ScopeContractFor(LegCursor)
	for _, d := range ScopeDimensions {
		if c.Enforces(d) != ScopeEnfNo {
			t.Fatalf("cursor %s: got %s, want no (cursor cannot enforce scope)", d, c.Enforces(d))
		}
	}
}

func TestScopeContractFor_UnregisteredLegIsUnknown(t *testing.T) {
	c := ScopeContractFor(Leg("nonexistent"))
	for _, d := range ScopeDimensions {
		if c.Enforces(d) != ScopeEnfUnknown {
			t.Fatalf("unregistered %s: got %s, want unknown", d, c.Enforces(d))
		}
	}
}

func TestCheckScope_FullIsAlwaysSatisfiable(t *testing.T) {
	for _, leg := range []Leg{LegClaude, LegCursor, LegCodex, LegGLM} {
		c := ScopeContractFor(leg)
		v := CheckScope(c, ScopeRequest{Level: ScopeFull})
		if v != nil {
			t.Fatalf("%s full scope: got violation %v, want nil", leg, v)
		}
	}
}

func TestCheckScope_WritePathsRejectsCursor(t *testing.T) {
	c := ScopeContractFor(LegCursor)
	v := CheckScope(c, ScopeRequest{Level: ScopeWritePaths, WritePaths: []string{"src/"}})
	if v == nil {
		t.Fatal("cursor write-paths scope: expected violation, got nil")
	}
	if v.Dimension != ScopeDimWritePaths {
		t.Fatalf("violation dimension: got %s, want %s", v.Dimension, ScopeDimWritePaths)
	}
}

func TestCheckScope_WritePathsAcceptsClaude(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	v := CheckScope(c, ScopeRequest{Level: ScopeWritePaths, WritePaths: []string{"src/"}})
	if v != nil {
		t.Fatalf("claude write-paths scope: got violation %v, want nil", v)
	}
}

func TestCheckScope_WritePathsRejectsEmptyWritePaths(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	v := CheckScope(c, ScopeRequest{Level: ScopeWritePaths})
	if v == nil {
		t.Fatal("write-paths scope with no write_paths: expected violation")
	}
}

func TestCheckScope_StrictRequiresAllDimensions(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	v := CheckScope(c, ScopeRequest{Level: ScopeStrict, WritePaths: []string{"src/"}})
	if v == nil {
		t.Fatal("claude strict scope: expected violation (read-paths is no), got nil")
	}
	if v.Dimension != ScopeDimReadPaths {
		t.Fatalf("violation dimension: got %s, want %s", v.Dimension, ScopeDimReadPaths)
	}
}

func TestCheckScope_StrictRejectsNoNetworkRequest(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	v := CheckScope(c, ScopeRequest{
		Level:      ScopeStrict,
		WritePaths: []string{"src/"},
		NoNetwork:  true,
	})
	if v == nil {
		t.Fatal("claude strict with no-network: expected violation (network enforcement is no)")
	}
}

func TestFormatScopeContract_HasAllDimensions(t *testing.T) {
	c := ScopeContractFor(LegClaude)
	out := FormatScopeContract(c)
	for _, d := range ScopeDimensions {
		if !strings.Contains(out, string(d)) {
			t.Fatalf("format missing dimension %s in:\n%s", d, out)
		}
	}
}
