package store

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/extractumio/todobem/internal/model"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGunzipJSONLimitsExpandedBody(t *testing.T) {
	var out map[string]string
	b := gzipBytes(t, []byte(`{"value":"`+string(bytes.Repeat([]byte{'x'}, 128))+`"}`))
	if err := gunzipJSON(b, &out, 64); err == nil {
		t.Fatal("oversized expanded JSON accepted")
	}
	if err := gunzipJSON(b, &out, 256); err != nil || len(out["value"]) != 128 {
		t.Fatalf("bounded decode: %v %#v", err, out)
	}
}

func sampleModel() *model.Session {
	op := &model.Operation{ID: "op1", Phase: "code", Kind: "read", Title: "cat x", Detail: "cat /very/long/path x.go"}
	return &model.Session{ID: "s1", Version: "v1", Lanes: []*model.Lane{{ID: "root", Path: "/root", Ops: []*model.Operation{op}}}}
}

func TestSaveLoadRoundTripReattachesDetail(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	fp := Fingerprint{Rules: "r1", Files: []FileFP{{Path: "a", Size: 10, Mod: 1}}}
	if err := st.Save("s1", sampleModel(), fp); err != nil {
		t.Fatal(err)
	}
	m, gotFP, ok := st.Load("s1")
	if !ok {
		t.Fatal("Load miss after Save")
	}
	if !gotFP.Equal(fp) {
		t.Errorf("fingerprint round-trip: %+v != %+v", gotFP, fp)
	}
	// Detail is json:"-" on the op, so it must come back via the sidecar map.
	if got := m.Lanes[0].Ops[0].Detail; got != "cat /very/long/path x.go" {
		t.Errorf("Detail not reattached: %q", got)
	}
}

func TestFingerprintEqual(t *testing.T) {
	a := Fingerprint{Rules: "r", Files: []FileFP{{Path: "x", Size: 1, Mod: 2}}}
	if !a.Equal(Fingerprint{Rules: "r", Files: []FileFP{{Path: "x", Size: 1, Mod: 2}}}) {
		t.Error("equal fingerprints reported unequal")
	}
	// Any change — a rule-hash change, a grown file, a new file — must differ.
	for _, b := range []Fingerprint{
		{Rules: "r2", Files: a.Files},
		{Rules: "r", Files: []FileFP{{Path: "x", Size: 2, Mod: 2}}},
		{Rules: "r", Files: []FileFP{{Path: "x", Size: 1, Mod: 2}, {Path: "y", Size: 1, Mod: 1}}},
	} {
		if a.Equal(b) {
			t.Errorf("expected inequality vs %+v", b)
		}
	}
}

func TestDisabledStoreAndMiss(t *testing.T) {
	if _, _, ok := New("").Load("s1"); ok {
		t.Error("disabled store returned a hit")
	}
	if err := New("").Save("s1", sampleModel(), Fingerprint{}); err != nil {
		t.Errorf("disabled Save should be a no-op, got %v", err)
	}
	if _, _, ok := New(t.TempDir()).Load("missing"); ok {
		t.Error("missing id returned a hit")
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName("01a0-9218_abc"); got != "01a0-9218_abc" {
		t.Errorf("plain id changed: %q", got)
	}
	if got := safeName("../escape"); got == "../escape" || filepath.Base(got) != got {
		t.Errorf("path-y id not neutralized: %q", got)
	}
}

func TestPruneKeepsKnownEntriesAndReportsSize(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	fp := Fingerprint{Rules: "r1"}
	for _, id := range []string{"known", "other-home", "weird/id"} {
		if err := st.Save(id, sampleModel(), fp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "known.json.gz.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if files, bytes := st.Size(); files != 3 || bytes <= 0 {
		t.Fatalf("size before: %d files, %d bytes", files, bytes)
	}
	removed, freed, err := st.Prune([]string{"known", "weird/id"})
	if err != nil {
		t.Fatal(err)
	}
	// the entry of another home and the leftover temp file go; unrelated files stay
	if removed != 2 || freed <= 0 {
		t.Fatalf("removed=%d freed=%d", removed, freed)
	}
	if _, _, ok := st.Load("known"); !ok {
		t.Error("the known entry was removed")
	}
	if _, _, ok := st.Load("weird/id"); !ok {
		t.Error("the hashed-name entry was removed")
	}
	if _, _, ok := st.Load("other-home"); ok {
		t.Error("the unknown entry survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("an unrelated file was removed")
	}
	if files, _ := st.Size(); files != 2 {
		t.Errorf("size after: %d files", files)
	}
	if n, _, err := New("").Prune(nil); n != 0 || err != nil {
		t.Errorf("disabled store: %d, %v", n, err)
	}
}

func TestSidecarRoundTripVersionAndPrune(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	fp := Fingerprint{Rules: "r1", Files: []FileFP{{Path: "a", Size: 1, Mod: 1}}}
	type rec struct {
		N int    `json:"n"`
		S string `json:"s"`
	}
	if err := st.SaveSidecar("facts", "s1", 3, fp, rec{N: 7, S: "x"}); err != nil {
		t.Fatal(err)
	}
	var got rec
	gotFP, ok := st.LoadSidecar("facts", "s1", 3, &got)
	if !ok || got.N != 7 || got.S != "x" || !gotFP.Equal(fp) {
		t.Fatalf("round trip: ok=%v got=%+v fp=%+v", ok, got, gotFP)
	}
	if _, ok := st.LoadSidecar("facts", "s1", 4, &got); ok {
		t.Error("another version must miss")
	}
	if _, ok := st.LoadSidecar("other", "s1", 3, &got); ok {
		t.Error("another kind must miss")
	}
	if _, ok := New("").LoadSidecar("facts", "s1", 3, &got); ok || New("").SaveSidecar("facts", "s1", 3, fp, got) != nil {
		t.Error("disabled store must miss and no-op")
	}
	// prune keeps the sidecars of known ids and drops the rest
	if err := st.SaveSidecar("facts", "gone", 3, fp, got); err != nil {
		t.Fatal(err)
	}
	if removed, _, err := st.Prune([]string{"s1"}); err != nil || removed != 1 {
		t.Fatalf("prune removed=%d err=%v", removed, err)
	}
	if _, ok := st.LoadSidecar("facts", "s1", 3, &got); !ok {
		t.Error("the known id's sidecar was pruned")
	}
	if _, ok := st.LoadSidecar("facts", "gone", 3, &got); ok {
		t.Error("the unknown id's sidecar survived")
	}
}
