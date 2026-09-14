// Package store persists derived session models to disk so an unchanged session is served
// without re-parsing its rollout files. It knows only about model.Session and the files it was
// built from — never about parsers or codex internals. The cache is derived data: safe to delete,
// and keyed so that ANY change to the source files or the classifier invalidates it (serving a
// stale classification would be worse than a re-parse — see CLAUDE.md product rules 3-4).
package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/extractumio/todobem/internal/model"
)

// cacheVersion is bumped when the on-disk shape changes or the parser's output for the same
// input changes (a marker rule, a new injected prefix); an older file is treated as a miss.
const cacheVersion = 3

// FileFP fingerprints one source file. Append-only rollouts change size on every write and the
// file set changes when a sub-agent appears, so (path,size,mtime) detects every real change.
type FileFP struct {
	Path string `json:"p"`
	Size int64  `json:"s"`
	Mod  int64  `json:"m"` // mod time, unix nanoseconds
}

// Fingerprint is the cache key: the source files plus a hash of the effective classifier. A rule
// change flips Rules even when no file changed, so the cache never serves an outdated phase.
type Fingerprint struct {
	Rules string   `json:"rules"`
	Files []FileFP `json:"files"` // sorted by path by the caller
}

// Equal reports whether two fingerprints describe the same inputs.
func (f Fingerprint) Equal(o Fingerprint) bool {
	if f.Rules != o.Rules || len(f.Files) != len(o.Files) {
		return false
	}
	for i := range f.Files {
		if f.Files[i] != o.Files[i] {
			return false
		}
	}
	return true
}

// Store writes one gzipped JSON file per session under dir. An empty dir disables it (every
// method is a no-op / miss), so the caller can turn caching off without special-casing.
type Store struct{ dir string }

// New returns a store rooted at dir, creating it. An empty dir returns a disabled store.
func New(dir string) *Store {
	if dir == "" {
		return &Store{}
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

func (s *Store) enabled() bool { return s != nil && s.dir != "" }

// Dir is the cache directory ("" when disabled).
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// cacheFile wraps the model with its fingerprint. Operation.Detail is json:"-" (kept out of the
// API payload) so it is carried in Details and reattached on load, keeping /api/op working from
// the cache without re-reading the source line.
type cacheFile struct {
	Version int               `json:"version"`
	FP      Fingerprint       `json:"fp"`
	Model   *model.Session    `json:"model"`
	Details map[string]string `json:"details,omitempty"`
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, safeName(id)+".json.gz")
}

// Load returns the cached model and the fingerprint it was built from. ok is false on any miss,
// decode error, or version mismatch — the caller then re-parses. It does NOT check freshness;
// the caller compares the returned fingerprint against the current inputs.
func (s *Store) Load(id string) (m *model.Session, fp Fingerprint, ok bool) {
	if !s.enabled() {
		return nil, Fingerprint{}, false
	}
	b, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, Fingerprint{}, false
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, Fingerprint{}, false
	}
	defer gz.Close()
	var cf cacheFile
	if json.NewDecoder(gz).Decode(&cf) != nil || cf.Version != cacheVersion || cf.Model == nil {
		return nil, Fingerprint{}, false
	}
	for _, l := range cf.Model.Lanes {
		for _, o := range l.Ops {
			if d, ok := cf.Details[o.ID]; ok {
				o.Detail = d
			}
		}
	}
	return cf.Model, cf.FP, true
}

// Save writes the model atomically (temp file + rename). A disabled store or nil model is a no-op.
func (s *Store) Save(id string, m *model.Session, fp Fingerprint) error {
	if !s.enabled() || m == nil {
		return nil
	}
	details := map[string]string{}
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.Detail != "" {
				details[o.ID] = o.Detail
			}
		}
	}
	cf := cacheFile{Version: cacheVersion, FP: fp, Model: m, Details: details}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(&cf); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	tmp := s.path(id) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(id))
}

// safeName keeps a plain thread id as the filename and hashes anything with unusual characters,
// so a crafted id can never escape the cache directory.
func safeName(id string) string {
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			sum := sha1.Sum([]byte(id))
			return hex.EncodeToString(sum[:])
		}
	}
	if id == "" {
		return "_empty"
	}
	return id
}
