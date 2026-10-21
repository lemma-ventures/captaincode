package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdPresence(args []string) {
	if e := runPresence(args); e != nil {
		fatal(e)
	}
}
func presenceTerminal() error {
	source := "terminal"
	if os.Getenv(captaincode.SentTurnEnv) == "1" || os.Getenv("CAPTAIN_JAILED") == "1" || os.Getenv(captaincode.GuardEnv) == "1" {
		source = "worker"
	}
	return pres.LocalAuthority(source, pres.TerminalInput())
}
func runPresence(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: captain presence enroll|export|import|list|remove|link|origin|window")
	}
	s := pres.DefaultStore()
	if args[0] != "list" && args[0] != "export" {
		if e := presenceTerminal(); e != nil {
			return e
		}
	}
	switch args[0] {
	case "enroll":
		if len(args) < 2 {
			return fmt.Errorf("usage: captain presence enroll <user> [--origin http://localhost:port]")
		}
		fs := flag.NewFlagSet("presence enroll", flag.ContinueOnError)
		origin := fs.String("origin", "http://localhost:14097", "allowed browser origin")
		if e := fs.Parse(args[2:]); e != nil {
			return e
		}
		u, e := url.Parse(*origin)
		if e != nil || !pres.ValidOrigin(*origin, u.Hostname()) {
			return fmt.Errorf("invalid origin")
		}
		if e = s.Update(func(c *pres.Config) error {
			if !pres.Allowed(c.Origins, *origin) {
				c.Origins = append(c.Origins, *origin)
			}
			return nil
		}); e != nil {
			return e
		}
		code, e := s.EnrollCode(args[1])
		if e != nil {
			return e
		}
		fmt.Printf("Enrollment code (valid 5 minutes): %s\n", code)
		return nil
	case "export":
		raw, e := s.Export()
		if e != nil {
			return e
		}
		fmt.Println(string(raw))
		return nil
	case "import":
		if len(args) != 2 {
			return fmt.Errorf("usage: captain presence import <file>")
		}
		f, e := os.Open(args[1])
		if e != nil {
			return e
		}
		defer f.Close()
		raw, e := io.ReadAll(io.LimitReader(f, pres.MaxBody+1))
		if e != nil {
			return e
		}
		return s.Import(raw)
	case "list":
		c, e := s.Read()
		if e != nil {
			return e
		}
		for _, u := range c.Users {
			for _, k := range u.Credentials {
				fmt.Printf("%s\t%s\t%s\t%s\n", u.Name, k.ID, k.Label, k.Origin)
			}
		}
		return nil
	case "remove":
		if len(args) != 3 {
			return fmt.Errorf("usage: captain presence remove <user> <credential-id>")
		}
		return s.Remove(args[1], args[2])
	case "link":
		if len(args) != 3 || !pres.ValidLabel(args[1]) {
			return fmt.Errorf("usage: captain presence link <label> <folder>")
		}
		dir, e := filepath.Abs(args[2])
		if e != nil {
			return e
		}
		dir, e = filepath.EvalSymlinks(dir)
		if e != nil {
			return e
		}
		st, e := os.Stat(dir)
		if e != nil || !st.IsDir() {
			return fmt.Errorf("folder not found")
		}
		return s.Update(func(c *pres.Config) error {
			if c.Folders == nil {
				c.Folders = map[string]string{}
			}
			if old := c.Folders[args[1]]; old != "" && old != dir {
				return fmt.Errorf("folder label already linked")
			}
			c.Folders[args[1]] = dir
			return nil
		})
	case "origin":
		if len(args) != 2 {
			return fmt.Errorf("usage: captain presence origin <origin>")
		}
		u, e := url.Parse(args[1])
		if e != nil || !pres.ValidOrigin(args[1], u.Hostname()) {
			return fmt.Errorf("invalid origin")
		}
		return s.Update(func(c *pres.Config) error {
			if !pres.Allowed(c.Origins, args[1]) {
				c.Origins = append(c.Origins, args[1])
			}
			return nil
		})
	case "window":
		if len(args) != 2 {
			return fmt.Errorf("usage: captain presence window <seconds>")
		}
		n, e := strconv.ParseInt(args[1], 10, 64)
		if e != nil || n < 1 || n > 604800 {
			return fmt.Errorf("window must be 1 to 604800 seconds")
		}
		return s.Update(func(c *pres.Config) error { c.Window = n; return nil })
	}
	return fmt.Errorf("unknown presence command")
}
func sendSigned(args []string) error {
	if len(args) != 2 || args[0] != "--signed" {
		return fmt.Errorf("usage: captain send --signed <file>; routing and text come from the signed envelope")
	}
	f, e := os.Open(args[1])
	if e != nil {
		return e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, pres.MaxBody+1))
	if e != nil {
		return e
	}
	var p pres.Signed
	if e = pres.ReadJSON(bytes.NewReader(raw), &p); e != nil {
		return e
	}
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := c.Post(brainURL()+"/v1/inbox", "application/json", bytes.NewReader(raw))
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	out, e := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if e != nil {
		return e
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("signed prompt refused: %s", strings.TrimSpace(string(out)))
	}
	fmt.Println(string(out))
	return nil
}
