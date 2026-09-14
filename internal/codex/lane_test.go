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

// The harness puts its own context into the user role ahead of the human's (or the parent's)
// first message; every such block is a system_message, so the first user_message is the prompt.
func TestHarnessContextInUserRoleIsNotUserInput(t *testing.T) {
	p := testParser(t, "child-id", "parent-id")
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "turn"}),
		rolloutLine(t, 1100, "response_item", "message", map[string]any{"role": "user", "content": textContent("<recommended_plugins>\nHere is a list of plugins…\n</recommended_plugins>")}),
		rolloutLine(t, 1200, "response_item", "message", map[string]any{"role": "user", "content": textContent("# AGENTS.md instructions for /work\n<INSTRUCTIONS>rules</INSTRUCTIONS>")}),
		rolloutLine(t, 1300, "response_item", "message", map[string]any{"role": "user", "content": textContent("Review module A in /work")}),
	)
	var kinds, users []string
	for _, marker := range p.lane.Markers {
		if marker.Kind == "system_message" || marker.Kind == "user_message" {
			kinds = append(kinds, marker.Kind+":"+marker.Ref)
		}
		if marker.Kind == "user_message" {
			users = append(users, marker.Text)
		}
	}
	if want := "system_message:recommended_plugins system_message:AGENTS.md user_message:"; strings.Join(kinds, " ") != want {
		t.Fatalf("markers = %q, want %q", strings.Join(kinds, " "), want)
	}
	if len(users) != 1 || users[0] != "Review module A in /work" {
		t.Fatalf("user messages = %q", users)
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

func TestSkillSelectionMarker(t *testing.T) {
	p := testParser(t, "thread-id", "")
	skillText := "<skill>\n<name>code-review-cc</name>\n<path>/home/u/.claude/skills/code-review-cc/SKILL.md</path>\n---\nname: code-review-cc\n(long body)"
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "t1"}),
		// the human's message: sets sawUserItem so the role=user fast path is engaged
		rolloutLine(t, 1100, "event_msg", "item_completed", map[string]any{"turn_id": "t1", "item": map[string]any{"type": "UserMessage", "content": textContent("$code-review-cc --fix xhigh")}}),
		// the harness injection: role=user, content_item_kinds marks it as a selected skill.
		// Built by hand with literal <>/tags because Codex writes JSON with HTML-escaping off,
		// unlike Go's json.Marshal (which would emit \u003c).
		skillSelectionLine(t, 1150, "t1", skillText),
		rolloutLine(t, 5000, "event_msg", "task_complete", map[string]any{"turn_id": "t1", "last_agent_message": "done"}),
	)
	if len(p.lane.Turns) != 1 || p.lane.Turns[0].Skill != "code-review-cc" {
		t.Fatalf("turn.Skill not set: %+v", p.lane.Turns)
	}
	var skills, users int
	for _, mk := range p.lane.Markers {
		switch mk.Kind {
		case "skill":
			skills++
			if mk.Ref != "code-review-cc" {
				t.Errorf("skill marker ref = %q", mk.Ref)
			}
		case "user_message":
			users++
		}
	}
	if skills != 1 {
		t.Errorf("want 1 skill marker, got %d", skills)
	}
	if users != 1 {
		t.Errorf("skill injection leaked into user messages: %d user markers", users)
	}
}

func TestParseSkillSelectionNegatives(t *testing.T) {
	// A user literally typing "<skill>" without the selected-skill kind marker must not match.
	line := []byte(`{"timestamp":"x","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"<skill> what is this?"}]}}`)
	if _, _, ok := parseSkillSelection(line); ok {
		t.Error("plain '<skill>' text should not be a skill selection")
	}
	// The genuine shape parses.
	line = []byte(`{"timestamp":"x","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"<skill>\n<name>simplify-code</name>\n<path>/x/SKILL.md</path>\nbody"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["skills.selected_skill_instructions"]}}}`)
	name, path, ok := parseSkillSelection(line)
	if !ok || name != "simplify-code" || path != "/x/SKILL.md" {
		t.Errorf("parseSkillSelection = %q,%q,%v", name, path, ok)
	}
}

