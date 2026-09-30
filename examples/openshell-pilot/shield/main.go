package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

const limit = 4 << 20

func mask(in []byte) ([]byte, error) {
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
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			out, rep := captaincode.RedactWith(x, "on")
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
	body, err := json.Marshal(value)
	if err != nil || len(body) > limit {
		return nil, fmt.Errorf("masked body exceeds limit")
	}
	return json.Marshal(struct {
		Body       json.RawMessage `json:"body"`
		Secrets    int             `json:"secrets"`
		Identities int             `json:"identities"`
	}{body, secrets, identities})
}

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
	if err != nil || len(data) > limit {
		fmt.Fprintln(os.Stderr, "Shield input exceeds limit")
		os.Exit(1)
	}
	out, err := mask(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(out)
}
