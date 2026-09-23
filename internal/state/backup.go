package state

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepBackups is how many migration backups stay; older ones go when a new one is taken.
const keepBackups = 3

// outside lists what a backup leaves out and a restore never touches: derived data (the cache),
// the installed binaries, the backups themselves, the locks, and rules.json — the user's own
// file, which no migration rewrites and no rollback may take back. Everything else in the state
// directory is state.
var outside = map[string]bool{"cache": true, "bin": true, BackupsDir: true, LockName: true, "update.lock": true, "rules.json": true}

// inScope reports whether a top-level entry of the state directory belongs to a backup.
// Dot-files are atomicfile's temporaries.
func inScope(name string) bool { return !outside[name] && !strings.HasPrefix(name, ".") }

// takeBackup copies the state to backups/state-<schema>-<UTC time>/ (0700, each file's mode
// kept: the auth code refuses a key another user can read) and returns its path relative to dir.
func takeBackup(dir string, schema int, now time.Time) (string, error) {
	root := filepath.Join(dir, BackupsDir)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return "", err
	}
	rel := filepath.Join(BackupsDir, fmt.Sprintf("state-%d-%s", schema, now.UTC().Format("20060102T150405.000Z")))
	dst := filepath.Join(dir, rel)
	if err := os.Mkdir(dst, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !inScope(e.Name()) {
			continue
		}
		if err := copyTree(filepath.Join(dir, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			os.RemoveAll(dst)
			return "", err
		}
	}
	return rel, nil
}

// restore puts the backup at rel (relative to dir) back: every in-scope entry that is not in
// the backup goes, every entry of the backup is copied back with its mode.
func restore(dir, rel string) error {
	src := filepath.Join(dir, rel)
	saved, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	current, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range current {
		if inScope(e.Name()) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	for _, e := range saved {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RestoreFor brings the state back to schema `want` for a rollback to a binary that reads it:
// nothing to do when the state already is that schema; the backup of the last migration when it
// started from `want`; an error otherwise. It takes the state exclusively, so every todobem
// using this directory must be stopped.
func RestoreFor(dir string, want int) (restored string, err error) {
	f, ok, err := Read(dir)
	if err != nil || !ok || f.Schema == want {
		return "", err
	}
	if f.Schema < want {
		return "", fmt.Errorf("the state is schema %d, older than the %d the previous version reads", f.Schema, want)
	}
	if f.MigratedFrom == nil || f.MigratedFrom.Schema != want {
		return "", fmt.Errorf("the state is schema %d and no backup of schema %d is recorded: the previous version cannot read it", f.Schema, want)
	}
	lock, err := Lock(filepath.Join(dir, LockName), true)
	if errors.Is(err, ErrLocked) {
		return "", errors.New("todobem is running: stop every viewer and agent of this user first")
	}
	if err != nil {
		return "", err
	}
	defer lock.Close()
	return f.MigratedFrom.Backup, restore(dir, f.MigratedFrom.Backup)
}

// pruneBackups keeps the newest keepBackups backups and always the one just taken.
func pruneBackups(dir, keep string) {
	root := filepath.Join(dir, BackupsDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "state-") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return backupTime(names[i]) > backupTime(names[j]) })
	for i, n := range names {
		if i >= keepBackups && filepath.Join(BackupsDir, n) != keep {
			os.RemoveAll(filepath.Join(root, n))
		}
	}
}

// backupTime is the sortable time part of a backup's name (state-<schema>-<time>).
func backupTime(name string) string { return name[strings.LastIndex(name, "-")+1:] }

// copyTree copies a file or a directory tree, keeping modes; symlinks are copied as links.
func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.Mkdir(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())
	case info.Mode().IsRegular():
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(dst, info.Mode().Perm())
	}
	return nil // sockets, devices: not state
}
