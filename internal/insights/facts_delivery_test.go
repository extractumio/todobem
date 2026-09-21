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
	if d = f.Delivery; d.Verified || d.Tests != 1 || d.TestsFailed != 1 || !d.LastVerdictFailed {
		t.Fatalf("a failed hook is a failed test verdict: %+v", d)
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
	// Compound shares allocate elapsed time but do not preserve shell control flow or per-step
	// outcomes. Neither apparent order is delivery evidence.
	compound := edit("both", 2*minute)
	compound.End = 4 * minute
	compound.Shares = []model.Share{
		{Phase: classify.Code, Kind: "edit", Ms: minute},
		{Phase: classify.Test, Kind: "go test", Ms: minute},
	}
	f = Extract(session(t, rootLane(compound)))
	if d = f.Delivery; d.Changes != 0 || d.Tests != 0 || d.Verified {
		t.Fatalf("compound edit then test: %+v", d)
	}
	compound = test("reverse", 2*minute, 4*minute, "completed")
	compound.Shares = []model.Share{
		{Phase: classify.Test, Kind: "go test", Ms: minute},
		{Phase: classify.Code, Kind: "edit", Ms: minute},
	}
	f = Extract(session(t, rootLane(compound)))
	if d = f.Delivery; d.Changes != 0 || d.Tests != 0 || d.Verified {
		t.Fatalf("compound test then edit: %+v", d)
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
	// A test share cannot be ordered against another lane's edit from estimated boundaries.
	compound := test("slow", 0, 10*minute, "completed")
	compound.Shares = []model.Share{
		{Phase: classify.WaitWorker, Kind: "sleep", Ms: 9 * minute, Literal: true},
		{Phase: classify.Test, Kind: "go test", Ms: minute},
	}
	root = rootLane(compound)
	child = &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: minute, Ended: 6 * minute}
	child.Turns = []*model.Turn{{ID: "a1", Start: minute, End: 6 * minute, Status: "completed"}}
	ce = op("e2", "A", classify.Code, "edit", 5*minute, 5*minute+1000, "completed")
	ce.Turn = "a1"
	child.Ops = []*model.Operation{ce}
	f = Extract(session(t, root, child))
	if d := f.Delivery; d.Verified || !d.AmbiguousTestAfterLastChange || d.LastChangeAt != 5*minute {
		t.Fatalf("cross-lane share order: %+v", d)
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
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "mixed" || w[0].RetryOp != "c2" {
		t.Fatalf("mixed recovery: %+v", w)
	}
	// only reads and queries between: none; a checkout or a narrowed test run between: other
	f = Extract(session(t, rootLane(test("c1", 1*minute, 2*minute, "failed"), op("r1", "R", classify.Code, "read", 2*minute+10e3, 2*minute+11e3, "completed"), op("q1", "R", classify.Code, "git status", 2*minute+20e3, 2*minute+21e3, "completed"), test("c2", 4*minute, 5*minute, "completed"))))
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "none" {
		t.Fatalf("reads only: %+v", w)
	}
	narrow := op("n1", "R", classify.Test, "go test", 2*minute+10e3, 2*minute+20e3, "completed")
	narrow.Identity = "/proj\ngo test -run TestX ./..."
	f = Extract(session(t, rootLane(test("c1", 1*minute, 2*minute, "failed"), narrow, test("c2", 4*minute, 5*minute, "completed"))))
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "other" {
		t.Fatalf("narrowed run between: %+v", w)
	}
	// a sub-agent's edit inside the root's window is the session's fix
	root = &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Trigger: "user"}}
	a, b = test("c1", 1*minute, 2*minute, "failed"), test("c2", 5*minute, 6*minute, "completed")
	a.Turn, b.Turn = "t1", "t1"
	root.Ops = []*model.Operation{a, b}
	child := &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: 2 * minute, Ended: 4 * minute}
	child.Turns = []*model.Turn{{ID: "a1", Start: 2 * minute, End: 4 * minute, Status: "completed"}}
	ce := op("e1", "A", classify.Code, "edit", 3*minute, 3*minute+1000, "completed")
	ce.Turn = "a1"
	child.Ops = []*model.Operation{ce}
	f = Extract(session(t, root, child))
	if w := f.Groups[0].Windows; len(w) != 1 || w[0].Recovery != "fix" {
		t.Fatalf("sub-agent fix: %+v", w)
	}
	// compaction between two edits is inside the change window; after the last edit it is not
	c1 := op("k1", "R", classify.Compaction, "compaction", 2*minute, 2*minute+5000, "completed")
	c2 := op("k2", "R", classify.Compaction, "compaction", 5*minute, 5*minute+5000, "completed")
	f = Extract(session(t, rootLane(edit("e1", 1*minute), c1, edit("e2", 3*minute), c2)))
	if len(f.Compactions) != 2 || !f.Compactions[0].InChangeWindow || f.Compactions[1].InChangeWindow {
		t.Fatalf("compactions %+v", f.Compactions)
	}
	// a gap after a turn that changed nothing and one after a turn that edited a file
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
	if len(f.Gaps) != 2 || f.Gaps[0].TurnChangedFiles || !f.Gaps[1].TurnChangedFiles {
		t.Fatalf("gaps %+v", f.Gaps)
	}
	// a stop hook is listed among the long ops under its command's shape
	f = Extract(session(t, rootLane(hook("h1", 9*minute, 9*minute+5000, classify.Test, "completed"))))
	if len(f.LongOps) != 1 || f.LongOps[0].Shape != "hook go test" {
		t.Fatalf("hook among long ops: %+v", f.LongOps)
	}
}

