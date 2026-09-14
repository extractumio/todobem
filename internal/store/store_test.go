package store

import (
	"path/filepath"
	"testing"

	"github.com/extractumio/todobem/internal/model"
)

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
