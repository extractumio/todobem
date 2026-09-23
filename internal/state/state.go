// Package state versions todobem's durable files under ~/.todobem and migrates them from one
// format to the next: state.json names the schema the files are in, the binary knows the schema
// it reads (Schema), and the gate that runs before any state is read brings the two together —
// migrating forward under a backup, refusing state newer than the binary. docs/ARCHITECTURE.md §7.5.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/extractumio/todobem/internal/atomicfile"
)

// Names inside the state directory.
const (
	FileName   = "state.json"
	LockName   = "state.lock"
	BackupsDir = "backups"
)

// ErrLocked is a lock another process holds.
var ErrLocked = errors.New("locked by another process")

// ErrBusy is the gate meeting a running todobem: a migration needs the state to itself.
var ErrBusy = errors.New("another todobem is running: stop it first (a state migration needs the files to itself)")

// NewerError is state written by a newer todobem than this binary reads.
type NewerError struct{ Have, Know int }

func (e *NewerError) Error() string {
	return fmt.Sprintf("the state in ~/.todobem is schema %d, this todobem understands %d: run `todobem upgrade rollback`, or upgrade", e.Have, e.Know)
}

// File is state.json.
type File struct {
	Schema       int       `json:"schema"`
	MigratedFrom *Migrated `json:"migrated_from,omitempty"`
}

// Migrated records the last migration: the schema it started from and the backup it took,
// relative to the state directory. A rollback to a binary of that schema restores the backup.
type Migrated struct {
	Schema int    `json:"schema"`
	Backup string `json:"backup"`
}

// Report says what the gate did, for the startup line; nil when nothing happened.
type Report struct {
	From, To int
	Backup   string
}

// Read loads state.json from dir; ok is false when it does not exist.
func Read(dir string) (f File, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, err
	}
	if err := json.Unmarshal(b, &f); err != nil || f.Schema < 1 {
		return File{}, false, fmt.Errorf("%s: not a todobem state file", filepath.Join(dir, FileName))
	}
	return f, true, nil
}

func write(dir string, f File) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, FileName), append(b, '\n'), 0600)
}

// Prepare is the gate, run before anything reads the state in dir: state of this binary's
// schema passes; older state is migrated (backup first, restored on any failure); newer state
// is refused with a NewerError; absent state is stamped with this schema — a fresh
// installation, or files from a build before the first release, which are used as they are.
func Prepare(dir string) (*Report, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, ok, err := Read(dir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, write(dir, File{Schema: Schema()})
	}
	switch {
	case f.Schema == Schema():
		return nil, nil
	case f.Schema > Schema():
		return nil, &NewerError{Have: f.Schema, Know: Schema()}
	}
	lock, err := Lock(filepath.Join(dir, LockName), true)
	if errors.Is(err, ErrLocked) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	f, _, err = Read(dir) // another process may have migrated between the first read and the lock
	if err != nil || f.Schema == Schema() {
		return nil, err
	}
	return migrate(dir, f.Schema, time.Now())
}

// Hold runs the gate and then keeps a shared lock on the state for as long as the returned
// closer is open: a running viewer or agent holds it, so a migration or a rollback started
// meanwhile finds the files in use and stops instead of changing them underneath.
func Hold(dir string) (io.Closer, *Report, error) {
	rep, err := Prepare(dir)
	if err != nil || dir == "" {
		return io.NopCloser(nil), rep, err
	}
	lock, err := Lock(filepath.Join(dir, LockName), false)
	if errors.Is(err, ErrLocked) {
		return nil, rep, errors.New("the state is locked by a migration or a rollback in progress: try again when it has finished")
	}
	if err != nil {
		return nil, rep, err
	}
	// A rollback may have restored an older schema between the gate and the lock.
	if f, _, err := Read(dir); err != nil || f.Schema != Schema() {
		lock.Close()
		if err == nil {
			err = fmt.Errorf("the state changed to schema %d while starting: start again", f.Schema)
		}
		return nil, rep, err
	}
	return lock, rep, nil
}
