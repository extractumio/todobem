package insights

import (
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// rootLane is a one-turn root lane of ten minutes whose ops are given in order; every op is
// inside the turn.
func rootLane(ops ...*model.Operation) *model.Lane {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Trigger: "user"}}
	for _, o := range ops {
		o.Turn = "t1"
		o.Lane = "R"
	}
	root.Ops = ops
	return root
}

func edit(id string, at int64) *model.Operation {
	return op(id, "R", classify.Code, "edit", at, at+1000, "completed")
}

func test(id string, s, e int64, status string) *model.Operation {
	o := op(id, "R", classify.Test, "go test", s, e, status)
	o.Identity = "/proj\ngo test ./..."
	return o
}

func hook(id string, s, e int64, phase model.Phase, status string) *model.Operation {
	o := op(id, "R", classify.WaitWorker, "hook", s, e, status)
	o.Rule = classify.HookRule(phase, "go test")
	o.Detail = "go test ./..."
	return o
}

func TestDeliveryWalkVerificationAndReview(t *testing.T) {
	// edit → passing test: verified by the agent
	f := Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "completed"))))
	d := f.Delivery
	if d.Changes != 1 || !d.Verified || d.VerifiedBy != "agent" || d.VerifiedOp != "c1" || d.Tests != 1 || d.LastVerdictFailed || d.EditTurns != 1 || d.EditTurnsUnverified != 0 {
		t.Fatalf("verified: %+v", d)
	}
	// edit → test starts → edit: the test saw an older generation
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "completed"), edit("e2", 2*minute+30e3))))
	if d = f.Delivery; d.Verified || d.LastChangeOp != "e2" || d.EditTurnsUnverified != 1 {
		t.Fatalf("test before the last edit: %+v", d)
	}
	// edit → failed test: the last verdict failed
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "failed"))))
	if d = f.Delivery; d.Verified || !d.LastVerdictFailed || d.LastVerdictOp != "c1" {
		t.Fatalf("failed verdict: %+v", d)
	}
	// edit → stop hook running a test command without error: verified by the hook
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Test, "completed"))))
	if d = f.Delivery; !d.Verified || d.VerifiedBy != "hook" || d.VerifiedOp != "h1" || d.EditTurnsUnverified != 0 {
		t.Fatalf("hook verification: %+v", d)
	}
	// the same hook with an error, or one running a lint-free formatter, verifies nothing
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Test, "failed"))))
	if f.Delivery.Verified {
		t.Fatal("a failed hook is not a verification")
	}
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Code, "completed"))))
	if f.Delivery.Verified {
		t.Fatal("a hook that ran no test is not a verification")
	}
	// a build after the last edit is not a verification
	f = Extract(session(t, rootLane(edit("e1", 1*minute), op("b1", "R", classify.Build, "go build", 2*minute, 3*minute, "completed"))))
	if f.Delivery.Verified || f.Delivery.Tests != 0 {
		t.Fatalf("build is not verification: %+v", f.Delivery)
	}
	// an unknown command after the last edit: the walk may have missed a test
	f = Extract(session(t, rootLane(edit("e1", 1*minute), op("u1", "R", classify.Unknown, "unknown", 2*minute, 3*minute, "completed"))))
	if f.Delivery.BlindAfterLastChangeMs != 1*minute {
		t.Fatalf("blind after the last change: %+v", f.Delivery)
	}
	// no change op at all
	f = Extract(session(t, rootLane(test("c1", 2*minute, 3*minute, "completed"))))
	if d = f.Delivery; d.Changes != 0 || d.Verified || d.EditTurns != 0 {
		t.Fatalf("no changes: %+v", d)
	}
}

