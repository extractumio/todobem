package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/store"
)

// agentSvc is agent mode (`todobem -agent`, docs/AGENT-MODE.md): the same server — index, cache,
// classifier, incremental parsers — answering a paired hub over /agent/v1/ instead of serving a
// UI. Every route but pair needs a bearer signed by the agent key; the cursor tracker makes the
// session list a delta; the digest parses closed sessions in the background so facts and first
// opens are cheap. The agent never opens a connection of its own.
type agentSvc struct {
	srv      *Server
	keyPath  string
	verifier *auth.Verifier
	tracker  *fleet.Tracker
	boot     time.Time
	hostname string

	mu          sync.Mutex
	tried       map[string]string // digest: id → the fingerprint key it was last parsed at
	lastScan    time.Duration
	lastRefusal time.Time // refused credentials are logged once a minute
}

// agentRoutes is the method each route under /agent/v1/ takes; "sessions/" covers a model and
// its version poll. The wrapper enforces it (405 / 415 as JSON), so handlers do not.
var agentRoutes = map[string]string{"pair": "POST", "hello": "GET", "sessions": "GET", "sessions/": "GET", "facts": "POST", "event": "GET", "doctor": "GET", "rotate": "POST"}

// AgentHandler turns the server into an agent: it returns the handler of /agent/v1/* (everything
// else is 404) and starts the digest. keyPath is the agent key file (created when absent);
// boot is the start time tokens must postdate.
func (s *Server) AgentHandler(keyPath string, boot time.Time) (http.Handler, error) {
	if _, err := auth.LoadOrCreateKey(keyPath); err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	a := &agentSvc{srv: s, keyPath: keyPath, verifier: auth.NewFileVerifier(keyPath, boot), tracker: fleet.NewTracker(), boot: boot, hostname: host, tried: map[string]string{}}
	s.agent = a
	go a.digestLoop()
	mux := http.NewServeMux()
	mux.HandleFunc(fleet.BasePath+"pair", a.handlePair)
	mux.HandleFunc(fleet.BasePath+"hello", a.handleHello)
	mux.HandleFunc(fleet.BasePath+"sessions", a.handleSessions)
	mux.HandleFunc(fleet.BasePath+"sessions/", a.handleSession)
	mux.HandleFunc(fleet.BasePath+"facts", a.handleFacts)
	mux.HandleFunc(fleet.BasePath+"event", a.handleEvent)
	mux.HandleFunc(fleet.BasePath+"doctor", a.handleDoctor)
	mux.HandleFunc(fleet.BasePath+"rotate", a.handleRotate)
	gz := gzipMiddleware(mux) // a model answer is the gzipped cache file: the middleware leaves it alone
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, fleet.BasePath)
		method, known := agentRoutes[route]
		if !known && strings.HasPrefix(route, "sessions/") {
			method = agentRoutes["sessions/"]
		} else if !known || route == r.URL.Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != method {
			w.Header().Set("Allow", method)
			jsonError(w, http.StatusMethodNotAllowed, "method", "")
			return
		}
		if method == "POST" && !jsonBody(r) {
			jsonError(w, http.StatusUnsupportedMediaType, "content_type", "expected application/json")
			return
		}
		if route != "pair" && !a.authenticated(w, r) {
			return
		}
		gz.ServeHTTP(w, r)
	}), nil
}

// PairingString mints a one-time token under the agent key and prints the pairing string with
// the certificate's pin (docs/AGENT-MODE.md §6.2). revoke rotates the key first.
func PairingString(keyPath, pin, port string, revoke bool, ttl time.Duration, now time.Time) (string, error) {
	tok, err := auth.MintFromFile(keyPath, revoke, ttl, now)
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	return fleet.FormatPairing(host, port, pin, tok), nil
}

// AgentBearerTTL is how long a bearer lives: a year, refreshed by rotation.
const AgentBearerTTL = 365 * 24 * time.Hour

func (a *agentSvc) authenticated(w http.ResponseWriter, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") && a.verifier.VerifySession(strings.TrimSpace(h[7:]), time.Now()) == nil {
		return true
	}
	a.logRefusal(r)
	jsonError(w, http.StatusUnauthorized, "auth", "")
	return false
}

// logRefusal logs a refused credential at most once a minute.
func (a *agentSvc) logRefusal(r *http.Request) {
	a.mu.Lock()
	throttled := time.Since(a.lastRefusal) < time.Minute
	if !throttled {
		a.lastRefusal = time.Now()
	}
	a.mu.Unlock()
	if !throttled {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		log.Printf("agent: refused a request from %s (%s %s): bad or missing bearer", host, r.Method, r.URL.Path)
	}
}

