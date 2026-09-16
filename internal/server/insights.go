package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/source"
	"github.com/extractumio/todobem/internal/store"
)

// insightsSvc serves /api/insights/*: a period report over the project's sessions, built from
// per-session facts. Facts come, in order, from an open parser-backed session, memory, the
// facts sidecar, or the model cache (extracted and saved); sessions with none of these are
// "pending" and are parsed only when the user asks (POST scan), on a dedicated path that never
// touches the server's session pools (docs/ARCHITECTURE.md §10.3).
type insightsSvc struct {
	srv     *Server
	scanner *insights.Scanner

	mu      sync.Mutex
	facts   map[string]factsEntry
	parseFP map[string]store.Fingerprint // fingerprint taken before a scan parse, keyed by id
}

type factsEntry struct {
	f  insights.Facts
	fp store.Fingerprint
}

func newInsights(s *Server) *insightsSvc {
	svc := &insightsSvc{srv: s, facts: map[string]factsEntry{}, parseFP: map[string]store.Fingerprint{}}
	svc.scanner = insights.NewScanner(scanLoader{svc}, 2, svc.storeFacts)
	return svc
}

// scanLoader parses one session for the scanner: a private source.Session, refreshed once, its
// model cached on disk when closed, never inserted into the server's opened/cached pools.
type scanLoader struct{ svc *insightsSvc }

func (l scanLoader) Parse(id string) (*model.Session, error) {
	s := l.svc.srv
	fp := s.fingerprint(id)
	l.svc.mu.Lock()
	l.svc.parseFP[id] = fp
	l.svc.mu.Unlock()
	sess, err := s.src.Open(id)
	if err != nil {
		return nil, err
	}
	if _, err := sess.Refresh(); err != nil {
		return nil, err
	}
	var m *model.Session
	sess.View(func(x *model.Session) {
		m = x
		if !x.Live {
			_ = s.cache.Save(id, x, fp)
		}
	})
	return m, nil
}

// storeFacts is the scanner's sink: remember the facts and persist them under the fingerprint
// taken before the parse (a file that grew meanwhile misses on the next request, as it should).
func (svc *insightsSvc) storeFacts(id string, f insights.Facts) {
	svc.mu.Lock()
	fp, ok := svc.parseFP[id]
	delete(svc.parseFP, id)
	svc.mu.Unlock()
	if !ok {
		fp = svc.srv.fingerprint(id)
	}
	svc.remember(id, f, fp)
	if !f.Live {
		_ = svc.srv.cache.SaveSidecar("facts", id, insights.FactsVersion, fp, f)
	}
}

func (svc *insightsSvc) remember(id string, f insights.Facts, fp store.Fingerprint) {
	svc.mu.Lock()
	if len(svc.facts) >= 2000 {
		svc.facts = map[string]factsEntry{}
	}
	svc.facts[id] = factsEntry{f, fp}
	svc.mu.Unlock()
}

// factsFor returns current facts for id without parsing anything. ok is false when only a parse
// could produce them (the session is pending).
func (svc *insightsSvc) factsFor(id string) (insights.Facts, bool) {
	s := svc.srv
	fp := s.fingerprint(id)
	s.mu.Lock()
	sess := s.opened[id]
	entry := s.cached[id]
	s.mu.Unlock()
	if sess != nil {
		var f insights.Facts
		sess.View(func(m *model.Session) { f = insights.Extract(m) })
		return f, true
	}
	svc.mu.Lock()
	e, ok := svc.facts[id]
	svc.mu.Unlock()
	if ok && e.fp.Equal(fp) {
		return e.f, true
	}
	if entry != nil && entry.fp.Equal(fp) {
		f := insights.Extract(entry.m)
		svc.remember(id, f, fp)
		return f, true
	}
	var f insights.Facts
	if sfp, ok := s.cache.LoadSidecar("facts", id, insights.FactsVersion, &f); ok && sfp.Equal(fp) {
		svc.remember(id, f, fp)
		return f, true
	}
	if m, mfp, ok := s.cache.Load(id); ok && mfp.Equal(fp) {
		f = insights.Extract(m)
		svc.remember(id, f, fp)
		_ = s.cache.SaveSidecar("facts", id, insights.FactsVersion, fp, f)
		return f, true
	}
	return insights.Facts{}, false
}

// paramsFrom reads the report parameters: cwd, sources (comma-separated source names; absent =
// all), period (7d|30d|90d|all|custom|session), from/to (ms, custom), session (one session),
// include_live.
func paramsFrom(r *http.Request, now int64) insights.Params {
	q := r.URL.Query()
	p := insights.Params{CWD: q.Get("cwd"), IncludeLive: q.Get("include_live") == "1"}
	if v := q.Get("sources"); v != "" {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				p.Sources = append(p.Sources, name)
			}
		}
	}
	p.Period = insights.Period{Kind: q.Get("period"), Session: q.Get("session")}
	if p.Period.Kind == "custom" {
		p.Period.From, _ = strconv.ParseInt(q.Get("from"), 10, 64)
		p.Period.To, _ = strconv.ParseInt(q.Get("to"), 10, 64)
		if p.Period.To <= 0 {
			p.Period.To = now
		}
	}
	if p.Period.Session != "" {
		p.Period.Kind = "session"
	}
	p.Period = p.Period.Resolve(now)
	return p
}

