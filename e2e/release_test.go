//go:build e2e

package e2e

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var (
	release  = flag.String("release", "", "the release under test (required)")
	previous = flag.String("previous", "", "the release before it (absent for the first release)")
	repo     = flag.String("repo", "extractumio/todobem", "the GitHub repository the releases are published in")
	dist     = flag.String("dist", "", "directory with next/ and broken/ (required)")
)

func TestRelease(t *testing.T) {
	if *release == "" || *dist == "" {
		t.Fatal("-release and -dist are required")
	}
	next := findArchive(t, filepath.Join(*dist, "next"))
	broken := findArchive(t, filepath.Join(*dist, "broken"))
	h := newHost(t, "fresh")

	t.Run("E1 fresh install", func(t *testing.T) {
		h.install(t, *release)
		v := h.info(t, h.bin())
		if v.Version != *release || v.OS != runtime.GOOS || v.Arch != runtime.GOARCH || v.StateSchema < 1 {
			t.Fatalf("installed %+v", v)
		}
		if exists(h.prev()) {
			t.Fatal("a fresh installation has a previous version")
		}
	})
	stopIfFailed(t)

	t.Run("E2 working product", func(t *testing.T) {
		h.product(t, true)
	})
	stopIfFailed(t)

	t.Run("E4 self-upgrade over the network", func(t *testing.T) {
		out := h.run(t, 0, h.bin(), "upgrade", "-version", *release, "-reinstall")
		mustContain(t, out, "Installed "+*release)
		if h.info(t, h.bin()).Version != *release || h.info(t, h.prev()).Version != *release {
			t.Fatal("the reinstall did not go through apply")
		}
	})
	stopIfFailed(t)

	var before map[string]string
	t.Run("E5 migration", func(t *testing.T) {
		writeJSON(t, filepath.Join(h.dir(), "e2e-marker.json"), map[string]string{"old": "kept"})
		before = h.stateFiles(t)
		out := h.run(t, 0, h.bin(), "upgrade", "-file", next)
		mustContain(t, out, "migrates the state")
		h.product(t, false) // its first start migrates
		st := h.stateJSON(t)
		if st.Schema != h.info(t, h.bin()).StateSchema || st.MigratedFrom == nil || !exists(filepath.Join(h.dir(), st.MigratedFrom.Backup)) {
			t.Fatalf("state after the migration: %+v", st)
		}
		marker := readJSON(t, filepath.Join(h.dir(), "e2e-marker.json"))
		if marker["new"] != "kept" || marker["old"] != nil {
			t.Fatalf("marker after the migration: %v", marker)
		}
	})
	stopIfFailed(t)

	t.Run("E6 rollback", func(t *testing.T) {
		out := h.run(t, 0, h.bin(), "upgrade", "rollback")
		mustContain(t, out, "Restored "+*release)
		if h.info(t, h.bin()).Version != *release {
			t.Fatal("the rollback did not bring the release back")
		}
		sameFiles(t, before, h.stateFiles(t))
		h.product(t, false)
	})
	stopIfFailed(t)

	t.Run("E7 refusals", func(t *testing.T) {
		installed := h.binaries(t)
		out := h.run(t, 4, h.bin(), "upgrade", "-file", broken)
		mustContain(t, out, "error[incompatible]")
		tampered := tamper(t, next)
		out = h.run(t, 6, h.bin(), "upgrade", "-file", tampered)
		mustContain(t, out, "error[verification_failed]")
		copied := filepath.Join(t.TempDir(), "todobem")
		copyFile(t, h.bin(), copied, 0755)
		out = h.run(t, 4, copied, "upgrade", "-file", next)
		mustContain(t, out, "error[not_managed]")
		sameFiles(t, installed, h.binaries(t))

		saved := readFile(t, filepath.Join(h.dir(), "state.json"))
		writeJSON(t, filepath.Join(h.dir(), "state.json"), map[string]int{"schema": 99})
		r := h.runExit(t, h.bin(), "token")
		if r.code == 0 {
			t.Fatal("a binary started on state newer than it reads")
		}
		mustContain(t, r.text, "schema 99")
		os.WriteFile(filepath.Join(h.dir(), "state.json"), saved, 0600)
	})

	t.Run("E3 upgrade from the previous release", func(t *testing.T) {
		if *previous == "" {
			t.Skip("the first release has no previous release")
		}
		p := newHost(t, "previous")
		p.install(t, *previous)
		// The previous release's frozen Latest(): GitHub's /releases/latest names it, since this
		// release is still a prerelease.
		mustContain(t, p.run(t, 0, p.bin(), "upgrade", "check"), "latest "+*previous)
		p.product(t, true)
		before := p.stateFiles(t)
		out := p.run(t, 0, p.bin(), "upgrade", "-version", *release) // the previous binary's updater
		mustContain(t, out, "Installed "+*release)
		if p.info(t, p.bin()).Version != *release {
			t.Fatal("the previous release did not install this one")
		}
		p.product(t, false) // no re-pairing: keys, pairing and settings carried over
		after := p.stateFiles(t)
		delete(before, "state.json")
		delete(after, "state.json")
		if p.info(t, p.prev()).StateSchema == p.info(t, p.bin()).StateSchema {
			sameFiles(t, before, after)
		}
	})
}
