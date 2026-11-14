package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// Machine-fleet plan step 5 (F4): a pinned bundle installs on a clean account
// and every artifact is verified by hash before it runs. The manifest is plain
// JSON so the check works offline; its own SHA-256 is printed for comparison
// over a channel the bundle did not travel through.
const bundleManifestName = "bundle.json"
const bundleFormat = 1
const bundleBinaryArtifact = "captain"

type bundleArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

type bundleManifest struct {
	Format    int              `json:"format"`
	Created   string           `json:"created"`
	Revision  string           `json:"revision"`
	Platform  string           `json:"platform"`
	GoVersion string           `json:"go_version"`
	Artifacts []bundleArtifact `json:"artifacts"`
}

func cmdBundle(args []string) {
	if err := runBundle(args); err != nil {
		fatal(err)
	}
}

func runBundle(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: captain bundle create|verify|install <dir> [flags]")
	}
	dir := ""
	if len(args) > 1 {
		dir = args[1]
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("bundle create", flag.ContinueOnError)
		binary := fs.String("binary", "", "wrap this built binary instead of running go build")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if dir == "" || fs.NArg() > 0 {
			return fmt.Errorf("usage: captain bundle create <dir> [--binary <file>]")
		}
		return bundleCreate(dir, *binary)
	case "verify":
		if dir == "" || len(args) != 2 {
			return fmt.Errorf("usage: captain bundle verify <dir>")
		}
		return bundleVerify(dir, os.Stdout)
	case "install":
		fs := flag.NewFlagSet("bundle install", flag.ContinueOnError)
		bin := fs.String("bin", "", "destination path for the captain binary (default ~/.local/bin/captain)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if dir == "" || fs.NArg() > 0 {
			return fmt.Errorf("usage: captain bundle install <dir> [--bin <path>]")
		}
		if err := bundleTerminal(); err != nil {
			return err
		}
		return bundleInstall(dir, *bin)
	default:
		return fmt.Errorf("usage: captain bundle create|verify|install <dir> [flags]")
	}
}

// bundleTerminal keeps install a person's act: a sent turn, a worker or the
// jail never replaces the machine's captain binary.
func bundleTerminal() error {
	if os.Getenv(captaincode.SentTurnEnv) == "1" || os.Getenv("CAPTAIN_JAILED") == "1" || os.Getenv(captaincode.GuardEnv) == "1" {
		return fmt.Errorf("captain bundle install runs from a terminal only")
	}
	return nil
}

func bundleCreate(dir, binary string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	artifact := bundleBinaryArtifact
	if binary != "" {
		if err := copyRegularFile(binary, filepath.Join(dir, artifact), 0o755); err != nil {
			return err
		}
	} else {
		build := exec.Command("go", "build", "-o", filepath.Join(dir, artifact), "./cmd/captaincode")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("go build failed (run it from the captaincode checkout, or pass --binary): %w", err)
		}
	}
	rev, modified, ok := captaincode.GoBuildStamp()
	revField := "unstamped"
	if ok {
		revField = rev
		if modified {
			revField += " (dirty)"
		}
	}
	m := bundleManifest{
		Format:    bundleFormat,
		Created:   time.Now().UTC().Format(time.RFC3339),
		Revision:  revField,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion: runtime.Version(),
		Artifacts: []bundleArtifact{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == bundleManifestName || e.IsDir() {
			continue
		}
		a, err := hashArtifact(dir, e.Name())
		if err != nil {
			return err
		}
		m.Artifacts = append(m.Artifacts, a)
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, bundleManifestName), append(body, '\n'), 0o644); err != nil {
		return err
	}
	digest, _, err := fileSHA256(filepath.Join(dir, bundleManifestName))
	if err != nil {
		return err
	}
	fmt.Printf("bundle: %s (%d artifact(s), revision %s)\n", dir, len(m.Artifacts), m.Revision)
	fmt.Printf("manifest sha256: %s\n", digest)
	fmt.Printf("compare that digest over a channel this bundle did not travel through, then run: captain bundle install %s\n", dir)
	return nil
}

// hashArtifact records the digest, size and permission bits of one regular
// file in the bundle directory. Symlinks and other non-regular files are
// refused: a bundle entry must be the bytes that were hashed.
func hashArtifact(dir, name string) (bundleArtifact, error) {
	if err := bundleArtifactPath(name); err != nil {
		return bundleArtifact{}, err
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return bundleArtifact{}, err
	}
	if !info.Mode().IsRegular() {
		return bundleArtifact{}, fmt.Errorf("bundle entry %s is not a regular file", name)
	}
	digest, size, err := fileSHA256(path)
	if err != nil {
		return bundleArtifact{}, err
	}
	if size != info.Size() {
		return bundleArtifact{}, fmt.Errorf("bundle entry %s changed while hashing", name)
	}
	return bundleArtifact{Path: name, SHA256: digest, Size: size, Mode: uint32(info.Mode().Perm())}, nil
}

