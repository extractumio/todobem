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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/claude"
	"github.com/extractumio/todobem/internal/codex"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
	"github.com/extractumio/todobem/internal/source"
	"github.com/extractumio/todobem/internal/store"
)

type Server struct {
	auth      *auth.Verifier // nil = the UI is open (-auth=off)
	src       *source.Multi  // the session sources (Codex, Claude Code) as one index
	mu        sync.Mutex
	opened    map[string]*source.Session // parser-backed sessions (LRU, expensive)
	lastUse   map[string]time.Time
	cached    map[string]*cachedEntry // read-only models served from disk (cheap; separate LRU)
	cache     *store.Store
	lastScan  time.Time
	web       fs.FS
	build     string // fingerprint of web (BuildHeader): the page reloads when it changes
	maxOpen   int
	maxCached int
	insights  *insightsSvc // /api/insights/*: period reports over the project's sessions
	settings  *settingsSvc // /api/settings: the session folders and the file they are saved in
	fleet     *fleet.Fleet // the paired agents (hub); nil = local only
	agent     *agentSvc    // agent mode (set by AgentHandler); nil = the viewer
}

// cachedEntry is a fingerprint-validated model served without a parser. Its model is immutable
// once loaded, so it can be read without locking the (evicted) session it came from.
type cachedEntry struct {
	m    *model.Session
	fp   store.Fingerprint
	used time.Time
}

// view is what handlers need from a loaded session, satisfied by both a parser-backed
// *source.Session and a read-only cached model.
type view interface {
	View(func(*model.Session))
	Version() string
}

type cachedView struct{ m *model.Session }

func (c cachedView) View(fn func(*model.Session)) { fn(c.m) }
func (c cachedView) Version() string              { return c.m.Version }

// New serves the sessions of the given homes: Codex homes (each contains sessions/) and Claude
// Code homes (each contains projects/); see codex.NewIndex / claude.NewIndex for how several
// homes of one source combine. A source with no homes is simply empty.
func New(homes settings.Homes, web fs.FS) *Server { return NewWithCache(homes, web, "") }

// NewWithCache is New plus a directory for the derived-session cache ("" disables it).
func NewWithCache(homes settings.Homes, web fs.FS, cacheDir string) *Server {
	src := source.NewMulti(codex.NewIndex(homes.Codex...), claude.NewIndex(homes.Claude...))
	s := &Server{src: src, opened: map[string]*source.Session{}, lastUse: map[string]time.Time{}, cached: map[string]*cachedEntry{}, cache: store.New(cacheDir), web: web, build: webFingerprint(web), maxOpen: 6, maxCached: 32}
	s.insights = newInsights(s)
	s.settings = &settingsSvc{}
	return s
}

// SetHomes points the server at another set of homes, live. Each source drops the files of a
// removed home before anything else happens; the in-memory pools go too (a parser pins its
// file path, so an open session of a removed home would otherwise be served until eviction —
// the disk cache re-serves closed sessions on demand); a running Insights scan is cancelled
// (it would open ids that just vanished); then a synchronous scan indexes the new homes so the
// caller can answer with fresh counts.
func (s *Server) SetHomes(homes settings.Homes) {
	s.insights.scanner.Cancel()
	s.src.Source(source.Codex).SetHomes(homes.Codex)
	s.src.Source(source.Claude).SetHomes(homes.Claude)
	s.mu.Lock()
	s.opened = map[string]*source.Session{}
	s.lastUse = map[string]time.Time{}
	s.cached = map[string]*cachedEntry{}
	s.mu.Unlock()
	s.Scan()
	log.Printf("settings: codex homes → %v · claude homes → %v", homes.Codex, homes.Claude)
}

