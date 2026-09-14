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
	// trailing message m1 (520-590) and the gap to the turn end are model output with no tool
	// call after them. t2 has no ops at all: all llm. Outside turns passes through.
	if l.ByLifecycle["wait_user"] != 400 {
		t.Fatalf("wait_user %d", l.ByLifecycle["wait_user"])
	}
	// implement: think [100,120) → r1, r1, c2 after c1 [300,400), think [400,410) → f1, f1
	if l.ByLifecycle[classify.LcImplement] != 20+10+100+10+10 {
		t.Fatalf("implement %d", l.ByLifecycle[classify.LcImplement])
	}
	// test: think [130,200) → c1, c1 exclusive [200,300), think [420,450) → c3, c3
	if l.ByLifecycle[classify.LcTest] != 70+100+30+50 {
		t.Fatalf("test %d", l.ByLifecycle[classify.LcTest])
	}
	if l.ByLifecycle["llm"] != 20+70+10+100 { // [500,520) + m1 [520,590) + [590,600) ; t2 [800,900)
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
	// before the push it is release; before the comment it is review; a compaction closes the
	// bracket, so the think before it stays llm; the edit pulls the think before it into
	// implementation and the trailing think of the turn stays llm.
	by := l.ByLifecycle
	wantMs := map[Lifecycle]int64{
		classify.LcImplement: 100 + 50 + 80 + 1, // think→logs1 (demoted), logs1, think→edit, edit
		classify.LcPlan:      150 + 1,
		classify.LcRelease:   99 + 20,
		classify.LcReview:    80 + 10,
		classify.LcOperate:   90 + 50,
		"compaction":         20,
		"llm":                50 + 199,
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