// skillSelectionLine builds a skills.selected_skill_instructions injection exactly as Codex
// writes it: role=user, literal <skill>/<name>/<path> tags (HTML-escaping off), and the
// content_item_kinds marker.
func skillSelectionLine(t *testing.T, ts int64, turn, body string) []byte {
	t.Helper()
	esc := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return []byte(fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":%s}],"internal_chat_message_metadata_passthrough":{"turn_id":%q,"content_item_kinds":["skills.selected_skill_instructions"]}}}`,
		time.UnixMilli(ts).UTC().Format(time.RFC3339Nano), literalTags(esc(body)), turn))
}

// literalTags undoes Go's HTML escaping of <, >, & inside an already-JSON-quoted string, matching
// Codex's encoder which leaves them literal.
func literalTags(s string) string {
	s = strings.ReplaceAll(s, `\u003c`, "<")
	s = strings.ReplaceAll(s, `\u003e`, ">")
	s = strings.ReplaceAll(s, `\u0026`, "&")
	return s
}

func TestLifecycleSignalsFromRollout(t *testing.T) {
	// A design-document path pinned by an overlay; roles/skills are covered by classify tests.
	if err := classify.ApplyUserConfig(classify.UserConfig{Lifecycle: classify.LifecycleMatcherConfig{Paths: map[classify.Lifecycle][]string{classify.LcDesign: {`(^|/)docs/DESIGN\.md$`}}}}, "test"); err != nil {
		t.Fatal(err)
	}
	p := testParser(t, "thread-id", "")
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "t1"}),
		// turn_context: Codex plan collaboration mode
		rolloutLine(t, 1001, "turn_context", "", map[string]any{"turn_id": "t1", "model": "m", "collaboration_mode": map[string]any{"mode": "plan", "settings": map[string]any{"model": "m"}}}),
		// a plan being written (step 1 already in progress, nothing completed) → anchor op
		rolloutLine(t, 1100, "response_item", "function_call", map[string]any{"name": "update_plan", "call_id": "p1", "arguments": `{"plan":[{"step":"read","status":"in_progress"},{"step":"edit","status":"pending"}]}`}),
		rolloutLine(t, 1101, "response_item", "function_call_output", map[string]any{"call_id": "p1", "output": "ok"}),
		// a progress update (one step completed) → marker only
		rolloutLine(t, 1200, "response_item", "function_call", map[string]any{"name": "update_plan", "call_id": "p2", "arguments": `{"plan":[{"step":"read","status":"completed"},{"step":"edit","status":"in_progress"}]}`}),
		rolloutLine(t, 1201, "response_item", "function_call_output", map[string]any{"call_id": "p2", "output": "ok"}),
		// an edit of the design document and one of a source file
		rolloutLine(t, 1300, "event_msg", "item_completed", map[string]any{"turn_id": "t1", "started_at_ms": 1300, "completed_at_ms": 1300, "item": map[string]any{"type": "FileChange", "id": "fc1", "changes": map[string]any{"docs/DESIGN.md": map[string]any{"type": "update"}, "internal/x.go": map[string]any{"type": "update"}}, "status": "completed"}}),
		rolloutLine(t, 1400, "event_msg", "item_completed", map[string]any{"turn_id": "t1", "started_at_ms": 1400, "completed_at_ms": 1400, "item": map[string]any{"type": "FileChange", "id": "fc2", "changes": map[string]any{"internal/y.go": map[string]any{"type": "update"}}, "status": "completed"}}),
		// a review verb and a log read through the command classifier
		rolloutLine(t, 1500, "response_item", "function_call", map[string]any{"name": "exec_command", "call_id": "c1", "arguments": `{"cmd":"gh pr comment 7 --body ok"}`}),
		rolloutLine(t, 1501, "response_item", "function_call_output", map[string]any{"call_id": "c1", "output": "ok"}),
		rolloutLine(t, 1600, "response_item", "function_call", map[string]any{"name": "exec_command", "call_id": "c2", "arguments": `{"cmd":"journalctl -u app -n 20"}`}),
		rolloutLine(t, 1601, "response_item", "function_call_output", map[string]any{"call_id": "c2", "output": "ok"}),
		rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "t1", "last_agent_message": "done"}),
		rolloutLine(t, 3000, "event_msg", "task_started", map[string]any{"turn_id": "t2"}),
		rolloutLine(t, 3001, "turn_context", "", map[string]any{"turn_id": "t2", "model": "m", "collaboration_mode": map[string]any{"mode": "default"}}),
		rolloutLine(t, 4000, "event_msg", "task_complete", map[string]any{"turn_id": "t2", "last_agent_message": "done"}),
	)
	if len(p.lane.Turns) != 2 || p.lane.Turns[0].Mode != "plan" || p.lane.Turns[1].Mode != "" {
		t.Fatalf("turn modes: %+v", p.lane.Turns)
	}
	byKind := map[string]*model.Operation{}
	for _, o := range p.lane.Ops {
		byKind[o.Kind+"|"+o.ID] = o
	}
	plan := byKind["plan|p1"]
	if plan == nil || plan.Phase != classify.LLM || plan.Lifecycle != classify.LcPlan || plan.Start != plan.End {
		t.Fatalf("plan creation op: %+v", plan)
	}
	if byKind["plan|p2"] != nil {
		t.Fatal("a progress update must not become an op")
	}
	var planMarkers int
	for _, mk := range p.lane.Markers {
		if mk.Kind == "plan" {
			planMarkers++
		}
	}
	if planMarkers != 2 {
		t.Errorf("plan markers = %d, want 2", planMarkers)
	}
	if fc := byKind["edit|fc1"]; fc == nil || fc.Lifecycle != classify.LcDesign || fc.LifecycleRule != "edited path" {
		t.Errorf("design edit: %+v", fc)
	}
	if fc := byKind["edit|fc2"]; fc == nil || fc.Lifecycle != "" {
		t.Errorf("source edit must stay unpinned: %+v", fc)
	}
	if c := byKind["pr comment|c1"]; c == nil || c.Lifecycle != classify.LcReview {
		t.Errorf("pr comment: %+v", c)
	}
	if c := byKind["journalctl|c2"]; c == nil || c.Lifecycle != classify.LcOperate {
		t.Errorf("journalctl: %+v", c)
	}
}

