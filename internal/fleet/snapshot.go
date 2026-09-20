package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/extractumio/todobem/internal/atomicfile"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/store"
)

// Snapshot is what the hub keeps per agent between runs (~/.todobem/fleet/<name>.json.gz): the
// last hello, the cursor and the rows as the agent sent them (plain ids; the host is added when
// served), the facts held, and the last contact. Derived data: deleting it costs one full list.
type Snapshot struct {
	Hello        Hello                  `json:"hello"`                  // zero until the agent was ever reached
	Incompatible bool                   `json:"incompatible,omitempty"` // the agent does not speak protocol v1
	Cursor       string                 `json:"cursor"`
	Rows         []model.SessionSummary `json:"rows"`
	LastOK       int64                  `json:"last_ok,omitempty"`  // ms, the last successful poll
	LastErr      string                 `json:"last_err,omitempty"` // the last failure's text
	Since        int64                  `json:"since,omitempty"`    // ms, unreachable since (0 = reachable)
	Attempts     int                    `json:"attempts,omitempty"` // consecutive failed polls
	Facts        map[string]int64       `json:"facts,omitempty"`    // uuid → the row's Updated the held facts were fetched under
}

func snapshotPath(dir, name string) string { return filepath.Join(dir, name+".json.gz") }

// LoadSnapshot reads an agent's snapshot; a missing file is an empty snapshot.
func LoadSnapshot(dir, name string) (Snapshot, error) {
	var s Snapshot
	if dir == "" {
		return s, nil
	}
	b, err := os.ReadFile(snapshotPath(dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := store.GunzipJSON(b, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// SaveSnapshot writes it atomically (dir created 0700, file 0600: the rows carry titles and last
// answers of other machines' sessions).
func SaveSnapshot(dir, name string, s Snapshot) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := store.GzipJSON(&s)
	if err != nil {
		return err
	}
	return atomicfile.Write(snapshotPath(dir, name), b, 0o600)
}

// RemoveSnapshot deletes an agent's snapshot (a missing file is fine).
func RemoveSnapshot(dir, name string) error {
	if dir == "" {
		return nil
	}
	err := os.Remove(snapshotPath(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// SnapshotIDs lists the hub ids every snapshot under dir holds (`todobem cache -prune` keeps
// them). Only the ids are decoded.
func SnapshotIDs(dir string) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json.gz")
		if e.IsDir() || !ok || name == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var slim struct {
			Rows []struct {
				ID string `json:"id"`
			} `json:"rows"`
		}
		if store.GunzipJSON(b, &slim) != nil {
			continue
		}
		for _, r := range slim.Rows {
			ids = append(ids, Join(r.ID, name))
		}
	}
	return ids
}
