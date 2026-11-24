package presence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rootFixture(t *testing.T) *Store {
	t.Helper()
	s := &Store{Path: filepath.Join(t.TempDir(), "presence.json")}
	if e := s.Update(func(c *Config) error { c.Machine = "m"; return nil }); e != nil {
		t.Fatal(e)
	}
	return s
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); e != nil {
		t.Fatal(e)
	}
}

func TestAllowRootValidates(t *testing.T) {
	s := rootFixture(t)
	base := t.TempDir()
	if e := s.AllowRoot(filepath.Join(base, "missing")); e == nil || Code(e) != "root_not_found" {
		t.Fatal(e)
	}
	if e := s.AllowRoot(base); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(base, "..", "linked-root")
	real := t.TempDir()
	if e := os.Symlink(real, link); e != nil {
		t.Fatal(e)
	}
	if e := s.AllowRoot(link); e == nil || Code(e) != "root_not_directory" {
		t.Fatal(e)
	}
	inner := filepath.Join(base, "inner")
	if e := os.Mkdir(inner, 0o700); e != nil {
		t.Fatal(e)
	}
	if e := s.AllowRoot(inner); e == nil || Code(e) != "root_overlaps" {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e := s.AllowRoot(filepath.Dir(outside)); e == nil || Code(e) != "root_overlaps" {
		t.Fatal(e)
	}
}

func TestScanProjectsFindsGitRepos(t *testing.T) {
	s := rootFixture(t)
	base := t.TempDir()
	gitRepo(t, filepath.Join(base, "alpha"))
	gitRepo(t, filepath.Join(base, "nested", "beta"))
	gitRepo(t, filepath.Join(base, "nope", "deep", "deeper", "gamma"))
	if e := os.MkdirAll(filepath.Join(base, "plain"), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := s.AllowRoot(base); e != nil {
		t.Fatal(e)
	}
	items, e := s.ScanProjects()
	if e != nil {
		t.Fatal(e)
	}
	names := map[string]string{}
	for _, p := range items {
		names[p.Name] = p.State
		if filepath.IsAbs(p.ID) || len(p.ID) != 16 {
			t.Fatalf("bad id %q", p.ID)
		}
	}
	if names["alpha"] != "discovered" || names["beta"] != "discovered" {
		t.Fatal(names)
	}
	if _, ok := names["gamma"]; ok {
		t.Fatal("scan went deeper than its depth bound")
	}
	if _, ok := names["plain"]; ok {
		t.Fatal("a directory without .git became a project")
	}
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	// IDs are stable across scans.
	again, _ := s.ScanProjects()
	if again[0].ID != items[0].ID || again[1].ID != items[1].ID {
		t.Fatal("ids moved between scans")
	}
}

func TestPrepareEnvelopeValidation(t *testing.T) {
	s := rootFixture(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	gitRepo(t, repo)
	if e := s.AllowRoot(base); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ScanProjects(); e != nil {
		t.Fatal(e)
	}
	c, _ := s.Read()
	var id string
	for _, p := range c.Projects {
		id = p.ID
	}
	env := func(folder string, prepare bool) Envelope {
		return Envelope{V: 1, Machine: "m", Folder: folder, TextSHA256: HashText("x"), Nonce: Encode(make([]byte, 32)), Prepare: prepare}
	}
	cRead, _ := s.Read()
	if _, e := cRead.checkEnvelope(env("repo", true), "x", nowUnix()); e == nil || Code(e) != "unknown_project" {
		t.Fatal("nonexistent project accepted")
	}
	if _, e := env(id, true).Hash(); e != nil {
		t.Fatal(e)
	}
	// The prepare flag changes the envelope hash: a signed prompt cannot be
	// retargeted between a linked folder and a project.
	h1, _ := env(id, false).Hash()
	h2, _ := env(id, true).Hash()
	if string(h1) == string(h2) {
		t.Fatal("prepare not bound into the envelope hash")
	}
	// A project whose root was removed is no longer allowed.
	if e := s.Update(func(c *Config) error { delete(c.Roots, ProjectID(base)); return nil }); e != nil {
		t.Fatal(e)
	}
	c3, _ := s.Read()
	if _, e := c3.checkEnvelope(env(id, true), "x", nowUnix()); e == nil || Code(e) != "project_not_allowed" {
		t.Fatal("project outside every root accepted")
	}
}

func TestAdmissionReceipt(t *testing.T) {
	s := rootFixture(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	gitRepo(t, repo)
	if e := s.AllowRoot(base); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ScanProjects(); e != nil {
		t.Fatal(e)
	}
	c, _ := s.Read()
	var id string
	for _, p := range c.Projects {
		id = p.ID
	}
	nonce := Encode(make([]byte, 32))
	st, e := s.AdmitProject(nonce, id, HashText("first"))
	if e != nil || st != "initializing" {
		t.Fatal(st, e)
	}
	if p, _ := s.Project(id); p.State != "initializing" {
		t.Fatal(p.State)
	}
	// Same nonce, same content: the receipt returns its state.
	if st, e = s.AdmitProject(nonce, id, HashText("first")); e != nil || st != "initializing" {
		t.Fatal(st, e)
	}
	// Same nonce, different content: refused.
	if _, e = s.AdmitProject(nonce, id, HashText("edited")); e == nil || Code(e) != "admission_conflict" {
		t.Fatal("edited content admitted")
	}
	if e = s.FinishAdmission(nonce, "ready"); e != nil {
		t.Fatal(e)
	}
	if st, _ = s.AdmitProject(nonce, id, HashText("first")); st != "ready" {
		t.Fatal(st)
	}
	if p, _ := s.Project(id); p.State != "ready" {
		t.Fatal(p.State)
	}
	// A new prompt retries a failed setup.
	n2 := Encode([]byte("second-nonce-32-bytes-aaaaaaaaaa"))
	if _, e = s.AdmitProject(n2, id, HashText("retry")); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishAdmission(n2, "setup_failed"); e != nil {
		t.Fatal(e)
	}
	if p, _ := s.Project(id); p.State != "setup_failed" {
		t.Fatal(p.State)
	}
	if _, e = s.AdmitProject(Encode([]byte("third-nonce-32-bytes-aaaaaaaaaaa")), id, HashText("again")); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishAdmission("not-a-nonce", "ready"); e == nil || Code(e) != "unknown_admission" {
		t.Fatal(e)
	}
}

// A machine with no allowed root has no projects. The list must still be a
// JSON array: Galaxy refuses `null` and showed "Project discovery returned an
// invalid list." instead of an empty list (24 Nov).
func TestScanProjectsWithNoRootIsAnEmptyList(t *testing.T) {
	s := rootFixture(t)
	items, e := s.ScanProjects()
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(map[string]any{"items": items})
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != `{"items":[]}` {
		t.Fatalf("got %s", b)
	}
}