func (a *agentSvc) handlePair(w http.ResponseWriter, r *http.Request) {
	var body fleet.PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", "expected {\"token\": …}")
		return
	}
	value, expiry, reason, err := redeem(a.verifier, body.Token, time.Now())
	if reason != "" {
		a.logRefusal(r)
		jsonError(w, http.StatusUnauthorized, "auth", reason)
		return
	}
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "key", "agent key unavailable")
		return
	}
	log.Printf("agent: paired with %s (bearer until %s)", r.RemoteAddr, expiry.Format(time.DateOnly))
	writeJSON(w, fleet.PairResponse{Bearer: value, Expires: expiry.UnixMilli(), Hello: a.hello()})
}

func (a *agentSvc) hello() fleet.Hello {
	s := a.srv
	h := fleet.Hello{Protocol: fleet.Protocol, Version: fleet.BuildVersion(), OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: a.hostname, Started: a.boot.UnixMilli(), Now: time.Now().UnixMilli(), CacheVersion: store.CacheVersion(), FactsVersion: insights.FactsVersion, RulesFingerprint: classify.RulesFingerprint(), Homes: []fleet.HomeInfo{}}
	for _, src := range s.src.Sources() {
		for _, hs := range src.HomeStatuses() {
			h.Homes = append(h.Homes, fleet.HomeInfo{Source: src.Name(), Path: hs.Path, Status: hs.Status, Sessions: hs.Sessions})
		}
	}
	h.Roots = len(s.src.Roots())
	p := s.insights.scanner.Progress()
	h.Digest = fleet.Digest{Done: p.Done, Total: p.Total}
	return h
}

func (a *agentSvc) handleHello(w http.ResponseWriter, r *http.Request) {
	a.srv.maybeScan(20 * time.Second)
	writeJSON(w, a.hello())
}

// handleSessions is the delta list: the rows changed since the cursor, or every row.
func (a *agentSvc) handleSessions(w http.ResponseWriter, r *http.Request) {
	a.srv.maybeScan(20 * time.Second)
	writeJSON(w, a.tracker.Page(r.URL.Query().Get("since"), a.srv.summaries()))
}

// handleSession serves one model as the cache file (conditional on If-None-Match) or its version.
func (a *agentSvc) handleSession(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, fleet.BasePath+"sessions/")
	id, sub, _ := strings.Cut(rest, "/")
	if id == "" || sub != "" && sub != "version" {
		jsonError(w, http.StatusNotFound, "not_found", "")
		return
	}
	s := a.srv
	if sub == "version" {
		v, err := s.loadModel(id, true)
		if err != nil {
			jsonError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		v.View(func(m *model.Session) { writeJSON(w, versionPayload(m)) })
		return
	}
	v, err := s.loadModel(id, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	fp := s.fingerprint(id)
	var file []byte
	var version string
	var encErr error
	v.View(func(m *model.Session) {
		version = m.Version
		if etag, err := strconv.Unquote(r.Header.Get("If-None-Match")); err == nil && etag == m.Version {
			return
		}
		// a model served from the disk cache is sent as stored; a parsed one is encoded once
		if _, cached := v.(cachedView); cached {
			if raw, ok := s.cache.ReadRaw(id); ok {
				file = raw
				return
			}
		}
		file, encErr = store.Encode(m, fp)
	})
	w.Header().Set("ETag", strconv.Quote(version))
	if encErr != nil {
		jsonError(w, http.StatusInternalServerError, "encode", encErr.Error())
		return
	}
	if file == nil {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(file)))
	w.Write(file)
}

// handleFacts answers with the facts at hand: closed sessions the digest has parsed; live or not
// yet digested ones are pending, ids the index does not know are unknown.
func (a *agentSvc) handleFacts(w http.ResponseWriter, r *http.Request) {
	var req fleet.FactsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || len(req.IDs) > fleet.MaxFactsIDs {
		jsonError(w, http.StatusBadRequest, "bad_request", "expected {\"ids\": […]} with at most 100 ids")
		return
	}
	s := a.srv
	page := fleet.FactsPage{FactsVersion: insights.FactsVersion, Facts: []insights.Facts{}, Pending: []string{}, Unknown: []string{}}
	for _, id := range req.IDs {
		if _, known := s.src.Get(id); !known {
			page.Unknown = append(page.Unknown, id)
			continue
		}
		f, ok := s.insights.factsFor(id)
		if !ok || f.Live {
			page.Pending = append(page.Pending, id)
			continue
		}
		page.Facts = append(page.Facts, f)
	}
	writeJSON(w, page)
}

