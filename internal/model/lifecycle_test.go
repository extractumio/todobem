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

// A model-output op carries the stage of the segment that covers it, so the operation an
// operator clicks under a stage row and the time the row shows agree (the fixture's trailing
// message m1 stands on the test stage of the call before it).
func TestLifecycleModelOutputOpsCarryTheirSegmentStage(t *testing.T) {
	s := fixture()
	Derive(s, 2000)
	l := s.Lanes[0]
	var m1 *Operation
	for _, o := range l.Ops {
		if o.ID == "m1" {
			m1 = o
		}
	}
	if m1 == nil || m1.Lifecycle != classify.LcTest || m1.LifecycleRule != "model output: the stage of the nearest tool call in the turn" {
		t.Fatalf("m1: %+v", m1)
	}
	for _, o := range l.Ops {
		if o.Lifecycle == "llm" {
			t.Errorf("op %s still carries the llm pass-through stage", o.ID)
		}
	}
	// a message op in a turn without any tool call keeps llm, and says so
	l2 := &Lane{ID: "L2", Path: "/root", Started: 0, Ended: 100, Turns: []*Turn{{ID: "t", Start: 0, End: 100, Status: "completed"}}}
	l2.Ops = []*Operation{{ID: "m", Lane: "L2", Turn: "t", Phase: classify.LLM, Kind: "message", Start: 10, End: 20, Status: "completed"}}
	Derive(&Session{ID: "s2", Lanes: []*Lane{l2}}, 2000)
	if l2.Ops[0].Lifecycle != "llm" || l2.Ops[0].LifecycleRule != "model output in a turn without tool calls" {
		t.Fatalf("text-only turn: %q %q", l2.Ops[0].Lifecycle, l2.Ops[0].LifecycleRule)
	}
}

// Composition (rule 4): between the turn's first and last change op every code, build, test and
// infra call is implementation; a test after the last change is the verification pass; release
// ops and op-level pins keep their stage inside the window; reads before the first plan anchor
// are planning, a change op there is not.
func TestLifecycleChangeWindowAndPlanRun(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1400}
	l.Turns = []*Turn{{ID: "t1", Start: 0, End: 1000, Status: "completed"}, {ID: "t2", Start: 1000, End: 1400, Status: "completed"}}
	op := func(id, turn string, ph Phase, kind string, s, e int64) *Operation {
		return &Operation{ID: id, Lane: "L", Turn: turn, Phase: ph, Kind: kind, Start: s, End: e, Status: "completed"}
	}
	l.Ops = []*Operation{
		op("read0", "t1", classify.Code, "read", 50, 60),            // before the first edit: phase default
		op("test0", "t1", classify.Test, "go test", 100, 150),       // before the first edit: a reproduction run stays test
		op("edit1", "t1", classify.Code, "edit", 200, 200),          // first change
		op("test1", "t1", classify.Test, "go test|rerun", 300, 350), // inside the window: the loop
		op("build1", "t1", classify.Build, "go build", 400, 420),    // inside the window
		op("push", "t1", classify.Release, "git push", 450, 460),    // release stays release
		op("rev", "t1", classify.Release, "pr review", 470, 480),    // op pin (review) beats the window
		op("edit2", "t1", classify.Code, "edit", 500, 500),          // last change
		op("test2", "t1", classify.Test, "go test", 600, 700),       // after the last change: verification
		op("cmt", "t1", classify.Code, "git commit", 800, 810),      // after the last change: implement (default)
		op("read2", "t2", classify.Code, "read", 1050, 1060),        // before the plan anchor: planning
		op("edit3", "t2", classify.Code, "edit", 1100, 1100),        // a change before the anchor is not planning
		op("plan", "t2", classify.LLM, "plan", 1200, 1200),          // the anchor (pinned by the adapter)
		op("read3", "t2", classify.Code, "read", 1300, 1310),        // after the anchor: phase default
	}
	l.Ops[11].Lifecycle, l.Ops[11].LifecycleRule = "", ""
	l.Ops[6].Lifecycle = classify.LcReview
	l.Ops[12].Lifecycle, l.Ops[12].LifecycleRule = classify.LcPlan, "plan created (update_plan)"
	Derive(&Session{ID: "s", Lanes: []*Lane{l}}, 2000)
	want := map[string][2]string{
		"read0": {"implement", "phase code"}, "test0": {"test", "phase test"},
		"edit1":  {"implement", "inside the turn's change window (first edit … last edit)"},
		"test1":  {"implement", "inside the turn's change window (first edit … last edit)"},
		"build1": {"implement", "inside the turn's change window (first edit … last edit)"},
		"push":   {"release", "phase release"}, "rev": {"review", "kind pr review"},
		"edit2": {"implement", "inside the turn's change window (first edit … last edit)"},
		"test2": {"test", "phase test"}, "cmt": {"implement", "phase code"},
		"read2": {"plan", "before the turn's plan was submitted"}, "edit3": {"implement", "inside the turn's change window (first edit … last edit)"},
		"plan": {"plan", "plan created (update_plan)"}, "read3": {"implement", "phase code"},
	}
	for _, o := range l.Ops {
		w := want[o.ID]
		if string(o.Lifecycle) != w[0] || o.LifecycleRule != w[1] {
			t.Errorf("%s: %q %q, want %q %q", o.ID, o.Lifecycle, o.LifecycleRule, w[0], w[1])
		}
	}
	if lifecycleSum(l) != 1400 {
		t.Fatalf("partition %d", lifecycleSum(l))
	}
	// the activity partition never moves: the test runs inside the window are still test time
	if l.ByPhase[classify.Test] != 50+50+100 {
		t.Fatalf("by_phase test %d", l.ByPhase[classify.Test])
	}
}

