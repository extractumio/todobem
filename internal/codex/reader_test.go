package codex

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestTailReaderPropagatesReadErrors(t *testing.T) {
	want := errors.New("synthetic read failure")
	tr := &tailReader{r: bufio.NewReader(failingReader{want})}
	if _, _, _, err := tr.next(); !errors.Is(err, want) {
		t.Fatalf("read error=%v; want original error", err)
	}
}

func TestTailReaderOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	long := strings.Repeat("x", 3<<20) // longer than the 1 MB buffer
	content := "a\n" + long + "\nb\npartial"
	os.WriteFile(path, []byte(content), 0644)
	tr, _ := openTail(path, 0)
	var lines []string
	var offs []int64
	for {
		l, st, _, err := tr.next()
		if err != nil {
			break
		}
		lines = append(lines, string(l))
		offs = append(offs, st)
	}
	if len(lines) != 3 || lines[0] != "a" || len(lines[1]) != len(long) || lines[2] != "b" {
		t.Fatalf("lines: %d", len(lines))
	}
	if offs[1] != 2 || offs[2] != int64(2+len(long)+1) || tr.off != int64(2+len(long)+1+2) {
		t.Fatalf("offsets %v final %d", offs, tr.off)
	}
	// resume from the recorded offset after the partial line is completed
	tr.Close()
	os.WriteFile(path, []byte(content+" done\n"), 0644)
	tr2, _ := openTail(path, tr.off)
	l, _, _, err := tr2.next()
	if err != nil || string(l) != "partial done" {
		t.Fatalf("resume: %q %v", l, err)
	}
}

func TestTailReaderSkipsLongCompacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	big := `{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":5,"type":"compacted","payload":{"x":"` + strings.Repeat("y", 4<<20) + `"}}`
	content := "a\n" + big + "\nb\n"
	os.WriteFile(path, []byte(content), 0644)
	tr, _ := openTail(path, 0)
	l1r, _, sk1, _ := tr.next()
	l1 := string(l1r) // slices point into the reader buffer: copy before the next read
	l2r, st2, sk2, _ := tr.next()
	l2 := append([]byte(nil), l2r...)
	l3, st3, sk3, _ := tr.next()
	if l1 != "a" || sk1 || !sk2 || sk3 || string(l3) != "b" {
		t.Fatalf("skip flags: %v %v %v (%q)", sk1, sk2, sk3, l3)
	}
	if lineType(l2) != "compacted" || prefixTS(l2) == 0 {
		t.Fatalf("head of skipped line unusable: %q", l2[:60])
	}
	if st2 != 2 || st3 != int64(2+len(big)+1) || tr.off != st3+2 {
		t.Fatalf("offsets %d %d %d", st2, st3, tr.off)
	}
}

func TestLineType(t *testing.T) {
	if lineType([]byte(`{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":0,"type":"session_meta","payload":{}}`)) != "session_meta" {
		t.Fatal("type")
	}
	if payloadType([]byte(`{"timestamp":"x","type":"event_msg","payload":{"type":"task_started","turn_id":"1"}}`)) != "task_started" {
		t.Fatal("ptype")
	}
	if prefixTS([]byte(`{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":0}`)) != 1789038946609 {
		t.Fatal("ts")
	}
}
