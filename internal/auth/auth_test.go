package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "auth.key")
	key, err := LoadOrCreateKey(p)
	if err != nil || len(key) != KeyBytes {
		t.Fatalf("create: %v (%d bytes)", err, len(key))
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o, want 600", st.Mode().Perm())
	}
	again, err := LoadOrCreateKey(p)
	if err != nil || string(again) != string(key) {
		t.Fatalf("reload changed the key: %v", err)
	}
	rotated, err := RotateKey(p)
	if err != nil || string(rotated) == string(key) {
		t.Fatalf("rotate: %v same=%v", err, string(rotated) == string(key))
	}
	// a key readable by others is refused, not silently used
	os.Chmod(p, 0o644)
	if _, err := LoadOrCreateKey(p); err == nil {
		t.Fatal("world-readable key must be refused")
	}
	os.Chmod(p, 0o600)
	os.WriteFile(p, []byte("short"), 0o600)
	if _, err := LoadOrCreateKey(p); err == nil {
		t.Fatal("truncated key must be refused")
	}
	if _, err := LoadOrCreateKey(""); err == nil {
		t.Fatal("empty path must be refused")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	key := make([]byte, KeyBytes)
	for i := range key {
		key[i] = byte(i)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tok, err := MintToken(key, 7*24*time.Hour, now)
	if err != nil || len(tok) != 52 {
		t.Fatalf("mint: %v len=%d", err, len(tok))
	}
	v := NewVerifier(key, now.Add(-time.Hour))
	ttl, err := v.RedeemToken(tok, now.Add(2*time.Minute))
	if err != nil || ttl != 7*24*time.Hour {
		t.Fatalf("redeem: %v ttl=%v", err, ttl)
	}
	if _, err := v.RedeemToken(tok, now.Add(3*time.Minute)); err != ErrUsed {
		t.Fatalf("second use: %v, want ErrUsed", err)
	}
	// lower-case and surrounding whitespace are tolerated (typed by hand)
	tok2, _ := MintToken(key, time.Hour, now)
	if _, err := v.RedeemToken("  "+lower(tok2)+"\n", now); err != nil {
		t.Fatalf("lower-case token: %v", err)
	}
	// expiry window
	tok3, _ := MintToken(key, time.Hour, now)
	if _, err := v.RedeemToken(tok3, now.Add(TokenWindow+time.Second)); err != ErrExpired {
		t.Fatalf("expired: %v", err)
	}
	// wrong key, tampered token, garbage
	other := NewVerifier(append([]byte{1}, key[1:]...), now.Add(-time.Hour))
	tok4, _ := MintToken(key, time.Hour, now)
	if _, err := other.RedeemToken(tok4, now); err != ErrBadToken {
		t.Fatalf("wrong key: %v", err)
	}
	tampered := []byte(tok4)
	tampered[3] ^= 1
	if _, err := v.RedeemToken(string(tampered), now); err != ErrBadToken {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := v.RedeemToken("not a token", now); err != ErrBadToken {
		t.Fatalf("garbage: %v", err)
	}
	// ttl clamping
	tok5, _ := MintToken(key, 5*MaxTTL, now)
	if ttl, _ := v.RedeemToken(tok5, now); ttl != MaxTTL {
		t.Fatalf("ttl clamp: %v", ttl)
	}
	// a token minted before the server booted is refused (restart replay window)
	tok6, _ := MintToken(key, time.Hour, now)
	if _, err := NewVerifier(key, now.Add(time.Second)).RedeemToken(tok6, now.Add(2*time.Second)); err != ErrExpired {
		t.Fatalf("pre-boot token: %v", err)
	}
	// a token is never accepted as a session and vice versa (domain separation)
	if err := v.VerifySession(tok4, now); err != ErrBadSession {
		t.Fatalf("token accepted as session: %v", err)
	}
}

func TestFileVerifierFollowsRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.key")
	key, err := LoadOrCreateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	v := NewFileVerifier(p, now.Add(-time.Minute))
	val, _, _ := MintSession(key, time.Hour, now)
	if err := v.VerifySession(val, now); err != nil {
		t.Fatalf("session with the file's key: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // a rotated file has a newer mtime
	if _, err := RotateKey(p); err != nil {
		t.Fatal(err)
	}
	if err := v.VerifySession(val, now); err != ErrBadSession {
		t.Fatalf("session must die on rotation: %v", err)
	}
	os.Remove(p)
	if err := v.VerifySession(val, now); err != ErrBadSession {
		t.Fatalf("missing key file must deny: %v", err)
	}
	if _, err := v.RedeemToken("AAAA", now); err != ErrBadToken {
		t.Fatalf("missing key file must deny tokens: %v", err)
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestSessionRoundTrip(t *testing.T) {
	key := make([]byte, KeyBytes)
	key[0] = 7
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	val, exp, err := MintSession(key, 30*24*time.Hour, now)
	if err != nil || exp != now.Add(30*24*time.Hour) {
		t.Fatalf("mint: %v exp=%v", err, exp)
	}
	v := NewVerifier(key, now)
	if err := v.VerifySession(val, now.Add(29*24*time.Hour)); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}
	if err := v.VerifySession(val, exp); err != ErrBadSession {
		t.Fatalf("expired session accepted: %v", err)
	}
	if err := NewVerifier(append([]byte{9}, key[1:]...), now).VerifySession(val, now); err != ErrBadSession {
		t.Fatalf("rotated key must invalidate sessions: %v", err)
	}
	if err := v.VerifySession(val[:len(val)-2]+"zz", now); err != ErrBadSession {
		t.Fatalf("tampered session accepted")
	}
	if err := v.VerifySession("", now); err != ErrBadSession {
		t.Fatalf("empty session accepted")
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "1.5d": 36 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseTTL(in); err != nil || got != want {
			t.Errorf("%q -> %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-1h", "soon", "3w"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
