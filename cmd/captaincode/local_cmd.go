package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdLocal(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: captain local status|qualify|title|commit|digest (text on stdin)"))
	}
	switch args[0] {
	case "status":
		q, e := captaincode.ReadLocalQualification()
		if e != nil {
			fatal(fmt.Errorf("local model not qualified: %w", e))
		}
		_ = json.NewEncoder(os.Stdout).Encode(q)
	case "qualify":
		c, e := captaincode.LocalConfigFromEnv()
		if e != nil {
			fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		q, e := captaincode.QualifyLocal(ctx, c)
		if e != nil {
			fatal(e)
		}
		_ = json.NewEncoder(os.Stdout).Encode(q)
		if !q.Passed {
			fatal(fmt.Errorf("qualification failed; local helper remains disabled"))
		}
	case "title", "commit", "digest":
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
		if e != nil {
			fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, e := captaincode.RunLocalSmall(ctx, args[0], string(raw))
		if e != nil {
			fatal(e)
		}
		fmt.Println(res.Text)
	default:
		fatal(fmt.Errorf("unknown local helper command"))
	}
}