// selection is the sessions of a report: those with facts, and those still to parse.
type selection struct {
	inputs  []insights.Input
	pending []insights.Source
	live    int
}

func (svc *insightsSvc) selectSessions(p insights.Params) selection {
	s := svc.srv
	s.maybeScan(20 * time.Second)
	var sel selection
	for _, sum := range s.summaries() {
		if p.Period.Kind != "session" && (p.CWD != "" && sum.CWD != p.CWD || !p.HasSource(sum.Source)) {
			continue
		}
		if !p.InPeriod(sum.ID, sum.Updated, sum.Live) {
			if sum.Live && p.Period.Kind != "session" && sum.CWD == p.CWD && sum.Updated >= p.Period.From && sum.Updated <= p.Period.To {
				sel.live++
			}
			continue
		}
		f, ok := svc.factsFor(sum.ID)
		if !ok {
			sel.pending = append(sel.pending, insights.Source{ID: sum.ID, Title: sum.Title, Ended: sum.Updated, Live: sum.Live})
			continue
		}
		if f.Live && !p.IncludeLive && p.Period.Kind != "session" {
			sel.live++
			continue
		}
		if f.Title == "" {
			f.Title = sum.Title
		}
		sel.inputs = append(sel.inputs, insights.Input{Facts: f, Fingerprint: fingerprintKey(s.fingerprint(sum.ID))})
	}
	return sel
}

func fingerprintKey(fp store.Fingerprint) string {
	var b strings.Builder
	b.WriteString(fp.Rules)
	for _, f := range fp.Files {
		b.WriteString("|")
		b.WriteString(f.Path)
		b.WriteString(":")
		b.WriteString(strconv.FormatInt(f.Size, 10))
		b.WriteString(":")
		b.WriteString(strconv.FormatInt(f.Mod, 10))
	}
	return b.String()
}

func (svc *insightsSvc) handle(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/api/insights/") {
	case "report":
		svc.handleReport(w, r)
	case "scan":
		svc.handleScan(w, r)
	case "status":
		writeJSON(w, svc.scanner.Progress())
	case "rules":
		svc.handleRules(w)
	default:
		http.NotFound(w, r)
	}
}

// handleReport builds the report for the parameters from the facts at hand (sub-second; never
// parses). GET and POST behave the same; POST is the page's explicit "Regenerate".
func (svc *insightsSvc) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UnixMilli()
	p := paramsFrom(r, now)
	sel := svc.selectSessions(p)
	if r.URL.Query().Get("probe") == "1" {
		// a cheap staleness probe: the sources the report would be built from right now
		var src []insights.Source
		for _, in := range sel.inputs {
			src = append(src, insights.Source{ID: in.Facts.ID, Fingerprint: in.Fingerprint})
		}
		sort.Slice(src, func(a, b int) bool { return src[a].ID < src[b].ID })
		writeJSON(w, map[string]any{"sources_hash": insights.SourcesHash(src), "pending": len(sel.pending), "scan": svc.scanner.Progress()})
		return
	}
	rep := insights.Build(p, sel.inputs, now)
	rep.Scope.Pending = sel.pending
	if rep.Scope.Pending == nil {
		rep.Scope.Pending = []insights.Source{}
	}
	rep.Scope.LiveExcluded = sel.live
	writeJSON(w, rep)
}

// handleScan: POST starts parsing the pending sessions of the parameters (one scan at a time),
// DELETE cancels, GET reports progress.
func (svc *insightsSvc) handleScan(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		p := paramsFrom(r, time.Now().UnixMilli())
		sel := svc.selectSessions(p)
		ids := make([]string, 0, len(sel.pending))
		for _, src := range sel.pending {
			ids = append(ids, src.ID)
		}
		started := svc.scanner.Start(ids)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !started {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		writeJSON(w, map[string]any{"started": started, "queued": len(ids), "scan": svc.scanner.Progress()})
	case http.MethodDelete:
		svc.scanner.Cancel()
		writeJSON(w, svc.scanner.Progress())
	case http.MethodGet:
		writeJSON(w, svc.scanner.Progress())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRules exposes the detector catalogue the way /api/rules exposes the classifier.
func (svc *insightsSvc) handleRules(w http.ResponseWriter) {
	type rule struct {
		ID    string `json:"id"`
		Group string `json:"group"`
		Title string `json:"title"`
		Class string `json:"class,omitempty"`
	}
	rules := make([]rule, 0, len(insights.Catalogue))
	for _, d := range insights.Catalogue {
		rules = append(rules, rule{d.ID, d.Group, d.Title, d.Class})
	}
	writeJSON(w, map[string]any{"rules": rules, "groups": insights.GroupOrder, "gap_buckets": insights.GapBucketOrder(), "facts_version": insights.FactsVersion})
}

// summaries lists the root sessions of every source, with the opened ones' live state.
func (s *Server) summaries() []model.SessionSummary {
	s.mu.Lock()
	opened := map[string]*source.Session{}
	for k, v := range s.opened {
		opened[k] = v
	}
	s.mu.Unlock()
	return s.src.Summaries(opened)
}
