package model

import (
	"testing"

	"github.com/extractumio/todobem/internal/classify"
)

func lifecycleSum(l *Lane) int64 {
	var n int64
	for _, v := range l.ByLifecycle {
		n += v
	}
	return n
}

func TestLifecyclePartitionBalances(t *testing.T) {
	s := fixture()
	Derive(s, 2000)
	l := s.Lanes[0]
	if lifecycleSum(l) != l.Ended-l.Started {
		t.Fatalf("by_lifecycle sum %d != elapsed %d", lifecycleSum(l), l.Ended-l.Started)
	}
	var sum int64
	for _, v := range s.Totals.ByLifecycle {
		sum += v
	}
	if sum != s.Totals.ElapsedMs {
		t.Fatalf("totals by_lifecycle sum %d != elapsed %d", sum, s.Totals.ElapsedMs)
	}
	for _, sg := range l.Segments {
		if sg.Lifecycle == "" {
			t.Fatalf("segment without lifecycle: %+v", sg)
		}
	}
	// t1: think before r1 (code) is implementation; think before c1 (test) is testing; the
	// trailing message m1 (520-590) and the gap to the turn end have no tool call after them
	// and take the stage of the last one before them (c3, a test). t2 has no ops at all: llm.
	// Outside turns passes through.
	if l.ByLifecycle["wait_user"] != 400 {
		t.Fatalf("wait_user %d", l.ByLifecycle["wait_user"])
	}
	// implement: think [100,120) → r1, r1, c2 after c1 [300,400), think [400,410) → f1, f1
	if l.ByLifecycle[classify.LcImplement] != 20+10+100+10+10 {
		t.Fatalf("implement %d", l.ByLifecycle[classify.LcImplement])
	}
	// test: think [130,200) → c1, c1 exclusive [200,300), think [420,450) → c3, c3, then the
	// tail of the turn looks back to c3: [500,520) + m1 [520,590) + [590,600)
	if l.ByLifecycle[classify.LcTest] != 70+100+30+50+20+70+10 {
		t.Fatalf("test %d", l.ByLifecycle[classify.LcTest])
	}
	if l.ByLifecycle["llm"] != 100 { // t2 [800,900): a turn without a tool call
		t.Fatalf("llm %d", l.ByLifecycle["llm"])
	}
	for _, o := range l.Ops {
		if o.LifecycleRule == "" {
			t.Errorf("op %s has no lifecycle rule", o.ID)
		}
	}
}

func TestLifecycleTurnSignalsCoverWholeTurn(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{
		{ID: "t1", Start: 0, End: 300, Status: "completed", Skill: "code-review-cc"},
		{ID: "t2", Start: 300, End: 600, Status: "completed", Mode: "plan"}, // back-to-back: no gap
		{ID: "t3", Start: 700, End: 1000, Status: "completed"},
	}
	l.Markers = []Marker{{T: 710, Kind: "enteredreviewmode", Lane: "L", Turn: "t3"}}
	l.Ops = []*Operation{
		{ID: "a", Lane: "L", Turn: "t1", Phase: classify.Test, Kind: "k", Start: 100, End: 200, Status: "completed"},
		{ID: "w", Lane: "L", Turn: "t1", Phase: classify.WaitWorker, Kind: "sleep", Start: 200, End: 250, Status: "completed"},
		{ID: "b", Lane: "L", Turn: "t2", Phase: classify.Code, Kind: "k", Start: 400, End: 450, Status: "completed"},
		{ID: "c", Lane: "L", Turn: "t3", Phase: classify.Build, Kind: "k", Start: 800, End: 900, Status: "completed"},
	}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	if l.Turns[0].Lifecycle != classify.LcReview || l.Turns[1].Lifecycle != classify.LcPlan || l.Turns[2].Lifecycle != classify.LcReview {
		t.Fatalf("turn lifecycles %q %q %q", l.Turns[0].Lifecycle, l.Turns[1].Lifecycle, l.Turns[2].Lifecycle)
	}
	// everything inside a signalled turn takes the stage: tests, waits and model output alike;
	// the activity partition is untouched
	if l.ByLifecycle[classify.LcReview] != 300+300 || l.ByLifecycle[classify.LcPlan] != 300 || l.ByLifecycle["wait_user"] != 100 {
		t.Fatalf("by_lifecycle %v", l.ByLifecycle)
	}
	if l.ByPhase[classify.Test] != 100 || l.ByPhase[classify.WaitWorker] != 50 || l.ByPhase[classify.Build] != 100 {
		t.Fatalf("by_phase changed: %v", l.ByPhase)
	}
	for _, o := range l.Ops {
		want := classify.LcReview
		if o.Turn == "t2" {
			want = classify.LcPlan
		}
		if o.Lifecycle != want {
			t.Errorf("op %s lifecycle %q, want %q (%s)", o.ID, o.Lifecycle, want, o.LifecycleRule)
		}
	}
	if l.Ops[0].LifecycleRule != "skill code-review-cc" || l.Ops[2].LifecycleRule != "plan mode" || l.Ops[3].LifecycleRule != "review mode" {
		t.Errorf("rules: %q %q %q", l.Ops[0].LifecycleRule, l.Ops[2].LifecycleRule, l.Ops[3].LifecycleRule)
	}
	// a segment never straddles the t1/t2 boundary even though both sides are model output
	for _, sg := range l.Segments {
		if sg.Start < 300 && sg.End > 300 {
			t.Fatalf("segment straddles a turn boundary: %+v", sg)
		}
	}
	if lifecycleSum(l) != 1000 {
		t.Fatalf("partition %d", lifecycleSum(l))
	}
	if s.Totals.Reviews != 1 {
		t.Errorf("reviews %d (only skill invocations count)", s.Totals.Reviews)
	}
}

