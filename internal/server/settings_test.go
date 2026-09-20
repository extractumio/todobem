package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
)

// writeRollout puts one root rollout with a recorded user message under <home>/sessions:
// one completed turn, started at a fixed time, with a shell command when cmd is given (an op
// with a detail and a source span).
func writeRollout(t *testing.T, home, id string) string {
	t.Helper()
	return writeRolloutAt(t, home, id, 1_757_671_200_000, "")
}

func writeRolloutAt(t *testing.T, home, id string, started int64, cmd string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "day")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ts := func(ms int64) string { return time.UnixMilli(started + ms).UTC().Format(time.RFC3339Nano) }
	lines := []string{
		fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":"/srv/app","cli_version":"0.150.0"}}`, ts(0), id, ts(0)),
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}`, ts(1000)),
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage","id":"message","content":[{"type":"text","text":"harmless fixture"}]}}}`, ts(2000)),
	}
	if cmd != "" {
		lines = append(lines,
			fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":%q}}`, ts(3000), `{"cmd":"`+cmd+`"}`),
			fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`, ts(5000)),
		)
	}
	lines = append(lines, fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn","last_agent_message":"done"}}`, ts(6000)))
	file := filepath.Join(dir, "rollout-"+id+".jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func settingsCall(s *Server, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1/api/settings", strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func decodeSettings(t *testing.T, w *httptest.ResponseRecorder) settingsState {
	t.Helper()
	var st settingsState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("settings payload: %v: %s", err, w.Body.String())
	}
	return st
}

func sessionIDs(t *testing.T, s *Server) []string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/sessions", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var sums []model.SessionSummary
	if err := json.Unmarshal(w.Body.Bytes(), &sums); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sum := range sums {
		ids = append(ids, sum.ID)
	}
	return ids
}

// TestSettingsSaveAppliesLive: a POST writes the file, re-points the index, and the response
// describes the new homes; a session of the removed home is gone from the list, its model 404s
// and its previously recorded source span is no longer served.
func TestSettingsSaveAppliesLive(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeRollout(t, first, "thread-first")
	writeRollout(t, second, "thread-second")
	s := New(settings.Homes{Codex: []string{first}}, fstest.MapFS{})
	path := filepath.Join(t.TempDir(), "settings.json")
	s.ConfigureSettings(path, settings.Settings{CodexHomes: []string{first}}, false)
	s.Scan()

	st := decodeSettings(t, settingsCall(s, http.MethodGet, ""))
	codex := func(st settingsState) []settingsHome { return st.Sources[0].Homes }
	if st.Path != path || st.Exists || st.Pinned || len(st.Sources) != 2 || st.Sources[0].Name != "codex" || st.Sources[1].Name != "claude" || len(codex(st)) != 1 || codex(st)[0].Path != first || codex(st)[0].Status != "ok" || codex(st)[0].Sessions != 1 || len(st.Sources[1].Homes) != 0 {
		t.Fatalf("initial state: %+v", st)
	}
	// open the first home's session so a source span is recorded
	sess, err := s.session("thread-first", true)
	if err != nil {
		t.Fatal(err)
	}
	var src model.Src
	sess.View(func(m *model.Session) {
		for _, mk := range m.Lanes[0].Markers {
			if mk.Src != nil {
				src = *mk.Src
			}
		}
	})
	if w := eventRequest(s, src); w.Code != http.StatusOK {
		t.Fatalf("recorded span before the change: %d", w.Code)
	}

	missing := filepath.Join(t.TempDir(), "not-mounted")
	w := settingsCall(s, http.MethodPost, `{"codex_homes":["`+second+`", "`+missing+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	st = decodeSettings(t, w)
	if !st.Exists || len(codex(st)) != 2 || codex(st)[0].Path != second || codex(st)[0].Status != "ok" || codex(st)[0].Sessions != 1 || codex(st)[1].Status != "missing" {
		t.Fatalf("state after save: %+v", st)
	}
	saved, exists, err := settings.Load(path)
	if err != nil || !exists || len(saved.CodexHomes) != 2 || saved.CodexHomes[0] != second || len(saved.ClaudeHomes) != 0 {
		t.Fatalf("file after save (claude_homes absent in the POST is saved as off): %+v exists=%v err=%v", saved, exists, err)
	}
	if ids := sessionIDs(t, s); len(ids) != 1 || ids[0] != "thread-second" {
		t.Fatalf("sessions after save: %v", ids)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/sessions/thread-first", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a session of the removed home must 404, got %d", rec.Code)
	}
	if w := eventRequest(s, src); w.Code != http.StatusNotFound {
		t.Fatalf("a span of the removed home must not be served, got %d", w.Code)
	}
}

