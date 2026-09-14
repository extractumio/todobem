package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/extractumio/todobem/internal/insights"
)

func insightsRequest(s *Server, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestInsightsReportOverAnOpenedSession(t *testing.T) {
	s, _, _ := sourceFixture(t, false)
	w := insightsRequest(s, http.MethodGet, "/api/insights/report?period=all")
	if w.Code != http.StatusOK {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	var rep insights.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Params.Period.Kind != "all" || rep.Scope.Sessions != 1 || rep.Fallback != "fewer_than_3_sessions" || len(rep.Scope.Pending) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Sources[0].ID != "root-thread" || rep.GeneratedAt == 0 || rep.SourcesHash == "" {
		t.Fatalf("sources %+v", rep.Sources)
	}
	// one-session scope selects by id whatever the project filter says
	w = insightsRequest(s, http.MethodGet, "/api/insights/report?session=root-thread&cwd=/elsewhere")
	if w.Code != http.StatusOK {
		t.Fatalf("session report: %d", w.Code)
	}
	var one insights.Report // a fresh value: omitted (empty) fields must not inherit the previous decode
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one.Scope.Sessions != 1 || one.Params.Period.Kind != "session" || one.Fallback != "" {
		t.Fatalf("session report %+v err=%v", one, err)
	}
	// a project with no sessions in the period
	w = insightsRequest(s, http.MethodGet, "/api/insights/report?period=30d&cwd=/elsewhere")
	var empty insights.Report
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil || empty.Scope.Sessions != 0 || empty.Fallback != "no_sessions" {
		t.Fatalf("empty report %+v err=%v", empty, err)
	}
	// the probe answers with the sources hash only
	w = insightsRequest(s, http.MethodGet, "/api/insights/report?period=all&probe=1")
	var probe map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &probe); err != nil || probe["sources_hash"] == "" {
		t.Fatalf("probe %s err=%v", w.Body.String(), err)
	}
	if w := insightsRequest(s, http.MethodPut, "/api/insights/report"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT report: %d", w.Code)
	}
}

func TestInsightsRulesScanAndStatus(t *testing.T) {
	s, _, _ := sourceFixture(t, false)
	w := insightsRequest(s, http.MethodGet, "/api/insights/rules")
	var rules struct {
		Rules  []map[string]string `json:"rules"`
		Groups []string            `json:"groups"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rules) != nil || len(rules.Rules) != len(insights.Catalogue) || len(rules.Groups) != len(insights.GroupOrder) {
		t.Fatalf("rules: %d %s", w.Code, w.Body.String())
	}
	// nothing pending: a scan starts and finishes with nothing to do
	w = insightsRequest(s, http.MethodPost, "/api/insights/scan?period=all")
	if w.Code != http.StatusAccepted {
		t.Fatalf("scan: %d %s", w.Code, w.Body.String())
	}
	w = insightsRequest(s, http.MethodGet, "/api/insights/status")
	var p insights.Progress
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Total != 0 {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if w := insightsRequest(s, http.MethodDelete, "/api/insights/scan"); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d", w.Code)
	}
	if w := insightsRequest(s, http.MethodGet, "/api/insights/nothing"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown route: %d", w.Code)
	}
}
