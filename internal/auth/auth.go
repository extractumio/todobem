// Package auth is the UI's lite authentication: a key file only the local user can read is the
// root of trust; `todobem token` mints a short-lived one-time token from it; the server exchanges
// the token for a long-lived browser session, also signed with the key. Nothing is stored server
// side except the nonces of tokens already used, so sessions survive restarts and revoking
// everything is rotating the key. Stdlib only: crypto/hmac, crypto/rand, crypto/sha256.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	KeyBytes    = 32
	TokenWindow = 5 * time.Minute     // a token must be used within this time of being minted
	DefaultTTL  = 30 * 24 * time.Hour // browser session length when -ttl is not given
	MaxTTL      = 365 * 24 * time.Hour
	tokenMACLen = 16
)

var (
	ErrBadToken   = errors.New("invalid token")
	ErrExpired    = errors.New("token expired")
	ErrUsed       = errors.New("token already used")
	ErrBadSession = errors.New("invalid session")
)

// DefaultKeyPath is ~/.todobem/auth.key.
func DefaultKeyPath() string {
	if p := os.Getenv("TODOBEM_AUTH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".todobem", "auth.key")
}

// LoadOrCreateKey reads the key file, creating it (0600, parent 0700) when absent. A key file
// readable by others is refused: it would let another local user mint tokens.
func LoadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("auth key path is empty")
	}
	key, err := LoadKey(path)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	key, err = createKey(path)
	if errors.Is(err, os.ErrExist) {
		// the server and the CLI raced to create it: whoever won, read theirs
		return LoadKey(path)
	}
	return key, err
}

// LoadKey reads and validates an existing key file (os.ErrNotExist when absent).
func LoadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("%s: expected %d bytes, found %d (delete it to generate a new key)", path, KeyBytes, len(key))
	}
	return key, nil
}

// RotateKey replaces the key file with fresh bytes: every token and every browser session dies.
func RotateKey(path string) ([]byte, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return createKey(path)
}

func createKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

// ---- one-time login tokens ------------------------------------------------------------------

// Token layout (32 bytes, base32 without padding = 52 chars, typed or pasted by the user):
//
//	nonce[8] || issued[4] (unix seconds) || ttl[4] (session seconds) || HMAC-SHA256(key, first 16)[:16]
var tokenEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// MintToken creates a token valid for TokenWindow that, once exchanged, yields a browser session
// of length ttl (clamped to [1 minute, MaxTTL]).
func MintToken(key []byte, ttl time.Duration, now time.Time) (string, error) {
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	buf := make([]byte, 16, 32)
	if _, err := rand.Read(buf[:8]); err != nil {
		return "", err
	}
	binary.BigEndian.PutUint32(buf[8:12], uint32(now.Unix()))
	binary.BigEndian.PutUint32(buf[12:16], uint32(ttl/time.Second))
	buf = append(buf, mac(key, tagToken, buf[:16])[:tokenMACLen]...)
	return tokenEnc.EncodeToString(buf), nil
}

// Verifier checks tokens and sessions against a key and remembers used token nonces. The key
// comes from a KeySource so a rotated key file takes effect on the next request.
type Verifier struct {
	key  func() []byte
	boot time.Time
	mu   sync.Mutex
	used map[[8]byte]time.Time // nonce -> issued; entries older than TokenWindow are pruned
}

// NewVerifier verifies against a fixed key; boot is the server start (tokens minted before it
// are refused).
func NewVerifier(key []byte, boot time.Time) *Verifier {
	return &Verifier{key: func() []byte { return key }, boot: boot, used: map[[8]byte]time.Time{}}
}

// NewFileVerifier verifies against the key file at path, re-read whenever it changes, so
// `todobem token -revoke` (rotate) invalidates every session and token of a running server
// at once. A missing or unreadable file denies everything.
func NewFileVerifier(path string, boot time.Time) *Verifier {
	src := &KeySource{path: path}
	return &Verifier{key: src.Current, boot: boot, used: map[[8]byte]time.Time{}}
}

// KeySource caches a key file and reloads it when its size or mtime changes.
type KeySource struct {
	path string
	mu   sync.Mutex
	key  []byte
	size int64
	mod  time.Time
}

