package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const limit = 4 << 20

var handlePattern = regexp.MustCompile(`\[\[secret:[a-z\-]+:[0-9a-f]{6}\]\]`)

func mask(in []byte) ([]byte, error) {
	return transform(in, false)
}

func transform(in []byte, restore bool) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	secrets, identities := 0, 0
	capabilities := make(map[string]string)
	var scopeErr error
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if restore {
				out, n := captaincode.RestoreIdentity(x)
				identities += n
				return out
			}
			out, rep := captaincode.RedactWith(x, "on")
			for _, handle := range handlePattern.FindAllString(out, -1) {
				if strings.Contains(x, handle) {
					continue
				}
				original, n := captaincode.Restore(handle)
				if n != 1 || !strings.Contains(x, original) {
					scopeErr = fmt.Errorf("masked secret does not match request")
					continue
				}
				capabilities[handle] = original
				if len(capabilities) > 1024 {
					scopeErr = fmt.Errorf("too many request secrets")
				}
			}
			secrets += rep.Secrets
			identities += rep.Identity
			return out
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, v := range x {
				out[walk(k).(string)] = walk(v)
			}
			return out
		}
		return v
	}
	value = walk(value)
	if scopeErr != nil {
		return nil, scopeErr
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > limit {
		return nil, fmt.Errorf("transformed body exceeds limit")
	}
	return json.Marshal(struct {
		Body        json.RawMessage   `json:"body"`
		Secrets     int               `json:"secrets"`
		Identities  int               `json:"identities"`
		ToolSecrets map[string]string `json:"tool_secrets,omitempty"`
	}{body, secrets, identities, capabilities})
}

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	if err != nil || len(data) > limit {
		fmt.Fprintln(os.Stderr, "Shield input exceeds limit")
		os.Exit(1)
	}
	restore := len(os.Args) == 2 && os.Args[1] == "restore-identity"
	if len(os.Args) > 1 && !restore {
		fmt.Fprintln(os.Stderr, "unsupported Shield operation")
		os.Exit(1)
	}
	if restore {
		_ = os.Setenv("CAPTAIN_REDACT", "on")
	}
	out, err := transform(data, restore)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(out)
}
