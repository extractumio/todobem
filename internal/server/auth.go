package server

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/extractumio/todobem/internal/auth"
)

// The UI's lite authentication (internal/auth). Every /api/* route needs a valid session cookie;
// POST /api/login exchanges a one-time token (`todobem token`) for one; POST /api/logout drops
// it; GET /api/auth says whether the gate is on and whether this browser is in. Static files
// stay open: they carry no data, and the lock screen lives in the SPA.
//
// The cookie is HttpOnly (the UI renders agent text; a script injection could read localStorage,
// not this), SameSite=Strict (a foreign page cannot ride it), Path=/api with a distinctive name
// (cookies are not port-scoped: it is sent to every http://127.0.0.1:<port> the user visits, so
// it must never be mistaken by another local app), Max-Age only (Safari drops Secure on plain
// http and Expires is redundant: the expiry is inside the signed value and checked here).
const sessionCookie = "todobem_session"

// SetAuth turns the gate on. A nil verifier (the -auth=off flag) leaves every route open.
func (s *Server) SetAuth(v *auth.Verifier) { s.auth = v }

// authGate wraps the API routes. next serves /api/* once the request is allowed.
func (s *Server) authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A browser that says the request comes from another site never gets an API answer,
		// cookie or not (defense in depth next to SameSite and the host check).
		if site := strings.ToLower(r.Header.Get("Sec-Fetch-Site")); site == "cross-site" {
			http.Error(w, "cross-site request", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/auth":
			s.handleAuthState(w, r)
			return
		case "/api/login":
			s.handleLogin(w, r)
			return
		case "/api/logout":
			s.handleLogout(w, r)
			return
		}
		if s.auth != nil && !s.authenticated(r) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"auth"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.auth.VerifySession(c.Value, time.Now()) == nil
}

func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"enabled": s.auth != nil, "authenticated": s.auth == nil || s.authenticated(r)})
}

// jsonPost admits only a real JSON POST: a cross-site HTML form can send text/plain or
// form-encoded bodies without a preflight, never application/json.
func jsonPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !jsonPost(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.auth == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := time.Now()
	ttl, err := s.auth.RedeemToken(body.Token, now)
	if err != nil {
		reason := "invalid"
		switch {
		case errors.Is(err, auth.ErrExpired):
			reason = "expired"
		case errors.Is(err, auth.ErrUsed):
			reason = "used"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "auth", "reason": reason})
		return
	}
	value, _, err := s.auth.MintSession(ttl, now)
	if err != nil {
		http.Error(w, "auth key unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(ttl / time.Second)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !jsonPost(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