// handleEvent serves one recorded source span of a session the hub names: the session's model
// is loaded first so the span is checked against the record it belongs to.
func (a *agentSvc) handleEvent(w http.ResponseWriter, r *http.Request) {
	s := a.srv
	if _, err := s.loadModel(r.URL.Query().Get("session"), false); err != nil {
		jsonError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	b, status, reason := s.readSpan(r)
	if status != http.StatusOK {
		jsonError(w, status, "span", reason)
		return
	}
	writeRaw(w, b)
}

// handleDoctor is the read-only health report.
func (a *agentSvc) handleDoctor(w http.ResponseWriter, r *http.Request) {
	log.Printf("agent: doctor asked by %s", r.RemoteAddr)
	writeJSON(w, a.doctor(r))
}

func (a *agentSvc) doctor(r *http.Request) map[string]any {
	s := a.srv
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	files, bytes := s.cache.Size()
	mode := func(path string) string {
		st, err := os.Stat(path)
		if err != nil {
			return "absent"
		}
		return "0" + strconv.FormatUint(uint64(st.Mode().Perm()), 8)
	}
	certPath, tlsKeyPath := fleet.CertPaths(a.keyPath)
	pin, _ := fleet.CertPin(certPath)
	var bearerExpires int64
	if v := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); v != "" {
		bearerExpires = auth.SessionExpiry(v)
	}
	a.mu.Lock()
	lastScan := a.lastScan
	tried := len(a.tried)
	a.mu.Unlock()
	nextStaged := "absent"
	if _, err := os.Stat(auth.NextPath(a.keyPath)); err == nil {
		nextStaged = "staged (a rotation awaits its first use)"
	}
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	return map[string]any{
		"hello":          a.hello(),
		"uptime_s":       int64(time.Since(a.boot).Seconds()),
		"go":             runtime.Version(),
		"goroutines":     runtime.NumGoroutine(),
		"heap_mb":        float64(ms.HeapAlloc) / 1e6,
		"cache":          map[string]any{"dir": s.cache.Dir(), "entries": files, "bytes": bytes},
		"last_scan_ms":   lastScan.Milliseconds(),
		"digest_tried":   tried,
		"files":          map[string]any{"agent_key": mode(a.keyPath), "agent_key_next": nextStaged, "tls_key": mode(tlsKeyPath), "cert": mode(certPath), "cert_pin": pin},
		"bearer_expires": bearerExpires,
		"pid":            os.Getpid(),
		"executable":     exe,
		"cwd":            cwd,
	}
}

// handleRotate stages a new key and answers with a bearer under it; the first request that
// carries that bearer retires the old key (auth.keySource). A lost answer changes nothing.
func (a *agentSvc) handleRotate(w http.ResponseWriter, r *http.Request) {
	key, err := auth.StageKey(a.keyPath)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "key", err.Error())
		return
	}
	now := time.Now()
	value, expiry, err := auth.MintSession(key, AgentBearerTTL, now)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "key", err.Error())
		return
	}
	log.Printf("agent: key rotation staged by %s; the old key retires on the new bearer's first use", r.RemoteAddr)
	writeJSON(w, fleet.RotateResponse{Bearer: value, Expires: expiry.UnixMilli()})
}

// ---- the digest ------------------------------------------------------------------------------

// digestLoop parses, every five minutes, the closed root sessions that have no facts yet (one
// pass per fingerprint: a session that was tried is not tried again until its files change),
// newest first, through the Insights scanner on its private parse path. It is what makes a
// facts request an answer instead of a wait.
func (a *agentSvc) digestLoop() {
	time.Sleep(15 * time.Second) // let the index scan and the first hub contact go first
	for {
		a.digest()
		time.Sleep(5 * time.Minute)
	}
}

func (a *agentSvc) digest() {
	s := a.srv
	t := time.Now()
	s.maybeScan(20 * time.Second)
	a.mu.Lock()
	a.lastScan = time.Since(t)
	a.mu.Unlock()
	roots := s.src.Roots() // newest first
	var ids []string
	for _, fm := range roots {
		if _, ok := s.insights.factsFor(fm.ID); ok {
			continue
		}
		key := fingerprintKey(s.fingerprint(fm.ID))
		a.mu.Lock()
		tried := a.tried[fm.ID] == key
		if !tried {
			a.tried[fm.ID] = key
		}
		a.mu.Unlock()
		if !tried {
			ids = append(ids, fm.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if s.insights.scanner.Start(ids) {
		log.Printf("agent: digest: parsing %d sessions (%d roots)", len(ids), len(roots))
	}
}