// bundleArtifactPath accepts one plain file name: no separators, no parent
// steps, no empty or dot name. A manifest that names anything else is refused
// before it is used.
func bundleArtifactPath(name string) error {
	if name == "" || name == "." || name == ".." || name == bundleManifestName ||
		strings.ContainsAny(name, "/\\") || filepath.Clean(name) != name {
		return fmt.Errorf("bad bundle artifact path %q", name)
	}
	return nil
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func loadBundleManifest(dir string) (bundleManifest, error) {
	var m bundleManifest
	body, err := os.ReadFile(filepath.Join(dir, bundleManifestName))
	if err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("bundle manifest: %w", err)
	}
	if dec.More() {
		return m, fmt.Errorf("bundle manifest has trailing content")
	}
	if m.Format != bundleFormat {
		return m, fmt.Errorf("bundle manifest format %d, expected %d", m.Format, bundleFormat)
	}
	if len(m.Artifacts) == 0 {
		return m, fmt.Errorf("bundle manifest lists no artifacts")
	}
	if _, err := time.Parse(time.RFC3339, m.Created); err != nil {
		return m, fmt.Errorf("bundle manifest created: %w", err)
	}
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		if err := bundleArtifactPath(a.Path); err != nil {
			return m, err
		}
		if seen[a.Path] {
			return m, fmt.Errorf("bundle manifest lists %s twice", a.Path)
		}
		seen[a.Path] = true
		if len(a.SHA256) != 64 {
			return m, fmt.Errorf("bundle artifact %s has no SHA-256", a.Path)
		}
	}
	return m, nil
}

// bundleVerify re-hashes every listed artifact and refuses anything the
// manifest does not name: an extra file, a symlink, a size or permission
// change, a trailing byte. It reads nothing but the directory.
func bundleVerify(dir string, out io.Writer) error {
	m, err := loadBundleManifest(dir)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	for _, a := range m.Artifacts {
		listed[a.Path] = true
		path := filepath.Join(dir, a.Path)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", a.Path)
		}
		if info.Size() != a.Size {
			return fmt.Errorf("%s is %d bytes, manifest says %d", a.Path, info.Size(), a.Size)
		}
		if uint32(info.Mode().Perm()) != a.Mode {
			return fmt.Errorf("%s has mode %v, manifest says %v", a.Path, info.Mode().Perm(), os.FileMode(a.Mode))
		}
		digest, _, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if digest != a.SHA256 {
			return fmt.Errorf("%s hash mismatch: found %s, manifest says %s", a.Path, digest, a.SHA256)
		}
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if !d.Type().IsRegular() && rel != bundleManifestName {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if d.Type().IsRegular() && rel != bundleManifestName && !listed[rel] {
			return fmt.Errorf("%s is present but not listed in the manifest", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	digest, _, err := fileSHA256(filepath.Join(dir, bundleManifestName))
	if err != nil {
		return err
	}
	for _, a := range m.Artifacts {
		fmt.Fprintf(out, "ok  %s  %d bytes\n", a.Path, a.Size)
	}
	fmt.Fprintf(out, "manifest sha256: %s\nrevision %s, built with %s for %s\n", digest, m.Revision, m.GoVersion, m.Platform)
	return nil
}

func bundleInstall(dir, bin string) error {
	if err := bundleVerify(dir, os.Stdout); err != nil {
		return err
	}
	var artifact *bundleArtifact
	m, err := loadBundleManifest(dir)
	if err != nil {
		return err
	}
	for i := range m.Artifacts {
		if m.Artifacts[i].Path == bundleBinaryArtifact {
			artifact = &m.Artifacts[i]
		}
	}
	if artifact == nil {
		return fmt.Errorf("the bundle has no %s artifact", bundleBinaryArtifact)
	}
	if m.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return fmt.Errorf("bundle is for %s, this machine is %s/%s", m.Platform, runtime.GOOS, runtime.GOARCH)
	}
	if bin == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		bin = filepath.Join(home, ".local", "bin", "captain")
	}
	if info, err := os.Lstat(bin); err == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory", bin)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	// Deploy beside, then rename into place: never overwrite the running
	// binary mid-write (INSTALL.md, "Deploy the brain atomically").
	staged := bin + ".bundle-new"
	if err := copyRegularFile(filepath.Join(dir, artifact.Path), staged, os.FileMode(artifact.Mode)); err != nil {
		return err
	}
	if err := os.Rename(staged, bin); err != nil {
		os.Remove(staged)
		return err
	}
	fmt.Printf("installed %s (revision %s) as %s\n", artifact.Path, m.Revision, bin)
	fmt.Println("next: captain init, captain doctor")
	return nil
}

func copyRegularFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
