package codex

import (
	"github.com/extractumio/todobem/internal/source"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHome lays out a synthetic Codex home: sessions/<file> for every (id, parent) pair and an
// optional session_index.jsonl of names. Returns the rollout paths by thread id.
func writeHome(t *testing.T, home string, threads [][2]string, names map[string]string) map[string]string {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, th := range threads {
		id, parent := th[0], th[1]
		meta := `{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/synthetic"`
		if parent != "" {
			meta += `,"parent_thread_id":"` + parent + `","agent_path":"/root/` + id + `"`
		}
		meta += "}}\n"
		p := filepath.Join(dir, "rollout-2026-01-01T00-00-00-"+id+".jsonl")
		if err := os.WriteFile(p, []byte(meta), 0600); err != nil {
			t.Fatal(err)
		}
		paths[id] = p
	}
	if len(names) > 0 {
		var b strings.Builder
		for id, name := range names {
			b.WriteString(`{"id":"` + id + `","thread_name":"` + name + `"}` + "\n")
		}
		if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(b.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

// TestIndexSeveralHomes: every configured home is scanned; a rollout present in two homes (a
// tree copied from another machine) is ONE session, taken from the first home — in the list,
// in the descendants, and in the names.
func TestIndexSeveralHomes(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	shared := [][2]string{{"root-a", ""}, {"sub-a1", "root-a"}}
	inFirst := writeHome(t, first, shared, map[string]string{"root-a": "from first"})
	writeHome(t, second, append(shared, [2]string{"root-b", ""}), map[string]string{"root-a": "from second", "root-b": "only second"})

	ix := NewIndex(first, second)
	ix.Scan()
	roots := ix.Roots()
	if len(roots) != 2 {
		t.Fatalf("roots: want root-a and root-b once each, got %d: %+v", len(roots), roots)
	}
	if fm, _ := ix.Get("root-a"); fm.Path != inFirst["root-a"] {
		t.Fatalf("root-a must come from the first home, got %s", fm.Path)
	}
	desc := ix.Descendants("root-a")
	if len(desc) != 1 || desc[0].Path != inFirst["sub-a1"] {
		t.Fatalf("descendants of root-a: want the first home's sub-a1 once, got %+v", desc)
	}
	if ids := ix.IDs(); len(ids) != 3 {
		t.Fatalf("ids: want 3 distinct, got %v", ids)
	}
	a, _ := ix.Get("root-a")
	b, _ := ix.Get("root-b")
	if a.Title != "from first" || b.Title != "only second" {
		t.Fatalf("names: %q %q", a.Title, b.Title)
	}
	if n := ix.SubtreeSize("root-a"); n <= 0 {
		t.Fatalf("subtree size: %d", n)
	}
	// the second home holds root-a too, but it is read from the first: the status says so
	st := ix.HomeStatuses()
	if st[0].Sessions != 1 || st[0].Elsewhere != 0 || st[1].Sessions != 1 || st[1].Elsewhere != 1 {
		t.Fatalf("home statuses: %+v", st)
	}
	sums := source.Summaries(ix, nil)
	if len(sums) != 2 {
		t.Fatalf("summaries: %d", len(sums))
	}
	for _, s := range sums {
		if s.ID == "root-a" && s.Agents != 1 {
			t.Fatalf("root-a agents counted twice: %d", s.Agents)
		}
	}
	// the order of homes decides which copy is the session
	ix2 := NewIndex(second, first)
	ix2.Scan()
	fm, _ := ix2.Get("root-a")
	if !strings.HasPrefix(fm.Path, second+string(filepath.Separator)) {
		t.Fatalf("with the homes swapped root-a must come from the second home, got %s", fm.Path)
	}
	if fm.Title != "from second" {
		t.Fatalf("names follow the home order: %q", fm.Title)
	}
}

// TestIndexSetHomesDropsTheRemovedHome: files of a home that is no longer configured leave the
// index at once, before any rescan; the next Scan adds the new home's files.
func TestIndexSetHomesDropsTheRemovedHome(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeHome(t, first, [][2]string{{"root-a", ""}}, nil)
	writeHome(t, second, [][2]string{{"root-b", ""}}, nil)
	ix := NewIndex(first)
	ix.Scan()
	if _, ok := ix.Get("root-a"); !ok {
		t.Fatal("root-a not indexed")
	}
	ix.SetHomes([]string{second})
	if _, ok := ix.Get("root-a"); ok {
		t.Fatal("root-a still indexed after its home was removed")
	}
	if len(ix.Roots()) != 0 {
		t.Fatalf("roots after SetHomes: %+v", ix.Roots())
	}
	ix.Scan()
	if _, ok := ix.Get("root-b"); !ok {
		t.Fatal("root-b not indexed after the rescan")
	}
	if got := ix.Homes(); len(got) != 1 || got[0] != second {
		t.Fatalf("homes: %v", got)
	}
	// a sibling folder whose name shares a prefix is not "under" a home
	sibling := first + "2"
	writeHome(t, sibling, [][2]string{{"root-c", ""}}, nil)
	ix.SetHomes([]string{first})
	ix.Scan()
	if _, ok := ix.Get("root-c"); ok {
		t.Fatal("a sibling folder with a common prefix was scanned as part of the home")
	}
	dirs := ix.Dirs()
	if len(dirs) != 2 || dirs[0] != filepath.Join(first, "sessions") || dirs[1] != filepath.Join(first, "archived_sessions") {
		t.Fatalf("rollout dirs: %v", dirs)
	}
}

// TestIndexHomeStatuses: the settings page's view of each home — present with sessions,
// present without a sessions/ folder, or missing — with the root threads indexed under it.
func TestIndexHomeStatuses(t *testing.T) {
	ok, bare := t.TempDir(), t.TempDir()
	writeHome(t, ok, [][2]string{{"root-a", ""}, {"sub-a1", "root-a"}, {"root-b", ""}}, nil)
	missing := filepath.Join(t.TempDir(), "not-mounted")
	ix := NewIndex(ok, bare, missing)
	ix.Scan()
	st := ix.HomeStatuses()
	if len(st) != 3 {
		t.Fatalf("statuses: %+v", st)
	}
	if st[0] != (source.HomeStatus{Path: ok, Status: source.HomeOK, Sessions: 2}) {
		t.Errorf("ok home: %+v", st[0])
	}
	if st[1] != (source.HomeStatus{Path: bare, Status: source.HomeNoSessionsDir}) {
		t.Errorf("bare home: %+v", st[1])
	}
	if st[2] != (source.HomeStatus{Path: missing, Status: source.HomeMissing}) {
		t.Errorf("missing home: %+v", st[2])
	}
}