func TestTokensSumPerCallUsageOnCounterChange(t *testing.T) {
	usage := func(in, out, total int64) map[string]any {
		return map[string]any{"input_tokens": in, "cached_input_tokens": in / 2, "output_tokens": out, "reasoning_output_tokens": out / 2, "total_tokens": total}
	}
	count := func(ts int64, total, last map[string]any) []byte {
		return rolloutLine(t, ts, "event_msg", "token_count", map[string]any{"info": map[string]any{"total_token_usage": total, "last_token_usage": last}})
	}
	t.Run("root: duplicates, a zero record, info:null and a counter restart", func(t *testing.T) {
		p := testParser(t, "thread-id", "")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
			count(1100, usage(100, 10, 110), usage(100, 10, 110)), // first call: total == last
			count(1200, usage(100, 10, 110), usage(100, 10, 110)), // repeated verbatim: not counted
			count(1300, usage(100, 10, 110), usage(40, 4, 44)),    // same total, different last: still a repeat
			count(1400, usage(0, 0, 0), usage(0, 0, 0)),           // zero usage around a compaction: skipped
			rolloutLine(t, 1450, "event_msg", "token_count", map[string]any{"info": nil}),
			count(1500, usage(300, 30, 330), usage(200, 20, 220)), // +220
			count(1600, usage(50, 5, 55), usage(50, 5, 55)),       // the counter restarted: the call counts, the drop does not
			count(1700, usage(80, 8, 88), usage(30, 3, 33)),       // +33
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "first"}),
		)
		want := model.TokenUsage{Input: 380, Cached: 190, Output: 38, Reasoning: 18, Total: 418}
		if p.lane.Tokens == nil || *p.lane.Tokens != want {
			t.Fatalf("tokens %+v, want %+v", p.lane.Tokens, want)
		}
	})
	t.Run("forked child: the first record carries the parent's counter", func(t *testing.T) {
		p := testParser(t, "child-id", "parent-thread")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
			count(1100, usage(9000, 900, 9900), usage(120, 12, 132)), // inherited total, own call
			count(1200, usage(9200, 920, 10120), usage(200, 20, 220)),
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "first"}),
		)
		want := model.TokenUsage{Input: 320, Cached: 160, Output: 32, Reasoning: 16, Total: 352}
		if p.lane.Tokens == nil || *p.lane.Tokens != want {
			t.Fatalf("tokens %+v, want %+v", p.lane.Tokens, want)
		}
	})
	t.Run("a record without per-call usage counts the counter's increase", func(t *testing.T) {
		p := testParser(t, "thread-id", "")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
			rolloutLine(t, 1100, "event_msg", "token_count", map[string]any{"info": map[string]any{"total_token_usage": usage(100, 10, 110)}}),
			rolloutLine(t, 1200, "event_msg", "token_count", map[string]any{"info": map[string]any{"total_token_usage": usage(150, 15, 165)}}),
		)
		if p.lane.Tokens == nil || p.lane.Tokens.Total != 165 {
			t.Fatalf("tokens %+v, want total 165", p.lane.Tokens)
		}
	})
	t.Run("a failed search is recorded verbatim and derived as a query miss", func(t *testing.T) {
		p := testParser(t, "thread-id", "")
		item := func(id, cmd string, exit int, parsed string, started, completed int64) []byte {
			return rolloutLine(t, completed, "event_msg", "item_completed", map[string]any{"turn_id": "first", "started_at_ms": started, "completed_at_ms": completed,
				"item": map[string]any{"type": "CommandExecution", "id": id, "command": []string{"bash", "-lc", cmd}, "cwd": "/repo", "status": "failed", "exit_code": exit, "parsed_cmd": []map[string]any{{"type": parsed, "cmd": cmd}}}})
		}
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "first"}),
			item("c1", "rg -n 'needle' src", 1, "search", 1100, 1200),
			item("c2", "go test ./...", 1, "unknown", 1300, 1400),
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "first"}),
		)
		s := &model.Session{Lanes: []*model.Lane{p.lane}}
		model.Derive(s, 2000)
		search, test := p.lane.Ops[0], p.lane.Ops[1]
		if search.Status != "failed" || search.Exit == nil || *search.Exit != 1 || !search.QueryMiss {
			t.Fatalf("search op %+v", search)
		}
		if test.Status != "failed" || test.QueryMiss || !test.Failure() {
			t.Fatalf("test op %+v", test)
		}
		if s.Totals.Failed != 1 || s.Totals.QueryMisses != 1 {
			t.Fatalf("failed=%d query_misses=%d", s.Totals.Failed, s.Totals.QueryMisses)
		}
	})
}

