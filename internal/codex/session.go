package codex

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// Session is an opened root thread with all its sub-agent lanes, refreshed incrementally.
type Session struct {
	mu      sync.Mutex
	ix      *Index
	rootID  string
	parsers map[string]*laneParser // by thread id
	order   []string               // lane order: root first, then by start
	Model   *model.Session
	lastRef time.Time
}

// Open creates a session for a root thread id (does not parse yet).
func Open(ix *Index, rootID string) (*Session, error) {
	fm, ok := ix.Get(rootID)
	if !ok {
		return nil, fmt.Errorf("unknown thread %s", rootID)
	}
	s := &Session{ix: ix, rootID: rootID, parsers: map[string]*laneParser{}}
	s.Model = &model.Session{ID: rootID, Source: "codex", CWD: fm.CWD, Branch: fm.Branch, Model: fm.Model, CLI: fm.CLIVersion, Started: fm.Started}
	s.addLane(fm)
	return s, nil
}

func (s *Session) addLane(fm FileMeta) {
	if _, ok := s.parsers[fm.ThreadID]; ok {
		return
	}
	p := newLaneParser(fm)
	s.parsers[fm.ThreadID] = p
	s.order = append(s.order, fm.ThreadID)
}

// Refresh reads new bytes from every lane file, attaches newly discovered sub-agents,
// and re-derives the model if anything changed. Returns true when the model changed.
func (s *Session) Refresh() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	changed := false
	// discover new sub-agent files
	for _, d := range s.ix.Descendants(s.rootID) {
		if _, ok := s.parsers[d.ThreadID]; !ok {
			s.addLane(d)
			changed = true
		}
	}
	var total int64
	for _, id := range s.order {
		p := s.parsers[id]
		st, err := os.Stat(p.meta.Path)
		if err != nil {
			continue
		}
		total += st.Size()
		if st.Size() > p.off {
			_, err := p.consume(now)
			if err == errRewritten {
				s.parsers[id] = newLaneParser(p.meta)
				_, err = s.parsers[id].consume(now)
			}
			if err != nil {
				return changed, err
			}
			changed = true
		} else if st.Size() < p.off {
			// truncated/rotated: start over
			s.parsers[id] = newLaneParser(p.meta)
			if _, err := s.parsers[id].consume(now); err != nil {
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

func (s *Session) rebuild(now, total int64) {
	m := s.Model
	m.Lanes = m.Lanes[:0]
	for _, id := range s.order {
		p := s.parsers[id]
		// Derive may have extended this shared lane for a live display. Start again
		// from recorded evidence, including when only another lane changed.
		p.lane.Ended = max(p.lane.Started, p.lastTS)
		m.Lanes = append(m.Lanes, p.lane)
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
		m.Title = s.ix.Name(s.rootID)
	}
	if m.Title == "" {
		for _, mk := range root.Markers {
			if mk.Kind == "user_message" {
				m.Title = firstLine(mk.Text)
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
		fmt.Fprintf(h, "%s:%d;", id, s.parsers[id].off)
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
		read += p.Bytes
		decoded += p.Decoded
	}
	return
}

// Version returns the current model version without refreshing.
func (s *Session) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Model.Version
}

// ReadSource returns the exact source line for an op/marker.
func ReadSource(src model.Src) ([]byte, error) {
	f, err := os.Open(src.File)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, src.Len)
	n, err := f.ReadAt(buf, src.Off)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// Summaries builds the session list from the index (cheap; no parsing).
func Summaries(ix *Index, opened map[string]*Session) []model.SessionSummary {
	out := []model.SessionSummary{}
	for _, fm := range ix.Roots() {
		if strings.HasSuffix(fm.Path, ".tmp") {
			continue
		}
		desc := ix.Descendants(fm.ThreadID)
		sum := model.SessionSummary{ID: fm.ThreadID, Title: ix.Name(fm.ThreadID), CWD: fm.CWD, Branch: fm.Branch, Started: fm.Started, Updated: fm.ModTime.UnixMilli(), Bytes: fm.Size, Agents: len(desc), CLI: fm.CLIVersion, Model: fm.Model, LastAnswer: fm.LastAnswer}
		for _, d := range desc {
			sum.Bytes += d.Size
			if d.ModTime.After(fm.ModTime) {
				sum.Updated = d.ModTime.UnixMilli()
			}
		}
		if s, ok := opened[fm.ThreadID]; ok {
			s.mu.Lock()
			t := s.Model.Totals
			sum.Totals = &t
			sum.Live = s.Model.Live
			if s.Model.Title != "" {
				sum.Title = s.Model.Title
			}
			// a parsed session knows its last completed turn exactly
			if len(s.Model.Lanes) > 0 {
				for _, tn := range s.Model.Lanes[0].Turns {
					if tn.Status == "completed" && tn.Final != "" {
						sum.LastAnswer = clip(tn.Final, 1200)
					}
				}
			}
			s.mu.Unlock()
		}
		if sum.Title == "" {
			sum.Title = "(untitled) " + clip(fm.ThreadID, 8)
		}
		out = append(out, sum)
	}
	return out
}
