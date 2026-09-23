package state

import (
	"fmt"
	"time"
)

// Migration turns the state from one schema into the next: a function over the files of the
// state directory, every write through atomicfile. It never rewrites rules.json (the user's own
// file) and never touches cache/ (versioned and recomputed on its own).
type Migration struct {
	Name string
	Run  func(dir string) error
}

// migrations[i] turns schema i+1 into schema i+2. Append only: a published migration never
// changes. A new one ships with a fixture of the previous schema's files and a test (§12 of
// docs/ARCHITECTURE.md says when a change needs one).
var migrations []Migration

// Schema is the state schema this binary reads and writes: 1 plus one per migration.
func Schema() int { return 1 + len(migrations) }

// migrate runs the migrations from schema `from` up to Schema(), under the exclusive lock the
// caller holds, after copying the state to a backup; any failure restores the backup.
func migrate(dir string, from int, now time.Time) (*Report, error) {
	backup, err := takeBackup(dir, from, now)
	if err != nil {
		return nil, fmt.Errorf("state backup before migrating from schema %d: %w", from, err)
	}
	for s := from; s < Schema(); s++ {
		m := migrations[s-1]
		if err := m.Run(dir); err != nil {
			if rerr := restore(dir, backup); rerr != nil {
				return nil, fmt.Errorf("migration %d→%d (%s): %v; restoring the backup %s also failed: %v", s, s+1, m.Name, err, backup, rerr)
			}
			return nil, fmt.Errorf("migration %d→%d (%s): %w (the state is back as it was)", s, s+1, m.Name, err)
		}
	}
	if err := write(dir, File{Schema: Schema(), MigratedFrom: &Migrated{Schema: from, Backup: backup}}); err != nil {
		_ = restore(dir, backup)
		return nil, err
	}
	pruneBackups(dir, backup)
	return &Report{From: from, To: Schema(), Backup: backup}, nil
}
