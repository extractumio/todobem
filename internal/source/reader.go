package source

import (
	"bufio"
	"errors"
	"io"
	"os"
	"time"
)

// MaxLine is the largest single line a reader accepts. Codex `compacted` lines reach ~5 MB and
// Claude Code tool results ~3 MB; 64 MB leaves ample room.
const MaxLine = 64 << 20

// ErrRewritten is returned by a LaneParser whose file was rewritten from the start (an
// ordinal went backwards): the joiner restarts that lane from offset 0.
var ErrRewritten = errors.New("session file rewritten")

// TailReader reads complete lines from a file starting at a byte offset. It never returns a
// partial trailing line: the offset only advances past '\n'. Lines the source does not need
// (a multi-megabyte compaction record) are never accumulated: Skip decides from the first
// buffered chunk and the rest is discarded up to '\n'.
type TailReader struct {
	f       *os.File
	r       *bufio.Reader
	off     int64
	pending int64 // bytes of a discarded line consumed so far
	// Skip says whether a line whose head is this chunk should be discarded undecoded; nil
	// keeps every line.
	Skip func(head []byte) bool
}

// OpenTail opens path at off with the given skip predicate.
func OpenTail(path string, off int64, skip func(head []byte) bool) (*TailReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &TailReader{f: f, r: bufio.NewReaderSize(f, 1<<20), off: off, Skip: skip}, nil
}

// NewTailReader wraps an arbitrary reader (tests); Offset starts at 0.
func NewTailReader(r io.Reader) *TailReader { return &TailReader{r: bufio.NewReader(r)} }

func (t *TailReader) Close() error {
	if t.f == nil {
		return nil
	}
	return t.f.Close()
}

// Offset is the position after the last complete line returned.
func (t *TailReader) Offset() int64 { return t.off }

// Next returns the next complete line (without '\n') and its start offset. io.EOF means "no
// complete line available right now". Skipped lines return only their first chunk (for the
// timestamp) with skipped=true. The returned slice may point into the reader's buffer: copy
// it before the next call when it must outlive it.
func (t *TailReader) Next() (line []byte, start int64, skipped bool, err error) {
	start = t.off
	var buf []byte
	discard := false
	for {
		chunk, e := t.r.ReadSlice('\n')
		if e == nil {
			if discard {
				t.off = start + t.pending + int64(len(chunk))
				return buf, start, true, nil
			}
			if buf == nil {
				line = chunk[:len(chunk)-1]
			} else {
				buf = append(buf, chunk[:len(chunk)-1]...)
				line = buf
			}
			t.off = start + int64(len(line)) + 1 // line bytes + '\n'
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			return line, start, false, nil
		}
		if errors.Is(e, bufio.ErrBufferFull) {
			if discard {
				t.pending += int64(len(chunk))
				continue
			}
			if buf == nil && t.Skip != nil && t.Skip(chunk) {
				// keep a copy of the head only (for the timestamp), drop the rest
				buf = append([]byte(nil), chunk[:min(len(chunk), 160)]...)
				discard = true
				t.pending = int64(len(chunk))
				continue
			}
			buf = append(buf, chunk...)
			if len(buf) > MaxLine {
				// pathological line: drop it entirely but keep going
				discard = true
				t.pending = int64(len(buf))
				buf = buf[:160]
			}
			continue
		}
		// Leave a partial line unconsumed; propagate failures rather than treating them as EOF.
		t.pending = 0
		return nil, start, false, e
	}
}

// ParseTS parses an RFC 3339 timestamp into Unix milliseconds (0 when absent or malformed).
func ParseTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