func TestLifecycleOpLevelPinsAndOperateGuard(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{{ID: "t1", Start: 0, End: 1000, Status: "completed"}}
	l.Ops = []*Operation{
		{ID: "logs1", Lane: "L", Turn: "t1", Phase: classify.Code, Kind: "docker logs", Lifecycle: classify.LcOperate, Start: 100, End: 150, Status: "completed"},
		{ID: "plan", Lane: "L", Turn: "t1", Phase: classify.LLM, Kind: "plan", Lifecycle: classify.LcPlan, LifecycleRule: "plan created (update_plan)", Start: 300, End: 300, Status: "completed"},
		{ID: "push", Lane: "L", Turn: "t1", Phase: classify.Release, Kind: "git push", Start: 400, End: 420, Status: "completed"},
		{ID: "cmt", Lane: "L", Turn: "t1", Phase: classify.Release, Kind: "pr comment", Lifecycle: classify.LcReview, Start: 500, End: 510, Status: "completed"},
		{ID: "logs2", Lane: "L", Turn: "t1", Phase: classify.Code, Kind: "docker logs", Lifecycle: classify.LcOperate, Start: 600, End: 650, Status: "completed"},
		{ID: "cmp", Lane: "L", Turn: "t1", Phase: classify.Compaction, Kind: "compaction", Start: 700, End: 720, Status: "completed"},
		{ID: "edit", Lane: "L", Turn: "t1", Phase: classify.Code, Kind: "edit", Start: 800, End: 800, Status: "completed"},
	}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	got := map[string]Lifecycle{}
	for _, o := range l.Ops {
		got[o.ID] = o.Lifecycle
	}
	want := map[string]Lifecycle{"logs1": classify.LcImplement, "plan": classify.LcPlan, "push": classify.LcRelease, "cmt": classify.LcReview, "logs2": classify.LcOperate, "cmp": "compaction", "edit": classify.LcImplement}
	for id, lc := range want {
		if got[id] != lc {
			t.Errorf("%s: %q, want %q", id, got[id], lc)
		}
	}
	if l.Ops[0].LifecycleRule != "operations before this lane's first release → implementation" || l.Ops[4].LifecycleRule != "kind docker logs" || l.Ops[2].LifecycleRule != "phase release" {
		t.Errorf("rules: %q / %q / %q", l.Ops[0].LifecycleRule, l.Ops[4].LifecycleRule, l.Ops[2].LifecycleRule)
	}
	// Model output before the plan anchor (instantaneous ops get a 1 ms anchor) is planning;
	// before the push it is release; before the comment it is review; the compaction is
	// transparent, so the think before it reaches the edit; the trailing think of the turn
	// looks back to the edit. Nothing is left as llm.
	by := l.ByLifecycle
	wantMs := map[Lifecycle]int64{
		classify.LcImplement: 100 + 50 + 50 + 80 + 1 + 199, // think→logs1 (demoted), logs1, think across the compaction→edit, think→edit, edit, tail←edit
		classify.LcPlan:      150 + 1,
		classify.LcRelease:   99 + 20,
		classify.LcReview:    80 + 10,
		classify.LcOperate:   90 + 50,
		"compaction":         20,
	}
	for lc, v := range wantMs {
		if by[lc] != v {
			t.Errorf("%s = %d, want %d", lc, by[lc], v)
		}
	}
	if lifecycleSum(l) != 1000 || len(by) != len(wantMs) {
		t.Fatalf("by_lifecycle %v", by)
	}
}

