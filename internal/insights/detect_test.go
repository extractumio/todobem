package insights

import (
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

const minute = 60e3

func op(id, lane string, phase model.Phase, kind string, s, e int64, status string) *model.Operation {
	return &model.Operation{ID: id, Lane: lane, Turn: "", Phase: phase, Kind: kind, Start: s, End: e, Status: status}
}

func usage(in, cached, out int64) *model.TokenUsage {
	return &model.TokenUsage{Input: in, Cached: cached, Output: out, Total: in + out}
}

// session builds a derived session from lanes (Derive assigns segments, groups, totals).
func session(t *testing.T, lanes ...*model.Lane) *model.Session {
	t.Helper()
	s := &model.Session{ID: "s1", Source: "codex", CWD: "/proj", Lanes: lanes}
	model.Derive(s, lanes[0].Ended)
	return s
}

func TestSerialDelegationMeasuresSoloWaitOnly(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed"}}
	wait := op("w1", "R", classify.WaitWorker, "agent", 1*minute, 9*minute, "completed")
	wait.Turn = "t1"
	root.Ops = []*model.Operation{wait}
	a := &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 9 * minute}
	a.Turns = []*model.Turn{{ID: "a1", Start: 1 * minute, End: 5 * minute, Status: "completed"}}
	b := &model.Lane{ID: "B", Path: "/root/b", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 9 * minute}
	b.Turns = []*model.Turn{{ID: "b1", Start: 4 * minute, End: 9 * minute, Status: "completed"}}
	f := Extract(session(t, root, a, b))
	r := detectSerialDelegation(&f)
	// wait [1,9): A alone [1,4) = 3 min, both [4,5) = 1 min (excluded), B alone [5,9) = 4 min
	if len(r.Findings) != 1 || r.Findings[0].TimeMs != 7*minute || r.Findings[0].Key != "several, one at a time" {
		t.Fatalf("findings %+v", r.Findings)
	}
	// negative: both sub-agents inside a turn for the whole wait → nothing solo
	a.Turns[0].Start, a.Turns[0].End = 1*minute, 9*minute
	b.Turns[0].Start, b.Turns[0].End = 1*minute, 9*minute
	f = Extract(session(t, root, a, b))
	if r := detectSerialDelegation(&f); len(r.Findings) != 0 {
		t.Fatalf("expected no solo wait, got %+v", r.Findings)
	}
	// a single sub-agent: nothing could have run in parallel, the rule does not apply
	f = Extract(session(t, root, a))
	if r := detectSerialDelegation(&f); !r.NotApplicable || len(r.Findings) != 0 {
		t.Fatalf("one sub-agent must be not applicable, got %+v", r)
	}
}

func TestRetryLoopsNeedAFailedAttemptAndCarryWindowTokens(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Tokens: usage(1000, 800, 100)}}
	first := op("c1", "R", classify.Test, "go test", 1*minute, 2*minute, "failed")
	first.Identity = "/proj\ngo test ./..."
	fix := op("e1", "R", classify.Code, "edit", 3*minute, 3*minute+1000, "completed")
	retry := op("c2", "R", classify.Test, "go test", 5*minute, 6*minute, "completed")
	retry.Identity = first.Identity
	for _, o := range []*model.Operation{first, fix, retry} {
		o.Turn = "t1"
	}
	root.Ops = []*model.Operation{first, fix, retry}
	f := Extract(session(t, root))
	r := detectRetryLoops(&f)
	if len(r.Findings) != 1 {
		t.Fatalf("findings %+v", r.Findings)
	}
	fd := r.Findings[0]
	if fd.TimeMs != 1*minute+1000 || fd.Key != "test go test" || fd.Tokens == nil {
		t.Fatalf("finding %+v", fd)
	}
	// the window [2,5) covers 3 of the turn's 10 minutes: 30 % of its tokens
	if fd.Tokens.Input != 300 || fd.Tokens.Cached != 240 {
		t.Fatalf("window tokens %+v", fd.Tokens)
	}
	// negative: a group of two passes is a rerun, not a retry loop
	first.Status, first.Exit = "completed", nil
	f = Extract(session(t, root))
	if r := detectRetryLoops(&f); len(r.Findings) != 0 {
		t.Fatalf("expected no finding, got %+v", r.Findings)
	}
}

