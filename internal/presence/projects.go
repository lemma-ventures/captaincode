package presence

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project discovery and prepare admission (machine fleet plan, step 2).
//
// The owner allows repository roots from a terminal (`captain presence
// allow-root`). A scan lists the git repositories under them by stable ID;
// a remote caller sees the ID and the name, never a host path. A signed
// prompt with the prepare action names the project by that ID; the host
// admits it once (receipt, LM-16), prepares the repository, and only then
// queues the prompt there.

// scanDepth is how deep below a root a scan looks for repositories.
const scanDepth = 3

// scanDirs caps how many directories one scan reads; scanProjects caps how
// many repositories it registers. A misshapen root cannot make a scan cost
// unbounded work.
const (
	scanDirs     = 4096
	scanProjects = 256
)

// admissionKeep is how long a finished admission receipt is kept.
const admissionKeep = 24 * 3600

// ProjectID is the stable ID of a repository directory: the first 16 hex
// characters of the SHA-256 of its cleaned path. It satisfies the label
// rules, so it can travel in the envelope's folder field.
func ProjectID(dir string) string {
	return HashText(filepath.Clean(dir))[:16]
}

// underRoots says whether dir is a root or lies under one.
func underRoots(roots map[string]string, dir string) bool {
	dir = filepath.Clean(dir)
	for _, root := range roots {
		root = filepath.Clean(root)
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AllowRoot adds a directory tree the owner allows, from a terminal only.
// A symlinked root is refused: the tree it names could move. A root inside
// an allowed root, or one that contains it, is refused: one repository
// belongs to exactly one root.
func (s *Store) AllowRoot(dir string) error {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	abs = filepath.Clean(abs)
	info, e := os.Lstat(abs)
	if e != nil {
		return fail("root_not_found")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fail("root_not_directory")
	}
	return s.Update(func(c *Config) error {
		for _, root := range c.Roots {
			root = filepath.Clean(root)
			if abs == root ||
				strings.HasPrefix(abs, root+string(filepath.Separator)) ||
				strings.HasPrefix(root, abs+string(filepath.Separator)) {
				return fail("root_overlaps")
			}
		}
		if c.Roots == nil {
			c.Roots = map[string]string{}
		}
		c.Roots[ProjectID(abs)] = abs
		return nil
	})
}

// ProjectSummary is what a caller outside the host learns about a project:
// its ID, its name and its state. Never its path.
type ProjectSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// ScanProjects walks the allowed roots, registers every git repository it
// finds and lists them by ID. Known projects keep their state and admission
// history; only their name is refreshed. Directories whose name starts with
// a dot are not entered, symlinks are not followed.
func (s *Store) ScanProjects() ([]ProjectSummary, error) {
	type found struct{ dir, name string }
	var repos []found
	for _, root := range sortedRoots(s) {
		dirs := 0
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || len(repos) >= scanProjects || dirs >= scanDirs {
				return fs.SkipAll
			}
			if path != root {
				rel := strings.TrimPrefix(path, root+string(filepath.Separator))
				if strings.Count(rel, string(filepath.Separator)) >= scanDepth {
					return fs.SkipDir
				}
				if strings.HasPrefix(d.Name(), ".") {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if !d.IsDir() {
					return nil
				}
			}
			dirs++
			if _, e := os.Lstat(filepath.Join(path, ".git")); e == nil {
				repos = append(repos, found{dir: path, name: filepath.Base(path)})
			}
			return nil
		})
	}
	out := []ProjectSummary{}
	e := s.Update(func(c *Config) error {
		if len(c.Projects)+len(repos) >= 4096 {
			return fail("capacity")
		}
		if c.Projects == nil {
			c.Projects = map[string]Project{}
		}
		now := nowUnix()
		for _, r := range repos {
			id := ProjectID(r.dir)
			p, known := c.Projects[id]
			if !known {
				p = Project{ID: id, Dir: r.dir, State: "discovered", Binding: BindingVersion, At: now}
			}
			p.Name = r.name
			c.Projects[id] = p
			out = append(out, ProjectSummary{ID: id, Name: p.Name, State: p.State})
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func sortedRoots(s *Store) []string {
	c, e := s.Read()
	if e != nil {
		return nil
	}
	roots := make([]string, 0, len(c.Roots))
	for _, root := range c.Roots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

// Project returns one project record.
func (s *Store) Project(id string) (Project, bool) {
	c, e := s.Read()
	if e != nil {
		return Project{}, false
	}
	p, ok := c.Projects[id]
	return p, ok
}

// AdmitProject records the receipt for one prepare prompt and returns the
// admission's state (LM-16): a new nonce starts initializing; the same nonce
// with the same content returns the recorded state; the same nonce with
// different content is refused.
func (s *Store) AdmitProject(nonce, project, content string) (string, error) {
	var state string
	e := s.Update(func(c *Config) error {
		if c.Projects[project].Dir == "" {
			return fail("unknown_project")
		}
		pruneAdmissions(c, nowUnix())
		if a, ok := c.Admissions[nonce]; ok {
			if a.Project != project || a.Content != content {
				return fail("admission_conflict")
			}
			state = a.State
			return nil
		}
		if len(c.Admissions) >= 1024 {
			return fail("capacity")
		}
		if c.Admissions == nil {
			c.Admissions = map[string]Admission{}
		}
		c.Admissions[nonce] = Admission{Nonce: nonce, Project: project, Content: content, State: "initializing", At: nowUnix()}
		if p := c.Projects[project]; p.State == "discovered" || p.State == "" {
			p.State = "initializing"
			c.Projects[project] = p
		}
		state = "initializing"
		return nil
	})
	return state, e
}

// FinishAdmission sets the state of one admission and its project: ready or
// setup_failed.
func (s *Store) FinishAdmission(nonce, state string) error {
	if state != "ready" && state != "setup_failed" {
		return fail("invalid_state")
	}
	return s.Update(func(c *Config) error {
		a, ok := c.Admissions[nonce]
		if !ok {
			return fail("unknown_admission")
		}
		a.State = state
		a.At = nowUnix()
		c.Admissions[nonce] = a
		if p := c.Projects[a.Project]; p.Dir != "" {
			p.State = state
			c.Projects[a.Project] = p
		}
		return nil
	})
}

func pruneAdmissions(c *Config, now int64) {
	for n, a := range c.Admissions {
		if a.At < now-admissionKeep {
			delete(c.Admissions, n)
		}
	}
}