func TestLifecycleLaneRoleFromOverlay(t *testing.T) {
	if err := classify.ApplyUserConfig(classify.UserConfig{Lifecycle: classify.LifecycleMatcherConfig{Roles: map[classify.Lifecycle][]string{classify.LcReview: {"^pragmatic$"}}}}, "test"); err != nil {
		t.Fatal(err)
	}
	root := &Lane{ID: "R", Path: "/root", Started: 0, Ended: 1000, Turns: []*Turn{{ID: "t", Start: 0, End: 1000, Status: "completed"}}}
	root.Ops = []*Operation{{ID: "w", Lane: "R", Turn: "t", Phase: classify.WaitWorker, Kind: "agent", Start: 100, End: 900, Status: "completed"}}
	sub := &Lane{ID: "S", Path: "/root/check", Parent: "R", Role: "pragmatic", Depth: 1, Started: 100, Ended: 900, Turns: []*Turn{{ID: "u", Start: 100, End: 900, Status: "completed"}}}
	sub.Ops = []*Operation{{ID: "g", Lane: "S", Turn: "u", Phase: classify.Code, Kind: "read", Start: 200, End: 300, Status: "completed"}}
	s := &Session{ID: "s", Lanes: []*Lane{root, sub}}
	Derive(s, 2000)
	if sub.Lifecycle != classify.LcReview || sub.ByLifecycle[classify.LcReview] != 800 || sub.Ops[0].LifecycleRule != "agent role pragmatic" {
		t.Fatalf("sub-agent lane: lc=%q by=%v rule=%q", sub.Lifecycle, sub.ByLifecycle, sub.Ops[0].LifecycleRule)
	}
	// no cross-lane inference: the root's wait for that agent stays a wait
	if root.ByLifecycle["wait_worker"] != 800 || root.ByLifecycle[classify.LcReview] != 0 {
		t.Fatalf("root by_lifecycle %v", root.ByLifecycle)
	}
}

