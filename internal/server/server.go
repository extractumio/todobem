// Package server exposes the model over a tiny JSON API and serves the SPA.
package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/codex"
	"github.com/extractumio/todobem/internal/model"
)

type Server struct {
	ix       *codex.Index
	home     string
	mu       sync.Mutex
	opened   map[string]*codex.Session
	lastUse  map[string]time.Time
	lastScan time.Time
	web      fs.FS
	maxOpen  int
}

func New(codexHome string, web fs.FS) *Server {
	if abs, err := filepath.Abs(codexHome); err == nil {
		codexHome = abs
	}
	s := &Server{ix: codex.NewIndex(codexHome), home: codexHome, opened: map[string]*codex.Session{}, lastUse: map[string]time.Time{}, web: web, maxOpen: 6}
	return s
}

// Scan does the initial (or periodic) index scan.
func (s *Server) Scan() {
	t := time.Now()
	s.ix.Scan()
	s.mu.Lock()
	s.lastScan = time.Now()
	s.mu.Unlock()
	log.Printf("index scan: %d root sessions in %s", len(s.ix.Roots()), time.Since(t).Round(time.Millisecond))
}

func (s *Server) maybeScan(maxAge time.Duration) {
	s.mu.Lock()
	stale := time.Since(s.lastScan) > maxAge
	s.mu.Unlock()
	if stale {
		s.Scan()
	}
}

// Handler accepts the configured listen address when it uses an explicit hostname.
// Other DNS names are rejected so resolving an unrelated origin to this listener
// does not give that origin access to the local session API.
func (s *Server) Handler(listenAddr ...string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSession)
	mux.HandleFunc("/api/event", s.handleEvent)
	mux.HandleFunc("/api/rules", s.handleRules)
	static := http.FileServer(http.FS(s.web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	configuredHost := ""
	if len(listenAddr) > 0 {
		configuredHost = hostName(listenAddr[0])
	}
	next := gzipMiddleware(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostName(r.Host)
		if host == "" || (host != "localhost" && net.ParseIP(host) == nil && host != configuredHost) {
			http.Error(w, "untrusted host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostName(authority string) string {
	if host, _, err := net.SplitHostPort(authority); err == nil {
		authority = host
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(authority, "[]"), "."))
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.maybeScan(20 * time.Second)
	s.mu.Lock()
	opened := map[string]*codex.Session{}
	for k, v := range s.opened {
		opened[k] = v
	}
	s.mu.Unlock()
	writeJSON(w, codex.Summaries(s.ix, opened))
}

// session returns an opened (and refreshed) session, opening it on demand.
func (s *Server) session(id string, refresh bool) (*codex.Session, error) {
	s.mu.Lock()
	sess, ok := s.opened[id]
	if !ok {
		s.maybeScanLocked()
		var err error
		sess, err = codex.Open(s.ix, id)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.evictLocked()
		s.opened[id] = sess
		refresh = true
	}
	s.lastUse[id] = time.Now()
	s.mu.Unlock()
	if refresh {
		// pick up sub-agent files created since the last scan
		s.maybeScan(10 * time.Second)
		if _, err := sess.Refresh(); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

func (s *Server) maybeScanLocked() {
	if time.Since(s.lastScan) > 10*time.Second {
		s.mu.Unlock()
		s.Scan()
		s.mu.Lock()
	}
}

func (s *Server) evictLocked() {
	for len(s.opened) >= s.maxOpen {
		var oldest string
		var t time.Time
		for id, u := range s.lastUse {
			if oldest == "" || u.Before(t) {
				oldest, t = id, u
			}
		}
		delete(s.opened, oldest)
		delete(s.lastUse, oldest)
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1:
		sess, err := s.session(id, true)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		sess.View(func(m *model.Session) { writeJSON(w, m) })
	case len(parts) == 2 && parts[1] == "version":
		sess, err := s.session(id, true)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		sess.View(func(m *model.Session) {
			writeJSON(w, map[string]any{"version": m.Version, "live": m.Live, "ended": m.Ended, "now": m.Now, "ops": m.Totals.Ops})
		})
	case len(parts) == 3 && parts[1] == "op":
		sess, err := s.session(id, false)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		var resp map[string]any
		sess.View(func(m *model.Session) {
			if op := findOp(m, parts[2]); op != nil {
				cp := *op
				resp = map[string]any{"op": &cp, "detail": op.Detail}
				if op.Src != nil {
					if b, err := s.readSource(*op.Src); err == nil {
						resp["source"] = json.RawMessage(validJSON(b))
					}
				}
			}
		})
		if resp == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, resp)
	default:
		http.NotFound(w, r)
	}
}

func findOp(m *model.Session, id string) *model.Operation {
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.ID == id {
				return o
			}
		}
	}
	return nil
}

// handleEvent returns an exact source span already exposed by an opened session.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	off, offErr := strconv.ParseInt(r.URL.Query().Get("off"), 10, 64)
	ln, lenErr := strconv.Atoi(r.URL.Query().Get("len"))
	abs, err := filepath.Abs(file)
	if err != nil || offErr != nil || lenErr != nil || off < 0 || ln <= 0 || ln > 32<<20 {
		http.Error(w, "bad request", 400)
		return
	}
	src := model.Src{File: abs, Off: off, Len: ln}
	if !s.recordedSource(src) {
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}
	b, err := s.readSource(src)
	if err != nil {
		http.Error(w, "source unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(validJSON(b))
}

func (s *Server) recordedSource(src model.Src) bool {
	s.mu.Lock()
	sessions := make([]*codex.Session, 0, len(s.opened))
	for _, sess := range s.opened {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		found := false
		sess.View(func(m *model.Session) {
			for _, lane := range m.Lanes {
				for _, marker := range lane.Markers {
					if marker.Src != nil && *marker.Src == src {
						found = true
						return
					}
				}
				for _, op := range lane.Ops {
					if op.Src != nil && *op.Src == src {
						found = true
						return
					}
				}
			}
		})
		if found {
			return true
		}
	}
	return false
}

// readSource also checks the resolved filesystem location: a previously indexed
// rollout may have been replaced with a symlink since the session was loaded.
func (s *Server) readSource(src model.Src) ([]byte, error) {
	root, err := filepath.EvalSymlinks(s.home)
	if err != nil {
		return nil, err
	}
	file, err := filepath.EvalSymlinks(src.File)
	if err != nil {
		return nil, err
	}
	logical, err := filepath.Rel(s.home, src.File)
	if err != nil || file != filepath.Join(root, logical) {
		return nil, errors.New("source path contains a symlink")
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || (!strings.HasPrefix(rel, "sessions"+string(filepath.Separator)) && !strings.HasPrefix(rel, "archived_sessions"+string(filepath.Separator))) {
		return nil, errors.New("source outside rollout directories")
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	src.File = file
	return codex.ReadSource(src)
}

func validJSON(b []byte) []byte {
	if json.Valid(b) {
		return b
	}
	q, _ := json.Marshal(string(b))
	return q
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"rules": classify.Rules, "priority": classify.Priority})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

type gzipWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer gz.Close()
		next.ServeHTTP(&gzipWriter{ResponseWriter: w, gz: gz}, r)
	})
}