func push(id string, at int64) *model.Operation {
	return op(id, "R", classify.Release, "git push", at, at+2000, "completed")
}

// TestDeliveryWalkPushes: every `git push` after a change is read against the window since the
// last change; a PR comment is a release op and not a push; edits after the last push are the
// unreleased tail; a sub-agent's test in the window verifies the root's push (session scope).
func TestDeliveryWalkPushes(t *testing.T) {
	// edit → passing test → push: verified
	f := Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "completed"), push("p1", 4*minute))))
	d := f.Delivery
	if len(d.Pushes) != 1 || !d.Pushes[0].Verified || d.Pushes[0].Tests != 1 || d.Pushes[0].LastChangeAt != 1*minute || d.Pushes[0].Op != "p1" || d.ChangesAfterLastPush != 0 {
		t.Fatalf("verified push: %+v", d.Pushes)
	}
	// edit → push, then a passing test after it: the push was unverified, the session is verified
	f = Extract(session(t, rootLane(edit("e1", 1*minute), push("p1", 2*minute), test("c1", 3*minute, 4*minute, "completed"))))
	if d = f.Delivery; len(d.Pushes) != 1 || d.Pushes[0].Verified || d.Pushes[0].Tests != 0 || !d.Verified {
		t.Fatalf("push before the test: %+v verified=%v", d.Pushes, d.Verified)
	}
	// edit → failed test → push → passing test: unverified push after a failed verdict, D17 verified
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "failed"), push("p1", 4*minute), test("c2", 5*minute, 6*minute, "completed"))))
	if d = f.Delivery; len(d.Pushes) != 1 || d.Pushes[0].Verified || d.Pushes[0].Tests != 1 || !d.Pushes[0].LastTestFailed || !d.Verified || d.TestsFailed != 1 {
		t.Fatalf("push after a failed test: %+v", d.Pushes)
	}
	// edit → unknown command → push: the window is blind
	f = Extract(session(t, rootLane(edit("e1", 1*minute), op("u1", "R", classify.Unknown, "unknown", 2*minute, 3*minute, "completed"), push("p1", 4*minute))))
	if d = f.Delivery; len(d.Pushes) != 1 || d.Pushes[0].BlindMs != 1*minute {
		t.Fatalf("blind push window: %+v", d.Pushes)
	}
	// push → edit: no push after a change; the edit is the unreleased tail
	f = Extract(session(t, rootLane(edit("e0", 30e3), test("c0", 40e3, 50e3, "completed"), push("p1", 1*minute), edit("e1", 2*minute), edit("e2", 3*minute))))
	if d = f.Delivery; len(d.Pushes) != 1 || !d.Pushes[0].Verified || d.ChangesAfterLastPush != 2 {
		t.Fatalf("edits after the last push: %+v after=%d", d.Pushes, d.ChangesAfterLastPush)
	}
	// a push with no change before it is not read; a PR comment is not a push
	f = Extract(session(t, rootLane(push("p0", 30e3), edit("e1", 1*minute), op("g1", "R", classify.Release, "pr comment", 2*minute, 2*minute+1000, "completed"))))
	if d = f.Delivery; len(d.Pushes) != 0 || d.ChangesAfterLastPush != 0 {
		t.Fatalf("no push after a change: %+v", d.Pushes)
	}
	// a sub-agent edits, the root tests and pushes: verified (session scope)
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Trigger: "user"}}
	rt, rp := test("c1", 5*minute, 6*minute, "completed"), push("p1", 7*minute)
	rt.Turn, rp.Turn = "t1", "t1"
	root.Ops = []*model.Operation{rt, rp}
	child := &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: 1 * minute, Ended: 4 * minute}
	child.Turns = []*model.Turn{{ID: "a1", Start: 1 * minute, End: 4 * minute, Status: "completed"}}
	ce := op("e1", "A", classify.Code, "edit", 2*minute, 2*minute+1000, "completed")
	ce.Turn = "a1"
	child.Ops = []*model.Operation{ce}
	f = Extract(session(t, root, child))
	if d = f.Delivery; len(d.Pushes) != 1 || !d.Pushes[0].Verified || d.Pushes[0].Lane != 0 {
		t.Fatalf("session scope push: %+v", d.Pushes)
	}
	// A successful compound still has no per-step outcome or control-flow trace. Its test makes
	// the window unknown and its apparent push share is not recorded as a push.
	compound := push("both", 1*minute)
	compound.End = 3 * minute
	compound.Shares = []model.Share{
		{Phase: classify.Test, Kind: "go test", Ms: minute},
		{Phase: classify.Release, Kind: "git push", Ms: minute},
	}
	f = Extract(session(t, rootLane(edit("e1", 30e3), compound)))
	if d = f.Delivery; d.Verified || d.Tests != 0 || len(d.Pushes) != 0 || !d.AmbiguousTestAfterLastChange || d.EditTurnsAmbiguous != 1 || d.EditTurnsUnverified != 0 {
		t.Fatalf("compound test then push: %+v", d)
	}
	compound.Status = "failed"
	f = Extract(session(t, rootLane(edit("e2", 30e3), compound)))
	if d = f.Delivery; d.Verified || d.Tests != 0 || d.TestsFailed != 0 || d.LastVerdictFailed || len(d.Pushes) != 0 || !d.AmbiguousTestAfterLastChange || d.EditTurnsAmbiguous != 1 || d.EditTurnsUnverified != 0 {
		t.Fatalf("ambiguous failed compound: %+v", d)
	}
	r := detectUnverifiedChanges(&f)
	if r.Measurable || r.NotApplicable || r.Reason == "" {
		t.Fatalf("ambiguous failed compound should be no data: %+v", r)
	}
	// The dominant phase is classifier priority, not a per-share failure verdict. Changing it
	// cannot make the preceding test known to have passed or failed.
	compound.Phase = classify.Test
	f = Extract(session(t, rootLane(edit("e3", 30e3), compound)))
	if d = f.Delivery; d.Verified || d.Tests != 0 || !d.AmbiguousTestAfterLastChange || len(d.Pushes) != 0 {
		t.Fatalf("priority-independent compound ambiguity: %+v", d)
	}
	// a failed push never claims that changes were released
	badPush := push("bad", 2*minute)
	badPush.Status = "failed"
	f = Extract(session(t, rootLane(edit("e1", 1*minute), badPush)))
	if d = f.Delivery; len(d.Pushes) != 0 || d.ChangesAfterLastPush != 0 {
		t.Fatalf("failed push recorded as delivery: %+v", d)
	}
}

