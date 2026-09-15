package codex

import (
	"github.com/extractumio/todobem/internal/source"
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
	sums := source.Summaries(ix, nil)
	if len(sums) != 1 || sums[0].LastAnswer != "" {
		t.Fatalf("summaries: %+v", sums)
	}
}

// TestIndexReadsPendingQuestionFromTail: a request_user_input call nothing has answered marks the
// session as waiting for the user, with the call's time; its output or a later user message
// clears it. All of it is read from the file's tail, without a parse.
func TestIndexReadsPendingQuestionFromTail(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	line := func(ts, kind, payload string) string {
		return `{"timestamp":"` + ts + `","type":"` + kind + `","payload":` + payload + "}\n"
	}
	askedAt := "2026-01-01T00:10:00.000Z"
	meta := line("2026-01-01T00:00:00.000Z", "session_meta", `{"id":"thread-q","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/synthetic"}`)
	done := line("2026-01-01T00:05:00.000Z", "event_msg", `{"type":"task_complete","turn_id":"t1","last_agent_message":"Two candidates."}`)
	ask := line(askedAt, "response_item", `{"type":"function_call","name":"request_user_input","arguments":"{\"questions\":[{\"question\":\"Which one?\"}]}","call_id":"call-q"}`)
	path := filepath.Join(dir, "rollout-2026-01-01T00-00-00-thread-q.jsonl")
	if err := os.WriteFile(path, []byte(meta+done+ask), 0600); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(root)
	ix.Scan()
	fm, ok := ix.Get("thread-q")
	if !ok || fm.Question != source.ParseTS(askedAt) || fm.LastAnswer != "Two candidates." {
		t.Fatalf("after the question: question=%d last=%q (ok=%v)", fm.Question, fm.LastAnswer, ok)
	}
	if sums := source.Summaries(ix, nil); len(sums) != 1 || sums[0].Question != source.ParseTS(askedAt) {
		t.Fatalf("summaries: %+v", sums)
	}
	// the answer arrives: the grown file is re-read, nothing is pending
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(line("2026-01-01T00:12:00.000Z", "response_item", `{"type":"function_call_output","call_id":"call-q","output":"{\"answers\":{}}"}`))
	f.Close()
	ix.Scan()
	if fm, _ := ix.Get("thread-q"); fm.Question != 0 {
		t.Fatalf("after the answer: question=%d", fm.Question)
	}
	// a user message after the question supersedes it, whatever its form
	for i, user := range []string{
		line("2026-01-01T00:12:00.000Z", "event_msg", `{"type":"user_message","message":"never mind"}`),
		line("2026-01-01T00:12:00.000Z", "response_item", `{"type":"message","role":"user","content":[{"type":"input_text","text":"never mind"}]}`),
	} {
		p := filepath.Join(dir, "rollout-2026-01-01T00-00-0"+string(rune('1'+i))+"-thread-u"+string(rune('1'+i))+".jsonl")
		id := "thread-u" + string(rune('1'+i))
		if err := os.WriteFile(p, []byte(line("2026-01-01T00:00:00.000Z", "session_meta", `{"id":"`+id+`","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/synthetic"}`)+ask+user), 0600); err != nil {
			t.Fatal(err)
		}
		ix.Scan()
		if fm, _ := ix.Get(id); fm.Question != 0 {
			t.Fatalf("user message form %d: question=%d", i, fm.Question)
		}
	}
	// the tools list in a turn_context names the tool without calling it: no question
	ctx := line("2026-01-01T00:10:00.000Z", "turn_context", `{"turn_id":"t2","tools":["request_user_input"]}`)
	p := filepath.Join(dir, "rollout-2026-01-01T00-00-09-thread-c.jsonl")
	if err := os.WriteFile(p, []byte(line("2026-01-01T00:00:00.000Z", "session_meta", `{"id":"thread-c","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/synthetic"}`)+ctx), 0600); err != nil {
		t.Fatal(err)
	}
	ix.Scan()
	if fm, _ := ix.Get("thread-c"); fm.Question != 0 {
		t.Fatalf("turn_context mention: question=%d", fm.Question)
	}
}
