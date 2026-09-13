package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// Keep the rollout's envelope and payload type fields first, as the writer does.
func rolloutLine(t *testing.T, ts int64, kind, payloadKind string, fields map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	rest := string(b[1 : len(b)-1])
	if rest != "" {
		rest = "," + rest
	}
	return []byte(fmt.Sprintf(`{"timestamp":%q,"type":%q,"payload":{"type":%q%s}}`, time.UnixMilli(ts).UTC().Format(time.RFC3339Nano), kind, payloadKind, rest))
}

func testParser(t *testing.T, id, parent string) *laneParser {
	t.Helper()
	return newLaneParser(FileMeta{ThreadID: id, ParentID: parent, Started: 1000, Path: filepath.Join(t.TempDir(), "rollout-test.jsonl")})
}

func appendRollout(t *testing.T, p *laneParser, lines ...[]byte) {
	t.Helper()
	f, err := os.OpenFile(p.meta.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if _, err := f.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.consume(0); err != nil {
		t.Fatal(err)
	}
}

func textContent(text string) []map[string]string {
	return []map[string]string{{"type": "text", "text": text}}
}

func TestShortTurnsPreserveUserInputAndFinals(t *testing.T) {
	for _, parent := range []string{"", "parent-thread"} {
		t.Run("parent="+parent, func(t *testing.T) {
			p := testParser(t, "thread-id", parent)
			appendRollout(t, p,
				rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"}),
				rolloutLine(t, 1100, "response_item", "message", map[string]any{"role": "user", "content": textContent("hello")}),
				rolloutLine(t, 1500, "event_msg", "task_complete", map[string]any{"turn_id": "turn", "last_agent_message": "hello back"}),
			)
			if len(p.lane.Turns) != 1 || p.lane.Turns[0].Final != "hello back" {
				t.Fatalf("lost genuine short turn: %+v", p.lane.Turns)
			}
		})
	}
	t.Run("child final without user item", func(t *testing.T) {
		p := testParser(t, "child", "parent")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"}),
			rolloutLine(t, 1500, "event_msg", "task_complete", map[string]any{"turn_id": "turn", "last_agent_message": "recorded result"}),
		)
		if len(p.lane.Turns) != 1 {
			t.Fatal("child final answer was mistaken for empty replay")
		}
	})
	t.Run("empty child replay", func(t *testing.T) {
		p := testParser(t, "child", "parent")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "replayed"}),
			rolloutLine(t, 1000, "event_msg", "task_complete", map[string]any{"turn_id": "replayed"}),
		)
		if len(p.lane.Turns) != 0 {
			t.Fatal("empty instantaneous child replay retained")
		}
	})
	t.Run("superseded root and child with user input", func(t *testing.T) {
		for _, parent := range []string{"", "parent"} {
			p := testParser(t, "thread", parent)
			appendRollout(t, p,
				rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
				rolloutLine(t, 1100, "response_item", "message", map[string]any{"role": "user", "content": textContent("real input")}),
				rolloutLine(t, 1500, "event_msg", "task_started", map[string]any{"turn_id": "second"}),
			)
			if len(p.lane.Turns) != 2 || p.lane.Turns[0].Status != "orphaned" {
				t.Fatalf("parent %q lost superseded genuine turn", parent)
			}
		}
	})
}

func TestShortChildTurnsPreserveMarkerOnlyToolActivity(t *testing.T) {
	for _, tc := range []struct {
		tool, marker, args string
	}{
		{"update_plan", "plan", `{"plan":[{"step":"inspect input","status":"completed"}]}`},
		{"request_user_input", "question", `{"questions":[{"title":"choose","question":"continue?"}]}`},
	} {
		for _, boundary := range []string{"task_complete", "task_started"} {
			t.Run(tc.tool+"/"+boundary, func(t *testing.T) {
				p := testParser(t, "child-thread", "parent-thread")
				endTurn, wantStatus := "real-turn", "completed"
				if boundary == "task_started" {
					endTurn, wantStatus = "next-turn", "orphaned"
				}
				appendRollout(t, p,
					rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "real-turn"}),
					rolloutLine(t, 1100, "response_item", "function_call", map[string]any{"name": tc.tool, "call_id": "real-call", "arguments": tc.args}),
					rolloutLine(t, 1200, "response_item", "function_call_output", map[string]any{"call_id": "real-call", "output": "recorded"}),
					rolloutLine(t, 1500, "event_msg", boundary, map[string]any{"turn_id": endTurn}),
				)
				if len(p.lane.Turns) == 0 || p.lane.Turns[0].ID != "real-turn" || p.lane.Turns[0].Status != wantStatus {
					t.Fatalf("lost genuine child activity: %+v", p.lane.Turns)
				}
				for _, marker := range p.lane.Markers {
					if marker.Kind == tc.marker && marker.Turn == "real-turn" {
						return
					}
				}
				t.Fatalf("%s marker lost its recorded turn", tc.marker)
			})
		}
	}
}