func TestCompactionsReportContextAndReread(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed"}}
	c := op("k1", "R", classify.Compaction, "compaction", 2*minute, 5*minute, "completed")
	c.Turn, c.Context, c.Tokens = "t1", 210000, usage(26000, 13000, 500)
	root.Ops = []*model.Operation{c}
	f := Extract(session(t, root))
	r := detectCompactions(&f)
	if len(r.Findings) != 1 || r.Findings[0].TimeMs != 3*minute || r.Findings[0].Key != "main thread" || r.Findings[0].Tokens.Input != 26000 || r.Findings[0].Note != "context before: 210 k tokens" {
		t.Fatalf("findings %+v", r.Findings)
	}
	root.Ops = nil
	f = Extract(session(t, root))
	if r := detectCompactions(&f); len(r.Findings) != 0 {
		t.Fatalf("expected none, got %+v", r.Findings)
	}
}

func TestCacheAfterBreakBucketsGapsAndCountsNoData(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 100 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Tokens: usage(100, 90, 10), First: usage(100, 90, 10)},
		{ID: "t2", Start: 30 * minute, End: 40 * minute, Status: "completed", Trigger: "user", Tokens: usage(200, 100, 10), First: usage(200, 100, 10)},     // 20 min gap
		{ID: "t3", Start: 42 * minute, End: 50 * minute, Status: "completed", Trigger: "user", Tokens: usage(210, 205, 10)},                                 // 2 min gap, no first call recorded
		{ID: "t4", Start: 55 * minute, End: 60 * minute, Status: "completed", Trigger: "system", Tokens: usage(1, 1, 1), First: usage(1, 1, 1)},             // harness-triggered: not a reply
		{ID: "t5", Start: 60*minute + 400, End: 70 * minute, Status: "completed", Trigger: "user", Tokens: usage(300, 290, 10), First: usage(300, 290, 10)}, // 0.4 s gap: a queued message, not a break
	}
	f := Extract(session(t, root))
	r := detectCacheAfterBreak(&f)
	if !r.Measurable || len(r.Findings) != 1 || r.NoData != 1 {
		t.Fatalf("result %+v", r)
	}
	if r := detectReplyLatency(&f); len(r.Findings) != 2 || r.Findings[0].TimeMs != 20*minute || r.Findings[1].TimeMs != 2*minute {
		t.Fatalf("D2 must count the 20 and 2 minute gaps only: %+v", r.Findings)
	}
	fd := r.Findings[0]
	if fd.Key != "15-60 min" || fd.TimeMs != 0 || fd.Tokens.Input != 200 || fd.Note != "break of 20m; first call after it: 0 k input, 0 k not cached" {
		t.Fatalf("finding %+v", fd)
	}
	// not measurable: no usage records at all
	for _, tn := range root.Turns {
		tn.Tokens, tn.First = nil, nil
	}
	f = Extract(session(t, root))
	if r := detectCacheAfterBreak(&f); r.Measurable || r.Reason == "" {
		t.Fatalf("expected not measurable, got %+v", r)
	}
}

