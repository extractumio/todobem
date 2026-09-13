//go:build darwin || linux

package codex

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIndexSkipsNonregularRollouts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(dir, "rollout-pipe.jsonl")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "synthetic.jsonl")
	if err := os.WriteFile(external, []byte(`{"type":"session_meta","payload":{"id":"outside-thread"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "rollout-link.jsonl")); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(root)
	done := make(chan struct{})
	go func() { ix.Scan(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		// Unblock a regressed reader so this test does not leave a goroutine behind.
		f, err := os.OpenFile(pipe, os.O_RDWR|syscall.O_NONBLOCK, 0600)
		if err == nil {
			_, _ = f.WriteString("{}\n")
			_ = f.Close()
		}
		<-done
		t.Fatal("index blocked opening a FIFO rollout")
	}
	if len(ix.files) != 0 {
		t.Fatalf("indexed nonregular rollouts: %+v", ix.files)
	}
}