func TestTokensPerTurnAndCompaction(t *testing.T) {
	usage := func(in, cached, out, total int64) map[string]any {
		return map[string]any{"input_tokens": in, "cached_input_tokens": cached, "output_tokens": out, "reasoning_output_tokens": int64(0), "total_tokens": total}
	}
	count := func(ts int64, total, last map[string]any) []byte {
		return rolloutLine(t, ts, "event_msg", "token_count", map[string]any{"info": map[string]any{"total_token_usage": total, "last_token_usage": last}})
	}
	compaction := func(id, turn string, s, e int64) []byte {
		return rolloutLine(t, e, "event_msg", "item_completed", map[string]any{"turn_id": turn, "started_at_ms": s, "completed_at_ms": e, "item": map[string]any{"type": "ContextCompaction", "id": id}})
	}
	t.Run("turn sums, responses, first call, context peak; compaction context and re-read", func(t *testing.T) {
		p := testParser(t, "thread-id", "")
		appendRollout(t, p,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "t1"}),
			count(1100, usage(100, 50, 10, 110), usage(100, 50, 10, 110)), // counted: first call of t1
			count(1200, usage(100, 50, 10, 110), usage(100, 50, 10, 110)), // repeat: not counted
			count(1300, usage(400, 250, 30, 430), usage(300, 200, 20, 320)),
			count(1350, usage(0, 0, 0, 0), usage(0, 0, 0, 0)), // zero record before the compaction: skipped
			compaction("c1", "t1", 1400, 1500),
			count(1550, usage(0, 0, 0, 0), usage(0, 0, 0, 0)), // zero record after it: skipped, must not become the re-read
			count(1600, usage(430, 260, 33, 463), usage(30, 10, 3, 33)),
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "t1"}),
			rolloutLine(t, 3000, "event_msg", "task_started", map[string]any{"turn_id": "t2"}),
			count(3100, usage(430, 260, 33, 463), usage(30, 10, 3, 33)), // repeats the previous turn's last record: not the first call of t2
			count(3200, usage(500, 320, 40, 540), usage(70, 60, 7, 77)),
			rolloutLine(t, 4000, "event_msg", "task_complete", map[string]any{"turn_id": "t2"}),
		)
		if len(p.lane.Turns) != 2 {
			t.Fatalf("turns %d", len(p.lane.Turns))
		}
		t1, t2 := p.lane.Turns[0], p.lane.Turns[1]
		if want := (model.TokenUsage{Input: 430, Cached: 260, Output: 33, Total: 463}); t1.Tokens == nil || *t1.Tokens != want {
			t.Fatalf("t1 tokens %+v, want %+v", t1.Tokens, want)
		}
		if t1.Responses != 3 || t1.ContextPeak != 300 || t1.First == nil || t1.First.Input != 100 || t1.First.Cached != 50 {
			t.Fatalf("t1 responses=%d peak=%d first=%+v", t1.Responses, t1.ContextPeak, t1.First)
		}
		if want := (model.TokenUsage{Input: 70, Cached: 60, Output: 7, Total: 77}); t2.Tokens == nil || *t2.Tokens != want || t2.Responses != 1 || t2.First == nil || *t2.First != want || t2.ContextPeak != 70 {
			t.Fatalf("t2 %+v responses=%d first=%+v peak=%d", t2.Tokens, t2.Responses, t2.First, t2.ContextPeak)
		}
		var sum model.TokenUsage
		for _, tn := range p.lane.Turns {
			sum.Add(tn.Tokens)
		}
		if p.lane.Tokens == nil || sum != *p.lane.Tokens {
			t.Fatalf("turn sum %+v != lane %+v", sum, p.lane.Tokens)
		}
		var comp *model.Operation
		for _, o := range p.lane.Ops {
			if o.Kind == "compaction" {
				comp = o
			}
		}
		if comp == nil || comp.Context != 300 {
			t.Fatalf("compaction op %+v", comp)
		}
		if want := (model.TokenUsage{Input: 30, Cached: 10, Output: 3, Total: 33}); comp.Tokens == nil || *comp.Tokens != want {
			t.Fatalf("re-read after compaction %+v, want %+v", comp.Tokens, want)
		}
	})
	t.Run("a sub-agent turn learns its root turn from token_usage_record; a root lane ignores the record", func(t *testing.T) {
		record := func(ts int64, turn, root string) []byte {
			return rolloutLine(t, ts, "token_usage_record", "", map[string]any{"turn_id": turn, "root_turn_id": root, "response_id": "r1"})
		}
		child := testParser(t, "child-id", "parent-thread")
		appendRollout(t, child,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "c1"}),
			record(1100, "other-turn", "r-other"), // another turn's record: ignored
			record(1200, "c1", "r9"),
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "c1"}),
		)
		if got := child.lane.Turns[0].RootTurn; got != "r9" {
			t.Fatalf("child root turn %q", got)
		}
		root := testParser(t, "root-id", "")
		appendRollout(t, root,
			rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "r1"}),
			record(1100, "r1", "r1"),
			rolloutLine(t, 2000, "event_msg", "task_complete", map[string]any{"turn_id": "r1"}),
		)
		if got := root.lane.Turns[0].RootTurn; got != "" {
			t.Fatalf("root lane must not carry a root turn, got %q", got)
		}
	})
}
