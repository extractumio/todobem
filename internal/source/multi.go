package source

import (
	"fmt"
	"sort"

	"github.com/extractumio/todobem/internal/model"
)

// Multi is the configured sources as one: sessions of every source in one list, an id looked
// up in the source that knows it. Ids are global and unprefixed (both harnesses use UUIDs;
// Meta.Source names the format of a row). Sources are consulted in registration order.
type Multi struct {
	sources []Source
}

// NewMulti registers sources in the order their sessions tie-break and their ids resolve.
func NewMulti(sources ...Source) *Multi { return &Multi{sources: sources} }

// Sources returns the registered sources in order.
func (m *Multi) Sources() []Source { return m.sources }

// Source returns the registered source of that name, or nil.
func (m *Multi) Source(name string) Source {
	for _, s := range m.sources {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// Scan scans every source.
func (m *Multi) Scan() {
	for _, s := range m.sources {
		s.Scan()
	}
}

// Dirs concatenates every source's directories.
func (m *Multi) Dirs() []string {
	var out []string
	for _, s := range m.sources {
		out = append(out, s.Dirs()...)
	}
	return out
}

// Roots merges the root sessions of every source, newest first.
func (m *Multi) Roots() []Meta {
	var out []Meta
	for _, s := range m.sources {
		out = append(out, s.Roots()...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.After(out[j].ModTime)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// IDs lists every id every source knows.
func (m *Multi) IDs() []string {
	var out []string
	for _, s := range m.sources {
		out = append(out, s.IDs()...)
	}
	sort.Strings(out)
	return out
}

// owner returns the first source that knows id.
func (m *Multi) owner(id string) (Source, Meta, bool) {
	for _, s := range m.sources {
		if fm, ok := s.Get(id); ok {
			return s, fm, true
		}
	}
	return nil, Meta{}, false
}

// Get returns the meta of an id from the source that knows it.
func (m *Multi) Get(id string) (Meta, bool) {
	_, fm, ok := m.owner(id)
	return fm, ok
}

// Descendants returns the sub-agent files of a root from the source that knows it.
func (m *Multi) Descendants(rootID string) []Meta {
	if s, _, ok := m.owner(rootID); ok {
		return s.Descendants(rootID)
	}
	return nil
}

// Open opens a root session in the source that knows it.
func (m *Multi) Open(rootID string) (*Session, error) {
	if s, _, ok := m.owner(rootID); ok {
		return s.Open(rootID)
	}
	return nil, fmt.Errorf("unknown session %s", rootID)
}

// Summaries lists every source's root sessions, newest first.
func (m *Multi) Summaries(opened map[string]*Session) []model.SessionSummary {
	out := []model.SessionSummary{}
	for _, s := range m.sources {
		out = append(out, Summaries(s, opened)...)
	}
	SortSummaries(out)
	return out
}

// SortSummaries orders list rows newest first, ties by id: the one order of the session list,
// whatever sources and hosts the rows came from.
func SortSummaries(rows []model.SessionSummary) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Updated != rows[j].Updated {
			return rows[i].Updated > rows[j].Updated
		}
		return rows[i].ID < rows[j].ID
	})
}
