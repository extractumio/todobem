// Package atomicfile writes a file so that a reader sees either the old content or the new one,
// never a partial write: the bytes land in a temporary file in the same directory, which is then
// renamed over the path. Every durable file todobem writes (settings, cache entries, the agents
// file, fleet snapshots) goes through it.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data, mode applied to the new file (subject to the umask on
// creation; an explicit chmod follows so a 0600 request is honoured). The directory must exist.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
