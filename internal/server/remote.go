package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/store"
)

// The hub side of agent mode (docs/AGENT-MODE.md §5): a Fleet, when set, adds the paired agents'
// sessions to the list under composite ids (`<uuid>@<host>`). loadModel hands a remote id here
// and gets back a remoteView — a cached model that also knows how to poll its version and fetch
// a source span from the agent — so the handlers ask the view, not the id, what it can do. The
// server owns every cache write; the fleet only fetches. /api/fleet* is the Servers section of
// the Settings page.

// SetFleet attaches the hub's fleet (nil leaves the server local) and gives it the facts sink.
func (s *Server) SetFleet(f *fleet.Fleet) {
	s.fleet = f
	if f != nil {
		f.OnFacts(s.insights.storeRemote)
	}
}

// remote reports whether an id names a session of a paired agent.
func (s *Server) remote(id string) bool { return s.fleet != nil && fleet.IsRemote(id) }

// remoteView is a remote session's model as the handlers see it, plus the two things only its
// agent can answer.
type remoteView struct {
	cachedView
	s  *Server
	id string
}

// poller is a view that answers the Follow-mode poll itself; spanner one that serves a
// recorded source span. A local view has neither: the server reads its own files.
type (
	poller interface {
		Poll() (json.RawMessage, error)
	}
	spanner interface {
		Span(model.Src) ([]byte, error)
	}
)

func (r remoteView) Poll() (json.RawMessage, error)     { return r.s.fleet.Version(r.id) }
func (r remoteView) Span(src model.Src) ([]byte, error) { return r.s.fleet.Event(r.id, src) }

// remoteModel serves a remote session: the hub's copy when its cache key still matches the
// agent's row, the session is not live and no poll has seen a newer version — else a
// conditional fetch from the agent (a 304 keeps the copy, re-keyed). The copy lands in the
// in-memory pool and, on a fresh fetch, in the disk cache under the composite id.
func (s *Server) remoteModel(id string, refresh bool) (view, error) {
	fp := s.fleet.Fingerprint(id)
	var known *model.Session
	if !refresh {
		s.mu.Lock()
		entry := s.cached[id]
		s.mu.Unlock()
		if entry != nil && entry.fp.Equal(fp) {
			known = entry.m
		} else if m, dfp, ok := s.cache.Load(id); ok && dfp.Equal(fp) {
			known = m
		}
		if known != nil && !known.Live && s.fleet.Current(id, known.Version) {
			s.putCached(id, known, fp)
			return remoteView{cachedView{known}, s, id}, nil
		}
	}
	etag := ""
	if known != nil {
		etag = known.Version
	}
	m, notModified, err := s.fleet.Model(id, refresh, etag)
	if err != nil {
		return nil, err
	}
	if notModified {
		m = known
	} else {
		_ = s.cache.Save(id, m, fp)
	}
	s.putCached(id, m, fp)
	return remoteView{cachedView{m}, s, id}, nil
}

// fleetState is the /api/fleet answer: the file and every agent's status.
func (s *Server) fleetState() map[string]any {
	return map[string]any{"path": s.fleet.Path(), "agents": s.fleet.Status()}
}

// handleFleet serves /api/fleet (GET: the status of every agent) and its actions (JSON POSTs):
// /api/fleet/add {pairing, name, addr} · /api/fleet/remove {name, revoke} · /api/fleet/rotate
// {name} · /api/fleet/poll {name} (synchronous: the answer is the fresh status); GET
// /api/fleet/doctor?name= relays the agent's report.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.Error(w, "no fleet on this server", http.StatusNotFound)
		return
	}
	action := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/fleet"), "/")
	switch action {
	case "":
		if getOnly(w, r) {
			writeJSON(w, s.fleetState())
		}
	case "doctor":
		if !getOnly(w, r) {
			return
		}
		rep, err := s.fleet.Doctor(r.URL.Query().Get("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeRaw(w, rep)
	case "add", "remove", "rotate", "poll":
		if !jsonPost(w, r) {
			return
		}
		var body struct {
			Pairing string `json:"pairing"`
			Name    string `json:"name"`
			Addr    string `json:"addr"`
			Revoke  bool   `json:"revoke"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var err error
		switch action {
		case "add":
			err = s.fleet.Add(body.Pairing, strings.TrimSpace(body.Name), strings.TrimSpace(body.Addr))
		case "remove":
			var ids []string
			if ids, err = s.fleet.Remove(body.Name, body.Revoke); err == nil {
				s.dropRemote(ids)
			}
		case "rotate":
			err = s.fleet.Rotate(body.Name)
		case "poll":
			err = s.fleet.PollNow(body.Name)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, s.fleetState())
	default:
		http.NotFound(w, r)
	}
}

// dropRemote forgets a removed agent's sessions: the pools and the cache entries.
func (s *Server) dropRemote(ids []string) {
	s.mu.Lock()
	for _, id := range ids {
		delete(s.cached, id)
	}
	s.mu.Unlock()
	s.insights.forget(ids)
	_ = s.cache.Remove(ids)
}

// storeRemote is the fleet's facts sink: remembered like a local session's facts and written
// to the sidecar under the row's cache key.
func (svc *insightsSvc) storeRemote(id string, f insights.Facts, fp store.Fingerprint) bool {
	svc.remember(id, f, fp)
	if svc.srv.cache.Dir() == "" {
		return false
	}
	if err := svc.srv.cache.SaveSidecar("facts", id, insights.FactsVersion, fp, f); err != nil {
		log.Printf("fleet: facts cache: %v", err)
		return false
	}
	return true
}

// forget drops the facts of removed sessions from the memo.
func (svc *insightsSvc) forget(ids []string) {
	svc.mu.Lock()
	for _, id := range ids {
		delete(svc.facts, id)
	}
	svc.mu.Unlock()
}
