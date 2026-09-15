package claude

import (
	"os"
	"testing"

	"github.com/extractumio/todobem/internal/source"
)

// TestIndexReadsPendingQuestionFromTail: an AskUserQuestion call nothing has answered marks the
// session as waiting for the user, with the call's time; its tool_result, a later model message
// or a typed prompt clears it. A plan submitted with ExitPlanMode waits the same way. All of it
// is read from the file's tail, without a parse.
func TestIndexReadsPendingQuestionFromTail(t *testing.T) {
	question := map[string]any{"questions": []any{map[string]any{"question": "Which docs?"}}}
	asked := promptLine(1_000, "u1", "Update the docs", map[string]any{"promptSource": "typed"}) +
		assistantLine(20_000, "a1", "m1", "end_turn", text("Two candidates."), usage{in: 10, out: 5}) +
		assistantLine(24_000, "a2", "m2", "tool_use", toolUse("t1", "AskUserQuestion", question), usage{in: 10, out: 25})
	h, path := home(t, asked)
	ix := NewIndex(h)
	ix.Scan()
	fm, ok := ix.Get(fxSession)
	if !ok || fm.Question != 24_000 || fm.LastAnswer != "Two candidates." {
		t.Fatalf("after the question: question=%d last=%q (ok=%v)", fm.Question, fm.LastAnswer, ok)
	}
	if sums := source.Summaries(ix, nil); len(sums) != 1 || sums[0].Question != 24_000 {
		t.Fatalf("summaries: %+v", sums)
	}
	// the answer arrives and the turn completes: the grown file is re-read, nothing is pending
	answered := asked + resultLine(54_000, "r1", "t1", "User answered: README", false, "User answered: README") +
		assistantLine(60_000, "a3", "m3", "end_turn", text("Done."), usage{in: 10, out: 15})
	if err := os.WriteFile(path, []byte(answered), 0600); err != nil {
		t.Fatal(err)
	}
	ix.Scan()
	if fm, _ := ix.Get(fxSession); fm.Question != 0 || fm.LastAnswer != "Done." {
		t.Fatalf("after the answer: question=%d last=%q", fm.Question, fm.LastAnswer)
	}
	// a typed prompt supersedes an unanswered question; a harness injection does not
	for _, tc := range []struct {
		name string
		line string
		want int64
	}{
		{"typed prompt", promptLine(70_000, "u2", "Never mind, ship it", map[string]any{"promptSource": "typed"}), 0},
		{"harness injection", promptLine(70_000, "u2", "<system-reminder>context</system-reminder>", map[string]any{"isMeta": true}), 24_000},
		{"plan awaiting approval", assistantLine(70_000, "a9", "m9", "tool_use", toolUse("t9", "ExitPlanMode", map[string]any{"plan": "1. do it"}), usage{in: 10, out: 25}), 70_000},
		{"any other pending tool call", assistantLine(70_000, "a9", "m9", "tool_use", toolUse("t9", "Bash", map[string]any{"command": "go test ./..."}), usage{in: 10, out: 25}), 0},
	} {
		h, _ := home(t, asked+tc.line)
		ix := NewIndex(h)
		ix.Scan()
		if fm, _ := ix.Get(fxSession); fm.Question != tc.want {
			t.Fatalf("%s: question=%d, want %d", tc.name, fm.Question, tc.want)
		}
	}
}