func TestDeliveryWalkSessionScopeAndReview(t *testing.T) {
	// a child edits, the parent tests in its next turn: verified (session scope)
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 5 * minute, Status: "completed", Trigger: "user"},
		{ID: "t2", Start: 6 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
	}
	wait := op("w1", "R", classify.WaitWorker, "agent", 1*minute, 4*minute, "completed")
	wait.Turn = "t1"
	parentTest := test("c1", 7*minute, 8*minute, "completed")
	parentTest.Turn = "t2"
	root.Ops = []*model.Operation{wait, parentTest}
	child := &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 4 * minute}
	child.Turns = []*model.Turn{{ID: "a1", Start: 1 * minute, End: 4 * minute, Status: "completed"}}
	ce := op("e1", "A", classify.Code, "edit", 2*minute, 2*minute+1000, "completed")
	ce.Turn = "a1"
	child.Ops = []*model.Operation{ce}
	f := Extract(session(t, root, child))
	if d := f.Delivery; !d.Verified || d.LastChangeLane != 1 || d.VerifiedOp != "c1" || d.EditTurns != 1 || d.EditTurnsUnverified != 1 {
		t.Fatalf("session scope: %+v", d)
	}
	// review: a review-skill turn, then an edit after it
	root = &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 4 * minute, Status: "completed", Trigger: "user", Skill: "code-review-cc", Review: true},
		{ID: "t2", Start: 5 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
	}
	late := edit("e1", 6*minute)
	late.Turn = "t2"
	root.Ops = []*model.Operation{late}
	f = Extract(session(t, root))
	if d := f.Delivery; d.ReviewedAt != 4*minute || d.ChangesAfterReview != 1 {
		t.Fatalf("review then edit: %+v", d)
	}
	// edit inside the review turn, nothing after: covered
	late.Turn, late.Start, late.End = "t1", 2*minute, 2*minute+1000
	f = Extract(session(t, root))
	if d := f.Delivery; d.ReviewedAt != 4*minute || d.ChangesAfterReview != 0 {
		t.Fatalf("edit inside the review: %+v", d)
	}
	// no review run at all
	root.Turns[0].Skill, root.Turns[0].Review = "", false
	if f = Extract(session(t, root)); f.Delivery.ReviewedAt != 0 {
		t.Fatalf("no review: %+v", f.Delivery)
	}
}

func TestWindowRecoveryCompactionWindowAndGapAfterChange(t *testing.T) {
	// failed test → fix → passing test: a fix window; then a failed retry after an infra step
	first := test("c1", 1*minute, 2*minute, "failed")
	fix := edit("e1", 3*minute)
	second := test("c2", 4*minute, 5*minute, "failed")
	infra := op("i1", "R", classify.Infra, "docker compose", 6*minute, 6*minute+30e3, "completed")
	third := test("c3", 7*minute, 8*minute, "completed")
	f := Extract(session(t, rootLane(first, fix, second, infra, third)))
	if len(f.Groups) != 1 || len(f.Groups[0].Windows) != 2 {
		t.Fatalf("groups %+v", f.Groups)
	}
	w := f.Groups[0].Windows
	if w[0].Recovery != "fix" || !w[0].RetryFailed || w[0].HumanBoundary || w[1].Recovery != "infra" || w[1].RetryFailed {
		t.Fatalf("windows %+v", w)
	}
	// nothing between the failure and the retry: none; a user turn inside: HumanBoundary
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 3 * minute, Status: "completed", Trigger: "user"},
		{ID: "t2", Start: 4 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
	}
	a := test("c1", 1*minute, 2*minute, "failed")
	a.Turn = "t1"
	b := test("c2", 5*minute, 6*minute, "completed")
	b.Turn = "t2"
	root.Ops = []*model.Operation{a, b}
	f = Extract(session(t, root))
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "none" || !w[0].HumanBoundary {
		t.Fatalf("blind window across a user turn: %+v", w)
	}
	// a worker wait and a fix: mixed
	f = Extract(session(t, rootLane(test("c1", 1*minute, 2*minute, "failed"), op("w1", "R", classify.WaitWorker, "sleep", 2*minute+10e3, 2*minute+40e3, "completed"), edit("e1", 3*minute), test("c2", 4*minute, 5*minute, "completed"))))
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "mixed" {
		t.Fatalf("mixed recovery: %+v", w)
	}
	// compaction between two edits is inside the change window; after the last edit it is not
	c1 := op("k1", "R", classify.Compaction, "compaction", 2*minute, 2*minute+5000, "completed")
	c2 := op("k2", "R", classify.Compaction, "compaction", 5*minute, 5*minute+5000, "completed")
	f = Extract(session(t, rootLane(edit("e1", 1*minute), c1, edit("e2", 3*minute), c2)))
	if len(f.Compactions) != 2 || !f.Compactions[0].InChangeWindow || f.Compactions[1].InChangeWindow {
		t.Fatalf("compactions %+v", f.Compactions)
	}
	// a gap before the first change and one after it
	root = &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 2 * minute, Status: "completed", Trigger: "user"},
		{ID: "t2", Start: 3 * minute, End: 6 * minute, Status: "completed", Trigger: "user"},
		{ID: "t3", Start: 8 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
	}
	e := edit("e1", 4*minute)
	e.Turn = "t2"
	root.Ops = []*model.Operation{e}
	f = Extract(session(t, root))
	if len(f.Gaps) != 2 || f.Gaps[0].AfterFirstChange || !f.Gaps[1].AfterFirstChange {
		t.Fatalf("gaps %+v", f.Gaps)
	}
	// a stop hook is listed among the long ops under its command's shape
	f = Extract(session(t, rootLane(hook("h1", 9*minute, 9*minute+5000, classify.Test, "completed"))))
	if len(f.LongOps) != 1 || f.LongOps[0].Shape != "hook go test" {
		t.Fatalf("hook among long ops: %+v", f.LongOps)
	}
}