func TestExtractLaneKindsGapsAndShapes(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 30 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed"}, {ID: "t2", Start: 20 * minute, End: 30 * minute, Status: "completed", Trigger: "user"}}
	root.Markers = []model.Marker{{T: 9 * minute, Kind: "question", Lane: "R", Turn: "t1", Text: "which one?"}}
	ro := &model.Lane{ID: "A", Path: "/root/reader", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 5 * minute}
	ro.Turns = []*model.Turn{{ID: "a1", Start: 1 * minute, End: 5 * minute, Status: "completed"}}
	ro.Ops = []*model.Operation{op("r1", "A", classify.Code, "read", 2*minute, 2*minute+500, "completed"), op("r2", "A", classify.Code, "search", 3*minute, 3*minute+500, "failed")}
	wk := &model.Lane{ID: "B", Path: "/root/worker", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 5 * minute}
	wk.Turns = []*model.Turn{{ID: "b1", Start: 1 * minute, End: 5 * minute, Status: "completed"}}
	wk.Ops = []*model.Operation{op("e1", "B", classify.Code, "edit", 2*minute, 2*minute+1, "completed")}
	f := Extract(session(t, root, ro, wk))
	if f.Agents[0].Kind != LaneRoot || f.Agents[1].Kind != LaneReadOnly || f.Agents[2].Kind != LaneWorker {
		t.Fatalf("lane kinds %+v", f.Agents)
	}
	if len(f.Gaps) != 1 || !f.Gaps[0].AfterQuestion || f.Gaps[0].PrevTurn != "t1" || f.Gaps[0].NextTurn != "t2" || f.Gaps[0].NextTrigger != "user" {
		t.Fatalf("gaps %+v", f.Gaps)
	}
	if got := Shape(classify.Infra, "/p\nFOO=1 docker run --rm img"); got != "infra docker run" {
		t.Fatalf("shape %q", got)
	}
	if got := Shape(classify.Test, "/p\n./dev ci-build > /tmp/x.log"); got != "test ./dev ci-build" {
		t.Fatalf("shape %q", got)
	}
	if got := Shape(classify.Release, "/p\ngit push --force-with-lease origin main"); got != "release git push" {
		t.Fatalf("shape %q", got)
	}
	// make's options go and its target names the shape: `make -j8 test` and `make test` are one
	if got := Shape(classify.Test, "/p\nmake -j8 test"); got != "test make test" {
		t.Fatalf("shape %q", got)
	}
	if got := Shape(classify.Build, "/p\nmake -C src -j8 all"); got != "build make all" {
		t.Fatalf("shape %q", got)
	}
	// the deciding segment names the shape, not the first word of the text
	for _, c := range []struct {
		phase    model.Phase
		identity string
		want     string
	}{
		{classify.Test, "/p\nset -euo pipefail; skills/run-remote-tests.sh candidate-local > out.log 2>&1", "test skills/run-remote-tests.sh candidate-local"},
		{classify.Release, "/p\nexport GIT_SSH_COMMAND='ssh -o ServerAliveInterval=30'\ngit push origin main", "release git push"},
		{classify.Test, "/p\nwhile ! skills/run-remote-tests.sh app; do sleep 5; done", "test skills/run-remote-tests.sh app"},
		{classify.Test, "/p\nbash -o pipefail -c 'skills/run-remote-tests.sh app 2>&1 | rg x'", "test skills/run-remote-tests.sh app"},
		{classify.Test, "/p\ncat > /tmp/t.sh <<'SH'\ngo test ./...\nSH\nbash /tmp/t.sh", "test bash /tmp/t.sh"},
		// the classifier disagrees with the op's phase (a rule the test process lacks): the text's first words
		{classify.Test, "/p\nexport PATH=\"$HOME/bin:$PATH\"; rojo test", "test rojo test"},
		// a bare assignment segment never names the shape
		{classify.Release, "/p\nR=/tmp/ship.log && ./release-app macos-ship > \"$R\"", "release ./release-app macos-ship"},
	} {
		if got := Shape(c.phase, c.identity); got != c.want {
			t.Errorf("Shape(%q) = %q, want %q", c.identity, got, c.want)
		}
	}
}

// TestSleepsCountTheAgentsOwnWaits: a `sleep`, a poll loop and a `tail -f` on the root are the
// card's findings, keyed by kind with their measured time; the harness's wait for a sub-agent
// and a CI watch are not, and a session with only those has no finding.
func TestSleepsCountTheAgentsOwnWaits(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 20 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 20 * minute, Status: "completed"}}
	sleep := op("s1", "R", classify.WaitWorker, "sleep", 1*minute, 1*minute+30*1000, "completed")
	loop := op("l1", "R", classify.WaitWorker, "poll-loop", 3*minute, 8*minute, "completed")
	tail := op("f1", "R", classify.WaitWorker, "tail-f", 9*minute, 10*minute, "completed")
	agent := op("w1", "R", classify.WaitWorker, "agent", 11*minute, 15*minute, "completed")
	ci := op("c1", "R", classify.WaitWorker, "ci", 16*minute, 18*minute, "completed")
	for _, o := range []*model.Operation{sleep, loop, tail, agent, ci} {
		o.Turn = "t1"
	}
	root.Ops = []*model.Operation{sleep, loop, tail, agent, ci}
	f := Extract(session(t, root))
	r := detectSleeps(&f)
	if !r.Measurable || len(r.Findings) != 3 {
		t.Fatalf("findings %+v", r.Findings)
	}
	want := map[string]int64{"sleep": 30 * 1000, "poll loop": 5 * minute, "tail -f": 1 * minute}
	for _, fd := range r.Findings {
		if want[fd.Key] != fd.TimeMs {
			t.Fatalf("%s: %d ms, want %d", fd.Key, fd.TimeMs, want[fd.Key])
		}
		delete(want, fd.Key)
	}
	if len(want) != 0 || r.Stats["sleep"] != 1 || r.Stats["poll-loop"] != 1 || r.Stats["tail-f"] != 1 {
		t.Fatalf("keys left %v, stats %v", want, r.Stats)
	}
	if r.Findings[0].Note != "sleep · 30 s" || r.Findings[1].Note != "poll loop · 5 min 0 s" {
		t.Fatalf("notes %q %q", r.Findings[0].Note, r.Findings[1].Note)
	}
	// negative: only the harness's waits
	root.Ops = []*model.Operation{agent, ci}
	f = Extract(session(t, root))
	if r := detectSleeps(&f); !r.Measurable || len(r.Findings) != 0 {
		t.Fatalf("harness waits counted: %+v", r.Findings)
	}
}
