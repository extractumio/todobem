package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// stubSource is an in-memory Source: metas by id, parsers by id.
type stubSource struct {
	name  string
	metas map[string]Meta
	lanes map[string]*stubParser
}

func (s *stubSource) Name() string               { return s.name }
func (s *stubSource) Homes() []string            { return nil }
func (s *stubSource) SetHomes([]string)          {}
func (s *stubSource) HomeStatuses() []HomeStatus { return nil }
func (s *stubSource) Dirs() []string             { return []string{"/" + s.name} }
func (s *stubSource) Scan()                      {}
func (s *stubSource) Get(id string) (Meta, bool) { m, ok := s.metas[id]; return m, ok }
func (s *stubSource) IDs() (out []string) {
	for id := range s.metas {
		out = append(out, id)
	}
	return
}
func (s *stubSource) Roots() (out []Meta) {
	for _, m := range s.metas {
		if m.ParentID == "" {
			out = append(out, m)
		}
	}
	return
}
func (s *stubSource) Descendants(root string) (out []Meta) {
	for _, m := range s.metas {
		if m.ParentID == root {
			out = append(out, m)
		}
	}
	return
}
func (s *stubSource) Open(root string) (*Session, error) {
	m, ok := s.metas[root]
	if !ok {
		return nil, os.ErrNotExist
	}
	return NewSession(s, m, func(m Meta) LaneParser { return s.lanes[m.ID] }), nil
}

// stubParser consumes nothing but records the calls; its lane is whatever the test set.
type stubParser struct {
	lane   *model.Lane
	path   string
	off    int64
	lastTS int64
	err    error
	calls  int
}

func (p *stubParser) Lane() *model.Lane     { return p.lane }
func (p *stubParser) Path() string          { return p.path }
func (p *stubParser) Offset() int64         { return p.off }
func (p *stubParser) LastTS() int64         { return p.lastTS }
func (p *stubParser) Stats() (int64, int64) { return p.off, 0 }
func (p *stubParser) Consume(now int64) error {
	p.calls++
	if p.err != nil {
		err := p.err
		p.err = nil
		return err
	}
	if st, err := os.Stat(p.path); err == nil {
		p.off = st.Size()
	}
	return nil
}

func laneFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lane.jsonl")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRebuildResetsLiveBounds: a lane end a live derivation extended is recomputed from the
// parser's last evidence on every rebuild, and the model's version follows the offsets.
func TestRebuildResetsLiveBounds(t *testing.T) {
	path := laneFile(t, "x\n")
	lane := &model.Lane{ID: "root", Path: "/root", Started: 1000, Ended: 6000, Turns: []*model.Turn{{ID: "t", Start: 1000, End: 4500, Status: "completed"}}}
	p := &stubParser{lane: lane, path: path, lastTS: 4500}
	src := &stubSource{name: "stub", metas: map[string]Meta{"root": {Source: "stub", ID: "root", Path: path, Started: 1000}}, lanes: map[string]*stubParser{"root": p}}
	s, err := src.Open("root")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.Refresh()
	if err != nil || !changed || p.calls != 1 {
		t.Fatalf("refresh: changed=%v err=%v calls=%d", changed, err, p.calls)
	}
	if s.Model.Ended != 4500 || s.Model.Source != "stub" {
		t.Fatalf("rebuild retained live bound or lost the source: %+v", s.Model)
	}
	v1 := s.Version()
	// nothing grew, not live: no rebuild, same version
	if changed, _ := s.Refresh(); changed || s.Version() != v1 {
		t.Fatalf("unchanged file must not rebuild: changed=%v version %s vs %s", changed, s.Version(), v1)
	}
	// the file grew: consumed again, new version
	os.WriteFile(path, []byte("xy\n"), 0600)
	if changed, _ := s.Refresh(); !changed || s.Version() == v1 || p.calls != 2 {
		t.Fatalf("grown file: changed=%v calls=%d", changed, p.calls)
	}
}

// TestRewrittenAndTruncatedLanesRestart: ErrRewritten from a parser, or a file shorter than the
// consumed offset, replaces the lane's parser with a fresh one from the source.
func TestRewrittenAndTruncatedLanesRestart(t *testing.T) {
	path := laneFile(t, "abc\n")
	built := 0
	src := &stubSource{name: "stub", metas: map[string]Meta{"root": {ID: "root", Path: path}}}
	newLane := func(m Meta) LaneParser {
		built++
		return &stubParser{lane: &model.Lane{ID: m.ID, Path: "/root", Started: 1}, path: m.Path, err: map[bool]error{true: ErrRewritten}[built == 1]}
	}
	s := NewSession(src, src.metas["root"], newLane)
	if _, err := s.Refresh(); err != nil || built != 2 {
		t.Fatalf("rewritten lane must be rebuilt once: built=%d err=%v", built, err)
	}
	os.WriteFile(path, []byte("a\n"), 0600) // truncated below the consumed offset
	if _, err := s.Refresh(); err != nil || built != 3 {
		t.Fatalf("truncated lane must be rebuilt: built=%d err=%v", built, err)
	}
}

// TestMultiDispatchesByID: the sessions of every source in one list, an id resolved in the
// source that knows it, the directories of all sources served.
func TestMultiDispatchesByID(t *testing.T) {
	now := time.Now()
	a := &stubSource{name: "a", metas: map[string]Meta{"a1": {Source: "a", ID: "a1", Title: "A one", ModTime: now.Add(-time.Hour)}, "a1-sub": {Source: "a", ID: "a1-sub", ParentID: "a1", Size: 5}}, lanes: map[string]*stubParser{}}
	b := &stubSource{name: "b", metas: map[string]Meta{"b1": {Source: "b", ID: "b1", Title: "B one", ModTime: now}}, lanes: map[string]*stubParser{}}
	m := NewMulti(a, b)
	roots := m.Roots()
	if len(roots) != 2 || roots[0].ID != "b1" || roots[1].ID != "a1" {
		t.Fatalf("roots newest first across sources: %+v", roots)
	}
	if fm, ok := m.Get("a1-sub"); !ok || fm.Source != "a" {
		t.Fatalf("get: %+v %v", fm, ok)
	}
	if d := m.Descendants("a1"); len(d) != 1 || d[0].ID != "a1-sub" {
		t.Fatalf("descendants: %+v", d)
	}
	if ids := m.IDs(); len(ids) != 3 || ids[0] != "a1" {
		t.Fatalf("ids: %v", ids)
	}
	if dirs := m.Dirs(); len(dirs) != 2 || dirs[0] != "/a" || dirs[1] != "/b" {
		t.Fatalf("dirs: %v", dirs)
	}
	if _, err := m.Open("nope"); err == nil {
		t.Fatal("unknown id must not open")
	}
	if m.Source("b") != b || m.Source("c") != nil {
		t.Fatal("source lookup by name")
	}
	sums := m.Summaries(nil)
	if len(sums) != 2 || sums[0].ID != "b1" || sums[0].Source != "b" || sums[1].Source != "a" || sums[1].Agents != 1 || sums[1].Bytes != 5 {
		t.Fatalf("summaries: %+v", sums)
	}
	out, _ := json.Marshal(NewMulti().Summaries(nil))
	if string(out) != "[]" {
		t.Fatalf("empty summaries must be a JSON array: %s", out)
	}
}
