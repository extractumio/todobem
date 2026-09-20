package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Sidecar files hold small derived records next to a session's model cache, one per kind
// ("<id>.<kind>.json.gz", e.g. the Insights facts): {version, fp, data}. They share the model
// cache's fingerprint so any change to the source files or the classifier invalidates them, and
// they carry their own version so a change in how the record is derived invalidates only them.
// The store knows nothing about the record's type: callers pass the value to encode or decode.
type sidecarFile struct {
	Version int             `json:"version"`
	FP      Fingerprint     `json:"fp"`
	Data    json.RawMessage `json:"data"`
}

func (s *Store) sidecarPath(kind, id string) string {
	return filepath.Join(s.dir, safeName(id)+"."+kind+".json.gz")
}

// LoadSidecar decodes the record of kind for id into v. ok is false on a miss, a decode error or
// a version mismatch; the caller compares the returned fingerprint against the current inputs.
func (s *Store) LoadSidecar(kind, id string, version int, v any) (Fingerprint, bool) {
	if !s.enabled() {
		return Fingerprint{}, false
	}
	b, err := os.ReadFile(s.sidecarPath(kind, id))
	if err != nil {
		return Fingerprint{}, false
	}
	var sf sidecarFile
	if GunzipJSON(b, &sf) != nil || sf.Version != version || len(sf.Data) == 0 {
		return Fingerprint{}, false
	}
	if json.Unmarshal(sf.Data, v) != nil {
		return Fingerprint{}, false
	}
	return sf.FP, true
}

// SaveSidecar writes the record atomically (temp file + rename). A disabled store is a no-op.
func (s *Store) SaveSidecar(kind, id string, version int, fp Fingerprint, v any) error {
	if !s.enabled() {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b, err := GzipJSON(sidecarFile{Version: version, FP: fp, Data: data})
	if err != nil {
		return err
	}
	return s.write(s.sidecarPath(kind, id), b)
}