func TestConversationPreservesFullTextAndDeduplicatesFinalSources(t *testing.T) {
	user := strings.Repeat("question 界 ", 600)
	final := strings.Repeat("answer 界 ", 700)
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint("modern=", modern), func(t *testing.T) {
			p := testParser(t, "thread-id", "")
			lines := [][]byte{
				rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"}),
				rolloutLine(t, 1100, "response_item", "message", map[string]any{"role": "user", "content": textContent(user)}),
			}
			if modern {
				lines = append(lines, rolloutLine(t, 1110, "event_msg", "item_completed", map[string]any{"turn_id": "turn", "item": map[string]any{"type": "UserMessage", "content": textContent(user)}}))
			}
			lines = append(lines, rolloutLine(t, 2000, "response_item", "message", map[string]any{"role": "assistant", "phase": "final", "content": textContent(final)}))
			if modern {
				lines = append(lines, rolloutLine(t, 2100, "event_msg", "item_completed", map[string]any{"turn_id": "turn", "item": map[string]any{"id": "answer", "type": "AgentMessage", "phase": "final_answer", "content": textContent(final)}}))
			}
			lines = append(lines, rolloutLine(t, 2500, "event_msg", "task_complete", map[string]any{"turn_id": "turn", "last_agent_message": final}))
			appendRollout(t, p, lines...)
			users, finals := 0, 0
			for _, marker := range p.lane.Markers {
				switch marker.Kind {
				case "user_message":
					users++
					if marker.Text != user {
						t.Fatal("user text was changed or clipped")
					}
				case "final_answer":
					finals++
					if marker.Text != final || modern && marker.Fallback {
						t.Fatal("final text clipped or authoritative source not retained")
					}
				}
			}
			if users != 1 || finals != 1 || p.lane.Turns[0].Final != final {
				t.Fatalf("users=%d finals=%d turn final bytes=%d", users, finals, len(p.lane.Turns[0].Final))
			}
		})
	}
}

func TestFinalAnswerAvailableFromCompletionOrExplicitAssistantPhase(t *testing.T) {
	for _, source := range []string{"completion", "assistant"} {
		t.Run(source, func(t *testing.T) {
			p := testParser(t, "thread-id", "")
			lines := [][]byte{rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"})}
			if source == "completion" {
				lines = append(lines, rolloutLine(t, 3000, "event_msg", "task_complete", map[string]any{"turn_id": "turn", "last_agent_message": "recorded answer"}))
			} else {
				lines = append(lines, rolloutLine(t, 2000, "response_item", "message", map[string]any{"role": "assistant", "channel": "final", "content": textContent("recorded answer")}))
			}
			appendRollout(t, p, lines...)
			for _, marker := range p.lane.Markers {
				if marker.Kind == "final_answer" && marker.Text == "recorded answer" {
					return
				}
			}
			t.Fatal("recorded final answer has no conversation marker")
		})
	}
}

func TestOrphanBoundaryPreservesLastEvidenceAndPendingOperation(t *testing.T) {
	for _, boundary := range []string{"task_started", "session_meta"} {
		t.Run(boundary, func(t *testing.T) {
			p := testParser(t, "thread-id", "")
			lines := [][]byte{
				rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
				rolloutLine(t, 2000, "response_item", "function_call", map[string]any{"name": "wait_agent", "call_id": "wait", "arguments": "{}"}),
				rolloutLine(t, 2500, "event_msg", "token_count", map[string]any{"info": nil}),
			}
			if boundary == "task_started" {
				lines = append(lines, rolloutLine(t, 60000, "event_msg", boundary, map[string]any{"turn_id": "second"}))
			} else {
				lines = append(lines, rolloutLine(t, 60000, boundary, "", map[string]any{"id": "thread-id"}))
			}
			appendRollout(t, p, lines...)
			if p.lane.Turns[0].End != 2500 || p.lane.Ops[0].End != 2500 || p.lane.Ops[0].Open {
				t.Fatalf("turn end=%d op=%+v", p.lane.Turns[0].End, p.lane.Ops[0])
			}
			s := &model.Session{Lanes: []*model.Lane{p.lane}}
			model.Derive(s, 60000)
			if got := p.lane.ByPhase[classify.NoTelemetry]; got != 57500 {
				t.Fatalf("no telemetry=%d; want 57500", got)
			}
		})
	}
}

func TestRecordedEndpointsReplaceLiveDisplayBounds(t *testing.T) {
	p := testParser(t, "thread-id", "")
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"}),
		rolloutLine(t, 2000, "response_item", "function_call", map[string]any{"name": "wait_agent", "call_id": "wait", "arguments": "{}"}),
	)
	// Simulate live derivation while the tool's completion event is still buffered.
	p.lane.Ended, p.lane.Ops[0].End = 5000, 5000
	appendRollout(t, p,
		rolloutLine(t, 4000, "response_item", "function_call_output", map[string]any{"call_id": "wait", "output": "completed"}),
		rolloutLine(t, 4500, "event_msg", "task_complete", map[string]any{"turn_id": "turn"}),
	)
	if p.lane.Ended != 4500 || p.lane.Ops[0].End != 4000 {
		t.Fatalf("recorded lane end=%d op end=%d", p.lane.Ended, p.lane.Ops[0].End)
	}
	p.lane.Ended = 6000
	s := &Session{ix: NewIndex(t.TempDir()), rootID: "thread-id", parsers: map[string]*laneParser{"thread-id": p}, order: []string{"thread-id"}, Model: &model.Session{}}
	s.rebuild(7000, 0)
	if s.Model.Ended != 4500 {
		t.Fatalf("rebuild retained live bound: %d", s.Model.Ended)
	}
}

func TestGeneratedOperationIDsUseFullLaneNamespace(t *testing.T) {
	ids := map[string]bool{}
	for _, laneID := range []string{"r", "12345678-a", "12345678-b", "foo-ws", "foo", "lane/with?delimiters#", "日本語"} {
		t.Run(laneID, func(t *testing.T) {
			p := testParser(t, laneID, "")
			op := p.newOp("", "", classify.Code, "edit", 1000, 1000, nil)
			line := rolloutLine(t, 1500, "response_item", "web_search_call", map[string]any{})
			p.handle(line, 1)
			for _, id := range []string{op.ID, p.lane.Ops[1].ID} {
				if ids[id] || strings.ContainsAny(id, "/?#%") {
					t.Fatalf("URL-unsafe or colliding operation ID %q", id)
				}
				ids[id] = true
			}
		})
	}
}
