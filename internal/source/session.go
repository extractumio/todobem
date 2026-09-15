package source

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// Session is an opened root session with all its sub-agent lanes, refreshed incrementally.
// It is the same joiner for every source: the source supplies a LaneParser per file, the
// Session discovers new sub-agent files, feeds every parser the bytes its file grew by,
// restarts a lane whose file was rewritten or truncated, and re-derives the model.
type Session struct {
	mu      sync.Mutex
	src     Source
	rootID  string
	root    Meta
	newLane func(Meta) LaneParser
	parsers map[string]LaneParser // by lane id
	order   []string              // lane order: root first, then by start
	Model   *model.Session
	lastRef time.Time
}

// NewSession creates the session for a root file: the model carries the meta's identity, the
// first lane is the root's, nothing is parsed until Refresh. newLane builds the parser of one
// lane file (root or sub-agent).
func NewSession(src Source, root Meta, newLane func(Meta) LaneParser) *Session {
	s := &Session{src: src, rootID: root.ID, root: root, newLane: newLane, parsers: map[string]LaneParser{}}
	s.Model = &model.Session{ID: root.ID, Source: root.Source, CWD: root.CWD, Branch: root.Branch, Model: root.Model, CLI: root.CLI, Started: root.Started}
	s.addLane(root)
	return s
}

func (s *Session) addLane(m Meta) {
	if _, ok := s.parsers[m.ID]; ok {
		return
	}
	s.parsers[m.ID] = s.newLane(m)
	s.order = append(s.order, m.ID)
}

// Refresh reads new bytes from every lane file, attaches newly discovered sub-agents, and
// re-derives the model if anything changed. Returns true when the model changed.
func (s *Session) Refresh() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	changed := false
	// discover new sub-agent files
	for _, d := range s.src.Descendants(s.rootID) {
		if _, ok := s.parsers[d.ID]; !ok {
			s.addLane(d)
			changed = true
		}
	}
	var total int64
	for _, id := range s.order {
		p := s.parsers[id]
		st, err := os.Stat(p.Path())
		if err != nil {
			continue
		}
		total += st.Size()
		if st.Size() > p.Offset() {
			err := p.Consume(now)
			if errors.Is(err, ErrRewritten) {
				s.parsers[id] = s.restart(id)
				err = s.parsers[id].Consume(now)
			}
			if err != nil {
				return changed, err
			}
			changed = true
		} else if st.Size() < p.Offset() {
			// truncated/rotated: start over
			s.parsers[id] = s.restart(id)
			if err := s.parsers[id].Consume(now); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	if !changed && s.Model.Live {
		// live sessions: "now" moves even without new lines
		changed = true
	}
	if changed {
		s.rebuild(now, total)
	}
	s.lastRef = time.Now()
	return changed, nil
}

// restart builds a fresh parser for a lane whose file must be read from the start again. The
// meta comes from the index (the file's current size and mtime); the root's is kept as opened.
func (s *Session) restart(id string) LaneParser {
	if id == s.rootID {
		return s.newLane(s.root)
	}
	if m, ok := s.src.Get(id); ok {
		return s.newLane(m)
	}
	for _, d := range s.src.Descendants(s.rootID) {
		if d.ID == id {
			return s.newLane(d)
		}
	}
	return s.newLane(Meta{Source: s.root.Source, ID: id, ParentID: s.rootID, Path: s.parsers[id].Path()})
}

func (s *Session) rebuild(now, total int64) {
	m := s.Model
	m.Lanes = m.Lanes[:0]
	for _, id := range s.order {
		p := s.parsers[id]
		l := p.Lane()
		// Derive may have extended this shared lane for a live display. Start again from
		// recorded evidence, including when only another lane changed.
		l.Ended = max(l.Started, p.LastTS())
		m.Lanes = append(m.Lanes, l)
	}
	// root first, then sub-agents by start time, parents before children
	root := m.Lanes[0]
	rest := m.Lanes[1:]
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Started != rest[j].Started {
			return rest[i].Started < rest[j].Started
		}
		return rest[i].Path < rest[j].Path
	})
	m.Lanes = append([]*model.Lane{root}, rest...)
	m.Bytes = total
	if m.Title == "" {
		if rm, ok := s.src.Get(s.rootID); ok {
			m.Title = rm.Title
		}
	}
	if m.Title == "" {
		for _, mk := range root.Markers {
			if mk.Kind == "user_message" {
				m.Title = FirstLine(mk.Text)
				if len(m.Title) > 80 {
					m.Title = m.Title[:80] + "…"
				}
				break
			}
		}
	}
	model.Derive(m, now)
	h := sha1.New()
	for _, id := range s.order {
		fmt.Fprintf(h, "%s:%d;", id, s.parsers[id].Offset())
	}
	fmt.Fprintf(h, "live=%v", m.Live)
	if m.Live {
		fmt.Fprintf(h, "now=%d", now/60000)
	}
	m.Version = hex.EncodeToString(h.Sum(nil))[:16]
}

// View runs fn with the model locked against concurrent refreshes (use it to encode JSON).
func (s *Session) View(fn func(m *model.Session)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.Model)
}

// IOStats returns bytes read and bytes JSON-decoded across all lanes.
func (s *Session) IOStats() (read, decoded int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.parsers {
		r, d := p.Stats()
		read += r
		decoded += d
	}
	return
}

// Version returns the current model version without refreshing.
func (s *Session) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Model.Version
}

// Summaries builds the session list of one source from its index (cheap; no parsing), with
// the opened sessions' live state.
func Summaries(src Source, opened map[string]*Session) []model.SessionSummary {
	out := []model.SessionSummary{}
	for _, fm := range src.Roots() {
		desc := src.Descendants(fm.ID)
		sum := model.SessionSummary{ID: fm.ID, Source: fm.Source, Title: fm.Title, CWD: fm.CWD, Branch: fm.Branch, Started: fm.Started, Updated: fm.ModTime.UnixMilli(), Bytes: fm.Size, Agents: len(desc), CLI: fm.CLI, Model: fm.Model, LastAnswer: fm.LastAnswer, Question: fm.Question}
		for _, d := range desc {
			sum.Bytes += d.Size
			if d.ModTime.After(fm.ModTime) {
				sum.Updated = d.ModTime.UnixMilli()
			}
		}
		if s, ok := opened[fm.ID]; ok {
			s.mu.Lock()
			t := s.Model.Totals
			sum.Totals = &t
			sum.Live = s.Model.Live
			if s.Model.Title != "" {
				sum.Title = s.Model.Title
			}
			// a parsed session knows its model and its last completed turn exactly
			if len(s.Model.Lanes) > 0 {
				if sum.Model == "" {
					sum.Model = s.Model.Lanes[0].Model
				}
				for _, tn := range s.Model.Lanes[0].Turns {
					if tn.Status == "completed" && tn.Final != "" {
						sum.LastAnswer = Clip(tn.Final, 1200)
					}
				}
			}
			s.mu.Unlock()
		}
		if sum.Title == "" {
			sum.Title = "(untitled) " + Clip(fm.ID, 8)
		}
		out = append(out, sum)
	}
	return out
}
