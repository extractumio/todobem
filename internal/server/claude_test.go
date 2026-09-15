package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
)

// writeClaudeSession puts one Claude Code root session under <home>/projects.
func writeClaudeSession(t *testing.T, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, "projects", "-synthetic")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, id+".jsonl")
	tail := `,"userType":"external","entrypoint":"cli","cwd":"/synthetic","sessionId":"` + id + `","version":"2.1.270","gitBranch":"main"}` + "\n"
	content := `{"type":"ai-title","aiTitle":"Synthetic Claude session","sessionId":"` + id + `"}` + "\n" +
		`{"parentUuid":null,"isSidechain":false,"promptSource":"typed","message":{"role":"user","content":"harmless fixture"},"type":"user","uuid":"u1","timestamp":"2026-09-12T10:00:00Z"` + tail +
		`{"parentUuid":"u1","isSidechain":false,"message":{"model":"claude-synthetic-1","id":"m1","role":"assistant","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}},"type":"assistant","uuid":"a1","timestamp":"2026-09-12T10:00:04Z"` + tail
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestClaudeSourceThroughTheServer: a Claude home next to a Codex one — both in one list with
// their source named, a Claude session's model and source span served, the settings page
// describing both sources and accepting claude_homes, the Insights scope narrowed by source.
func TestClaudeSourceThroughTheServer(t *testing.T) {
	codexHome, claudeHome := t.TempDir(), t.TempDir()
	writeRollout(t, codexHome, "thread-codex")
	writeClaudeSession(t, claudeHome, "session-claude")
	s := New(settings.Homes{Codex: []string{codexHome}, Claude: []string{claudeHome}}, fstest.MapFS{})
	path := filepath.Join(t.TempDir(), "settings.json")
	s.ConfigureSettings(path, settings.Settings{CodexHomes: []string{codexHome}, ClaudeHomes: []string{claudeHome}}, false)
	s.Scan()

	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/sessions", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var sums []model.SessionSummary
	if err := json.Unmarshal(w.Body.Bytes(), &sums); err != nil || len(sums) != 2 {
		t.Fatalf("sessions: %s err=%v", w.Body.String(), err)
	}
	bySource := map[string]model.SessionSummary{}
	for _, sum := range sums {
		bySource[sum.Source] = sum
	}
	if c := bySource["claude"]; c.ID != "session-claude" || c.Title != "Synthetic Claude session" || c.CLI != "2.1.270" || c.Model != "claude-synthetic-1" || c.LastAnswer != "Done." {
		t.Fatalf("claude row: %+v", c)
	}
	if bySource["codex"].ID != "thread-codex" {
		t.Fatalf("codex row: %+v", bySource["codex"])
	}
	// the model and a recorded span of the Claude session
	sess, err := s.session("session-claude", true)
	if err != nil {
		t.Fatal(err)
	}
	var src model.Src
	sess.View(func(m *model.Session) {
		if m.Source != "claude" || len(m.Lanes[0].Turns) != 1 || m.Lanes[0].Turns[0].Status != "completed" {
			t.Fatalf("claude model: %+v", m)
		}
		for _, mk := range m.Lanes[0].Markers {
			if mk.Src != nil {
				src = *mk.Src
			}
		}
	})
	if w := eventRequest(s, src); w.Code != http.StatusOK {
		t.Fatalf("claude span: %d %s", w.Code, w.Body.String())
	}
	// settings: both sources described; a POST that turns Codex off is applied
	st := decodeSettings(t, settingsCall(s, http.MethodGet, ""))
	if len(st.Sources) != 2 || st.Sources[1].Name != "claude" || len(st.Sources[1].Homes) != 1 || st.Sources[1].Homes[0].Status != "ok" || st.Sources[1].Homes[0].Sessions != 1 {
		t.Fatalf("settings: %+v", st)
	}
	w = settingsCall(s, http.MethodPost, `{"codex_homes":[],"claude_homes":["`+claudeHome+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if st = decodeSettings(t, w); len(st.Sources[0].Homes) != 0 || len(st.Sources[1].Homes) != 1 {
		t.Fatalf("after save: %+v", st)
	}
	if ids := sessionIDs(t, s); len(ids) != 1 || ids[0] != "session-claude" {
		t.Fatalf("sessions with codex off: %v", ids)
	}
	saved, _, err := settings.Load(path)
	if err != nil || len(saved.CodexHomes) != 0 || len(saved.ClaudeHomes) != 1 {
		t.Fatalf("saved: %+v err=%v", saved, err)
	}
	// insights: the scope follows the sources parameter (the Claude session, not analysed since
	// the folders changed, is pending — only when its source is in scope)
	w = insightsRequest(s, http.MethodGet, "/api/insights/report?period=all&sources=codex")
	var rep insights.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Scope.Sessions != 0 || len(rep.Scope.Pending) != 0 || len(rep.Params.Sources) != 1 {
		t.Fatalf("report scoped to codex: %+v err=%v", rep, err)
	}
	w = insightsRequest(s, http.MethodGet, "/api/insights/report?period=all&sources=claude,codex")
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || len(rep.Scope.Pending) != 1 || rep.Scope.Pending[0].ID != "session-claude" {
		t.Fatalf("report over both: %+v err=%v", rep, err)
	}
}