// Rule 2 of assignLifecycle: a sub-agent turn with no signal of its own is the parent turn's
// tool call and inherits that turn's stage — linked by a recorded signal (root_turn_id, then the
// spawn / message marker, then an open worker-wait op window), never bare time overlap. The
// origin lane is named, the child's own signal beats the inherited one, and the chain reaches a
// grandchild through the parent's own spawn marker.
func TestLifecycleSubAgentTurnInheritsParentTurnStage(t *testing.T) {
	root := &Lane{ID: "R", Path: "/root", Started: 0, Ended: 2000, Turns: []*Turn{
		{ID: "t1", Start: 0, End: 1000, Status: "completed", Skill: "code-review-cc"},
		{ID: "t2", Start: 1100, End: 2000, Status: "completed", Mode: "plan"},
	}}
	root.Ops = []*Operation{
		{ID: "w1", Lane: "R", Turn: "t1", Phase: classify.WaitWorker, Kind: "agent", Start: 100, End: 900, Status: "completed"},
		{ID: "w2", Lane: "R", Turn: "t2", Phase: classify.WaitWorker, Kind: "agent", Start: 1200, End: 1800, Status: "completed"},
	}
	root.Markers = []Marker{
		{T: 100, Kind: "agent_started", Lane: "R", Turn: "t1", Ref: "S"},
		{T: 1150, Kind: "agent_interacted", Lane: "R", Turn: "t2", Ref: "S"}, // the agent re-used in a later parent turn
	}
	sub := &Lane{ID: "S", Path: "/root/find_a", Parent: "R", Depth: 1, Started: 100, Ended: 1800, Turns: []*Turn{
		{ID: "u1", Start: 100, End: 900, Status: "completed", RootTurn: "t1"}, // root_turn_id link (Codex >= 0.153)
		{ID: "u2", Start: 1200, End: 1500, Status: "completed"},               // linked by the agent_interacted marker → t2
		{ID: "u3", Start: 1500, End: 1800, Status: "completed", Mode: "plan"}, // own signal
	}}
	sub.Ops = []*Operation{
		{ID: "g1", Lane: "S", Turn: "u1", Phase: classify.Code, Kind: "read", Start: 200, End: 300, Status: "completed"},
		{ID: "g2", Lane: "S", Turn: "u2", Phase: classify.Code, Kind: "read", Start: 1300, End: 1400, Status: "completed"},
		{ID: "g3", Lane: "S", Turn: "u3", Phase: classify.Code, Kind: "read", Start: 1600, End: 1700, Status: "completed"},
	}
	sub.Markers = []Marker{{T: 1250, Kind: "agent_started", Lane: "S", Turn: "u2", Ref: "G"}}
	grand := &Lane{ID: "G", Path: "/root/find_a/second_read", Parent: "S", Depth: 2, Started: 1250, Ended: 1500, Turns: []*Turn{{ID: "v", Start: 1250, End: 1500, Status: "completed"}}}
	grand.Ops = []*Operation{{ID: "h", Lane: "G", Turn: "v", Phase: classify.Test, Kind: "go test", Start: 1300, End: 1400, Status: "completed"}}
	s := &Session{ID: "s", Lanes: []*Lane{root, sub, grand}}
	Derive(s, 2000)
	if sub.Turns[0].Lifecycle != classify.LcReview || sub.Turns[0].LifecycleRule != "inherited from /root (skill code-review-cc)" {
		t.Fatalf("u1 (root_turn_id link): %q %q", sub.Turns[0].Lifecycle, sub.Turns[0].LifecycleRule)
	}
	if sub.Ops[0].Lifecycle != classify.LcReview || sub.Ops[0].LifecycleRule != sub.Turns[0].LifecycleRule {
		t.Fatalf("g1: %q %q", sub.Ops[0].Lifecycle, sub.Ops[0].LifecycleRule)
	}
	if sub.ByLifecycle[classify.LcReview] != 800 { // the whole first turn, model output included
		t.Fatalf("sub by_lifecycle %v", sub.ByLifecycle)
	}
	if sub.Turns[1].Lifecycle != classify.LcPlan || sub.Turns[1].LifecycleRule != "inherited from /root (plan mode)" {
		t.Fatalf("u2 (marker link → plan turn): %q %q", sub.Turns[1].Lifecycle, sub.Turns[1].LifecycleRule)
	}
	if sub.Turns[2].Lifecycle != classify.LcPlan || sub.Turns[2].LifecycleRule != "plan mode" {
		t.Fatalf("u3 own signal beats inheritance: %q %q", sub.Turns[2].Lifecycle, sub.Turns[2].LifecycleRule)
	}
	// grandchild: linked through /root/find_a's spawn marker to u2 (plan, itself inherited);
	// the rule keeps naming the origin lane /root, not the chain
	if grand.Turns[0].Lifecycle != classify.LcPlan || grand.Turns[0].LifecycleRule != "inherited from /root (plan mode)" || grand.ByLifecycle[classify.LcPlan] != 250 {
		t.Fatalf("grandchild: %q %q %v", grand.Turns[0].Lifecycle, grand.Turns[0].LifecycleRule, grand.ByLifecycle)
	}
	// root's own stages come from its own signals; its waits are inside signalled turns
	if root.ByLifecycle[classify.LcReview] != 1000 || root.ByLifecycle[classify.LcPlan] != 900 || root.ByLifecycle["wait_user"] != 100 {
		t.Fatalf("root by_lifecycle %v", root.ByLifecycle)
	}
	for _, l := range s.Lanes {
		if lifecycleSum(l) != l.Ended-l.Started {
			t.Fatalf("%s partition %d != %d", l.Path, lifecycleSum(l), l.Ended-l.Started)
		}
	}
}