// Scan does the initial (or periodic) index scan of every source.
func (s *Server) Scan() {
	t := time.Now()
	s.src.Scan()
	s.mu.Lock()
	s.lastScan = time.Now()
	s.mu.Unlock()
	log.Printf("index scan: %d root sessions in %s", len(s.src.Roots()), time.Since(t).Round(time.Millisecond))
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
	mux.HandleFunc("/api/insights/", s.insights.handle)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/fleet", s.handleFleet)
	mux.HandleFunc("/api/fleet/", s.handleFleet)
	static := http.FileServer(http.FS(s.web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			http.SetCookie(w, &http.Cookie{Name: buildCookie, Value: s.build, Path: "/", SameSite: http.SameSiteStrictMode})
		}
		static.ServeHTTP(w, r)
	})
	configuredHost := ""
	if len(listenAddr) > 0 {
		configuredHost = hostName(listenAddr[0])
	}
	api := s.authGate(gzipMiddleware(mux)) // API answers stay gzipped once the gate lets them through
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostName(r.Host)
		if host == "" || (host != "localhost" && net.ParseIP(host) == nil && host != configuredHost) {
			http.Error(w, "untrusted host", http.StatusForbidden)
			return
		}
		w.Header().Set(BuildHeader, s.build)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
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
	if s.fleet != nil {
		s.fleet.Touch()
	}
	writeJSON(w, s.summaries())
}

