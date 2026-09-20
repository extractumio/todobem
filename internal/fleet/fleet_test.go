package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/extractumio/todobem/internal/model"
)

func TestSplitJoinAndNames(t *testing.T) {
	id := Join("aaaa-1111", "web-01")
	uuid, host, ok := Split(id)
	if !ok || uuid != "aaaa-1111" || host != "web-01" {
		t.Fatalf("split %s: %s %s %v", id, uuid, host, ok)
	}
	for _, local := range []string{"aaaa-1111", "", "@", "x@", "@x"} {
		if _, _, ok := Split(local); ok {
			t.Fatalf("%q read as remote", local)
		}
	}
	for name, want := range map[string]bool{"web-01": true, "a": true, "Web": false, "web.example": false, "-x": false, "": false, strings.Repeat("a", 63): true, strings.Repeat("a", 64): false} {
		if ValidName(name) != want {
			t.Fatalf("ValidName(%q) = %v", name, !want)
		}
	}
	if got := DefaultName("Web-01.example.com"); got != "web-01" {
		t.Fatalf("DefaultName: %q", got)
	}
	if got := DefaultName("___"); got != "agent" {
		t.Fatalf("DefaultName of nothing usable: %q", got)
	}
}

func TestPairingString(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	token := strings.Repeat("A", 52)
	s := FormatPairing("web-01", "7789", pin, token)
	p, err := ParsePairing(" " + s + "\n")
	if err != nil || p.Addr != "web-01:7789" || p.Pin != pin || p.Token != token {
		t.Fatalf("%v %+v", err, p)
	}
	for _, bad := range []string{"", "https://x/#a.b", "todobem-agent://web-01:7789/#short.token", "todobem-agent:///#" + pin + "." + token} {
		if _, err := ParsePairing(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if p, err := ParsePairing("todobem-agent://host/#" + pin + "." + token); err != nil || p.Addr != "host:7789" {
		t.Fatalf("default port: %v %+v", err, p)
	}
}

func row(id string, updated, bytes int64) model.SessionSummary {
	return model.SessionSummary{ID: id, Updated: updated, Bytes: bytes, Title: "t"}
}

func TestTrackerDeltas(t *testing.T) {
	tr := NewTracker()
	rows := []model.SessionSummary{row("a", 1, 10), row("b", 2, 20)}
	full := tr.Page("", rows)
	if !full.Full || len(full.Rows) != 2 || full.Count != 2 || full.IDsHash != IDsHash([]string{"b", "a"}) {
		t.Fatalf("first page: %+v", full)
	}
	same := tr.Page(full.Cursor, rows)
	if same.Full || len(same.Rows) != 0 || same.Cursor != full.Cursor {
		t.Fatalf("nothing changed: %+v", same)
	}
	rows[1].Bytes = 25
	rows = append(rows, row("c", 3, 30))
	delta := tr.Page(full.Cursor, rows)
	if delta.Full || len(delta.Rows) != 2 || delta.Count != 3 {
		t.Fatalf("delta: %+v", delta)
	}
	got := map[string]bool{}
	for _, r := range delta.Rows {
		got[r.ID] = true
	}
	if !got["b"] || !got["c"] {
		t.Fatalf("delta rows: %v", got)
	}
	// a removed row: no tombstone, the hash and count change
	rows = rows[1:]
	after := tr.Page(delta.Cursor, rows)
	if after.Full || len(after.Rows) != 0 || after.Count != 2 || after.IDsHash == delta.IDsHash {
		t.Fatalf("after a removal: %+v", after)
	}
	if p := tr.Page("otherboot:3", rows); !p.Full {
		t.Fatal("a cursor of another boot must be a full page")
	}
	if p := tr.Page("garbage", rows); !p.Full {
		t.Fatal("a malformed cursor must be a full page")
	}
	if IDsHash([]string{"b", "a"}) != IDsHash([]string{"a", "b"}) || len(IDsHash(nil)) != 16 {
		t.Fatal("IDsHash must be order-independent and 16 hex")
	}
}

func TestAgentsFileAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	agents, mod, err := LoadAgents(path)
	if err != nil || len(agents) != 0 || !mod.IsZero() {
		t.Fatalf("missing file: %v %d", err, len(agents))
	}
	want := []Agent{{Name: "b", Addr: "b:7789", Pin: "p", Bearer: "secret-b"}, {Name: "a", Addr: "a:7789", Pin: "p", Bearer: "secret-a"}}
	if err := SaveAgents(path, want); err != nil {
		t.Fatal(err)
	}
	if err := Record(path, Agent{Name: "a", Addr: "a:7790", Pin: "p", Bearer: "secret-a2"}); err != nil {
		t.Fatal(err)
	}
	if err := Forget(path, "nobody"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("agents file mode %o", st.Mode().Perm())
	}
	agents, _, err = LoadAgents(path)
	if err != nil || len(agents) != 2 || agents[0].Name != "a" || agents[0].Addr != "a:7790" || agents[1].Bearer != "secret-b" {
		t.Fatalf("round trip: %v %+v", err, agents)
	}
	if err := Forget(path, "b"); err != nil {
		t.Fatal(err)
	}
	if agents, _, _ = LoadAgents(path); len(agents) != 1 || agents[0].Name != "a" {
		t.Fatalf("after Forget: %+v", agents)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAgents(path); err == nil || !strings.Contains(err.Error(), "readable by other users") {
		t.Fatalf("an open agents file must be refused: %v", err)
	}
	snapDir := filepath.Join(dir, "fleet")
	snap := Snapshot{Cursor: "boot:3", Rows: []model.SessionSummary{row("x", 1, 2)}, Facts: map[string]int64{"x": 1}}
	if err := SaveSnapshot(snapDir, "a", snap); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSnapshot(snapDir, "a")
	if err != nil || got.Cursor != "boot:3" || len(got.Rows) != 1 || got.Facts["x"] != 1 {
		t.Fatalf("snapshot round trip: %v %+v", err, got)
	}
	if ids := SnapshotIDs(snapDir); len(ids) != 1 || ids[0] != "x@a" {
		t.Fatalf("snapshot ids: %v", ids)
	}
	if err := RemoveSnapshot(snapDir, "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadSnapshot(snapDir, "a"); len(got.Rows) != 0 {
		t.Fatal("snapshot not removed")
	}
}

func TestCertAndPin(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := CertPaths(filepath.Join(dir, "agent.key"))
	cert, pin, err := LoadOrCreateCert(certPath, keyPath)
	if err != nil || len(pin) != 64 || len(cert.Certificate) == 0 {
		t.Fatalf("%v pin=%q", err, pin)
	}
	again, pin2, err := LoadOrCreateCert(certPath, keyPath)
	if err != nil || pin2 != pin || len(again.Certificate) == 0 {
		t.Fatalf("second load minted a new certificate: %v %q", err, pin2)
	}
	if p, err := CertPin(certPath); err != nil || p != pin {
		t.Fatalf("CertPin: %v %q", err, p)
	}
	st, _ := os.Stat(keyPath)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("tls key mode %o", st.Mode().Perm())
	}
}
