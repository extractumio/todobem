package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/source"
)

// ---- fixture: synthetic Claude Code lines (cwd, paths and text are made up)

const (
	fxSession = "11111111-2222-4333-8444-555555555555"
	fxCWD     = "/synthetic/project"
)

func stamp(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

// metaLine is a small metadata line (mode, ai-title, agent-name, …): the CLI writes its type
// first, which is what the index and the parser key on.
func metaLine(kind string, fields map[string]any) string {
	var sb strings.Builder
	sb.WriteString(`{"type":"` + kind + `"`)
	for k, v := range fields {
		b, _ := json.Marshal(v)
		sb.WriteString(`,"` + k + `":` + string(b))
	}
	sb.WriteString(`,"sessionId":"` + fxSession + `"}` + "\n")
	return sb.String()
}

// msgLine is a message line: the fixed leading fields, then the payload, then the trailing
// fields, in the order the CLI writes them (parentUuid first, type after the payload).
func msgLine(kind string, ms int64, uuid string, fields map[string]any) string {
	var sb strings.Builder
	sb.WriteString(`{"parentUuid":"p-` + uuid + `","isSidechain":false`)
	for k, v := range fields {
		b, _ := json.Marshal(v)
		sb.WriteString(`,"` + k + `":` + string(b))
	}
	sb.WriteString(`,"type":"` + kind + `","uuid":"` + uuid + `","timestamp":"` + stamp(ms) + `","userType":"external","entrypoint":"cli","cwd":"` + fxCWD + `","sessionId":"` + fxSession + `","version":"2.1.270","gitBranch":"main"}` + "\n")
	return sb.String()
}

func promptLine(ms int64, uuid, text string, extra map[string]any) string {
	f := map[string]any{"message": map[string]any{"role": "user", "content": text}}
	for k, v := range extra {
		f[k] = v
	}
	return msgLine("user", ms, uuid, f)
}

func resultLine(ms int64, uuid, toolUseID, content string, isError bool, tur any) string {
	block := map[string]any{"tool_use_id": toolUseID, "type": "tool_result", "content": content}
	if isError {
		block["is_error"] = true
	}
	f := map[string]any{"message": map[string]any{"role": "user", "content": []any{block}}}
	if tur != nil {
		f["toolUseResult"] = tur
	}
	return msgLine("user", ms, uuid, f)
}

type usage struct{ in, cacheRead, cacheCreate, out int }

func assistantLine(ms int64, uuid, msgID, stop string, block map[string]any, u usage) string {
	msg := map[string]any{"model": "claude-synthetic-1", "id": msgID, "type": "message", "role": "assistant", "content": []any{block}, "stop_reason": stop,
		"usage": map[string]any{"input_tokens": u.in, "cache_read_input_tokens": u.cacheRead, "cache_creation_input_tokens": u.cacheCreate, "output_tokens": u.out}}
	return msgLine("assistant", ms, uuid, map[string]any{"message": msg, "requestId": "req-" + msgID, "effort": "high"})
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func thinking() map[string]any     { return map[string]any{"type": "thinking", "thinking": "…"} }
func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func systemLine(ms int64, uuid, subtype string, fields map[string]any) string {
	f := map[string]any{"subtype": subtype, "level": "info"}
	for k, v := range fields {
		f[k] = v
	}
	return msgLine("system", ms, uuid, f)
}

func attachmentLine(ms int64, uuid string) string {
	return msgLine("attachment", ms, uuid, map[string]any{"attachment": map[string]any{"type": "hook_success", "hookName": "SessionStart", "stdout": strings.Repeat("x", 300)}})
}

// home builds a Claude home with one root session and returns (home, root path).
func home(t *testing.T, content string) (string, string) {
	t.Helper()
	h := t.TempDir()
	dir := filepath.Join(h, "projects", "-synthetic-project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fxSession+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return h, path
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// open indexes the home and parses the root session through the shared joiner.
func open(t *testing.T, h string) (*Index, *source.Session) {
	t.Helper()
	ix := NewIndex(h)
	ix.Scan()
	s, err := ix.Open(fxSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	return ix, s
}

func opByID(l *model.Lane, id string) *model.Operation {
	for _, o := range l.Ops {
		if o.ID == id {
			return o
		}
	}
	return nil
}

func markers(l *model.Lane, kind string) []model.Marker {
	var out []model.Marker
	for _, mk := range l.Markers {
		if mk.Kind == kind {
			out = append(out, mk)
		}
	}
	return out
}

func checkPartition(t *testing.T, m *model.Session) {
	t.Helper()
	for _, l := range m.Lanes {
		var part, lc int64
		for _, sg := range l.Segments {
			part += sg.End - sg.Start
		}
		for _, v := range l.ByLifecycle {
			lc += v
		}
		if part != l.Ended-l.Started || lc != part {
			t.Fatalf("partition of %s: segments %d lifecycle %d span %d", l.Path, part, lc, l.Ended-l.Started)
		}
	}
}

// ---- tests

// TestTurnsOpsTokensAndHooks: a two-turn session. Turn 1: a failed Bash (exit 1), an edit, a
// read of a missing path (a query miss), the answer, then the stop hooks (inside the turn, and
// its end). Turn 2: a queued prompt, a question the user answers after 30 s, the answer, EOF.
func TestTurnsOpsTokensAndHooks(t *testing.T) {
	content := metaLine("mode", map[string]any{"mode": "normal"}) +
		promptLine(1_000, "u1", "Run the tests and fix the failure.", map[string]any{"promptSource": "typed", "origin": map[string]any{"kind": "human"}}) +
		metaLine("ai-title", map[string]any{"aiTitle": "Fix the failing test"}) +
		attachmentLine(1_100, "att1") +
		assistantLine(3_000, "a1", "m1", "tool_use", thinking(), usage{in: 100, cacheRead: 5000, cacheCreate: 200, out: 40}) +
		assistantLine(3_500, "a2", "m1", "tool_use", toolUse("t1", "Bash", map[string]any{"command": "go test ./...", "description": "Run tests"}), usage{in: 100, cacheRead: 5000, cacheCreate: 200, out: 40}) +
		resultLine(9_500, "r1", "t1", "Exit code 1\n--- FAIL: TestX", true, map[string]any{"stdout": "…", "stderr": "", "interrupted": false}) +
		assistantLine(12_000, "a3", "m2", "tool_use", toolUse("t2", "Edit", map[string]any{"file_path": fxCWD + "/pkg/x_test.go", "old_string": "a", "new_string": "b"}), usage{in: 50, cacheRead: 5300, out: 30}) +
		resultLine(12_200, "r2", "t2", "The file has been updated.", false, map[string]any{"filePath": fxCWD + "/pkg/x_test.go"}) +
		assistantLine(14_000, "a4", "m3", "tool_use", toolUse("t3", "Read", map[string]any{"file_path": fxCWD + "/missing.go"}), usage{in: 10, cacheRead: 5400, out: 20}) +
		resultLine(14_100, "r3", "t3", "<tool_use_error>File does not exist.</tool_use_error>", true, nil) +
		assistantLine(20_000, "a5", "m4", "end_turn", thinking(), usage{in: 10, cacheRead: 5500, out: 60}) +
		assistantLine(20_500, "a6", "m4", "end_turn", text("Fixed: the test passes now.\n\nDetails."), usage{in: 10, cacheRead: 5500, out: 60}) +
		systemLine(22_000, "s1", "stop_hook_summary", map[string]any{"hookCount": 1, "hookInfos": []any{map[string]any{"command": "lint.sh", "durationMs": 1500}}, "hookErrors": []any{}, "preventedContinuation": false}) +
		systemLine(22_100, "s2", "turn_duration", map[string]any{"durationMs": 21100, "messageCount": 12}) +
		promptLine(22_150, "u2", "Also update the docs?", map[string]any{"promptSource": "queued", "origin": map[string]any{"kind": "human"}}) +
		assistantLine(24_000, "a7", "m5", "tool_use", toolUse("t4", "AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "Which docs?"}}}), usage{in: 10, cacheRead: 5600, out: 25}) +
		resultLine(54_000, "r4", "t4", "User answered: README", false, "User answered: README") +
		assistantLine(60_000, "a8", "m6", "end_turn", text("Done."), usage{in: 10, cacheRead: 5700, out: 15})
	h, _ := home(t, content)
	ix, s := open(t, h)
	m := s.Model
	root := m.Lanes[0]
	checkPartition(t, m)
	if m.Source != source.Claude || m.Title != "Fix the failing test" || m.CLI != "2.1.270" || m.CWD != fxCWD || m.Branch != "main" || root.Model != "claude-synthetic-1" {
		t.Fatalf("session identity: source=%s title=%q cli=%s cwd=%s branch=%s model=%s", m.Source, m.Title, m.CLI, m.CWD, m.Branch, root.Model)
	}
	if m.Live {
		t.Fatal("a file ending on an answered end_turn is not live")
	}
	if len(root.Turns) != 2 {
		t.Fatalf("turns: %d", len(root.Turns))
	}
	t1, t2 := root.Turns[0], root.Turns[1]
	if t1.ID != "u1" || t1.Start != 1_000 || t1.End != 22_000 || t1.Status != "completed" || t1.Trigger != "user" || t1.Final != "Fixed: the test passes now.\n\nDetails." || t1.Model != "claude-synthetic-1" || t1.Effort != "high" {
		t.Fatalf("turn 1: %+v", t1)
	}
	if t2.ID != "u2" || t2.Start != 22_150 || t2.End != 60_000 || t2.Status != "completed" || t2.Final != "Done." {
		t.Fatalf("turn 2: %+v", t2)
	}
	// ops
	bash := opByID(root, "t1")
	if bash == nil || bash.Phase != classify.Test || bash.Start != 3_500 || bash.End != 9_500 || bash.Status != "failed" || bash.Exit == nil || *bash.Exit != 1 || bash.Turn != "u1" || !strings.HasPrefix(bash.Identity, fxCWD+"\n") {
		t.Fatalf("bash op: %+v", bash)
	}
	edit := opByID(root, "t2")
	// (path pins come from the user overlay only; none here, so the stage is the phase default)
	if edit == nil || edit.Phase != classify.Code || edit.Kind != "edit" || edit.Title != "edit x_test.go" || edit.End != 12_200 || edit.Status != "completed" || edit.Lifecycle != classify.LcImplement || edit.LifecycleRule != "phase code" {
		t.Fatalf("edit op: %+v", edit)
	}
	read := opByID(root, "t3")
	if read == nil || read.Kind != "read" || read.Status != "failed" || !read.QueryMiss || read.Failure() {
		t.Fatalf("a failed read is a query miss, not a failure: %+v", read)
	}
	var hook, question *model.Operation
	for _, o := range root.Ops {
		switch o.Kind {
		case "hook":
			hook = o
		case "question":
			question = o
		}
	}
	if hook == nil || hook.Phase != classify.WaitWorker || hook.Start != 20_500 || hook.End != 22_000 || hook.Turn != "u1" || hook.Title != "stop hooks · 1" {
		t.Fatalf("hook op: %+v", hook)
	}
	if question == nil || question.Phase != classify.WaitUser || question.Start != 24_000 || question.End != 54_000 || question.Turn != "u2" {
		t.Fatalf("question op: %+v", question)
	}
	if m.Totals.Failed != 1 || m.Totals.QueryMisses != 1 || m.Totals.Questions != 1 || m.Totals.UserMessages != 2 || m.Totals.Turns != 2 {
		t.Fatalf("totals: %+v", m.Totals)
	}
	// the question is the user's time; the hook the harness's
	if root.ByPhase[classify.WaitUser] < 30_000 || root.ByPhase[classify.WaitWorker] != 1_500 {
		t.Fatalf("by_phase: %+v", root.ByPhase)
	}
	// tokens: one call per message id (m1 has two block lines), the turn's sum, the peak context
	if root.Tokens == nil || root.Tokens.Total != 5300+40+5350+30+5410+20+5510+60+5610+25+5710+15 || root.Tokens.Cached != 5000+5300+5400+5500+5600+5700 || root.Tokens.CacheWrite != 200 {
		t.Fatalf("lane tokens: %+v", root.Tokens)
	}
	if t1.Responses != 4 || t1.Tokens == nil || t1.Tokens.Output != 150 || t1.ContextPeak != 5510 || t1.First == nil || t1.First.Input != 5300 {
		t.Fatalf("turn 1 tokens: responses=%d tokens=%+v peak=%d first=%+v", t1.Responses, t1.Tokens, t1.ContextPeak, t1.First)
	}
	if fa := markers(root, "final_answer"); len(fa) != 2 || fa[0].T != 20_500 || fa[0].Text != t1.Final {
		t.Fatalf("final answers: %+v", fa)
	}
	if q := markers(root, "question"); len(q) != 1 || q[0].Text != "Which docs?" {
		t.Fatalf("question marker: %+v", q)
	}
	// the list: title from the harness, the last answer from the tail, the source named
	sums := source.Summaries(ix, nil)
	if len(sums) != 1 || sums[0].Source != source.Claude || sums[0].Title != "Fix the failing test" || sums[0].LastAnswer != "Done." || sums[0].CLI != "2.1.270" || sums[0].Model != "claude-synthetic-1" {
		t.Fatalf("summary: %+v", sums[0])
	}
}

// TestSlashCommandsInterruptsAndSystemPrompts: a local slash command opens no turn but keeps
// the user's message; a command the model answers is a turn; an interrupt closes the turn as
// aborted with its pending op; harness prompts open system-triggered turns.
func TestSlashCommandsInterruptsAndSystemPrompts(t *testing.T) {
	content := promptLine(500, "c0", "<local-command-caveat>Caveat: the messages below were generated by the user while running local commands.</local-command-caveat>", map[string]any{"isMeta": true}) +
		promptLine(600, "c0b", "<command-name>/clear</command-name>\n<command-message>clear</command-message>\n<command-args></command-args>", nil) +
		systemLine(600, "sc0", "local_command", map[string]any{"content": "<command-name>/clear</command-name>"}) +
		promptLine(1_000, "c1", "<command-name>/cost</command-name>\n<command-message>cost</command-message>\n<command-args></command-args>", nil) +
		promptLine(1_050, "c2", "<local-command-stdout>Total cost: $0.42</local-command-stdout>", nil) +
		promptLine(1_500, "c2b", "<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args></command-args>", nil) +
		promptLine(2_000, "c3", "<command-name>/review</command-name>\n<command-message>review</command-message>\n<command-args>pkg/x</command-args>", nil) +
		assistantLine(4_000, "a1", "m1", "tool_use", toolUse("t1", "Bash", map[string]any{"command": "sleep 100"}), usage{in: 10, out: 5}) +
		promptLine(9_000, "i1", "[Request interrupted by user for tool use]", nil) +
		promptLine(20_000, "n1", "<task-notification>\n<task-id>x</task-id>\n<status>completed</status>\n</task-notification>", map[string]any{"promptSource": "system", "origin": map[string]any{"kind": "task-notification"}}) +
		assistantLine(21_000, "a2", "m2", "end_turn", text("The background task finished."), usage{in: 10, out: 5}) +
		promptLine(30_000, "u2", "[Image: source: /synthetic/shot.png]", map[string]any{"isMeta": true}) +
		promptLine(30_001, "u3", "Look at the screenshot.", map[string]any{"promptSource": "typed", "origin": map[string]any{"kind": "human"}}) +
		assistantLine(32_000, "a3", "m3", "end_turn", text("Looked."), usage{in: 10, out: 5}) +
		// hours later the session is closed: the harness delivers a pending notification and
		// records the interrupt at once — the model never ran, so this is not a turn
		systemLine(3_600_000, "s9", "agents_killed", nil) +
		promptLine(3_600_000, "n2", "<task-notification>\n<task-id>y</task-id>\n<status>completed</status>\n</task-notification>", map[string]any{"promptSource": "system", "origin": map[string]any{"kind": "task-notification"}}) +
		promptLine(3_600_001, "i2", "[Request interrupted by user]", nil)
	h, _ := home(t, content)
	_, s := open(t, h)
	m := s.Model
	root := m.Lanes[0]
	checkPartition(t, m)
	if len(root.Turns) != 3 {
		t.Fatalf("turns: %+v", root.Turns)
	}
	review, notif, shot := root.Turns[0], root.Turns[1], root.Turns[2]
	if review.ID != "c3" || review.Status != "aborted" || review.End != 9_000 || review.Trigger != "user" {
		t.Fatalf("the interrupted command turn: %+v", review)
	}
	if op := opByID(root, "t1"); op == nil || op.Status != "aborted" || op.End != 9_000 || op.Open {
		t.Fatalf("the pending op of an interrupted turn: %+v", op)
	}
	if notif.ID != "n1" || notif.Trigger != "system" || notif.Status != "completed" || notif.End != 21_000 {
		t.Fatalf("the task-notification turn: %+v", notif)
	}
	if shot.ID != "u3" || shot.Trigger != "user" {
		t.Fatalf("the prompt after an image attachment: %+v", shot)
	}
	// /clear (answered by a local_command record), /cost (by a stdout line) and /model (by
	// nothing before the next prompt) are the user's words but not turns
	users := markers(root, "user_message")
	if len(users) != 5 || users[0].Text != "/clear" || users[0].Turn != "" || users[1].Text != "/cost" || users[1].Turn != "" || users[2].Text != "/model" || users[2].Turn != "" || users[3].Text != "/review pkg/x" || users[3].Turn != "c3" || users[4].Text != "Look at the screenshot." {
		t.Fatalf("user messages: %+v", users)
	}
	if starts := markers(root, "turn_start"); len(starts) != 3 {
		t.Fatalf("turn starts: %+v", starts)
	}
	if sys := markers(root, "system_message"); len(sys) != 2 || sys[0].Ref != "task-notification" || sys[1].Turn != "" {
		t.Fatalf("system messages: %+v", sys)
	}
	if ir := markers(root, "interrupted"); len(ir) != 1 {
		t.Fatalf("interrupted markers: %+v", ir)
	}
	// idle before a harness-triggered turn is not the user's time; the hour before the
	// notification the model never answered is (no turn opened there)
	// (wait_worker: the 11 s before the notification turn plus the 5 s `sleep` op)
	if root.ByPhase[classify.WaitWorker] != 16_000 || root.ByPhase[classify.WaitUser] < 3_500_000 {
		t.Fatalf("by_phase: %+v", root.ByPhase)
	}
}

// TestSubAgentsBackgroundBashAndCompaction: the Agent tool links the spawn marker to the
// sub-agent lane through the result's agentId; a background Bash grows to its last poll; a
// compaction op starts no earlier than the last evidence and receives the next call's usage.
func TestSubAgentsBackgroundBashAndCompaction(t *testing.T) {
	agent := "agent-abc123def4567890a"
	content := promptLine(1_000, "u1", "Review this and run the suite in the background.", map[string]any{"promptSource": "typed"}) +
		assistantLine(2_000, "a1", "m1", "tool_use", toolUse("t1", "Agent", map[string]any{"description": "Review the change", "subagent_type": "pragmatic", "prompt": "Review …"}), usage{in: 10, out: 5}) +
		resultLine(30_000, "r1", "t1", "Review: fine.", false, map[string]any{"isAsync": false, "status": "completed", "agentId": strings.TrimPrefix(agent, "agent-"), "description": "Review the change"}) +
		assistantLine(31_000, "a2", "m2", "tool_use", toolUse("t2", "Bash", map[string]any{"command": "pytest -q", "run_in_background": true}), usage{in: 10, out: 5}) +
		resultLine(31_100, "r2", "t2", "Command running in background with ID: task-9", false, map[string]any{"backgroundTaskId": "task-9", "stdout": "", "stderr": "", "interrupted": false}) +
		assistantLine(32_000, "a3", "m3", "tool_use", toolUse("t3", "TaskOutput", map[string]any{"task_id": "task-9", "block": true}), usage{in: 10, out: 5}) +
		resultLine(45_000, "r3", "t3", "3 passed", false, map[string]any{"retrieval_status": "completed", "task": map[string]any{"status": "completed"}}) +
		systemLine(50_000, "s1", "compact_boundary", map[string]any{"content": "Conversation compacted", "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 180000, "postTokens": 12000, "durationMs": 20000}}) +
		assistantLine(52_000, "a4", "m4", "end_turn", text("All green."), usage{in: 12000, out: 5})
	h, root := home(t, content)
	subDir := filepath.Join(filepath.Dir(root), fxSession, "subagents")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(filepath.Dir(root), fxSession, "tool-results"), 0700)
	os.WriteFile(filepath.Join(filepath.Dir(root), fxSession, "tool-results", "big.txt"), []byte("ignored"), 0600)
	os.WriteFile(filepath.Join(subDir, agent+".meta.json"), []byte(`{"agentType":"pragmatic","description":"Review the change","name":"reviewer","spawnDepth":1,"model":"opus","toolUseId":"t1"}`), 0600)
	sub := msgLine("user", 2_500, "su1", map[string]any{"agentId": strings.TrimPrefix(agent, "agent-"), "message": map[string]any{"role": "user", "content": "Review …"}}) +
		assistantLine(4_000, "sa1", "sm1", "tool_use", toolUse("st1", "Grep", map[string]any{"pattern": "TODO", "path": "pkg"}), usage{in: 500, out: 20}) +
		resultLine(4_200, "sr1", "st1", "No matches", false, map[string]any{"returnCodeInterpretation": "No matches found"}) +
		assistantLine(29_000, "sa2", "sm2", "end_turn", text("Review: fine."), usage{in: 600, out: 40})
	os.WriteFile(filepath.Join(subDir, agent+".jsonl"), []byte(sub), 0600)
	ix, s := open(t, h)
	m := s.Model
	checkPartition(t, m)
	if len(m.Lanes) != 2 {
		t.Fatalf("lanes: %d", len(m.Lanes))
	}
	rl, al := m.Lanes[0], m.Lanes[1]
	if al.ID != agent || al.Path != "/root/reviewer" || al.Role != "pragmatic" || al.Nickname != "reviewer" || al.Depth != 1 || al.Parent != fxSession || al.Model != "claude-synthetic-1" {
		t.Fatalf("sub-agent lane: %+v", al)
	}
	if len(al.Turns) != 1 || al.Turns[0].Status != "completed" || al.Turns[0].End != 29_000 || al.Live {
		t.Fatalf("sub-agent turn closes at the end of its file: %+v live=%v", al.Turns[0], al.Live)
	}
	if grep := opByID(al, "st1"); grep == nil || grep.Kind != "search" || grep.Status != "completed" {
		t.Fatalf("sub-agent grep: %+v", grep)
	}
	spawn := markers(rl, "agent_started")
	if len(spawn) != 1 || spawn[0].Ref != agent || spawn[0].T != 2_000 || spawn[0].Text != "Review the change" {
		t.Fatalf("spawn marker: %+v", spawn)
	}
	if done := markers(rl, "agent_completed"); len(done) != 1 || done[0].Ref != agent || done[0].T != 30_000 {
		t.Fatalf("completed marker: %+v", done)
	}
	if wait := opByID(rl, "t1"); wait == nil || wait.Phase != classify.WaitWorker || wait.Kind != "agent" || wait.End != 30_000 {
		t.Fatalf("agent wait op: %+v", wait)
	}
	if m.Parallel.Agents != 1 || m.Parallel.AgentMs != 29_000-2_500 {
		t.Fatalf("parallel: %+v", m.Parallel)
	}
	bg := opByID(rl, "t2")
	if bg == nil || bg.Phase != classify.Test || bg.End != 45_000 || bg.Status != "completed" || bg.Background {
		t.Fatalf("background bash grows to its last poll: %+v", bg)
	}
	if poll := opByID(rl, "t3"); poll != nil {
		t.Fatalf("a poll of a known task is not its own op: %+v", poll)
	}
	var comp *model.Operation
	for _, o := range rl.Ops {
		if o.Phase == classify.Compaction {
			comp = o
		}
	}
	if comp == nil || comp.Start != 45_000 || comp.End != 50_000 || comp.Context != 180000 || comp.Tokens == nil || comp.Tokens.Input != 12000 || comp.Title != "context compaction (auto)" {
		t.Fatalf("compaction op: %+v tokens=%+v", comp, comp.Tokens)
	}
	if m.Totals.Compactions != 1 {
		t.Fatalf("totals: %+v", m.Totals)
	}
	// the index: sub-agent file found, tool-results ignored, statuses
	if d := ix.Descendants(fxSession); len(d) != 1 || d[0].ID != agent || d[0].ParentID != fxSession {
		t.Fatalf("descendants: %+v", d)
	}
	if ids := ix.IDs(); len(ids) != 2 {
		t.Fatalf("ids: %v", ids)
	}
	if sums := source.Summaries(ix, nil); len(sums) != 1 || sums[0].Agents != 1 {
		t.Fatalf("summaries: %+v", sums)
	}
}

// TestEndOfFileCloseReopensOnContinuation: a turn closed at the end of the file re-opens when
// the next read brings another block of the same message, and closes again on the next line.
func TestEndOfFileCloseReopensOnContinuation(t *testing.T) {
	content := promptLine(1_000, "u1", "Go.", map[string]any{"promptSource": "typed"}) +
		assistantLine(5_000, "a1", "m1", "end_turn", text("First part."), usage{in: 10, out: 5})
	h, path := home(t, content)
	_, s := open(t, h)
	root := s.Model.Lanes[0]
	if len(root.Turns) != 1 || root.Turns[0].Status != "completed" || root.Turns[0].End != 5_000 || root.Turns[0].Final != "First part." || s.Model.Live {
		t.Fatalf("closed at the end of the file: %+v live=%v", root.Turns[0], s.Model.Live)
	}
	appendTo(t, path, assistantLine(6_000, "a2", "m1", "end_turn", text("Second part."), usage{in: 10, out: 5}))
	if _, err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	root = s.Model.Lanes[0]
	if len(root.Turns) != 1 || root.Turns[0].Status != "completed" || root.Turns[0].End != 6_000 || root.Turns[0].Final != "Second part." || len(markers(root, "turn_end")) != 1 || len(markers(root, "final_answer")) != 1 {
		t.Fatalf("re-opened and closed again: %+v ends=%d finals=%d", root.Turns[0], len(markers(root, "turn_end")), len(markers(root, "final_answer")))
	}
	if root.Tokens.Total != 15 {
		t.Fatalf("a message continued across reads is still one call: %+v", root.Tokens)
	}
	// a new prompt while a turn is open orphans it; an assistant line with no turn re-opens a
	// system-triggered one from the last evidence
	appendTo(t, path, promptLine(10_000, "u2", "More.", map[string]any{"promptSource": "typed"})+
		assistantLine(12_000, "a3", "m2", "tool_use", toolUse("t1", "Read", map[string]any{"file_path": "/synthetic/a"}), usage{in: 10, out: 5})+
		promptLine(20_000, "u3", "Never mind.", map[string]any{"promptSource": "typed"})+
		assistantLine(21_000, "a4", "m3", "end_turn", text("Ok."), usage{in: 10, out: 5})+
		systemLine(21_500, "s1", "stop_hook_summary", map[string]any{"hookCount": 0, "hookInfos": []any{}})+
		assistantLine(25_000, "a5", "m4", "end_turn", text("Continuing after the hook."), usage{in: 10, out: 5}))
	if _, err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	root = s.Model.Lanes[0]
	checkPartition(t, s.Model)
	if len(root.Turns) != 4 {
		t.Fatalf("turns: %+v", root.Turns)
	}
	if orphan := root.Turns[1]; orphan.Status != "orphaned" || orphan.End != 12_000 {
		t.Fatalf("orphaned turn: %+v", orphan)
	}
	if op := opByID(root, "t1"); op == nil || op.Status != "aborted" {
		t.Fatalf("op of the orphaned turn: %+v", op)
	}
	if cont := root.Turns[3]; cont.Trigger != "system" || cont.Start != 21_500 || cont.End != 25_000 || cont.Status != "completed" {
		t.Fatalf("continuation turn: %+v", cont)
	}
}

// TestIndexTitlesGrowthAndHomes: the title is the harness's ai-title when present, else the
// first prompt (a slash command as typed, a teammate's first message without its tag); an
// untitled small file is re-scanned when it grows; the last answer follows the tail; homes are
// described for the settings page.
func TestIndexTitlesGrowthAndHomes(t *testing.T) {
	h, path := home(t, promptLine(1_000, "u1", "<command-name>/loop</command-name>\n<command-message>loop</command-message>\n<command-args>5m check</command-args>", nil))
	ix := NewIndex(h)
	ix.Scan()
	fm, ok := ix.Get(fxSession)
	if !ok || fm.Title != "/loop 5m check" || fm.LastAnswer != "" || fm.Source != source.Claude || fm.CWD != fxCWD || fm.Started != 1_000 {
		t.Fatalf("first scan: %+v ok=%v", fm, ok)
	}
	appendTo(t, path, assistantLine(3_000, "a1", "m1", "end_turn", text("Checked."), usage{in: 10, out: 5})+
		metaLine("ai-title", map[string]any{"aiTitle": "Loop check"}))
	ix.Scan()
	if fm, _ := ix.Get(fxSession); fm.Title != "Loop check" || fm.LastAnswer != "Checked." || fm.Model != "claude-synthetic-1" {
		t.Fatalf("after growth: %+v", fm)
	}
	// a teammate session: agent name and its first harness message
	mate := "22222222-2222-4333-8444-555555555555"
	dir := filepath.Dir(path)
	os.WriteFile(filepath.Join(dir, mate+".jsonl"), []byte(metaLine("agent-name", map[string]any{"agentName": "tests-vm"})+
		promptLine(2_000, "t1", "<teammate-message teammate_id=\"lead\">\nPort the tests.\n</teammate-message>", nil)), 0600)
	// an empty file and a stray non-session file are not sessions
	os.WriteFile(filepath.Join(dir, "33333333-2222-4333-8444-555555555555.jsonl"), nil, 0600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0600)
	ix.Scan()
	if fm, ok := ix.Get(mate); !ok || fm.Title != "tests-vm · Port the tests." {
		t.Fatalf("teammate title: %+v ok=%v", fm, ok)
	}
	if roots := ix.Roots(); len(roots) != 2 {
		t.Fatalf("roots: %+v", roots)
	}
	// homes
	bare, missing := t.TempDir(), filepath.Join(t.TempDir(), "gone")
	ix2 := NewIndex(h, bare, missing)
	ix2.Scan()
	st := ix2.HomeStatuses()
	if len(st) != 3 || st[0].Status != source.HomeOK || st[0].Sessions != 2 || st[1].Status != source.HomeNoSessionsDir || st[2].Status != source.HomeMissing {
		t.Fatalf("home statuses: %+v", st)
	}
	if dirs := ix2.Dirs(); len(dirs) != 3 || dirs[0] != filepath.Join(h, "projects") {
		t.Fatalf("dirs: %v", dirs)
	}
	if ix2.Name() != source.Claude {
		t.Fatal("name")
	}
	ix2.SetHomes([]string{bare})
	if _, ok := ix2.Get(fxSession); ok {
		t.Fatal("a dropped home's sessions must leave at once")
	}
}
