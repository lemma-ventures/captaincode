package main

// Runtime director control: HTTP handler + CLI command.
//
//	captain director               # show current director and ladder
//	captain director <leg>         # switch the director to <leg> immediately
//	captain director reset          # back to the env/default director
//
// The brain serves GET/POST /v1/director so the CLI and the TUI can switch
// the director while the brain is running, without a restart.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// directorHTTP handles GET /v1/director (current state) and POST /v1/director
// (switch the director at runtime).
func (b *brain) directorHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.mu.Lock()
		defer b.mu.Unlock()
		ladder := b.ladder()
		writeJSON(w, 200, map[string]any{
			"director":       string(b.effectiveDirector()),
			"primary":        string(ladder[0]),
			"mode":           b.directorModeName(),
			"reason":         b.dirModePick.Reason,
			"ladder":         legStrings(ladder),
			"rung":           b.dirIdx,
			"fallback":       !b.dirFallbackUntil.IsZero() && time.Now().Before(b.dirFallbackUntil),
			"fallback_until": formatTime(b.dirFallbackUntil),
		})
	case http.MethodPost:
		var req struct {
			Director string `json:"director"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]any{"error": "invalid body: " + err.Error()})
			return
		}
		leg := captaincode.Leg(strings.TrimSpace(req.Director))
		if leg == "" {
			writeJSON(w, 400, map[string]any{"error": "director field required"})
			return
		}
		pick, mode, err := b.switchDirector(string(leg))
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		b.mu.Lock()
		ladder := b.ladder()
		b.mu.Unlock()
		writeJSON(w, 200, map[string]any{
			"director": string(pick.Leg),
			"mode":     mode,
			"reason":   pick.Reason,
			"ladder":   legStrings(ladder),
		})
	default:
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
	}
}

// switchDirector is the one entry every surface uses (HTTP, CLI, /captain):
// "reset" → the env/default director; a mode word → that policy; a leg name
// → pinned. Returns the pick, the mode name and a message for the user.
func (b *brain) switchDirector(word string) (captaincode.DirectorPick, string, error) {
	word = strings.ToLower(strings.TrimSpace(word))
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case word == "reset":
		b.dirMode, b.dirModePick = captaincode.DirectorFixed, captaincode.DirectorPick{}
		b.ledger.DirectorMode = ""
		_ = b.ledger.Save()
		b.resetDirector()
		l := b.ladder()
		fmt.Printf("captain brain: director reset to %s\n", l[0])
		return captaincode.DirectorPick{Leg: l[0], Reason: "reset to the env/default director"}, "default", nil
	default:
		if mode, ok := captaincode.ParseDirectorMode(word); ok {
			pick, err := b.setDirectorMode(mode, "")
			if err != nil {
				return pick, string(mode), err
			}
			fmt.Printf("captain brain: director mode %s → %s - %s\n", mode, pick.Leg, pick.Reason)
			return pick, string(mode), nil
		}
		leg := captaincode.Leg(word)
		if !captaincode.KnownLeg(leg) {
			return captaincode.DirectorPick{}, "", fmt.Errorf("%q is neither a leg (%s) nor a mode (frontier|quality|auto|reset)", word, strings.Join(captaincode.LegIDs(), ", "))
		}
		if !captaincode.DirectorCapable(leg) {
			return captaincode.DirectorPick{}, "", fmt.Errorf("%s cannot direct: it runs as an agent with tools (codex exec / cursor-agent), and the director is a judge without them", leg)
		}
		pick, _ := b.setDirectorMode(captaincode.DirectorFixed, leg)
		fmt.Printf("captain brain: director switched to %s\n", leg)
		return pick, "fixed", nil
	}
}

func (b *brain) directorModeName() string {
	if b.dirMode == captaincode.DirectorFixed {
		if strings.HasPrefix(b.ledger.DirectorMode, "fixed:") {
			return "fixed"
		}
		return "default"
	}
	return string(b.dirMode)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func legStrings(legs []captaincode.Leg) []string {
	out := make([]string, len(legs))
	for i, l := range legs {
		out[i] = string(l)
	}
	return out
}

// cmdDirector implements `captain director` and `captain director <leg>`.
func cmdDirector(args []string) {
	c := &http.Client{Timeout: 5 * time.Second}
	if len(args) == 0 {
		resp, err := c.Get("http://127.0.0.1:14097/v1/director")
		if err != nil {
			fatal(fmt.Errorf("brain not reachable: %w", err))
		}
		defer resp.Body.Close()
		var out struct {
			Director      string   `json:"director"`
			Primary       string   `json:"primary"`
			Mode          string   `json:"mode"`
			Reason        string   `json:"reason"`
			Ladder        []string `json:"ladder"`
			Rung          int      `json:"rung"`
			Fallback      bool     `json:"fallback"`
			FallbackUntil string   `json:"fallback_until"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		fmt.Printf("director: %s (%s)\n", out.Director, out.Mode)
		if out.Reason != "" {
			fmt.Printf("  %s\n", out.Reason)
		}
		if out.Fallback && out.Director != out.Primary {
			fmt.Printf("  (fallback from %s, until %s)\n", out.Primary, out.FallbackUntil)
		}
		fmt.Printf("  ladder: %s\n", strings.Join(out.Ladder, " → "))
		fmt.Println("\nswitch: captain director <leg> | frontier | quality | auto | reset")
		fmt.Printf("known legs: %s\n", strings.Join(captaincode.LegIDs(), ", "))
		return
	}
	word := strings.TrimSpace(args[0])
	if _, isMode := captaincode.ParseDirectorMode(word); !isMode && word != "reset" && !captaincode.KnownLeg(captaincode.Leg(word)) {
		fatal(fmt.Errorf("%q is neither a leg (%s) nor a mode (frontier|quality|auto|reset)", word, strings.Join(captaincode.LegIDs(), ", ")))
	}
	body := fmt.Sprintf(`{"director":%q}`, word)
	resp, err := c.Post("http://127.0.0.1:14097/v1/director", "application/json", strings.NewReader(body))
	if err != nil {
		fatal(fmt.Errorf("brain not reachable: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		fatal(fmt.Errorf("brain rejected %q: %s", word, out.Error))
	}
	var out struct {
		Director string   `json:"director"`
		Mode     string   `json:"mode"`
		Reason   string   `json:"reason"`
		Ladder   []string `json:"ladder"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	fmt.Printf("director: %s (%s)\n", out.Director, out.Mode)
	if out.Reason != "" {
		fmt.Printf("  %s\n", out.Reason)
	}
	fmt.Printf("  ladder: %s\n", strings.Join(out.Ladder, " → "))
}

var _ = os.Stdout
