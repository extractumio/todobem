package codex

import (
	"strings"
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// TestExecEnvelopeCommands: the commands of an `exec` envelope are recovered from literal forms
// and flat arrays only; a nested pair list is a named unknown; a template with a hole is never a
// provisional key; a running cell is extended by its `wait` and closed by "Script completed".
func TestExecEnvelopeCommands(t *testing.T) {
	cases := map[string][]string{
		`const r = await tools.exec_command({cmd: "go test ./...", workdir: "/w"}); text(r.output);`:                                                          {"go test ./..."},
		`const r = await tools.exec_command({"cmd":"venv/bin/python -m pytest tests -q","workdir":"/w","yield_time_ms":30000});`:                              {"venv/bin/python -m pytest tests -q"},
		`let r = await tools.exec_command({"cmd": "ls -la"}); text(r.output);`:                                                                                {"ls -la"},
		`const r = await tools.exec_command({cmd:'swift build'});`:                                                                                            {"swift build"},
		"const base='/tmp/x'; const r = await tools.exec_command({cmd:`scp '${base}/a.png' buildhost:/tmp/`});":                                               {"scp '${base}/a.png' buildhost:/tmp/"},
		"const cmd = String.raw`set -e\ngit status --short`; const r = await tools.exec_command({cmd, workdir: '/w'});":                                       {"set -e\ngit status --short"},
		`const cmds = ["gzip -t a.gz", "unzip -t b.zip"]; const rs = await Promise.all(cmds.map(cmd => tools.exec_command({cmd})));`:                          {"gzip -t a.gz", "unzip -t b.zip"},
		`const cmds = [["status", "git status --short"], ["tests", "pytest -q"]]; for (const [n, cmd] of cmds) await tools.exec_command({cmd});`:              nil,
		`const ids = ["IQU-1", "IQU-2"]; const rs = await Promise.all(ids.map(id => tools.exec_command({cmd: ` + "`linear issue view ${id} --json`" + `})));`: {"linear issue view ${id} --json"},
		`const items = [1, 2]; text(items.length);`:                                                                                                           nil,
	}
	for input, want := range cases {
		got := execCommands(input)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("execCommands(%q)\n  got  %q\n  want %q", input, got, want)
		}
	}

	p := testParser(t, "thread-id", "")
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "t1"}),
		// a flat array envelope whose script keeps running in a cell, waited for, then completed
		rolloutLine(t, 1100, "response_item", "custom_tool_call", map[string]any{"name": "exec", "call_id": "e1", "input": `const cmds = ["gzip -t a.gz", "go test ./..."]; const rs = await Promise.all(cmds.map(cmd => tools.exec_command({cmd, workdir: "/w"})));`}),
		rolloutLine(t, 1200, "response_item", "custom_tool_call_output", map[string]any{"call_id": "e1", "output": "Script running with cell ID 7\nWall time 10.0 seconds\nOutput:\n"}),
		rolloutLine(t, 1300, "response_item", "function_call", map[string]any{"name": "wait", "call_id": "w1", "arguments": `{"cell_id":"7","yield_time_ms":30000}`}),
		rolloutLine(t, 1900, "response_item", "function_call_output", map[string]any{"call_id": "w1", "output": "Script completed\nWall time 12.7 seconds\nOutput:\n"}),
		// a nested pair list: no literal command
		rolloutLine(t, 2000, "response_item", "custom_tool_call", map[string]any{"name": "exec", "call_id": "e2", "input": `const jobs = [["a", "pytest -q"], ["b", "npm run lint"]]; for (const [n, cmd] of jobs) await tools.exec_command({cmd});`}),
		rolloutLine(t, 2100, "response_item", "custom_tool_call_output", map[string]any{"call_id": "e2", "output": "Script completed\nWall time 1.0 seconds\nOutput:\n"}),
		// a template with a hole: classified, but never a provisional key
		rolloutLine(t, 2200, "response_item", "custom_tool_call", map[string]any{"name": "exec", "call_id": "e3", "input": "const id='X'; const r = await tools.exec_command({cmd:`linear issue view ${id} --json`});"}),
		rolloutLine(t, 2300, "response_item", "custom_tool_call_output", map[string]any{"call_id": "e3", "output": "Script completed\nWall time 0.3 seconds\nOutput:\n"}),
		rolloutLine(t, 3000, "event_msg", "task_complete", map[string]any{"turn_id": "t1", "last_agent_message": "done"}),
	)
	ops := map[string]*model.Operation{}
	for _, o := range p.lane.Ops {
		ops[o.ID] = o
	}
	e1 := ops["e1"]
	if e1 == nil || e1.Phase != classify.Test || e1.Kind != "go test" || e1.Parallel != 2 {
		t.Fatalf("flat array envelope: %+v", e1)
	}
	if e1.Status != "completed" || e1.End != 1900 || e1.Open {
		t.Errorf("the cell wait must extend and close the envelope op: status=%s end=%d open=%v", e1.Status, e1.End, e1.Open)
	}
	if w := ops["w1"]; w != nil {
		t.Errorf("a wait on the cell is not an op of its own: %+v", w)
	}
	e2 := ops["e2"]
	if e2 == nil || e2.Phase != classify.Unknown || e2.Kind != "exec-script" || !strings.HasPrefix(e2.Title, "exec script (no command literal)") || e2.Detail == "" {
		t.Errorf("nested pair envelope: %+v", e2)
	}
	if e3 := ops["e3"]; e3 == nil || e3.Phase != classify.Code || e3.Kind != "linear query" {
		t.Errorf("template envelope: %+v", e3)
	}
	if len(p.provisional) != 0 {
		t.Errorf("provisional keys left: %v", p.provisional)
	}
}

