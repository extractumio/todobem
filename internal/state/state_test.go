package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withMigrations swaps the migration list for one test.
func withMigrations(t *testing.T, ms ...Migration) {
	t.Helper()
	saved := migrations
	migrations = ms
	t.Cleanup(func() { migrations = saved })
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setSchema(t *testing.T, dir string, n int) {
	t.Helper()
	if err := write(dir, File{Schema: n}); err != nil {
		t.Fatal(err)
	}
}

// rename is a migration that renames file a to b.
func rename(a, b string) Migration {
	return Migration{Name: a + "→" + b, Run: func(dir string) error { return os.Rename(filepath.Join(dir, a), filepath.Join(dir, b)) }}
}

func TestFreshDirectoryIsStampedWithTheCurrentSchema(t *testing.T) {
	withMigrations(t, rename("a", "b"))
	dir := filepath.Join(t.TempDir(), ".todobem")
	rep, err := Prepare(dir)
	if err != nil || rep != nil {
		t.Fatalf("Prepare = %v, %v", rep, err)
	}
	f, ok, err := Read(dir)
	if err != nil || !ok || f.Schema != 2 || f.MigratedFrom != nil {
		t.Fatalf("state = %+v ok=%v err=%v", f, ok, err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0700 {
		t.Fatalf("state dir mode %v", info.Mode().Perm())
	}
}

func TestCurrentSchemaPassesUntouched(t *testing.T) {
	dir := t.TempDir()
	setSchema(t, dir, Schema())
	writeFile(t, filepath.Join(dir, "settings.json"), "{}", 0600)
	if rep, err := Prepare(dir); err != nil || rep != nil {
		t.Fatalf("Prepare = %v, %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, BackupsDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a backup was taken without a migration")
	}
}

func TestMigrationRunsInOrderWithABackup(t *testing.T) {
	withMigrations(t, rename("a", "b"), rename("b", "c"))
	dir := t.TempDir()
	setSchema(t, dir, 1)
	writeFile(t, filepath.Join(dir, "a"), "payload", 0600)
	writeFile(t, filepath.Join(dir, "auth.key"), "secret", 0600)
	writeFile(t, filepath.Join(dir, "fleet", "web.json.gz"), "snap", 0600)
	writeFile(t, filepath.Join(dir, "cache", "x.json.gz"), "derived", 0600)
	rep, err := Prepare(dir)
	if err != nil || rep == nil || rep.From != 1 || rep.To != 3 {
		t.Fatalf("Prepare = %+v, %v", rep, err)
	}
	if readFile(t, filepath.Join(dir, "c")) != "payload" {
		t.Fatal("migrations did not run in order")
	}
	f, _, _ := Read(dir)
	if f.Schema != 3 || f.MigratedFrom == nil || f.MigratedFrom.Schema != 1 || f.MigratedFrom.Backup != rep.Backup {
		t.Fatalf("state after migration = %+v", f)
	}
	b := filepath.Join(dir, rep.Backup)
	if readFile(t, filepath.Join(b, "a")) != "payload" || readFile(t, filepath.Join(b, "fleet", "web.json.gz")) != "snap" {
		t.Fatal("backup misses state files")
	}
	if info, _ := os.Stat(filepath.Join(b, "auth.key")); info.Mode().Perm() != 0600 {
		t.Fatalf("backup changed a key's mode to %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(b, "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the cache went into the backup")
	}
	if info, _ := os.Stat(filepath.Join(dir, BackupsDir)); info.Mode().Perm() != 0700 {
		t.Fatalf("backups dir mode %v", info.Mode().Perm())
	}
}

func TestFailingMigrationRestoresTheBackup(t *testing.T) {
	boom := Migration{Name: "boom", Run: func(dir string) error {
		os.WriteFile(filepath.Join(dir, "half-written"), []byte("x"), 0600)
		return errors.New("boom")
	}}
	withMigrations(t, rename("a", "b"), boom)
	dir := t.TempDir()
	setSchema(t, dir, 1)
	writeFile(t, filepath.Join(dir, "a"), "payload", 0600)
	_, err := Prepare(dir)
	if err == nil || !strings.Contains(err.Error(), "back as it was") {
		t.Fatalf("Prepare error = %v", err)
	}
	if readFile(t, filepath.Join(dir, "a")) != "payload" {
		t.Fatal("the renamed file was not restored")
	}
	for _, gone := range []string{"b", "half-written"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the restore", gone)
		}
	}
	if f, _, _ := Read(dir); f.Schema != 1 {
		t.Fatalf("schema after a failed migration = %d", f.Schema)
	}
}

func TestNewerStateIsRefused(t *testing.T) {
	dir := t.TempDir()
	setSchema(t, dir, Schema()+1)
	var newer *NewerError
	if _, err := Prepare(dir); !errors.As(err, &newer) || newer.Have != Schema()+1 {
		t.Fatalf("Prepare = %v", err)
	}
}

func TestMigrationWaitsForNoRunningTodobem(t *testing.T) {
	withMigrations(t, rename("a", "b"))
	dir := t.TempDir()
	setSchema(t, dir, 1)
	writeFile(t, filepath.Join(dir, "a"), "payload", 0600)
	running, err := Lock(filepath.Join(dir, LockName), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("Prepare with a running todobem = %v", err)
	}
	running.Close()
	if _, err := Prepare(dir); err != nil {
		t.Fatalf("Prepare after it stopped = %v", err)
	}
}

func TestHoldKeepsASharedLock(t *testing.T) {
	dir := t.TempDir()
	h1, _, err := Hold(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := Hold(dir) // a viewer and an agent of the same user
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(filepath.Join(dir, LockName), true); !errors.Is(err, ErrLocked) {
		t.Fatalf("exclusive lock while held = %v", err)
	}
	h1.Close()
	h2.Close()
	l, err := Lock(filepath.Join(dir, LockName), true)
	if err != nil {
		t.Fatalf("exclusive lock after release = %v", err)
	}
	l.Close()
}

func TestRestoreForRollsBackTheLastMigration(t *testing.T) {
	withMigrations(t, rename("a", "b"))
	dir := t.TempDir()
	setSchema(t, dir, 1)
	writeFile(t, filepath.Join(dir, "a"), "payload", 0600)
	before := readFile(t, filepath.Join(dir, FileName))
	if _, err := Prepare(dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "made-later"), "lost on rollback", 0600)
	if _, err := RestoreFor(dir, 2); err != nil {
		t.Fatalf("RestoreFor(current schema) = %v", err)
	}
	restored, err := RestoreFor(dir, 1)
	if err != nil || restored == "" {
		t.Fatalf("RestoreFor(1) = %q, %v", restored, err)
	}
	if readFile(t, filepath.Join(dir, "a")) != "payload" || readFile(t, filepath.Join(dir, FileName)) != before {
		t.Fatal("state not restored byte for byte")
	}
	if _, err := os.Stat(filepath.Join(dir, "made-later")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file created after the migration survived the restore")
	}
	if _, err := os.Stat(filepath.Join(dir, restored)); err != nil {
		t.Fatal("the backup itself was removed by its restore")
	}
}

// rules.json is the user's own: a migration's backup does not take it and a rollback never
// takes back what the user wrote after the upgrade.
func TestRestoreLeavesTheUsersRules(t *testing.T) {
	withMigrations(t, rename("a", "b"))
	dir := t.TempDir()
	setSchema(t, dir, 1)
	writeFile(t, filepath.Join(dir, "a"), "payload", 0600)
	writeFile(t, filepath.Join(dir, "rules.json"), `{"v":1}`, 0600)
	rep, err := Prepare(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, rep.Backup, "rules.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rules.json went into the backup")
	}
	writeFile(t, filepath.Join(dir, "rules.json"), `{"v":2}`, 0600)
	if _, err := RestoreFor(dir, 1); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "rules.json")) != `{"v":2}` {
		t.Fatal("the rollback took back the user's rules")
	}
}

func TestHoldRefusesNewerState(t *testing.T) {
	dir := t.TempDir()
	setSchema(t, dir, Schema()+1)
	if _, _, err := Hold(dir); err == nil {
		t.Fatal("held state newer than the binary")
	}
}

func TestRestoreForRefusesWithoutAMatchingBackup(t *testing.T) {
	dir := t.TempDir()
	setSchema(t, dir, 3)
	if _, err := RestoreFor(dir, 1); err == nil {
		t.Fatal("restored schema 1 without a backup")
	}
}

func TestOnlyTheNewestBackupsStay(t *testing.T) {
	withMigrations(t, rename("a", "b"))
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 5; i++ {
		setSchema(t, dir, 1)
		writeFile(t, filepath.Join(dir, "a"), "x", 0600)
		os.Remove(filepath.Join(dir, "b"))
		rep, err := migrate(dir, 1, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		last = rep.Backup
	}
	entries, _ := os.ReadDir(filepath.Join(dir, BackupsDir))
	if len(entries) != keepBackups {
		t.Fatalf("%d backups kept, want %d", len(entries), keepBackups)
	}
	if _, err := os.Stat(filepath.Join(dir, last)); err != nil {
		t.Fatal("the newest backup was pruned")
	}
}
