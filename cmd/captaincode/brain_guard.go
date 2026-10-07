package main

// The browser guard. The brain listens on 127.0.0.1, which keeps other
// machines out but not a web page: any page the user opens can send a
// request to localhost, and a request that queues a prompt (/v1/inbox) or
// starts a turn (/v1/chat/completions) would run with a worker's full
// permissions (blind review, 2026-10-06). captain's own clients - the CLI,
// the TUI plugin, opencode - send no Origin and no Sec-Fetch-Site; a browser
// always does. So a request that changes something is refused when a browser
// sent it from anywhere but this machine. The Euclid dashboard, a local file,
// keeps its read-only and index routes (corsForDashboard).

import (
	"net/http"
	"net/url"
	"strings"
)

// localOrigin reports an Origin served from this machine: http(s) on
// localhost or 127.0.0.1, compared by host, not by prefix
// ("http://localhost.evil.example" is not local).
func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// dashboardPath: the Euclid dashboard's routes, which a local file page
// (Origin "null") calls.
func dashboardPath(p string) bool { return strings.HasPrefix(p, "/v1/euclid") }

func browserGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		site := strings.ToLower(r.Header.Get("Sec-Fetch-Site"))
		allowed := (origin == "" || localOrigin(origin) || (origin == "null" && dashboardPath(r.URL.Path))) &&
			site != "cross-site" && site != "same-site"
		if !allowed {
			writeErr(w, 403, "captain brain: a request from a web page cannot change anything here")
			return
		}
		next.ServeHTTP(w, r)
	})
}
