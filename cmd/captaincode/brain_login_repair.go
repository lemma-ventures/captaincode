package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
)

// loginRepairTimeout is how long the handler waits for the CLI to print
// both a verification URL and a user code before it answers no_code. It is
// a var so a test can shorten the wait.
var loginRepairTimeout = 30 * time.Second

// loginRepairWindow is how long the CLI process may keep waiting for the
// owner to finish in the browser. After the window the process is killed.
const loginRepairWindow = 15 * time.Minute

var (
	loginURL  = regexp.MustCompile(`https://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)
	loginCode = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}(?:-[A-Z0-9]{4})*\b`)
)

// loginRepair runs one CLI's documented device login for the machine owner
// from Galaxy. A passkey-signed request starts the fixed command with a
// minimal environment, and the route returns only the verification URL and
// user code the CLI prints. Neither value is logged, written to a run
// record, or kept after the process ends.
type loginRepair struct {
	mu     sync.Mutex
	active map[string]*loginRun
}

type loginRun struct {
	cancel context.CancelFunc
	state  string // waiting: url+code captured, running: started, done, failed
	url    string
	code   string
	at     time.Time
}

// capture records a parsed URL or code under the repair's lock.
func (r *loginRun) capture(url, code string) {
	if url != "" {
		r.url = url
	}
	if code != "" {
		r.code = code
	}
	if r.url != "" && r.code != "" && r.state == "running" {
		r.state = "waiting"
	}
}

func (l *loginRepair) run(cli string) (*loginRun, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == nil {
		l.active = map[string]*loginRun{}
	}
	if _, busy := l.active[cli]; busy {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginRepairWindow)
	r := &loginRun{cancel: cancel, state: "running", at: time.Now()}
	l.active[cli] = r
	go l.start(ctx, cli, r)
	return r, true
}

// start runs the fixed login command with a minimal environment: the CLI
// gets HOME and PATH and nothing else, so no captain setting, no provider
// key and no CAPTAIN_* variable reaches it. Its output stays in memory,
// bounded, and is discarded when the parse is done.
func (l *loginRepair) start(ctx context.Context, cli string, r *loginRun) {
	argv := pres.LoginRepairCLIs[cli]
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"TERM=dumb",
		"LC_ALL=C",
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		l.finish(cli, "failed")
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if e := cmd.Start(); e != nil {
		pr.Close()
		pw.Close()
		l.finish(cli, "failed")
		return
	}
	pw.Close()
	found := make(chan string, 64)
	go func() {
		scanLines(pr, found)
		_ = pr.Close()
		close(found)
	}()
	go func() {
		_ = cmd.Wait()
		l.mu.Lock()
		defer l.mu.Unlock()
		if r.state == "waiting" || r.state == "running" {
			r.state = "done"
		}
	}()
	deadline := time.After(loginRepairTimeout)
	for {
		select {
		case line, ok := <-found:
			if !ok {
				if r.url == "" || r.code == "" {
					l.finish(cli, "failed")
				}
				return
			}
			url, code := parseLoginLine(line)
			l.mu.Lock()
			r.capture(url, code)
			l.mu.Unlock()
		case <-deadline:
			return
		}
	}
}

func scanLines(src io.Reader, out chan<- string) {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 4096), 16*1024)
	for sc.Scan() {
		out <- sc.Text()
	}
}

// parseLoginLine keeps only a strict URL and code shape. Anything else the
// CLI prints is dropped here, so no other output can leave the process.
func parseLoginLine(line string) (url, code string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	if m := loginURL.FindString(line); m != "" {
		url = m
	}
	if m := loginCode.FindString(strings.ReplaceAll(line, "https://", "https//")); m != "" {
		code = m
	}
	return url, code
}

func (l *loginRepair) finish(cli, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := l.active[cli]; ok {
		r.state = state
	}
}

func (l *loginRepair) status(cli string) (string, int64, string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.active[cli]
	if !ok {
		return "none", 0, "", ""
	}
	return r.state, r.at.Unix(), r.url, r.code
}

// stop kills a running repair and forgets it, so the owner can start a new
// one after a page reload lost the code.
func (l *loginRepair) stop(cli string) bool {
	l.mu.Lock()
	r, ok := l.active[cli]
	if ok {
		delete(l.active, cli)
	}
	l.mu.Unlock()
	if ok {
		r.cancel()
	}
	return ok
}

// loginRepairChallenge answers the challenge a repair assertion must sign.
func (b *brain) loginRepairChallenge(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CLI string `json:"cli"`
	}
	if e := pres.ReadJSON(r.Body, &p); e != nil {
		presenceError(w, e)
		return
	}
	ch, e := pres.DefaultStore().LoginChallenge(p.CLI)
	if e != nil {
		presenceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]string{"challenge": pres.Encode(ch)})
}

// loginRepairStart verifies the owner's passkey and starts the fixed login
// command. The answer carries the URL and code once, to the caller that
// signed for it; the status route never repeats them.
func (b *brain) loginRepairStart(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CLI       string         `json:"cli"`
		Assertion pres.Assertion `json:"assertion"`
	}
	if e := pres.ReadJSON(r.Body, &p); e != nil {
		presenceError(w, e)
		return
	}
	if e := pres.DefaultStore().VerifyLoginRepair(p.CLI, p.Assertion); e != nil {
		presenceError(w, e)
		return
	}
	if _, ok := b.logins.run(p.CLI); !ok {
		writeJSON(w, 409, map[string]string{"error": "repair_busy"})
		return
	}
	deadline := time.Now().Add(loginRepairTimeout)
	for time.Now().Before(deadline) {
		if s, _, url, code := b.logins.status(p.CLI); url != "" && code != "" || s == "failed" || s == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, at, url, code := b.logins.status(p.CLI)
	if url == "" || code == "" {
		fmt.Printf("captain presence: login repair for %s found no code\n", p.CLI)
		b.logins.stop(p.CLI)
		writeJSON(w, 502, map[string]string{"error": "no_code"})
		return
	}
	fmt.Printf("captain presence: login repair for %s started (at %d)\n", p.CLI, at)
	writeJSON(w, 200, map[string]any{"ok": true, "cli": p.CLI, "url": url, "code": code})
}

// loginRepairStatus reports only the state of a repair, never the URL or
// code again: a reader that did not sign gets nothing it can use.
func (b *brain) loginRepairStatus(w http.ResponseWriter, r *http.Request) {
	cli := r.URL.Query().Get("cli")
	if _, ok := pres.LoginRepairCLIs[cli]; !ok {
		writeJSON(w, 403, map[string]string{"error": "unknown_cli"})
		return
	}
	state, at, _, _ := b.logins.status(cli)
	writeJSON(w, 200, map[string]any{"cli": cli, "state": state, "at": at})
}

// loginRepairCancel stops a running repair; it needs the owner's passkey
// too, because it is the one action that disturbs a login in progress.
func (b *brain) loginRepairCancel(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CLI       string         `json:"cli"`
		Assertion pres.Assertion `json:"assertion"`
	}
	if e := pres.ReadJSON(r.Body, &p); e != nil {
		presenceError(w, e)
		return
	}
	if e := pres.DefaultStore().VerifyLoginRepair(p.CLI, p.Assertion); e != nil {
		presenceError(w, e)
		return
	}
	if !b.logins.stop(p.CLI) {
		writeJSON(w, 404, map[string]string{"error": "no_repair"})
		return
	}
	fmt.Printf("captain presence: login repair for %s cancelled\n", p.CLI)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
