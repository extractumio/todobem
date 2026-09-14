// Package codex reads OpenAI Codex CLI rollout files (~/.codex/sessions/**/rollout-*.jsonl)
// and turns them into the normalized model (internal/model).
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Line types that carry nothing the timeline needs. They are dropped by looking at the
// line prefix only, so multi-megabyte `compacted` lines are never JSON-decoded.
var skipTypes = map[string]bool{
	"compacted":                          true,
	"world_state":                        true,
	"inter_agent_communication_metadata": true,
}

// rawLine is the envelope of every rollout line.
type rawLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   int64           `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// lineType extracts the top-level "type" value from the line prefix without decoding.
// Rollout lines always start with {"timestamp":"…","ordinal":N,"type":"…" (ordinal may be absent
// in older versions), so scanning the first ~120 bytes is enough.
func lineType(line []byte) string {
	head := line
	if len(head) > 160 {
		head = head[:160]
	}
	i := bytes.Index(head, []byte(`"type":"`))
	if i < 0 {
		return ""
	}
	rest := head[i+8:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// eventMsgType extracts payload.type for event_msg / response_item lines (prefix scan).
func payloadType(line []byte) string {
	head := line
	if len(head) > 240 {
		head = head[:240]
	}
	i := bytes.Index(head, []byte(`"payload":{"type":"`))
	if i < 0 {
		return ""
	}
	rest := line[i+19:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// prefixField finds a short string field ("call_id", "ordinal"…) in the first n bytes.
func prefixField(line []byte, key string, n int) string {
	head := line
	if len(head) > n {
		head = head[:n]
	}
	k := []byte(`"` + key + `":`)
	i := bytes.Index(head, k)
	if i < 0 {
		return ""
	}
	rest := head[i+len(k):]
	if len(rest) > 0 && rest[0] == '"' {
		rest = rest[1:]
		j := bytes.IndexByte(rest, '"')
		if j < 0 {
			return ""
		}
		return string(rest[:j])
	}
	j := 0
	for j < len(rest) && (rest[j] >= '0' && rest[j] <= '9' || rest[j] == '-') {
		j++
	}
	return string(rest[:j])
}

// maxLine is the largest single line we accept. Codex `compacted` lines reach ~5 MB;
// 64 MB leaves ample room.
const maxLine = 64 << 20

// tailReader reads complete lines from a file starting at a byte offset. It never
// returns a partial trailing line: the caller's offset only advances past '\n'.
type tailReader struct {
	f       *os.File
	r       *bufio.Reader
	off     int64
	pending int64 // bytes of a discarded line consumed so far
}

func openTail(path string, off int64) (*tailReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &tailReader{f: f, r: bufio.NewReaderSize(f, 1<<20), off: off}, nil
}

func (t *tailReader) Close() error { return t.f.Close() }

// next returns the next complete line (without '\n') and its start offset.
// io.EOF means "no complete line available right now". Lines whose type is in skipTypes
// are never accumulated: the first chunk decides, the rest is discarded up to '\n'.
// The returned skipped flag is true for such lines (line then holds only the first chunk).
func (t *tailReader) next() (line []byte, start int64, skipped bool, err error) {
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
			if buf == nil && skipTypes[lineType(chunk)] {
				// keep a copy of the head only (for the timestamp), drop the rest
				buf = append([]byte(nil), chunk[:min(len(chunk), 160)]...)
				discard = true
				t.pending = int64(len(chunk))
				continue
			}
			buf = append(buf, chunk...)
			if len(buf) > maxLine {
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

func parseTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
