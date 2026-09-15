package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
)

func sourceFixture(t *testing.T, relative bool) (*Server, model.Src, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "day")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "rollout-test.jsonl")
	lines := []string{
		`{"timestamp":"2026-09-12T10:00:00Z","type":"session_meta","payload":{"id":"root-thread","timestamp":"2026-09-12T10:00:00Z"}}`,
		`{"timestamp":"2026-09-12T10:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}`,
		`{"timestamp":"2026-09-12T10:00:02Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn","item":{"type":"UserMessage","id":"message","content":[{"type":"text","text":"harmless fixture"}]}}}`,
		`{"timestamp":"2026-09-12T10:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}`,
	}
	var content string
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	configuredHome := home
	if relative {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		configuredHome, err = filepath.Rel(cwd, home)
		if err != nil {
			t.Fatal(err)
		}
	}
	s := New(settings.Homes{Codex: []string{configuredHome}}, fstest.MapFS{})
	sess, err := s.session("root-thread", true)
	if err != nil {
		t.Fatal(err)
	}
	var src model.Src
	sess.View(func(m *model.Session) {
		for _, marker := range m.Lanes[0].Markers {
			if marker.Kind == "user_message" && marker.Src != nil {
				src = *marker.Src
			}
		}
	})
	if src.File == "" {
		t.Fatal("fixture has no recorded user-message source")
	}
	return s, src, lines[2]
}

func eventRequest(s *Server, src model.Src) *httptest.ResponseRecorder {
	query := url.Values{"file": {src.File}, "off": {strconv.FormatInt(src.Off, 10)}, "len": {strconv.Itoa(src.Len)}}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/event?"+query.Encode(), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRecordedSourceRequiresExactSpan(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(strconv.FormatBool(relative), func(t *testing.T) {
			s, src, line := sourceFixture(t, relative)
			w := eventRequest(s, src)
			if w.Code != http.StatusOK || w.Body.String() != line || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("recorded source: status=%d body=%q", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("source response may be cached")
			}
			for _, wrong := range []model.Src{
				{File: src.File, Off: src.Off + 1, Len: src.Len},
				{File: src.File, Off: src.Off, Len: src.Len - 1},
				{File: src.File, Off: -1, Len: src.Len},
			} {
				if got := eventRequest(s, wrong); got.Code == http.StatusOK {
					t.Fatalf("served an unrecorded span: %+v", wrong)
				}
			}
			unrecorded := filepath.Join(s.src.Source("codex").Homes()[0], "note.txt")
			if err := os.WriteFile(unrecorded, []byte("harmless fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := eventRequest(s, model.Src{File: unrecorded, Len: 16}); got.Code != http.StatusNotFound {
				t.Fatalf("unrecorded source status=%d", got.Code)
			}
		})
	}
}

func TestRecordedSourceRejectsSymlinkReplacement(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(strconv.FormatBool(parent), func(t *testing.T) {
			s, src, _ := sourceFixture(t, false)
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, filepath.Base(src.File))
			if err := os.WriteFile(outsideFile, []byte("harmless outside fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if parent {
				dir := filepath.Dir(src.File)
				if err := os.Rename(dir, dir+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(src.File); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideFile, src.File); err != nil {
					t.Fatal(err)
				}
			}
			if got := eventRequest(s, src); got.Code != http.StatusNotFound {
				t.Fatalf("symlink source status=%d", got.Code)
			}
			if _, err := s.readSource(src); err == nil {
				t.Fatal("per-operation source reader accepted the symlink escape")
			}
		})
	}
}

func TestHandlerHostPolicy(t *testing.T) {
	s := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{})
	for _, tc := range []struct {
		host       string
		configured string
		want       int
	}{
		{"127.0.0.1:7788", "", http.StatusOK},
		{"[::1]:7788", "", http.StatusOK},
		{"localhost:7788", "", http.StatusOK},
		{"LOCALHOST.:7788", "", http.StatusOK},
		{"192.0.2.1:7788", "0.0.0.0:7788", http.StatusOK},
		{"viewer.local:7788", "viewer.local:7788", http.StatusOK},
		{"untrusted.example:7788", "", http.StatusForbidden},
		{"localhost.untrusted.example:7788", "", http.StatusForbidden},
		{"untrusted.example:7788", "viewer.local:7788", http.StatusForbidden},
	} {
		t.Run(tc.host+"/"+tc.configured, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/rules", nil)
			r.Host = tc.host
			w := httptest.NewRecorder()
			s.Handler(tc.configured).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}

func TestRulesPayload(t *testing.T) {
	s := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{})
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/rules", nil)
	r.Host = "127.0.0.1:7788"
	w := httptest.NewRecorder()
	s.Handler("").ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var payload struct {
		Rules        []map[string]any `json:"rules"`
		BuiltinRules int              `json:"builtin_rules"`
		ReviewSkills []string         `json:"review_skills"`
		Lifecycle    struct {
			Stages   []string                       `json:"stages"`
			Defaults map[string]string              `json:"defaults"`
			Pins     map[string]string              `json:"pins"`
			Matchers map[string]map[string][]string `json:"matchers"`
		} `json:"lifecycle"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BuiltinRules == 0 || payload.BuiltinRules != len(payload.Rules) {
		t.Errorf("builtin_rules=%d rules=%d", payload.BuiltinRules, len(payload.Rules))
	}
	if len(payload.ReviewSkills) == 0 {
		t.Error("review_skills should list at least the built-in matcher")
	}
	lc := payload.Lifecycle
	if len(lc.Stages) != 8 || lc.Defaults["code"] != "implement" || lc.Defaults["test"] != "test" || lc.Pins["pr review"] != "review" || lc.Pins["journalctl"] != "operate" || len(lc.Matchers["skills"]["review"]) == 0 {
		t.Errorf("lifecycle tables not served: %+v", lc)
	}
}
