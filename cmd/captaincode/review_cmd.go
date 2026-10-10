package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdReview(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("usage: captain review capture --archive source.tar.gz --commit <sha> --out evidence.json\n       captain review run --profile profile.json --prompt prompt.txt --tier frontier|quality|cheap --legs primary,backup --out run.json\nOptional capture flag: --include README.md,src/. Optional run flags: --leg <id>, --exclude-vendor <vendor>. See docs/REVIEW-RUNNER.md.")
		return
	}
	if err := executeReview(args); err != nil {
		fatal(err)
	}
}

func readReviewInput(name string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open regular review input")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, errors.New("review input must be a bounded regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("review input read failed or exceeded its bound")
	}
	return b, nil
}

func executeReview(args []string) error {
	if len(args) == 0 {
		return errors.New("review subcommand required")
	}
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("out", "", "new private result file")
	var capture func() (any, error)
	switch args[0] {
	case "capture":
		archive := fs.String("archive", "", "archive file")
		commit := fs.String("commit", "", "full commit")
		include := fs.String("include", "", "comma-separated relative files or directory prefixes ending in slash")
		capture = func() (any, error) {
			b, err := readReviewInput(*archive, 32<<20)
			if err != nil {
				return nil, err
			}
			var scope []string
			if *include != "" {
				scope = strings.Split(*include, ",")
			}
			pack, err := captaincode.CaptureReviewScope(bytes.NewReader(b), *commit, scope)
			if err != nil {
				return nil, err
			}
			return pack, nil
		}
	case "run":
		profile := fs.String("profile", "", "trusted review profile")
		prompt := fs.String("prompt", "", "prompt file")
		tier := fs.String("tier", "", "required tier")
		legs := fs.String("legs", "", "explicit allow-list")
		forced := fs.String("leg", "", "one allowed leg")
		exclude := fs.String("exclude-vendor", "", "vendor to exclude")
		capture = func() (any, error) {
			b, err := readReviewInput(*profile, 64<<10)
			if err != nil {
				return nil, err
			}
			var p captaincode.ReviewProfile
			d := json.NewDecoder(bytes.NewReader(b))
			d.DisallowUnknownFields()
			if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
				return nil, errors.New("invalid review profile JSON")
			}
			text, err := readReviewInput(*prompt, 1<<20)
			if err != nil {
				return nil, err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			run, err := captaincode.RunReview(ctx, p, string(text), captaincode.ReviewOptions{Tier: *tier, Allow: strings.Split(*legs, ","), Forced: *forced, ExcludeVendor: *exclude})
			return run, err
		}
	default:
		return errors.New("unknown review subcommand")
	}
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *out == "" {
		return errors.New("invalid review arguments; use captain review --help")
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("review output must be a new writable file")
	}
	defer f.Close()
	result, runErr := capture()
	if result == nil {
		os.Remove(*out)
		return runErr
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if enc.Encode(result) != nil || f.Sync() != nil {
		return errors.New("review result write failed")
	}
	return runErr
}
