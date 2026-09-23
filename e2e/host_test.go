//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// host is one isolated installation: its own HOME (so its own ~/.todobem), its own session
// fixtures and its own free ports, kept across restarts so a paired agent stays where the hub
// recorded it.
type host struct {
	name       string
	home       string
	codex      string
	claude     string
	viewerPort int
	agentPort  int
}

func newHost(t *testing.T, name string) *host {
	t.Helper()
	root, err := os.MkdirTemp("", "todobem-e2e-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	h := &host{name: name, home: filepath.Join(root, "home"), viewerPort: freePort(t), agentPort: freePort(t)}
	if err := os.MkdirAll(h.home, 0700); err != nil {
		t.Fatal(err)
	}
	h.codex, h.claude = writeSessions(t, filepath.Join(root, "sessions"))
	return h
}

func (h *host) dir() string  { return filepath.Join(h.home, ".todobem") }
func (h *host) bin() string  { return filepath.Join(h.dir(), "bin", "todobem") }
func (h *host) prev() string { return filepath.Join(h.dir(), "bin", "todobem.prev") }

// env is the environment every process of this host runs with: its HOME, and none of the
// TODOBEM_* overrides of the account the tests run under.
func (h *host) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "TODOBEM_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+h.home)
}

// install runs the published install.sh of a release, as the README tells a user to.
func (h *host) install(t *testing.T, version string) {
	t.Helper()
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/install.sh", *repo, version)
	cmd := exec.Command("sh", "-c", `curl -fsSL "$1" | sh`, "sh", url)
	cmd.Env = append(h.env(), "TODOBEM_VERSION="+version, "TODOBEM_REPO="+*repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh %s: %v\n%s", version, err, out)
	}
	mustContain(t, string(out), "Installed todobem "+version)
}

type result struct {
	code int
	text string
}

// runExit runs a command of this host and returns its exit code and combined output.
func (h *host) runExit(t *testing.T, bin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = h.env()
	cmd.Dir = h.home
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return result{0, string(out)}
	case errors.As(err, &exit):
		return result{exit.ExitCode(), string(out)}
	}
	t.Fatalf("%s %v: %v", bin, args, err)
	return result{}
}

// run is runExit that requires an exit code.
func (h *host) run(t *testing.T, want int, bin string, args ...string) string {
	t.Helper()
	r := h.runExit(t, bin, args...)
	if r.code != want {
		t.Fatalf("%s %s: exit %d, want %d\n%s", filepath.Base(bin), strings.Join(args, " "), r.code, want, r.text)
	}
	return r.text
}

// versionInfo is `todobem version --json`.
type versionInfo struct {
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	StateSchema int    `json:"state_schema"`
}

func (h *host) info(t *testing.T, bin string) versionInfo {
	t.Helper()
	var v versionInfo
	if err := json.Unmarshal([]byte(h.run(t, 0, bin, "version", "--json")), &v); err != nil {
		t.Fatalf("%s version --json: %v", bin, err)
	}
	return v
}

type stateFile struct {
	Schema       int `json:"schema"`
	MigratedFrom *struct {
		Schema int    `json:"schema"`
		Backup string `json:"backup"`
	} `json:"migrated_from"`
}

func (h *host) stateJSON(t *testing.T) stateFile {
	t.Helper()
	var s stateFile
	if err := json.Unmarshal(readFile(t, filepath.Join(h.dir(), "state.json")), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// stateFiles hashes the top-level state files: keys, pairing, settings, state.json, the
// marker. Locks, the cache, snapshots, backups and binaries are not compared.
func (h *host) stateFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(h.dir())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".lock") {
			out[e.Name()] = hashFile(t, filepath.Join(h.dir(), e.Name()))
		}
	}
	return out
}

// binaries hashes the installed binaries.
func (h *host) binaries(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range []string{h.bin(), h.prev()} {
		if exists(p) {
			out[filepath.Base(p)] = hashFile(t, p)
		}
	}
	return out
}

// ---- small helpers

func stopIfFailed(t *testing.T) {
	if t.Failed() {
		t.FailNow()
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func mustContain(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("output lacks %q:\n%s", want, text)
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readFile(t, p), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func hashFile(t *testing.T, p string) string {
	t.Helper()
	sum := sha256.Sum256(readFile(t, p))
	return hex.EncodeToString(sum[:])
}

func sameFiles(t *testing.T, want, got map[string]string) {
	t.Helper()
	for name, h := range want {
		if got[name] != h {
			t.Errorf("%s changed", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s appeared", name)
		}
	}
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(dst, readFile(t, src), mode); err != nil {
		t.Fatal(err)
	}
}

// findArchive is the one release archive in dir (its SHA256SUMS beside it).
func findArchive(t *testing.T, dir string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "todobem_*.tar.gz"))
	if len(m) != 1 || !exists(filepath.Join(dir, "SHA256SUMS")) {
		t.Fatalf("%s: want one archive and its SHA256SUMS, found %v", dir, m)
	}
	return m[0]
}

// tamper copies an archive with its SHA256SUMS and flips one byte of the copy.
func tamper(t *testing.T, archive string) string {
	t.Helper()
	dir := t.TempDir()
	b := readFile(t, archive)
	b = bytes.Clone(b)
	b[len(b)/2] ^= 0xff
	out := filepath.Join(dir, filepath.Base(archive))
	os.WriteFile(out, b, 0600)
	copyFile(t, filepath.Join(filepath.Dir(archive), "SHA256SUMS"), filepath.Join(dir, "SHA256SUMS"), 0600)
	return out
}

// wait polls cond until it holds or the deadline passes.
func wait(t *testing.T, what string, d time.Duration, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %v", what, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
