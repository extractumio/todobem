//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Synthetic sessions — made-up ids, paths and text — one per source, each a closed turn with a
// tool call, so a parse exercises the classifier and the partition.
const (
	codexID  = "0199aaaa-e2e0-7000-8000-000000000001"
	claudeID = "0199aaaa-e2e0-7000-8000-000000000002"
	fakeCWD  = "/synthetic/e2e-project"
)

var t0 = time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

func at(sec int) string { return t0.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano) }

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// writeSessions writes one Codex rollout and one Claude Code session under root and returns the
// two homes.
func writeSessions(t *testing.T, root string) (codexHome, claudeHome string) {
	t.Helper()
	codexHome, claudeHome = filepath.Join(root, "codex"), filepath.Join(root, "claude")

	codex := func(sec int, kind string, payload map[string]any) string {
		return jsonLine(t, map[string]any{"timestamp": at(sec), "type": kind, "payload": payload})
	}
	rollout := codex(0, "session_meta", map[string]any{"id": codexID, "timestamp": at(0), "cwd": fakeCWD, "cli_version": "0.150.0"}) +
		codex(1, "event_msg", map[string]any{"type": "task_started", "turn_id": "turn-1"}) +
		codex(1, "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Run the tests."}}}) +
		codex(5, "response_item", map[string]any{"type": "function_call", "name": "shell", "call_id": "call-1", "arguments": `{"command":["go","test","./..."]}`}) +
		codex(35, "response_item", map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "ok"}) +
		codex(40, "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn-1", "last_agent_message": "The tests pass."})
	writeText(t, filepath.Join(codexHome, "sessions", "2026", "01", "02", "rollout-2026-01-02T10-00-00-"+codexID+".jsonl"), rollout)

	// Claude Code writes parentUuid first and the type of a metadata line first; the index keys
	// on those prefixes, so the fields go in that order, not json.Marshal's sorted one.
	msg := func(kind string, sec int, uuid string, fields map[string]any) string {
		m := map[string]any{"type": kind, "uuid": uuid, "timestamp": at(sec),
			"userType": "external", "entrypoint": "cli", "cwd": fakeCWD, "sessionId": claudeID, "version": "2.1.270", "gitBranch": "main"}
		for k, v := range fields {
			m[k] = v
		}
		rest := jsonLine(t, m)
		return `{"parentUuid":null,"isSidechain":false,` + rest[1:]
	}
	assistant := func(sec int, uuid, id, stop string, block map[string]any) string {
		return msg("assistant", sec, uuid, map[string]any{"requestId": "req-" + id, "message": map[string]any{
			"model": "claude-synthetic-1", "id": id, "type": "message", "role": "assistant", "content": []any{block}, "stop_reason": stop,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20}}})
	}
	session := `{"type":"ai-title","aiTitle":"Build the project","sessionId":"` + claudeID + `"}` + "\n" +
		msg("user", 0, "u1", map[string]any{"message": map[string]any{"role": "user", "content": "Build the project."}}) +
		assistant(4, "a1", "m1", "tool_use", map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "go build ./..."}}) +
		msg("user", 24, "u2", map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": "done"}}}}) +
		assistant(30, "a2", "m2", "end_turn", map[string]any{"type": "text", "text": "Built."})
	writeText(t, filepath.Join(claudeHome, "projects", "-synthetic-e2e-project", claudeID+".jsonl"), session)
	return codexHome, claudeHome
}

func writeText(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