// A sub-agent turn with no root_turn_id and no spawn marker still inherits through an open
// worker-wait op window on the parent — but only for the lane's FIRST turn (its spawn). A bare
// wait window cannot say which of several concurrent agents it belongs to, so a re-used agent's
// later turn that falls inside a different parent turn's wait window inherits nothing rather than
// that turn's stage. With no link at all a lane inherits nothing.
func TestLifecycleInheritViaWaitOpWindow(t *testing.T) {
	root := &Lane{ID: "R", Path: "/root", Started: 0, Ended: 1000, Turns: []*Turn{
		{ID: "t1", Start: 0, End: 600, Status: "completed", Skill: "code-review-cc"},
		{ID: "t2", Start: 650, End: 1000, Status: "completed", Mode: "plan"},
	}}
	root.Ops = []*Operation{
		{ID: "w1", Lane: "R", Turn: "t1", Phase: classify.WaitWorker, Kind: "agent", Start: 100, End: 550, Status: "completed"},
		{ID: "w2", Lane: "R", Turn: "t2", Phase: classify.WaitWorker, Kind: "agent", Start: 700, End: 950, Status: "completed"},
	}
	// re-used with no marker: u1 (first turn) spawns during t1's wait → review; u2 opens inside
	// t2's plan wait, but it is not the first turn and nothing links it there → no inheritance
	linked := &Lane{ID: "S1", Path: "/root/a", Parent: "R", Depth: 1, Started: 200, Ended: 900, Turns: []*Turn{
		{ID: "u1", Start: 200, End: 500, Status: "completed"},
		{ID: "u2", Start: 750, End: 900, Status: "completed"},
	}}
	linked.Ops = []*Operation{
		{ID: "g1", Lane: "S1", Turn: "u1", Phase: classify.Code, Kind: "read", Start: 300, End: 400, Status: "completed"},
		{ID: "g2", Lane: "S1", Turn: "u2", Phase: classify.Code, Kind: "read", Start: 800, End: 850, Status: "completed"},
	}
	// runs while a parent turn is open but starts after every wait op closed: no link
	orphan := &Lane{ID: "S2", Path: "/root/b", Parent: "R", Depth: 1, Started: 960, Ended: 990, Turns: []*Turn{{ID: "z", Start: 960, End: 990, Status: "completed"}}}
	orphan.Ops = []*Operation{{ID: "k", Lane: "S2", Turn: "z", Phase: classify.Code, Kind: "read", Start: 965, End: 980, Status: "completed"}}
	s := &Session{ID: "s", Lanes: []*Lane{root, linked, orphan}}
	Derive(s, 1000)
	if linked.Turns[0].Lifecycle != classify.LcReview || linked.Turns[0].LifecycleRule != "inherited from /root (skill code-review-cc)" {
		t.Fatalf("u1 linked via wait window: %q %q", linked.Turns[0].Lifecycle, linked.Turns[0].LifecycleRule)
	}
	if linked.Turns[1].Lifecycle != "" || linked.Ops[1].LifecycleRule != "phase code" {
		t.Fatalf("u2 (re-use, not first turn) must not grab t2's plan via a bare wait window: %q %q", linked.Turns[1].Lifecycle, linked.Ops[1].LifecycleRule)
	}
	if orphan.Turns[0].Lifecycle != "" || orphan.Ops[0].LifecycleRule != "phase code" {
		t.Fatalf("no link → no inheritance: %q %q", orphan.Turns[0].Lifecycle, orphan.Ops[0].LifecycleRule)
	}
}

// Rule 4 of assignLifecycle: model output takes the nearest tool call's stage, forward first,
// backward for the tail; waits for workers are transparent; an unknown command is not; a turn
// without a tool call stays llm.
func TestLifecycleModelOutputNearestCall(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1500}
	l.Turns = []*Turn{
		{ID: "t1", Start: 0, End: 600, Status: "completed"},     // think, spawn+wait, think, read, answer
		{ID: "t2", Start: 700, End: 1000, Status: "completed"},  // think, unknown command, think, test, answer
		{ID: "t3", Start: 1100, End: 1500, Status: "completed"}, // text only
	}
	l.Ops = []*Operation{
		{ID: "w", Lane: "L", Turn: "t1", Phase: classify.WaitWorker, Kind: "agent", Start: 50, End: 350, Status: "completed"},
		{ID: "r", Lane: "L", Turn: "t1", Phase: classify.Code, Kind: "read", Start: 400, End: 450, Status: "completed"},
		{ID: "u", Lane: "L", Turn: "t2", Phase: classify.Unknown, Kind: "unknown", Start: 750, End: 760, Status: "completed"},
		{ID: "x", Lane: "L", Turn: "t2", Phase: classify.Test, Kind: "go test", Start: 800, End: 850, Status: "completed"},
	}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	by := l.ByLifecycle
	want := map[Lifecycle]int64{
		classify.LcImplement: 50 + 50 + 150, // think → read, read, the answer ← read
		"wait_worker":        300,           // the spawn/wait op
		"unknown":            50 + 10,       // the think before the unknown command, and the command
		classify.LcTest:      40 + 50 + 150, // think → test, test, the answer ← test
		"llm":                50 + 400,      // the think before the wait (bracket closed) + t3 (no tool call)
		"wait_user":          100 + 100,
	}
	for lc, v := range want {
		if by[lc] != v {
			t.Errorf("%s = %d, want %d", lc, by[lc], v)
		}
	}
	if lifecycleSum(l) != 1500 || len(by) != len(want) {
		t.Fatalf("by_lifecycle %v", by)
	}
}
