package source

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
	tr := &TailReader{r: bufio.NewReader(failingReader{want})}
	if _, _, _, err := tr.Next(); !errors.Is(err, want) {
		t.Fatalf("read error=%v; want original error", err)
	}
}

func TestTailReaderOffsets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	long := strings.Repeat("x", 3<<20) // longer than the 1 MB buffer
	content := "a\n" + long + "\nb\npartial"
	os.WriteFile(path, []byte(content), 0644)
	tr, _ := OpenTail(path, 0, nil)
	var lines []string
	var offs []int64
	for {
		l, st, _, err := tr.Next()
		if err != nil {
			break
		}
		lines = append(lines, string(l))
		offs = append(offs, st)
	}
	if len(lines) != 3 || lines[0] != "a" || len(lines[1]) != len(long) || lines[2] != "b" {
		t.Fatalf("lines: %d", len(lines))
	}
	if offs[1] != 2 || offs[2] != int64(2+len(long)+1) || tr.Offset() != int64(2+len(long)+1+2) {
		t.Fatalf("offsets %v final %d", offs, tr.Offset())
	}
	// resume from the recorded offset after the partial line is completed
	tr.Close()
	os.WriteFile(path, []byte(content+" done\n"), 0644)
	tr2, _ := OpenTail(path, tr.Offset(), nil)
	l, _, _, err := tr2.Next()
	if err != nil || string(l) != "partial done" {
		t.Fatalf("resume: %q %v", l, err)
	}
}

// TestTailReaderSkipsLongLines: a line the predicate rejects on its first chunk is never
// accumulated; only its head comes back, and the offsets still advance past the whole line.
func TestTailReaderSkipsLongLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	big := `{"timestamp":"2026-09-10T11:15:46.609Z","ordinal":5,"type":"compacted","payload":{"x":"` + strings.Repeat("y", 4<<20) + `"}}`
	content := "a\n" + big + "\nb\n"
	os.WriteFile(path, []byte(content), 0644)
	skip := func(head []byte) bool {
		return strings.Contains(string(head[:min(len(head), 160)]), `"type":"compacted"`)
	}
	tr, _ := OpenTail(path, 0, skip)
	l1r, _, sk1, _ := tr.Next()
	l1 := string(l1r) // slices point into the reader buffer: copy before the next read
	l2r, st2, sk2, _ := tr.Next()
	l2 := append([]byte(nil), l2r...)
	l3, st3, sk3, _ := tr.Next()
	if l1 != "a" || sk1 || !sk2 || sk3 || string(l3) != "b" {
		t.Fatalf("skip flags: %v %v %v (%q)", sk1, sk2, sk3, l3)
	}
	if len(l2) > 160 || !strings.HasPrefix(string(l2), `{"timestamp":"2026-09-10T11:15:46.609Z"`) {
		t.Fatalf("head of skipped line unusable: %q", l2)
	}
	if st2 != 2 || st3 != int64(2+len(big)+1) || tr.Offset() != st3+2 {
		t.Fatalf("offsets %d %d %d", st2, st3, tr.Offset())
	}
}

func TestParseTS(t *testing.T) {
	if ParseTS("2026-09-10T11:15:46.609Z") != 1789038946609 || ParseTS("") != 0 || ParseTS("nope") != 0 {
		t.Fatal("ParseTS")
	}
}
