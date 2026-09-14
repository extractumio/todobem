package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexReadsLastAnswerFromTail: the session list describes a session by the final message of
// its last completed turn, read from the file's tail without parsing the file.
func TestIndexReadsLastAnswerFromTail(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-01-01T00-00-00-thread-a.jsonl")
	line := func(kind, payload string) string {
		return `{"timestamp":"2026-01-01T00:00:00.000Z","type":"` + kind + `","payload":` + payload + "}\n"
	}
	meta := line("session_meta", `{"id":"thread-a","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/synthetic"}`)
	first := line("event_msg", `{"type":"task_complete","turn_id":"t1","last_agent_message":"First answer.\n\nDetails follow."}`)
	if err := os.WriteFile(path, []byte(meta+first), 0600); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(root)
	ix.Scan()
	fm, ok := ix.Get("thread-a")
	if !ok || fm.LastAnswer != "First answer.\n\nDetails follow." {
		t.Fatalf("last answer after first scan: %q (ok=%v)", fm.LastAnswer, ok)
	}
	// the file grows by a turn: a size-only change still refreshes the answer
	second := line("event_msg", `{"type":"task_complete","turn_id":"t2","last_agent_message":"  Second answer, in place.  "}`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(line("event_msg", `{"type":"task_started","turn_id":"t2"}`) + second)
	f.Close()
	ix.Scan()
	if fm, _ := ix.Get("thread-a"); fm.LastAnswer != "Second answer, in place." {
		t.Fatalf("last answer after growth: %q", fm.LastAnswer)
	}
	// a huge trailing line (compaction) pushes the answer beyond the tail window: no answer
	// rather than a stale one is not required, but the read must stay bounded and not decode it
	big := line("compacted", `{"message":"`+strings.Repeat("x", lastAnswerTail+1024)+`"}`)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(big)
	f.Close()
	ix.Scan()
	if fm, _ := ix.Get("thread-a"); fm.LastAnswer != "" {
		t.Fatalf("answer beyond the tail window must be empty, got %q", fm.LastAnswer)
	}
	// sub-agent files carry no answer
	child := filepath.Join(dir, "rollout-2026-01-01T00-00-01-thread-b.jsonl")
	os.WriteFile(child, []byte(line("session_meta", `{"id":"thread-b","parent_thread_id":"thread-a","agent_path":"/root/b"}`)+first), 0600)
	ix.Scan()
	if fm, _ := ix.Get("thread-b"); fm.LastAnswer != "" {
		t.Fatalf("sub-agent answer must not be read: %q", fm.LastAnswer)
	}
	sums := Summaries(ix, nil)
	if len(sums) != 1 || sums[0].LastAnswer != "" {
		t.Fatalf("summaries: %+v", sums)
	}
}