// TestWebRunAndConnectors: the web tool's `run` is a lookup with one op per call id (the
// WebSearch item the harness also writes is not a second op); connector calls in an `mcp__*`
// namespace are mcp reads except the pull-request writes and reviews.
func TestWebRunAndConnectors(t *testing.T) {
	p := testParser(t, "thread-id", "")
	appendRollout(t, p,
		rolloutLine(t, 1000, "event_msg", "task_started", map[string]any{"turn_id": "t1"}),
		rolloutLine(t, 1100, "response_item", "function_call", map[string]any{"name": "run", "namespace": "web", "call_id": "r1", "arguments": `{"search_query":[{"q":"site:example.invalid webhooks"}]}`}),
		rolloutLine(t, 1150, "event_msg", "item_completed", map[string]any{"turn_id": "t1", "item": map[string]any{"type": "WebSearch", "id": "r1", "query": "webhooks"}}),
		rolloutLine(t, 1200, "response_item", "function_call_output", map[string]any{"call_id": "r1", "output": "results"}),
		rolloutLine(t, 1300, "response_item", "function_call", map[string]any{"name": "_get_pr_info", "namespace": "mcp__codex_apps__github", "call_id": "g1", "arguments": `{"repository_full_name":"org/repo","pr_number":1}`}),
		rolloutLine(t, 1400, "response_item", "function_call_output", map[string]any{"call_id": "g1", "output": "{}"}),
		rolloutLine(t, 1500, "response_item", "function_call", map[string]any{"name": "_create_pull_request", "namespace": "mcp__codex_apps__github", "call_id": "g2", "arguments": `{"title":"x"}`}),
		rolloutLine(t, 1600, "response_item", "function_call_output", map[string]any{"call_id": "g2", "output": "{}"}),
		rolloutLine(t, 1700, "response_item", "function_call", map[string]any{"name": "_add_pull_request_review_comment", "namespace": "mcp__codex_apps__github", "call_id": "g3", "arguments": `{"body":"nit"}`}),
		rolloutLine(t, 1800, "response_item", "function_call_output", map[string]any{"call_id": "g3", "output": "{}"}),
		rolloutLine(t, 1900, "response_item", "function_call", map[string]any{"name": "session_show_defaults", "namespace": "mcp__xcodebuildmcp", "call_id": "x1", "arguments": `{}`}),
		rolloutLine(t, 1950, "response_item", "function_call_output", map[string]any{"call_id": "x1", "output": "{}"}),
		rolloutLine(t, 3000, "event_msg", "task_complete", map[string]any{"turn_id": "t1", "last_agent_message": "done"}),
	)
	ids := map[string]int{}
	ops := map[string]*model.Operation{}
	for _, o := range p.lane.Ops {
		ids[o.ID]++
		ops[o.ID] = o
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("op id %q appears %d times", id, n)
		}
	}
	if r := ops["r1"]; r == nil || r.Phase != classify.Code || r.Kind != "web_search" || r.Title != "web search_query" || r.End != 1200 {
		t.Errorf("web run: %+v", r)
	}
	if g := ops["g1"]; g == nil || g.Phase != classify.Code || g.Kind != "mcp" || g.Title != "mcp codex_apps__github.get_pr_info" {
		t.Errorf("connector read: %+v", g)
	}
	if g := ops["g2"]; g == nil || g.Phase != classify.Release || g.Kind != "pr" {
		t.Errorf("connector PR write: %+v", g)
	}
	if g := ops["g3"]; g == nil || g.Phase != classify.Release || g.Kind != "pr review" || g.Lifecycle != classify.LcReview {
		t.Errorf("connector PR review: %+v", g)
	}
	if x := ops["x1"]; x != nil {
		t.Errorf("a trivial connector lookup stays no op: %+v", x)
	}
}
