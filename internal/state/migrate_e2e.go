//go:build e2emigration

package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/extractumio/todobem/internal/atomicfile"
)

// The end-to-end release tests build a `next` binary with this tag: one more schema, whose
// migration renames the field "old" to "new" in e2e-marker.json (a file only the tests write).
// It proves the migration path of a real binary on real files before a real migration exists.
// Release builds never carry the tag.
func init() {
	migrations = append(migrations, Migration{Name: "e2e: e2e-marker.json old → new", Run: func(dir string) error {
		p := filepath.Join(dir, "e2e-marker.json")
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		m["new"] = m["old"]
		delete(m, "old")
		out, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return atomicfile.Write(p, out, 0600)
	}})
}
