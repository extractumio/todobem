package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/extractumio/todobem/internal/state"
)

func TestVersionParseAndOrder(t *testing.T) {
	for _, bad := range []string{"", "1.2.3", "v1.2", "v1.2.3.4", "v01.2.3", "v1.2.3-", "v1.-2.3", "v1.2.3-a/b", "vx.y.z"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
	order := []string{"v0.1.0", "v0.1.1-e2e", "v0.1.1", "v0.1.10", "v0.2.0", "v1.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, err := ParseVersion(order[i])
		if err != nil {
			t.Fatal(err)
		}
		if a.Compare(b) != -1 || b.Compare(a) != 1 || b.Compare(b) != 0 {
			t.Errorf("%s < %s does not hold", a, b)
		}
		if b.String() != order[i] {
			t.Errorf("String() = %s, want %s", b, order[i])
		}
	}
}

func TestArchiveNameRoundTrip(t *testing.T) {
	name := ArchiveName("v0.1.1-e2e", "linux", "amd64")
	v, goos, goarch, err := ParseArchiveName(name)
	if err != nil || v.String() != "v0.1.1-e2e" || goos != "linux" || goarch != "amd64" {
		t.Fatalf("ParseArchiveName(%s) = %v %s %s %v", name, v, goos, goarch, err)
	}
	for _, bad := range []string{"todobem_v0.1.0_linux.tar.gz", "other_v0.1.0_linux_amd64.tar.gz", "todobem_v0.1.0_linux_amd64.zip", "todobem_0.1.0_linux_amd64.tar.gz"} {
		if _, _, _, err := ParseArchiveName(bad); err == nil {
			t.Errorf("ParseArchiveName(%q) accepted", bad)
		}
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	sums, err := ParseSums([]byte(h + "  todobem_v0.1.0_linux_amd64.tar.gz\n" + h + " *install.sh\n\n"))
	if err != nil || sums["todobem_v0.1.0_linux_amd64.tar.gz"] != h || sums["install.sh"] != h {
		t.Fatalf("ParseSums = %v, %v", sums, err)
	}
	for _, bad := range []string{"", "nothex  file\n", strings.Repeat("AB", 32) + "  f\n", h + "\n"} {
		if _, err := ParseSums([]byte(bad)); err == nil {
			t.Errorf("ParseSums(%q) accepted", bad)
		}
	}
}

// The redirect of /releases/latest is how every installed version finds its successor.
func TestLatestFromLocation(t *testing.T) {
	v, err := latestFromLocation("o/r", 302, "https://github.com/o/r/releases/tag/v0.1.1")
	if err != nil || v.String() != "v0.1.1" {
		t.Fatalf("latest = %v, %v", v, err)
	}
	for _, c := range []struct {
		status int
		loc    string
	}{
		{302, "https://github.com/o/r/releases"}, // no release published: GitHub redirects to the list
		{404, ""},
		{302, "https://github.com/o/r/releases/tag/latest-build"},
		{200, "https://github.com/o/r/releases/tag/v0.1.1"},
	} {
		if _, err := latestFromLocation("o/r", c.status, c.loc); err == nil {
			t.Errorf("latestFromLocation(%d, %q) accepted", c.status, c.loc)
		}
	}
}

func TestRedirectsStayOnGitHub(t *testing.T) {
	for host, ok := range map[string]bool{"github.com": true, "objects.githubusercontent.com": true, "release-assets.githubusercontent.com": true, "example.com": false, "githubusercontent.com.example.com": false} {
		if githubOwned(host) != ok {
			t.Errorf("githubOwned(%s) = %v", host, !ok)
		}
	}
}

type entry struct {
	name string
	body string
	kind byte
}

func makeArchive(t *testing.T, dir string, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0755, Size: int64(len(e.body)), Typeflag: kind}
		if kind != tar.TypeReg {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractBinaryAcceptsOnlyTheKnownFiles(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out")
	ok := makeArchive(t, dir, entry{name: "todobem", body: "#!bin"}, entry{name: "LICENSE", body: "agpl"}, entry{name: "OFL.txt", body: "ofl"})
	if err := ExtractBinary(ok, dst); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0755 {
		t.Fatalf("binary mode %v", info.Mode().Perm())
	}
	for name, entries := range map[string][]entry{
		"path":      {{name: "../todobem", body: "x"}},
		"subdir":    {{name: "bin/todobem", body: "x"}},
		"extra":     {{name: "todobem", body: "x"}, {name: "evil.sh", body: "x"}},
		"twice":     {{name: "todobem", body: "x"}, {name: "todobem", body: "y"}},
		"symlink":   {{name: "todobem", kind: tar.TypeSymlink}},
		"no binary": {{name: "LICENSE", body: "x"}},
	} {
		d := t.TempDir()
		if err := ExtractBinary(makeArchive(t, d, entries...), filepath.Join(d, "out")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("not gzip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBinary(filepath.Join(dir, "plain"), dst); err == nil {
		t.Error("a non-gzip file was accepted")
	}
}

func TestCheckSum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("release bytes"), 0600)
	sum := sha256.Sum256([]byte("release bytes"))
	good := map[string]string{"f": hex.EncodeToString(sum[:])}
	if err := CheckSum(p, "f", good); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("release bytez"), 0600)
	if err := CheckSum(p, "f", good); !errors.Is(err, ErrChecksum) {
		t.Fatalf("changed byte: %v", err)
	}
	if err := CheckSum(p, "g", good); err == nil {
		t.Fatal("an unlisted file passed")
	}
}

// fakeBinary is a script answering `version --json` like a todobem of that version would.
func fakeBinary(t *testing.T, path, version string, schema int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	script := "#!/bin/sh\necho '{\"version\":\"" + version + "\",\"os\":\"" + runtime.GOOS + "\",\"arch\":\"" + runtime.GOARCH + "\",\"state_schema\":" + string(rune('0'+schema)) + "}'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func newInstall(t *testing.T) Install {
	t.Helper()
	in := Install{Dir: t.TempDir()}
	if err := os.MkdirAll(in.BinDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Prepare(in.Dir); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestApplyThenRollbackTwice(t *testing.T) {
	in := newInstall(t)
	fakeBinary(t, in.Bin(), "v0.1.0", 1)
	if _, _, err := in.Rollback(); err == nil {
		t.Fatal("rolled back a first installation")
	}
	for _, next := range []string{"v0.1.1", "v0.1.2"} { // the second upgrade replaces an existing .prev
		fakeBinary(t, in.New(), next, 1)
		if err := in.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	if v := probeVersion(t, in.Bin()); v != "v0.1.2" {
		t.Fatalf("installed %s", v)
	}
	if v := probeVersion(t, in.Prev()); v != "v0.1.1" {
		t.Fatalf("previous %s", v)
	}
	if _, err := os.Stat(in.New()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staged file is still there")
	}
	prev, restored, err := in.Rollback()
	if err != nil || prev.Version != "v0.1.1" || restored != "" {
		t.Fatalf("Rollback = %+v %q %v", prev, restored, err)
	}
	if probeVersion(t, in.Bin()) != "v0.1.1" || probeVersion(t, in.Prev()) != "v0.1.2" {
		t.Fatal("rollback did not swap")
	}
	if _, _, err := in.Rollback(); err != nil || probeVersion(t, in.Bin()) != "v0.1.2" {
		t.Fatalf("second rollback did not go forward again: %v", err)
	}
}

func TestStageRefusesTheWrongBinary(t *testing.T) {
	in := newInstall(t)
	dir := t.TempDir()
	want, _ := ParseVersion("v0.1.1")
	script := "#!/bin/sh\necho '{\"version\":\"v0.1.9\",\"os\":\"" + runtime.GOOS + "\",\"arch\":\"" + runtime.GOARCH + "\"}'\n"
	if _, err := in.Stage(makeArchive(t, dir, entry{name: "todobem", body: script}), want, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("staged a binary of another version")
	}
	if _, err := in.Stage(makeArchive(t, t.TempDir(), entry{name: "todobem", body: "not an executable"}), want, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("staged a file that cannot run")
	}
	if _, err := os.Stat(in.New()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused candidate was left staged")
	}
}

func probeVersion(t *testing.T, bin string) string {
	t.Helper()
	info, err := Probe(bin)
	if err != nil {
		t.Fatal(err)
	}
	return info.Version
}
