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

	"github.com/extractumio/todobem/internal/settings"
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
	if d := settings.Dir(); d != "" {
		return filepath.Join(d, "auth.key")
	}
	return ""
}

// CheckPrivate refuses a file readable by other users: a key they could mint from, an agents
// file with bearers in it.
func CheckPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	return nil
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
	if err := CheckPrivate(path); err != nil {
		return nil, err
	}
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("%s: expected %d bytes, found %d (delete it to generate a new key)", path, KeyBytes, len(key))
	}
	return key, nil
}

// RotateKey replaces the key file with fresh bytes: every token and every session dies, a
// staged key (StageKey) with them.
func RotateKey(path string) ([]byte, error) {
	for _, p := range []string{path, NextPath(path)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return createKey(path)
}

// MintFromFile mints a one-time token under the key file at path, creating the key when absent
// and rotating it first when revoke is set: what `todobem token`, `todobem agent pair` and an
// agent's startup line do. Being able to read the file is what authorises minting.
func MintFromFile(path string, revoke bool, ttl time.Duration, now time.Time) (string, error) {
	var key []byte
	var err error
	if revoke {
		key, err = RotateKey(path)
	} else {
		key, err = LoadOrCreateKey(path)
	}
	if err != nil {
		return "", fmt.Errorf("auth key: %w", err)
	}
	return MintToken(key, ttl, now)
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

// Verifier checks tokens and sessions against a key and remembers used token nonces. The keys
// come from a KeySource so a rotated key file takes effect on the next request; a staged key
// (StageKey, the two-phase rotation of an agent) is accepted next to the current one until a
// session signed by it is first verified, which promotes it.
type Verifier struct {
	keys    func() [][]byte    // the current key first, a staged key second; nil = deny everything
	promote func([]byte) error // called with the staged key that verified a session (nil = never)
	boot    time.Time
	mu      sync.Mutex
	used    map[[8]byte]time.Time // nonce -> issued; entries older than TokenWindow are pruned
}

// NewVerifier verifies against a fixed key; boot is the server start (tokens minted before it
// are refused).
func NewVerifier(key []byte, boot time.Time) *Verifier {
	return &Verifier{keys: func() [][]byte { return [][]byte{key} }, boot: boot.Truncate(time.Second), used: map[[8]byte]time.Time{}}
}

// NewFileVerifier verifies against the key file at path, re-read whenever it changes, so
// `todobem token -revoke` (rotate) invalidates every session and token of a running server
// at once. A missing or unreadable file denies everything. A staged key file (`<path>.next`)
// is accepted too, and promoted to the key file by the first session verified under it.
func NewFileVerifier(path string, boot time.Time) *Verifier {
	src := &keySource{path: path}
	return &Verifier{keys: src.All, promote: func(staged []byte) error {
		if err := promoteKey(path, staged); err != nil {
			return err
		}
		src.invalidate()
		return nil
	}, boot: boot.Truncate(time.Second), used: map[[8]byte]time.Time{}}
}

// key is the current (first) key, for minting; nil when there is none.
func (v *Verifier) key() []byte {
	if keys := v.keys(); len(keys) > 0 {
		return keys[0]
	}
	return nil
}

// verifyAny reports which key (index and bytes) verifies the MAC of data, or -1 and nil.
func (v *Verifier) verifyAny(tag byte, data, sum []byte, n int) (int, []byte) {
	for i, key := range v.keys() {
		if key != nil && hmac.Equal(sum, mac(key, tag, data)[:n]) {
			return i, key
		}
	}
	return -1, nil
}

// keySource caches a key file and reloads it when its size or mtime changes. Next to it, a
// staged key file (`<path>.next`) is read the same way; All returns both while it exists.
type keySource struct {
	path string
	mu   sync.Mutex
	cur  cachedKey
	next cachedKey
}

type cachedKey struct {
	key  []byte
	size int64
	mod  time.Time
}

// NextPath is the staged key file of a key file.
func NextPath(path string) string { return path + ".next" }

// All returns the current key and, while a staged key file exists, the staged key after it.
func (k *keySource) All() [][]byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	keys := [][]byte{k.cur.load(k.path)}
	if next := k.next.load(NextPath(k.path)); next != nil {
		keys = append(keys, next)
	}
	return keys
}

func (k *keySource) invalidate() {
	k.mu.Lock()
	k.cur, k.next = cachedKey{}, cachedKey{}
	k.mu.Unlock()
}

// load returns the file's key, re-reading it when its size or mtime changed; nil when the file is
// missing, malformed or too open.
func (c *cachedKey) load(path string) []byte {
	st, err := os.Stat(path)
	if err != nil {
		c.key = nil
		return nil
	}
	if c.key != nil && st.Size() == c.size && st.ModTime().Equal(c.mod) {
		return c.key
	}
	key, err := LoadKey(path)
	if err != nil {
		c.key = nil
		return nil
	}
	c.key, c.size, c.mod = key, st.Size(), st.ModTime()
	return key
}

// StageKey returns the pending staged key at `<path>.next`, creating it (0600) when absent. A
// concurrent or repeated rotation reuses that key, so every bearer already returned remains
// usable. The current key stays valid until a session signed by the staged key is verified
// (Verifier promotes it) or Promote is called.
func StageKey(path string) ([]byte, error) {
	next := NextPath(path)
	key, err := LoadKey(next)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	key, err = createKey(next)
	if errors.Is(err, os.ErrExist) {
		return LoadKey(next)
	}
	return key, err
}

// Promote makes the staged key the key: `<path>.next` replaces `<path>` (rename, atomic). Every
// session signed by the old key is refused from the next request on.
func Promote(path string) error {
	return os.Rename(NextPath(path), path)
}

// promoteKey makes promotion idempotent for concurrent requests under the staged key. If
// another request won the rename, the current file must contain exactly the key we verified.
func promoteKey(path string, staged []byte) error {
	err := Promote(path)
	if err == nil {
		current, loadErr := LoadKey(path)
		if loadErr != nil {
			return loadErr
		}
		if !hmac.Equal(current, staged) {
			return errors.New("staged auth key changed before promotion")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, loadErr := LoadKey(path)
	if loadErr == nil && hmac.Equal(current, staged) {
		return nil
	}
	return err
}

// RedeemToken validates a token (signature, age, single use) and returns the session TTL it
// grants. Every failure is the same class of error to the caller; the variants are for logs.
func (v *Verifier) RedeemToken(token string, now time.Time) (time.Duration, error) {
	raw, err := tokenEnc.DecodeString(strings.ToUpper(strings.TrimSpace(token)))
	if err != nil || len(raw) != 32 {
		return 0, ErrBadToken
	}
	if i, _ := v.verifyAny(tagToken, raw[:16], raw[16:], tokenMACLen); i < 0 {
		return 0, ErrBadToken
	}
	issued := time.Unix(int64(binary.BigEndian.Uint32(raw[8:12])), 0)
	if now.Sub(issued) > TokenWindow || issued.After(now.Add(time.Minute)) {
		return 0, ErrExpired
	}
	// A token minted before this server's boot second cannot be in the used set any more;
	// refusing it closes the replay window a restart would otherwise open (tokens live 5
	// minutes). The wire timestamp has whole-second precision, so ordering inside that second is
	// unknowable and intentionally accepted.
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

// VerifySession checks a session value's signature and expiry. A session signed by the staged
// key promotes it: from this request on, the old key is gone.
func (v *Verifier) VerifySession(value string, now time.Time) error {
	raw, err := sessionEnc.DecodeString(value)
	if err != nil || len(raw) != 56 {
		return ErrBadSession
	}
	i, verifiedKey := v.verifyAny(tagSession, raw[:24], raw[24:], sha256.Size)
	if i < 0 {
		return ErrBadSession
	}
	if now.Unix() >= int64(binary.BigEndian.Uint64(raw[:8])) {
		return ErrBadSession
	}
	if i > 0 && v.promote != nil {
		if err := v.promote(verifiedKey); err != nil {
			return fmt.Errorf("promote staged auth key: %w", err)
		}
	}
	return nil
}

// SessionExpiry reads the expiry (ms) a session value carries without verifying it — for a
// report that names when the caller's own credential ends; 0 when the value is malformed.
func SessionExpiry(value string) int64 {
	raw, err := sessionEnc.DecodeString(value)
	if err != nil || len(raw) != 56 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(raw[:8])) * 1000
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