// session returns an opened (and refreshed) session, opening it on demand.
func (s *Server) session(id string, refresh bool) (*source.Session, error) {
	s.mu.Lock()
	sess, ok := s.opened[id]
	if !ok {
		s.maybeScanLocked()
		var err error
		sess, err = s.src.Open(id)
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

// fingerprint describes a session's current inputs: its session files (sizes/mtimes from the
// index) plus a hash of the effective classifier. A change to any file or rule flips it.
func (s *Server) fingerprint(id string) store.Fingerprint {
	if s.remote(id) {
		return s.fleet.Fingerprint(id)
	}
	fp := store.Fingerprint{Rules: classify.RulesFingerprint()}
	add := func(fm source.Meta) {
		fp.Files = append(fp.Files, store.FileFP{Path: fm.Path, Size: fm.Size, Mod: fm.ModTime.UnixNano()})
	}
	if fm, ok := s.src.Get(id); ok {
		add(fm)
	}
	for _, d := range s.src.Descendants(id) {
		add(d)
	}
	sort.Slice(fp.Files, func(i, j int) bool { return fp.Files[i].Path < fp.Files[j].Path })
	return fp
}

func (s *Server) putCached(id string, m *model.Session, fp store.Fingerprint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.cached) >= s.maxCached {
		var oldest string
		var t time.Time
		for k, e := range s.cached {
			if oldest == "" || e.used.Before(t) {
				oldest, t = k, e.used
			}
		}
		delete(s.cached, oldest)
	}
	s.cached[id] = &cachedEntry{m: m, fp: fp, used: time.Now()}
}

func (s *Server) dropCached(id string) {
	s.mu.Lock()
	delete(s.cached, id)
	s.mu.Unlock()
}

// loadModel returns a session view for id. With refresh=false it serves the freshest thing that is
// provably current: an open parser-backed session, or a cache entry whose fingerprint still
// matches the files and rules; only on a miss or a fingerprint change does it parse. refresh=true
// always re-parses (incremental for an open session, full otherwise) and refreshes the cache.
// It never returns a stale model. A remote id is served by remoteModel (the hub's copy or the
// agent's answer) under the same contract.
func (s *Server) loadModel(id string, refresh bool) (view, error) {
	if s.remote(id) {
		return s.remoteModel(id, refresh)
	}
	if !refresh {
		s.mu.Lock()
		if sess, ok := s.opened[id]; ok {
			s.lastUse[id] = time.Now()
			s.mu.Unlock()
			return sess, nil
		}
		entry := s.cached[id]
		s.mu.Unlock()
		fp := s.fingerprint(id)
		if entry != nil && entry.fp.Equal(fp) {
			s.mu.Lock()
			entry.used = time.Now()
			s.mu.Unlock()
			return cachedView{entry.m}, nil
		}
		if m, dfp, ok := s.cache.Load(id); ok && dfp.Equal(fp) {
			s.putCached(id, m, fp)
			return cachedView{m}, nil
		}
	}
	// Parse path: open (if needed) + refresh, then refresh the on-disk cache for next time.
	sess, err := s.session(id, refresh)
	if err != nil {
		return nil, err
	}
	fp := s.fingerprint(id)
	sess.View(func(m *model.Session) {
		if !m.Live { // a live session's files change every poll; caching it would only churn
			_ = s.cache.Save(id, m, fp)
		}
	})
	s.dropCached(id) // any stale in-memory cache entry is superseded by this fresh parse
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
		// Default load serves the cache when it is provably current; ?refresh=1 (the Refresh
		// button) forces a re-parse. Either way the model returned is never stale.
		v, err := s.loadModel(id, r.URL.Query().Get("refresh") == "1")
		if err != nil {
			http.Error(w, err.Error(), loadStatus(err))
			return
		}
		v.View(func(m *model.Session) { writeJSON(w, m) })
	case len(parts) == 2 && parts[1] == "version":
		// Follow-mode polling: refresh an open (live) session incrementally so its version can
		// advance, but never force a full parse just to answer a poll. A remote session's agent
		// answers the poll itself; its model is re-fetched only when the page sees a change.
		v, err := s.loadModel(id, !s.remote(id))
		if err != nil {
			http.Error(w, err.Error(), loadStatus(err))
			return
		}
		if p, ok := v.(poller); ok {
			raw, err := p.Poll()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			writeRaw(w, raw)
			return
		}
		v.View(func(m *model.Session) { writeJSON(w, versionPayload(m)) })
	case len(parts) == 3 && parts[1] == "op":
		v, err := s.loadModel(id, false)
		if err != nil {
			http.Error(w, err.Error(), loadStatus(err))
			return
		}
		var resp map[string]any
		v.View(func(m *model.Session) {
			if op := findOp(m, parts[2]); op != nil {
				cp := *op
				resp = map[string]any{"op": &cp, "detail": op.Detail}
				if op.Src != nil {
					if b, err := s.span(v, *op.Src); err == nil {
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

// handleEvent returns an exact source span already exposed by an opened session; `session`
// names the session it belongs to, which for a remote one means its agent serves it.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	b, status, reason := s.readSpan(r)
	if status != http.StatusOK {
		http.Error(w, reason, status)
		return
	}
	writeRaw(w, b)
}

// readSpan reads the span an event request names, on the surface that owns it: the agent of a
// remote session, else this server's files (the span must be recorded by a loaded session and
// lie under a current home). It answers the status and reason to send when it is not 200.
func (s *Server) readSpan(r *http.Request) ([]byte, int, string) {
	q := r.URL.Query()
	off, offErr := strconv.ParseInt(q.Get("off"), 10, 64)
	ln, lenErr := strconv.Atoi(q.Get("len"))
	abs, err := filepath.Abs(q.Get("file"))
	if err != nil || offErr != nil || lenErr != nil || off < 0 || ln <= 0 || ln > 32<<20 {
		return nil, http.StatusBadRequest, "bad request"
	}
	if id := q.Get("session"); s.remote(id) {
		v, err := s.loadModel(id, false)
		if err != nil {
			return nil, http.StatusNotFound, "source not found"
		}
		b, err := s.span(v, model.Src{File: q.Get("file"), Off: off, Len: ln})
		if err != nil {
			return nil, http.StatusNotFound, "source unavailable"
		}
		return b, http.StatusOK, ""
	}
	src := model.Src{File: abs, Off: off, Len: ln}
	if !s.recordedSource(src) {
		return nil, http.StatusNotFound, "source not found"
	}
	b, err := s.readSource(src)
	if err != nil {
		return nil, http.StatusNotFound, "source unavailable"
	}
	return b, http.StatusOK, ""
}

// span reads a source line the way the view's owner can: the agent for a remote view, this
// server's files otherwise.
func (s *Server) span(v view, src model.Src) ([]byte, error) {
	if sp, ok := v.(spanner); ok {
		return sp.Span(src)
	}
	return s.readSource(src)
}

// loadStatus is the status a failed load answers with: an agent that did not answer is a bad
// gateway (the session exists; ask again), anything else an unknown session.
func loadStatus(err error) int {
	var ae *fleet.AgentError
	if errors.As(err, &ae) {
		return http.StatusBadGateway
	}
	return http.StatusNotFound
}

// versionPayload is the Follow-mode poll's answer, the same on the hub and on an agent.
func versionPayload(m *model.Session) map[string]any {
	return map[string]any{"version": m.Version, "live": m.Live, "ended": m.Ended, "now": m.Now, "ops": m.Totals.Ops}
}

func (s *Server) recordedSource(src model.Src) bool {
	s.mu.Lock()
	sessions := make([]view, 0, len(s.opened)+len(s.cached))
	for _, sess := range s.opened {
		sessions = append(sessions, sess)
	}
	for _, e := range s.cached {
		sessions = append(sessions, cachedView{e.m})
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

// readSource also checks the resolved filesystem location: the file must lie under one of the
// directories a source reads (sessions/ or archived_sessions/ of a Codex home, projects/ of a
// Claude home) by its logical path, and resolve to the same place under the resolved home — no
// symlink anywhere below the home, the walkers follow none either. A previously indexed file
// may have been replaced with a symlink since the session was loaded, or its home removed from
// the settings.
func (s *Server) readSource(src model.Src) ([]byte, error) {
	file, err := filepath.EvalSymlinks(src.File)
	if err != nil {
		return nil, err
	}
	inside := false
	for _, dir := range s.src.Dirs() {
		rel, err := filepath.Rel(dir, src.File)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		home := filepath.Dir(dir)
		root, err := filepath.EvalSymlinks(home)
		if err != nil {
			continue
		}
		if file != filepath.Join(root, filepath.Base(dir), rel) {
			return nil, errors.New("source path contains a symlink")
		}
		inside = true
		break
	}
	if !inside {
		return nil, errors.New("source outside the session directories")
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	src.File = file
	return source.ReadSpan(src)
}

func validJSON(b []byte) []byte {
	if json.Valid(b) {
		return b
	}
	q, _ := json.Marshal(string(b))
	return q
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	defaults := map[classify.Phase]classify.Lifecycle{}
	for p := range classify.Priority {
		defaults[p] = classify.PhaseLifecycle(p)
	}
	writeJSON(w, map[string]any{
		"rules":         classify.Rules,
		"priority":      classify.Priority,
		"builtin_rules": classify.BuiltinRuleCount(), // rules[:n] are built-in; rules[n:] are user-added
		"review_skills": classify.ReviewSkillMatchers(),
		"subgroups":     classify.Subgroups(), // the breakdown's sub-rows: (phase, kind) → subgroup
		// the lifecycle (SDLC stage) partition: stage order, the phase → stage defaults, the
		// command kinds pinned to another stage, and the skill / role / path matchers
		"lifecycle": map[string]any{
			"stages":   classify.WorkLifecycles,
			"defaults": defaults,
			"pins":     classify.LifecyclePins,
			"matchers": classify.Matchers(),
		},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

// writeRaw sends bytes that are already JSON (a source line, an answer relayed from an agent).
func writeRaw(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(validJSON(b))
}

// gzipWriter compresses a response unless the handler already encoded it (an agent serving a
// cache file as stored) or sends no body (204, 304): the decision is made on the first write.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (g *gzipWriter) WriteHeader(status int) {
	if !g.decided {
		g.decided = true
		if g.Header().Get("Content-Encoding") == "" && status != http.StatusNoContent && status != http.StatusNotModified {
			g.Header().Set("Content-Encoding", "gzip")
			g.Header().Add("Vary", "Accept-Encoding")
			g.Header().Del("Content-Length")
			g.gz, _ = gzip.NewWriterLevel(g.ResponseWriter, gzip.BestSpeed)
		}
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
	}
}

// gzipMiddleware compresses the answers of the JSON routes it wraps for a client that accepts it.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}

// Roots lists the root sessions of every source, newest first (for command-line tools that walk
// sessions the way the UI's list does).
func (s *Server) Roots() []source.Meta { return s.src.Roots() }

// Sources exposes the session sources (command-line tools).
func (s *Server) Sources() *source.Multi { return s.src }

// Model gives fn a read-only view of a session's current model, served from the parsed-session
// cache when its fingerprint still matches and parsed otherwise — the same path as the UI.
func (s *Server) Model(id string, fn func(*model.Session)) error {
	v, err := s.loadModel(id, false)
	if err != nil {
		return err
	}
	v.View(fn)
	return nil
}