func TestSettingsRejects(t *testing.T) {
	s := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{})
	s.ConfigureSettings(filepath.Join(t.TempDir(), "settings.json"), settings.Settings{}, false)
	// not JSON → 415 (a cross-site form could send text/plain)
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/settings", strings.NewReader(`{"codex_homes":["/x"]}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", w.Code)
	}
	for body, want := range map[string]int{
		`{"codex_homes":[]}`:                   http.StatusBadRequest,
		`{"codex_homes":[],"claude_homes":[]}`: http.StatusBadRequest,
		`{"claude_homes":["relative/claude"]}`: http.StatusBadRequest,
		`{"codex_homes":["relative/codex"]}`:   http.StatusBadRequest,
		`{"codex_homes":["/a","/a"]}`:          http.StatusBadRequest,
		`{"codex_homes":[`:                     http.StatusBadRequest,
	} {
		if w := settingsCall(s, http.MethodPost, body); w.Code != want {
			t.Errorf("%s: %d want %d (%s)", body, w.Code, want, w.Body.String())
		}
	}
	if w := settingsCall(s, http.MethodDelete, ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: %d", w.Code)
	}
	// pinned by -codex: read-only
	s.ConfigureSettings(filepath.Join(t.TempDir(), "settings.json"), settings.Settings{CodexHomes: []string{"~/elsewhere"}}, true)
	st := decodeSettings(t, settingsCall(s, http.MethodGet, ""))
	if !st.Pinned || st.Sources[0].Homes[0].Path != "~/elsewhere" {
		t.Errorf("pinned state: %+v", st)
	}
	if w := settingsCall(s, http.MethodPost, `{"codex_homes":["/x"]}`); w.Code != http.StatusConflict {
		t.Errorf("pinned POST: %d", w.Code)
	}
	// no settings file configured at all (tools, tests): read-only too
	s2 := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{})
	if st := decodeSettings(t, settingsCall(s2, http.MethodGet, "")); !st.Pinned {
		t.Errorf("unconfigured must be read-only: %+v", st)
	}
}

func TestSettingsBehindTheAuthGate(t *testing.T) {
	s, h, key := authFixture(t)
	s.ConfigureSettings(filepath.Join(t.TempDir(), "settings.json"), settings.Settings{}, false)
	if w := call(h, http.MethodGet, "/api/settings", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET without a session: %d", w.Code)
	}
	if w := call(h, http.MethodPost, "/api/settings", "application/json", `{"codex_homes":["/x"]}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST without a session: %d", w.Code)
	}
	tok, _ := auth.MintToken(key, time.Hour, time.Now())
	_, c := login(t, h, tok)
	if w := call(h, http.MethodGet, "/api/settings", "", "", c); w.Code != http.StatusOK {
		t.Fatalf("GET with a session: %d", w.Code)
	}
}

// TestRecordedSourceAcrossHomes: a span is served from any configured home, never from a sibling
// folder whose name shares a prefix with one, and a home added live serves its spans.
func TestRecordedSourceAcrossHomes(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "codex")
	sibling := filepath.Join(base, "codex2")
	writeRollout(t, first, "thread-first")
	siblingFile := writeRollout(t, sibling, "thread-sibling")
	s := New(settings.Homes{Codex: []string{first}}, fstest.MapFS{})
	s.Scan()
	if _, err := s.readSource(model.Src{File: siblingFile, Off: 0, Len: 10}); err == nil {
		t.Fatal("a sibling folder with a common prefix was served as part of the home")
	}
	s.SetHomes(settings.Homes{Codex: []string{first, sibling}})
	sess, err := s.session("thread-sibling", true)
	if err != nil {
		t.Fatal(err)
	}
	var src model.Src
	sess.View(func(m *model.Session) {
		for _, mk := range m.Lanes[0].Markers {
			if mk.Src != nil {
				src = *mk.Src
			}
		}
	})
	if w := eventRequest(s, src); w.Code != http.StatusOK {
		t.Fatalf("span of a home added live: %d %s", w.Code, w.Body.String())
	}
	if ids := sessionIDs(t, s); len(ids) != 2 {
		t.Fatalf("sessions of both homes: %v", ids)
	}
}
