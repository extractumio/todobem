package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/extractumio/todobem/internal/settings"
)

// The build header names the UI the server serves: the same files give the same id across
// servers (a restart alone never reloads a tab), one changed byte a different one (a deploy
// does), and every response carries it — the static page, the API, even a locked answer.
func TestBuildHeaderFollowsTheEmbeddedWeb(t *testing.T) {
	web := func(js string) fstest.MapFS {
		return fstest.MapFS{"index.html": {Data: []byte("<html>")}, "app.js": {Data: []byte(js)}}
	}
	a := New(settings.Homes{Codex: []string{t.TempDir()}}, web("one"))
	same := New(settings.Homes{Codex: []string{t.TempDir()}}, web("one"))
	other := New(settings.Homes{Codex: []string{t.TempDir()}}, web("two"))
	if a.build == "" || len(a.build) != 12 {
		t.Fatalf("fingerprint %q", a.build)
	}
	if a.build != same.build {
		t.Fatalf("the same files must fingerprint alike: %s vs %s", a.build, same.build)
	}
	if a.build == other.build {
		t.Fatal("a changed file must change the fingerprint")
	}
	// `todobem unknown` and `todobem cache` build a server with no UI at all
	if noUI := New(settings.Homes{Codex: []string{t.TempDir()}}, nil); noUI.build != "" {
		t.Fatalf("a server without a web tree has no build id, got %q", noUI.build)
	}
	for _, path := range []string{"/", "/app.js", "/api/sessions", "/api/auth"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if got := w.Header().Get(BuildHeader); got != a.build {
			t.Fatalf("%s: %s=%q, want %q", path, BuildHeader, got, a.build)
		}
	}
}