// Current returns the key as of now; nil when the file is missing, malformed or too open.
func (k *KeySource) Current() []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	st, err := os.Stat(k.path)
	if err != nil {
		k.key = nil
		return nil
	}
	if k.key != nil && st.Size() == k.size && st.ModTime().Equal(k.mod) {
		return k.key
	}
	key, err := LoadKey(k.path)
	if err != nil {
		k.key = nil
		return nil
	}
	k.key, k.size, k.mod = key, st.Size(), st.ModTime()
	return key
}

// RedeemToken validates a token (signature, age, single use) and returns the session TTL it
// grants. Every failure is the same class of error to the caller; the variants are for logs.
func (v *Verifier) RedeemToken(token string, now time.Time) (time.Duration, error) {
	raw, err := tokenEnc.DecodeString(strings.ToUpper(strings.TrimSpace(token)))
	if err != nil || len(raw) != 32 {
		return 0, ErrBadToken
	}
	if !hmac.Equal(raw[16:], mac(v.key(), tagToken, raw[:16])[:tokenMACLen]) {
		return 0, ErrBadToken
	}
	issued := time.Unix(int64(binary.BigEndian.Uint32(raw[8:12])), 0)
	if now.Sub(issued) > TokenWindow || issued.After(now.Add(time.Minute)) {
		return 0, ErrExpired
	}
	// A token minted before this server started cannot be in the used set any more; refusing
	// it closes the replay window a restart would otherwise open (tokens live 5 minutes).
	if issued.Before(v.boot) {
		return 0, ErrExpired
	}
	var nonce [8]byte
	copy(nonce[:], raw[:8])
	v.mu.Lock()
	defer v.mu.Unlock()
	for n, t := range v.used {
		if now.Sub(t) > TokenWindow {
			delete(v.used, n)
		}
	}
	if _, dup := v.used[nonce]; dup {
		return 0, ErrUsed
	}
	v.used[nonce] = issued
	return time.Duration(binary.BigEndian.Uint32(raw[12:16])) * time.Second, nil
}

// ---- browser sessions -----------------------------------------------------------------------

// Session layout (56 bytes, base64url without padding):
//
//	expiry[8] (unix seconds) || nonce[16] || HMAC-SHA256(key, first 24)
var sessionEnc = base64.RawURLEncoding

// MintSession creates a session value that expires at now+ttl.
func MintSession(key []byte, ttl time.Duration, now time.Time) (value string, expiry time.Time, err error) {
	expiry = now.Add(ttl)
	buf := make([]byte, 24, 56)
	binary.BigEndian.PutUint64(buf[:8], uint64(expiry.Unix()))
	if _, err := rand.Read(buf[8:24]); err != nil {
		return "", time.Time{}, err
	}
	buf = append(buf, mac(key, tagSession, buf[:24])...)
	return sessionEnc.EncodeToString(buf), expiry, nil
}

// MintSession signs a session with the verifier's current key (an error when there is none).
func (v *Verifier) MintSession(ttl time.Duration, now time.Time) (string, time.Time, error) {
	key := v.key()
	if key == nil {
		return "", time.Time{}, errors.New("no auth key")
	}
	return MintSession(key, ttl, now)
}

// VerifySession checks a session value's signature and expiry.
func (v *Verifier) VerifySession(value string, now time.Time) error {
	raw, err := sessionEnc.DecodeString(value)
	if err != nil || len(raw) != 56 {
		return ErrBadSession
	}
	if !hmac.Equal(raw[24:], mac(v.key(), tagSession, raw[:24])) {
		return ErrBadSession
	}
	if now.Unix() >= int64(binary.BigEndian.Uint64(raw[:8])) {
		return ErrBadSession
	}
	return nil
}

// mac domain-separates the two uses of the key with a type byte, so a token can never be
// presented as a session or vice versa.
func mac(key []byte, tag byte, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte{tag})
	h.Write(data)
	return h.Sum(nil)
}

const (
	tagToken   byte = 'T'
	tagSession byte = 'S'
)

// ParseTTL accepts Go durations plus a day suffix: "30d", "12h", "90m".
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(s, "%gd", &days); err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid ttl %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid ttl %q", s)
	}
	return d, nil
}