// TestDeliveryWalkVerifiedKindsAndHooks: what verified the session is named by the kind the rule
// table gave it, in the order it ran; failed test runs are counted apart from failed tool calls;
// stop hooks are counted whatever they ran.
func TestDeliveryWalkVerifiedKindsAndHooks(t *testing.T) {
	// only a shell syntax check after the last edit (a heredoc classified by its inner command):
	// verified, by a static check alone
	lint := op("l1", "R", classify.Test, "script→syntax-check", 2*minute, 2*minute+1000, "completed")
	f := Extract(session(t, rootLane(edit("e1", 1*minute), lint)))
	if d := f.Delivery; !d.Verified || len(d.VerifiedKinds) != 1 || d.VerifiedKinds[0] != "syntax-check" {
		t.Fatalf("static check: %+v", f.Delivery)
	}
	// a vet then a go test: both kinds, in order; the first verification is the vet
	vet := op("v1", "R", classify.Test, "lint", 2*minute, 2*minute+1000, "completed")
	f = Extract(session(t, rootLane(edit("e1", 1*minute), vet, test("c1", 3*minute, 4*minute, "completed"))))
	if d := f.Delivery; len(d.VerifiedKinds) != 2 || d.VerifiedKinds[0] != "lint" || d.VerifiedKinds[1] != "go test" || d.VerifiedOp != "v1" {
		t.Fatalf("kinds in order: %+v", f.Delivery)
	}
	// a hook running a formatter and no test: a hook op, not a verification; a test hook names its command's kind
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Code, "completed"))))
	if d := f.Delivery; d.HookOps != 1 || d.Verified {
		t.Fatalf("formatter hook: %+v", f.Delivery)
	}
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Test, "completed"))))
	if d := f.Delivery; d.HookOps != 1 || !d.Verified || len(d.VerifiedKinds) != 1 || d.VerifiedKinds[0] != "go test" {
		t.Fatalf("test hook kind: %+v", f.Delivery)
	}
	// three failed pytest runs with different arguments (no retry group): counted as failed verdicts, not as failed tool calls
	var ops []*model.Operation
	ops = append(ops, edit("e1", 30e3))
	for i, args := range []string{"a", "b", "c"} {
		o := op("c"+args, "R", classify.Test, "pytest", int64(i+1)*minute, int64(i+1)*minute+10e3, "failed")
		o.Identity = "/proj\npytest tests/" + args
		ops = append(ops, o)
	}
	f = Extract(session(t, rootLane(ops...)))
	if d := f.Delivery; d.Tests != 3 || d.TestsFailed != 3 || d.Verified || len(f.Failures) != 0 || len(f.Groups) != 0 {
		t.Fatalf("failed verdicts: %+v failures=%d groups=%d", f.Delivery, len(f.Failures), len(f.Groups))
	}
}