// A skill invoked mid-turn (Claude Code's Skill tool call, recorded as a skill marker) pins a
// run from the marker to the turn's end; the ops before it keep their own composition. A marker
// before anything ran (Codex injects at the turn start) covers the whole turn as before, and a
// sub-agent spawned inside the run inherits the run's stage.
func TestLifecycleSkillRunFromMarker(t *testing.T) {
	root := &Lane{ID: "R", Path: "/root", Started: 0, Ended: 2000}
	root.Turns = []*Turn{
		{ID: "t1", Start: 0, End: 1000, Status: "completed", Skill: "agent-browser"}, // the first skill has no stage; simplify comes mid-turn
		{ID: "t2", Start: 1000, End: 2000, Status: "completed", Skill: "code-review-cc"},
	}
	root.Markers = []Marker{
		{T: 150, Kind: "skill", Lane: "R", Turn: "t1", Text: "agent-browser", Ref: "agent-browser"},
		{T: 500, Kind: "skill", Lane: "R", Turn: "t1", Text: "simplify", Ref: "simplify"},
		{T: 1005, Kind: "skill", Lane: "R", Turn: "t2", Text: "code-review-cc", Ref: "code-review-cc"},
		{T: 600, Kind: "agent_started", Lane: "R", Turn: "t1", Ref: "S"},
	}
	root.Ops = []*Operation{
		{ID: "e1", Lane: "R", Turn: "t1", Phase: classify.Code, Kind: "edit", Start: 100, End: 100, Status: "completed"},
		{ID: "x1", Lane: "R", Turn: "t1", Phase: classify.Test, Kind: "go test", Start: 200, End: 250, Status: "completed"},
		{ID: "e2", Lane: "R", Turn: "t1", Phase: classify.Code, Kind: "edit", Start: 300, End: 300, Status: "completed"},
		{ID: "x2", Lane: "R", Turn: "t1", Phase: classify.Test, Kind: "go test", Start: 550, End: 580, Status: "completed"},
		{ID: "w", Lane: "R", Turn: "t1", Phase: classify.WaitWorker, Kind: "agent", Start: 600, End: 900, Status: "completed"},
		{ID: "r2", Lane: "R", Turn: "t2", Phase: classify.Code, Kind: "read", Start: 1100, End: 1150, Status: "completed"},
	}
	sub := &Lane{ID: "S", Path: "/root/fix", Parent: "R", Depth: 1, Started: 600, Ended: 900, Turns: []*Turn{{ID: "u", Start: 600, End: 900, Status: "completed"}}}
	sub.Ops = []*Operation{{ID: "g", Lane: "S", Turn: "u", Phase: classify.Code, Kind: "read", Start: 700, End: 750, Status: "completed"}}
	s := &Session{ID: "s", Lanes: []*Lane{root, sub}}
	Derive(s, 2000)
	t1, t2 := root.Turns[0], root.Turns[1]
	if t1.Lifecycle != "" || len(t1.Runs) != 1 || t1.Runs[0].From != 500 || t1.Runs[0].Lifecycle != classify.LcReview || t1.Runs[0].Rule != "skill simplify" {
		t.Fatalf("t1 runs: lc=%q runs=%+v", t1.Lifecycle, t1.Runs)
	}
	if t2.Lifecycle != classify.LcReview || t2.LifecycleRule != "skill code-review-cc" || len(t2.Runs) != 0 {
		t.Fatalf("t2 (skill before anything ran) must cover the whole turn: %q %q %+v", t2.Lifecycle, t2.LifecycleRule, t2.Runs)
	}
	if !t1.Review || !t2.Review || s.Totals.Reviews != 2 {
		t.Errorf("review flags %v %v, reviews %d", t1.Review, t2.Review, s.Totals.Reviews)
	}
	got := map[string]string{}
	for _, o := range root.Ops {
		got[o.ID] = string(o.Lifecycle) + " / " + o.LifecycleRule
	}
	want := map[string]string{
		"e1": "implement / inside the turn's change window (first edit … last edit)",
		"x1": "implement / inside the turn's change window (first edit … last edit)",
		"e2": "implement / inside the turn's change window (first edit … last edit)",
		"x2": "review / skill simplify", "w": "review / skill simplify", "r2": "review / skill code-review-cc",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
	// the run covers model output and waits from the marker on; before it, the composition
	if root.ByLifecycle[classify.LcReview] != 500+1000 || root.ByLifecycle[classify.LcImplement] != 500 {
		t.Fatalf("root by_lifecycle %v", root.ByLifecycle)
	}
	for _, sg := range root.Segments {
		if sg.Start < 500 && sg.End > 500 {
			t.Fatalf("segment straddles the run start: %+v", sg)
		}
	}
	if sub.Turns[0].Lifecycle != classify.LcReview || sub.Turns[0].LifecycleRule != "inherited from /root (skill simplify)" {
		t.Fatalf("sub-agent spawned inside the run: %q %q", sub.Turns[0].Lifecycle, sub.Turns[0].LifecycleRule)
	}
	if lifecycleSum(root) != 2000 || lifecycleSum(sub) != 300 {
		t.Fatalf("partitions %d %d", lifecycleSum(root), lifecycleSum(sub))
	}
	// a skill invoked inside a plan-mode turn reviews the plan: the turn stays planning, no run
	pl := &Lane{ID: "P", Path: "/root", Started: 0, Ended: 1000, Turns: []*Turn{{ID: "p", Start: 0, End: 1000, Status: "completed", Mode: "plan"}}}
	pl.Markers = []Marker{{T: 400, Kind: "skill", Lane: "P", Turn: "p", Text: "code-review-cc", Ref: "code-review-cc"}}
	pl.Ops = []*Operation{
		{ID: "r", Lane: "P", Turn: "p", Phase: classify.Code, Kind: "read", Start: 100, End: 150, Status: "completed"},
		{ID: "x", Lane: "P", Turn: "p", Phase: classify.Test, Kind: "go test", Start: 500, End: 600, Status: "completed"},
	}
	Derive(&Session{ID: "s2", Lanes: []*Lane{pl}}, 2000)
	if pl.Turns[0].Lifecycle != classify.LcPlan || len(pl.Turns[0].Runs) != 0 || pl.ByLifecycle[classify.LcPlan] != 1000 || pl.Ops[1].LifecycleRule != "plan mode" {
		t.Fatalf("plan-mode turn with a skill: %q runs=%+v by=%v rule=%q", pl.Turns[0].Lifecycle, pl.Turns[0].Runs, pl.ByLifecycle, pl.Ops[1].LifecycleRule)
	}
}

func TestSubgroupSetOnEveryOp(t *testing.T) {
	s := fixture()
	Derive(s, 2000)
	for _, o := range s.Lanes[0].Ops {
		if want := classify.Subgroup(o.Phase, o.Kind); o.Subgroup != want {
			t.Errorf("%s: subgroup %q, want %q", o.ID, o.Subgroup, want)
		}
	}
}
