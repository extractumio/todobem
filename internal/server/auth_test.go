package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/settings"
)

func authFixture(t *testing.T) (*Server, http.Handler, []byte) {
	t.Helper()
	key := make([]byte, auth.KeyBytes)
	for i := range key {
		key[i] = byte(200 - i)
	}
	s := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{"index.html": {Data: []byte("<html>shell</html>")}})
	s.SetAuth(auth.NewVerifier(key, time.Now().Add(-time.Minute)))
	return s, s.Handler("127.0.0.1:7788"), key
}

func call(h http.Handler, method, path, contentType, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:7788"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:7788"
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, h http.Handler, token string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	w := call(h, http.MethodPost, "/api/login", "application/json", `{"token":"`+token+`"}`)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return w, c
		}
	}
	return w, nil
}

func TestAuthGateRoundTrip(t *testing.T) {
	_, h, key := authFixture(t)
	// locked: every API route answers 401 JSON, the shell stays open
	if w := call(h, http.MethodGet, "/api/sessions", "", ""); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"error":"auth"`) {
		t.Fatalf("locked API: %d %s", w.Code, w.Body.String())
	}
	if w := call(h, http.MethodGet, "/api/rules", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("locked rules: %d", w.Code)
	}
	if w := call(h, http.MethodGet, "/", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "shell") {
		t.Fatalf("static must stay open: %d", w.Code)
	}
	var st struct {
		Enabled, Authenticated bool
	}
	w := call(h, http.MethodGet, "/api/auth", "", "")
	json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != http.StatusOK || !st.Enabled || st.Authenticated {
		t.Fatalf("auth state before login: %d %+v", w.Code, st)
	}
	// a token opens a session; the cookie has the safe attributes
	tok, _ := auth.MintToken(key, 2*time.Hour, time.Now())
	w, c := login(t, h, tok)
	if w.Code != http.StatusNoContent || c == nil {
		t.Fatalf("login: %d cookie=%v", w.Code, c)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/api" || c.Secure || c.MaxAge != 7200 || !c.Expires.IsZero() || c.Domain != "" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	if w := call(h, http.MethodGet, "/api/sessions", "", "", c); w.Code != http.StatusOK {
		t.Fatalf("with session: %d %s", w.Code, w.Body.String())
	}
	w = call(h, http.MethodGet, "/api/auth", "", "", c)
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Authenticated {
		t.Fatalf("auth state after login: %+v", st)
	}
	// the same token cannot be used twice
	if w, _ := login(t, h, tok); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"reason":"used"`) {
		t.Fatalf("reused token: %d %s", w.Code, w.Body.String())
	}
	// tampered session, tampered token, expired token, pre-boot token
	bad := *c
	bad.Value = c.Value[:len(c.Value)-3] + "AAA"
	if w := call(h, http.MethodGet, "/api/sessions", "", "", &bad); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered session accepted: %d", w.Code)
	}
	if w, _ := login(t, h, "NOTATOKEN"); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"reason":"invalid"`) {
		t.Fatalf("garbage token: %d %s", w.Code, w.Body.String())
	}
	old, _ := auth.MintToken(key, time.Hour, time.Now().Add(-auth.TokenWindow-time.Minute))
	if w, _ := login(t, h, old); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"reason":"expired"`) {
		t.Fatalf("expired token: %d %s", w.Code, w.Body.String())
	}
	preBoot, _ := auth.MintToken(key, time.Hour, time.Now().Add(-2*time.Minute)) // boot was 1 minute ago
	if w, _ := login(t, h, preBoot); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"reason":"expired"`) {
		t.Fatalf("pre-boot token: %d %s", w.Code, w.Body.String())
	}
	// logout clears the cookie
	w = call(h, http.MethodPost, "/api/logout", "application/json", `{}`, c)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	cleared := false
	for _, cc := range w.Result().Cookies() {
		if cc.Name == sessionCookie && cc.MaxAge < 0 && cc.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout must clear the cookie: %v", w.Result().Cookies())
	}
}

func TestAuthGateRejectsCrossSiteShapes(t *testing.T) {
	_, h, key := authFixture(t)
	tok, _ := auth.MintToken(key, time.Hour, time.Now())
	// a form post (no preflight possible) is not a login
	if w := call(h, http.MethodPost, "/api/login", "text/plain", `{"token":"`+tok+`"}`); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain login: %d", w.Code)
	}
	if w := call(h, http.MethodPost, "/api/login", "application/x-www-form-urlencoded", "token="+tok); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form login: %d", w.Code)
	}
	if w := call(h, http.MethodGet, "/api/login?token="+tok, "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET login: %d", w.Code)
	}
	if w := call(h, http.MethodGet, "/api/logout", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET logout: %d", w.Code)
	}
	// a browser-declared cross-site request never reaches a route
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7788/api/login", strings.NewReader(`{"token":"`+tok+`"}`))
	r.Host = "127.0.0.1:7788"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", w.Code)
	}
	// the token was not consumed by any of the refused shapes
	if w, _ := login(t, h, tok); w.Code != http.StatusNoContent {
		t.Fatalf("token should still be valid: %d %s", w.Code, w.Body.String())
	}
	// an oversized body is refused before any decoding
	if w := call(h, http.MethodPost, "/api/login", "application/json", `{"token":"`+strings.Repeat("A", 5000)+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

func TestAuthOffLeavesRoutesOpen(t *testing.T) {
	s := New(settings.Homes{Codex: []string{t.TempDir()}}, fstest.MapFS{})
	h := s.Handler("127.0.0.1:7788")
	if w := call(h, http.MethodGet, "/api/sessions", "", ""); w.Code != http.StatusOK {
		t.Fatalf("auth off: %d", w.Code)
	}
	var st struct {
		Enabled, Authenticated bool
	}
	w := call(h, http.MethodGet, "/api/auth", "", "")
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Enabled || !st.Authenticated {
		t.Fatalf("auth state with auth off: %+v", st)
	}
	if w := call(h, http.MethodPost, "/api/login", "application/json", `{"token":"x"}`); w.Code != http.StatusNoContent {
		t.Fatalf("login with auth off is a no-op: %d", w.Code)
	}
}
