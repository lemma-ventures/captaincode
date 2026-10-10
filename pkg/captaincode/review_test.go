package captaincode

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func reviewArchive(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for i, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(bodies[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReviewCapture(t *testing.T) {
	h := []*tar.Header{
		{Name: "root/AGENTS.md", Mode: 0644, Size: 11},
		{Name: "root/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "root/binary", Mode: 0644, Size: 2},
	}
	archive := reviewArchive(t, h, []string{"ignore this", "", "\x00a"})
	pack, err := CaptureReview(bytes.NewReader(archive), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Files) != 1 || pack.Files[0].Text != "ignore this" || len(pack.Excluded) != 2 || len(pack.ArchiveSHA256) != 64 {
		t.Fatalf("bad pack: %+v", pack)
	}
}

func TestReviewCaptureRejectsHostileArchives(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "root/../escape", "root\\escape", "root/./alias", "root/a\nline", "C:/drive"} {
		t.Run(name, func(t *testing.T) {
			b := reviewArchive(t, []*tar.Header{{Name: name, Size: 1, Mode: 0644}}, []string{"a"})
			if _, err := CaptureReview(bytes.NewReader(b), strings.Repeat("a", 40)); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
		bodies  []string
	}{
		{"duplicate", []*tar.Header{{Name: "root/a", Size: 1}, {Name: "root/a", Size: 1}}, []string{"a", "b"}},
		{"oversize", []*tar.Header{{Name: "root/a", Size: (1 << 20) + 1}}, []string{strings.Repeat("a", (1<<20)+1)}},
		{"case collision", []*tar.Header{{Name: "root/A", Size: 1}, {Name: "root/a", Size: 1}}, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CaptureReview(bytes.NewReader(reviewArchive(t, tc.headers, tc.bodies)), strings.Repeat("b", 40)); err == nil {
				t.Fatal("hostile archive accepted")
			}
		})
	}
	if _, err := CaptureReview(strings.NewReader("not gzip"), strings.Repeat("a", 40)); err == nil {
		t.Fatal("bad gzip accepted")
	}
	if _, err := CaptureReview(bytes.NewReader(reviewArchive(t, nil, nil)), "main"); err == nil {
		t.Fatal("unpinned commit accepted")
	}
}

func TestReviewCaptureCountLimit(t *testing.T) {
	var h []*tar.Header
	var b []string
	for i := 0; i < 1001; i++ {
		h = append(h, &tar.Header{Name: fmt.Sprintf("root/%d", i), Size: 1})
		b = append(b, "a")
	}
	if _, err := CaptureReview(bytes.NewReader(reviewArchive(t, h, b)), strings.Repeat("a", 40)); err == nil {
		t.Fatal("file count limit ignored")
	}
}

func reviewTestLeg(id, url string) ReviewLeg {
	return ReviewLeg{ID: id, Tier: "frontier", Vendor: id, Model: "fixture-model", Endpoint: url, Transport: "chat-completions", Terms: "fixture terms", Retention: "fixture only"}
}

func TestReviewFallbackAndWireContract(t *testing.T) {
	var calls []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		for _, key := range []string{"tools", "functions", "tool_choice"} {
			if _, ok := payload[key]; ok {
				t.Errorf("tool authority in payload: %s", key)
			}
		}
		if payload["max_tokens"] != float64(8192) {
			t.Error("missing output limit")
		}
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/busy" {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"model":"fixture-resolved","choices":[{"finish_reason":"stop","message":{"content":"a draft"}}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
	}))
	defer s.Close()
	p := ReviewProfile{Version: 1, Legs: []ReviewLeg{reviewTestLeg("one", s.URL+"/busy"), reviewTestLeg("two", s.URL+"/ok")}}
	r, err := RunReview(context.Background(), p, "untrusted source", ReviewOptions{Tier: "frontier", Allow: []string{"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "/busy,/ok" || len(r.Attempts) != 2 || r.Attempts[1].Answer != "a draft" || r.Attempts[1].CostUSD != nil || r.Attempts[1].Model != "fixture-resolved" {
		t.Fatalf("bad result %+v", r)
	}
}

func TestReviewSelectionFailsClosed(t *testing.T) {
	for bits := 0; bits < 16; bits++ {
		p := ReviewProfile{Version: 1, Legs: []ReviewLeg{reviewTestLeg("one", "http://127.0.0.1:1")}}
		o := ReviewOptions{Tier: "frontier", Allow: []string{"one"}}
		if bits&1 == 0 {
			o.Allow = []string{"other"}
		}
		if bits&2 == 0 {
			p.Legs[0].Tier = "quality"
		}
		if bits&4 == 0 {
			o.ExcludeVendor = "one"
		}
		if bits&8 == 0 {
			p.Legs[0].Transport = "claude-cli"
		}
		plan, _ := PlanReview(p, o)
		if (len(plan) == 1) != (bits == 15) {
			t.Fatalf("policy conformance bits=%d plan=%v", bits, plan)
		}
	}
	p := ReviewProfile{Version: 1, Legs: []ReviewLeg{reviewTestLeg("one", "http://127.0.0.1:1"), reviewTestLeg("two", "http://127.0.0.1:1")}}
	if _, err := PlanReview(p, ReviewOptions{Tier: "frontier", Allow: []string{"one"}, Forced: "two"}); err == nil {
		t.Fatal("forced leg escaped allow-list")
	}
}

func TestReviewStopsOnUncertainOrInvalidResponse(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[{}]}}]}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"","refusal":"no"}}]}`,
		`not json`,
		strings.Repeat("x", (2<<20)+1),
	} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) }))
		p := ReviewProfile{Version: 1, Legs: []ReviewLeg{reviewTestLeg("one", s.URL), reviewTestLeg("two", s.URL)}}
		r, err := RunReview(context.Background(), p, "source", ReviewOptions{Tier: "frontier", Allow: []string{"one", "two"}})
		s.Close()
		if err == nil || calls != 1 || len(r.Attempts) != 1 {
			t.Fatalf("unsafe fallback: calls=%d err=%v", calls, err)
		}
	}
}

func TestReviewNoRedirectOrRawError(t *testing.T) {
	secret := "fixture-secret-token"
	t.Setenv("REVIEW_TEST_KEY", secret)
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/leak", 302)
			return
		}
		w.WriteHeader(401)
		fmt.Fprint(w, secret)
	}))
	defer s.Close()
	for _, path := range []string{"/redirect", "/error"} {
		calls = 0
		leg := reviewTestLeg("one", s.URL+path)
		leg.AuthEnv = "REVIEW_TEST_KEY"
		r, err := RunReview(context.Background(), ReviewProfile{Version: 1, Legs: []ReviewLeg{leg}}, "source", ReviewOptions{Tier: "frontier", Allow: []string{"one"}})
		b, _ := json.Marshal(r)
		if err == nil || calls != 1 || strings.Contains(string(b), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe record or redirect: calls=%d err=%v", calls, err)
		}
	}
}

func TestReviewAttemptLimit(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(429) }))
	defer s.Close()
	p := ReviewProfile{Version: 1}
	o := ReviewOptions{Tier: "frontier"}
	for i := 0; i < 6; i++ {
		id := fmt.Sprint(i)
		p.Legs = append(p.Legs, reviewTestLeg(id, s.URL))
		o.Allow = append(o.Allow, id)
	}
	r, err := RunReview(context.Background(), p, "source", o)
	if err == nil || calls != 4 || len(r.Attempts) != 4 {
		t.Fatalf("unbounded calls: %d %v", calls, err)
	}
}

func TestReviewRejectsMissingModelAndRedactsEscapedCredential(t *testing.T) {
	t.Setenv("REVIEW_TEST_KEY", "test-secret")
	for _, body := range []string{
		`{"choices":[{"finish_reason":"stop","message":{"content":"draft"}}]}`,
		`{"model":"test\u002dsecret","choices":[{"finish_reason":"stop","message":{"content":"draft"}}]}`,
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		leg := reviewTestLeg("one", s.URL)
		leg.AuthEnv = "REVIEW_TEST_KEY"
		r, err := RunReview(context.Background(), ReviewProfile{Version: 1, Legs: []ReviewLeg{leg}}, "source", ReviewOptions{Tier: "frontier", Allow: []string{"one"}})
		s.Close()
		b, _ := json.Marshal(r)
		if err == nil || strings.Contains(string(b), "test-secret") {
			t.Fatalf("invalid or secret-bearing model accepted: %s", b)
		}
	}
}

func TestReviewCaptureDecompressionAndEncoding(t *testing.T) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	chunk := make([]byte, 1<<20)
	for i := 0; i < 65; i++ {
		if _, err := gz.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureReview(bytes.NewReader(b.Bytes()), strings.Repeat("a", 40)); err == nil {
		t.Fatal("decompression bomb accepted")
	}
	h := []*tar.Header{{Name: "root/a", Size: 1}, {Name: "root/b", Size: 1}, {Name: "root/c", Typeflag: tar.TypeLink, Linkname: "root/a"}}
	p, err := CaptureReview(bytes.NewReader(reviewArchive(t, h, []string{"a", "\xff", ""})), strings.Repeat("a", 40))
	if err != nil || len(p.Excluded) != 2 {
		t.Fatalf("encoding or hard-link handling: %v", err)
	}
}

func TestReviewDoesNotRetryAmbiguousGatewayFailure(t *testing.T) {
	for _, status := range []int{502, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
			defer s.Close()
			p := ReviewProfile{Version: 1, Legs: []ReviewLeg{reviewTestLeg("one", s.URL), reviewTestLeg("two", s.URL)}}
			_, err := RunReview(context.Background(), p, "source", ReviewOptions{Tier: "frontier", Allow: []string{"one", "two"}})
			if err == nil || calls != 1 {
				t.Fatalf("ambiguous provider failure retried: calls=%d", calls)
			}
		})
	}
}

func TestReviewCaptureScopedAndColonNames(t *testing.T) {
	h := []*tar.Header{{Name: "root/README.md", Size: 1}, {Name: "root/src/main.go", Size: 1}, {Name: "root/runs/model:free.json", Size: 1}}
	b := reviewArchive(t, h, []string{"a", "b", "c"})
	p, err := CaptureReview(bytes.NewReader(b), strings.Repeat("a", 40))
	if err != nil || len(p.Files) != 3 {
		t.Fatalf("safe interior colon rejected: %v", err)
	}
	p, err = CaptureReviewScope(bytes.NewReader(b), strings.Repeat("a", 40), []string{"README.md", "src/"})
	if err != nil || len(p.Files) != 2 || len(p.Excluded) != 1 || len(p.Scope) != 2 {
		t.Fatalf("scoped capture lost coverage: %+v %v", p, err)
	}
	if _, err = CaptureReviewScope(bytes.NewReader(b), strings.Repeat("a", 40), []string{"../escape"}); err == nil {
		t.Fatal("unsafe scope accepted")
	}
	if _, err = CaptureReviewScope(bytes.NewReader(b), strings.Repeat("a", 40), []string{"missing"}); err == nil {
		t.Fatal("empty scope accepted")
	}
}

func TestReviewCaptureScopeAcceptsArchiveRootDirectory(t *testing.T) {
	b := reviewArchive(t, []*tar.Header{{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "fixture commit"}}, {Name: "root", Typeflag: tar.TypeDir}, {Name: "root/README.md", Size: 1}}, []string{"", "", "a"})
	p, err := CaptureReviewScope(bytes.NewReader(b), strings.Repeat("a", 40), []string{"README.md"})
	if err != nil || len(p.Files) != 1 {
		t.Fatalf("standard archive root rejected: %v", err)
	}
}
