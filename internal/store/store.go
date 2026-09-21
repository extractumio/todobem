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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/extractumio/todobem/internal/atomicfile"
	"github.com/extractumio/todobem/internal/model"
)

// cacheVersion is bumped when the on-disk shape changes or the parser's output for the same
// input changes (a marker rule, a new injected prefix); an older file is treated as a miss.
// 4: per-call token accounting (Lane.Tokens), Operation.QueryMiss and Totals.QueryMisses.
// 6: QueryMiss on a failed query kind without an exit code; SessionSummary/Session.Source.
// 7: a sub-agent's open turn after the root closed is orphaned (its lane is not live).
// 8: one op per Claude Code stop hook, with the command's classification and retry identity.
// 9: a Claude Code assistant line with the model "<synthetic>" never names the turn's model.
// 10: a Codex task_complete carrying an error is an llm_error marker.
// 11: Codex exec envelopes recover their commands from array literals (else `exec-script`), the
// web `run` tool and `mcp__*` connectors are mapped, the exec cell wait extends its op; Claude
// Code's Artifact and ReportFindings tools are mapped.
const cacheVersion = 11

// maxDecodedJSON bounds every derived cache object after decompression. The wire already caps
// compressed model responses; this second bound prevents a small gzip bomb from exhausting the
// hub while decoding an agent response or a damaged local cache file.
const maxDecodedJSON = int64(256 << 20)

// FileFP fingerprints one source file. Append-only rollouts change size on every write and the
// file set changes when a sub-agent appears, so (path,size,mtime) detects every real change.
// A remote session's fingerprint (RowFingerprint) reuses the shape for the row an agent
// reported: Path is then the agent's id, Mod the row's update time in milliseconds.
type FileFP struct {
	Path string `json:"p"`
	Size int64  `json:"s"`
	Mod  int64  `json:"m"` // mod time, unix nanoseconds (milliseconds for a RowFingerprint)
}

// RowFingerprint is the cache key of a session a paired agent reported (docs/AGENT-MODE.md
// §6.6): the agent's rules and the row's size and update time play the part a local file's
// path, size and mtime play. Nothing ever stats its Path.
func RowFingerprint(agentRules, id string, bytes, updated int64) Fingerprint {
	return Fingerprint{Rules: "agent:" + agentRules, Files: []FileFP{{Path: id, Size: bytes, Mod: updated}}}
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

// CacheVersion is the on-disk (and on-the-wire) shape version: two todobem builds can exchange
// a model only when theirs agree, the same rule the local cache applies to itself.
func CacheVersion() int { return cacheVersion }

// GzipJSON encodes v as gzipped JSON: the shape of every file under the cache directory and of
// the fleet's snapshots.
func GzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(v); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GunzipJSON is the inverse of GzipJSON.
func GunzipJSON(b []byte, v any) error {
	return gunzipJSON(b, v, maxDecodedJSON)
}

func gunzipJSON(b []byte, v any, limit int64) error {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer gz.Close()
	lr := &io.LimitedReader{R: gz, N: limit + 1}
	dec := json.NewDecoder(lr)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("more than one JSON value")
		}
		return err
	}
	if lr.N <= 0 {
		return fmt.Errorf("decompressed JSON exceeds %d bytes", limit)
	}
	return nil
}

// Encode serializes a model with its fingerprint into the cache file shape (gzipped JSON). It is
// the one encoder of that shape: Save writes its output to disk and an agent sends it to a hub
// (docs/AGENT-MODE.md §6.4), so a fetched file is byte-for-byte a cache entry.
func Encode(m *model.Session, fp Fingerprint) ([]byte, error) {
	if m == nil {
		return nil, errors.New("no model")
	}
	details := map[string]string{}
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.Detail != "" {
				details[o.ID] = o.Detail
			}
		}
	}
	return GzipJSON(cacheFile{Version: cacheVersion, FP: fp, Model: m, Details: details})
}

// Decode is the inverse of Encode. ok is false on a decode error or a version mismatch; the
// op details are reattached to the model.
func Decode(b []byte) (m *model.Session, fp Fingerprint, ok bool) {
	var cf cacheFile
	if GunzipJSON(b, &cf) != nil || cf.Version != cacheVersion || cf.Model == nil {
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

// ReadRaw returns a session's cache file as stored (an agent serves it as is once Load has
// proved it current); ok is false when there is none.
func (s *Store) ReadRaw(id string) ([]byte, bool) {
	if !s.enabled() {
		return nil, false
	}
	b, err := os.ReadFile(s.path(id))
	return b, err == nil
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
	return Decode(b)
}

// Save writes the model atomically (temp file + rename). A disabled store or nil model is a no-op.
func (s *Store) Save(id string, m *model.Session, fp Fingerprint) error {
	if !s.enabled() || m == nil {
		return nil
	}
	b, err := Encode(m, fp)
	if err != nil {
		return err
	}
	return s.write(s.path(id), b)
}

// Remove deletes the entries and every sidecar of the given sessions (a removed agent's rows)
// with one directory listing. Missing entries are not an error.
func (s *Store) Remove(ids []string) error {
	if !s.enabled() || len(ids) == 0 {
		return nil
	}
	owners := make(map[string]bool, len(ids))
	for _, id := range ids {
		owners[safeName(id)] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		owner, _, _ := strings.Cut(e.Name(), ".")
		if e.IsDir() || !owners[owner] {
			continue
		}
		if rmErr := os.Remove(filepath.Join(s.dir, e.Name())); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = rmErr
		}
	}
	return err
}

// write lands b at path atomically.
func (s *Store) write(path string, b []byte) error { return atomicfile.Write(path, b, 0o644) }

// Size reports the cache entries on disk and their total bytes (0, 0 when disabled).
func (s *Store) Size() (files int, bytes int64) {
	if !s.enabled() {
		return 0, 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json.gz") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files++
			bytes += info.Size()
		}
	}
	return files, bytes
}

// Prune deletes the entries whose session id is not in keep (a session that the current
// source tree no longer lists: another codex home, a deleted rollout) and any leftover
// temporary file. Entries are named by id, so no file is opened. The cache is derived data:
// a wrongly removed entry costs one re-parse, nothing else.
func (s *Store) Prune(keep []string) (removed int, freed int64, err error) {
	if !s.enabled() {
		return 0, 0, nil
	}
	known := map[string]bool{}
	for _, id := range keep {
		known[safeName(id)] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// "<id>.json.gz" and its sidecars "<id>.<kind>.json.gz" (safeName never contains a dot)
		owner, _, _ := strings.Cut(name, ".")
		stale := strings.HasSuffix(name, ".json.gz") && !known[owner] || strings.HasSuffix(name, ".tmp")
		if !stale {
			continue
		}
		info, statErr := e.Info()
		if rmErr := os.Remove(filepath.Join(s.dir, name)); rmErr != nil {
			err = rmErr
			continue
		}
		removed++
		if statErr == nil {
			freed += info.Size()
		}
	}
	return removed, freed, err
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
