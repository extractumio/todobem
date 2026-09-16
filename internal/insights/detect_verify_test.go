package insights

import (
	"strings"
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

func TestUnverifiedChangesCheck(t *testing.T) {
	// edit → final: one finding, keyed by source, the note names the case
	f := Extract(session(t, rootLane(edit("e1", 1*minute))))
	r := detectUnverifiedChanges(&f)
	if !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "Codex" || r.Findings[0].Op != "e1" || r.Findings[0].A != 1*minute || r.Findings[0].B != 10*minute || !strings.Contains(r.Findings[0].Note, "no test ran") {
		t.Fatalf("positive: %+v", r)
	}
	if r.Stats["edit_turns"] != 1 || r.Stats["edit_turns_unverified"] != 1 {
		t.Fatalf("stats: %+v", r.Stats)
	}
	// edit → passing test → final: no finding, the session counts as verified
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "completed"))))
	if r = detectUnverifiedChanges(&f); !r.Measurable || len(r.Findings) != 0 || r.Stats["verified"] != 1 || r.Stats["edit_turns_unverified"] != 0 {
		t.Fatalf("negative: %+v", r)
	}
	// edit → test starts → edit → final: the test ran before the last edit
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "completed"), edit("e2", 2*minute+30e3))))
	if r = detectUnverifiedChanges(&f); len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Note, "before the last edit") {
		t.Fatalf("test before the last edit: %+v", r)
	}
	// edit → failed test → final
	f = Extract(session(t, rootLane(edit("e1", 1*minute), test("c1", 2*minute, 3*minute, "failed"))))
	if r = detectUnverifiedChanges(&f); len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Note, "failed") || r.Stats["last_verdict_failed"] != 1 {
		t.Fatalf("failed verdict: %+v", r)
	}
	// edit → final → stop hook running the tests without error: verified by the hook
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Test, "completed"))))
	if r = detectUnverifiedChanges(&f); len(r.Findings) != 0 || r.Stats["verified_by_hook"] != 1 {
		t.Fatalf("hook verification: %+v", r)
	}
	// the same hook with a hook error: a finding
	f = Extract(session(t, rootLane(edit("e1", 1*minute), hook("h1", 9*minute, 9*minute+5000, classify.Test, "failed"))))
	if r = detectUnverifiedChanges(&f); len(r.Findings) != 1 {
		t.Fatalf("failed hook: %+v", r)
	}
	// an unknown command after the last edit: not measurable, with a reason
	f = Extract(session(t, rootLane(edit("e1", 1*minute), op("u1", "R", classify.Unknown, "unknown", 2*minute, 3*minute, "completed"))))
	if r = detectUnverifiedChanges(&f); r.Measurable || r.Reason == "" || r.NotApplicable {
		t.Fatalf("blind: %+v", r)
	}
	// no change op: not applicable
	f = Extract(session(t, rootLane(test("c1", 2*minute, 3*minute, "completed"))))
	if r = detectUnverifiedChanges(&f); !r.NotApplicable || r.Measurable {
		t.Fatalf("not applicable: %+v", r)
	}
	// a Claude Code session is keyed by its source
	s := session(t, rootLane(edit("e1", 1*minute)))
	s.Source = "claude"
	if f = Extract(s); detectUnverifiedChanges(&f).Findings[0].Key != "Claude Code" {
		t.Fatal("key by source")
	}
}

func TestReviewNotCoveringLastChangesCheck(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 10 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 4 * minute, Status: "completed", Trigger: "user", Skill: "code-review-cc", Review: true},
		{ID: "t2", Start: 5 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
	}
	e1, e2 := edit("e1", 6*minute), edit("e2", 7*minute)
	e1.Turn, e2.Turn = "t2", "t2"
	root.Ops = []*model.Operation{e1, e2}
	f := Extract(session(t, root))
	r := detectReviewNotCoveringLastChanges(&f)
	if !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "2-5 edits after the review" || r.Findings[0].A != 4*minute || r.Findings[0].B != 7*minute || r.Findings[0].Op != "e2" {
		t.Fatalf("positive: %+v", r)
	}
	// the edits inside the review turn: covered
	e1.Turn, e1.Start, e1.End = "t1", 2*minute, 2*minute+1000
	e2.Turn, e2.Start, e2.End = "t1", 3*minute, 3*minute+1000
	f = Extract(session(t, root))
	if r = detectReviewNotCoveringLastChanges(&f); !r.Measurable || len(r.Findings) != 0 {
		t.Fatalf("negative: %+v", r)
	}
	// Codex review mode pins the whole turn (a recorded marker); an edit in a later turn is uncovered
	root.Turns[0].Skill, root.Turns[0].Review = "", false
	root.Markers = []model.Marker{{T: 1 * minute, Kind: "enteredreviewmode", Lane: "R", Turn: "t1"}}
	e2.Turn, e2.Start, e2.End = "t2", 8*minute, 8*minute+1000
	f = Extract(session(t, root))
	if r = detectReviewNotCoveringLastChanges(&f); len(r.Findings) != 1 || r.Findings[0].Key != "1 edit after the review" {
		t.Fatalf("review mode, then an edit: %+v", r)
	}
	// no review run: not applicable
	root.Markers = nil
	f = Extract(session(t, root))
	if r = detectReviewNotCoveringLastChanges(&f); !r.NotApplicable {
		t.Fatalf("not applicable: %+v", r)
	}
}

func TestRetryWindowPathsAndCompactionAndQuestionStats(t *testing.T) {
	// D7: a blind window, a fix window whose retry failed again, an infra window
	f := Extract(session(t, rootLane(
		test("c1", 1*minute, 1*minute+30e3, "failed"),
		test("c2", 2*minute, 2*minute+30e3, "failed"),
		edit("e1", 3*minute),
		test("c3", 4*minute, 4*minute+30e3, "failed"),
		op("i1", "R", classify.Infra, "docker compose", 5*minute, 5*minute+30e3, "completed"),
		test("c4", 6*minute, 6*minute+30e3, "completed"),
	)))
	r := detectRetryLoops(&f)
	if len(r.Findings) != 1 || r.Stats["windows"] != 3 || r.Stats["windows_blind"] != 1 || r.Stats["windows_fix"] != 1 || r.Stats["windows_infra"] != 1 || r.Stats["retries_failed_again"] != 1 {
		t.Fatalf("D7 stats %+v findings %+v", r.Stats, r.Findings)
	}
	if n := r.Findings[0].Note; !strings.Contains(n, "1× retried with nothing recorded between") || !strings.Contains(n, "1× retried after a fix") {
		t.Fatalf("note %q", n)
	}
	// D11: a compaction between two edits carries the stat and the note; one after them does not
	c1 := op("k1", "R", classify.Compaction, "compaction", 2*minute, 2*minute+5000, "completed")
	c2 := op("k2", "R", classify.Compaction, "compaction", 5*minute, 5*minute+5000, "completed")
	f = Extract(session(t, rootLane(edit("e1", 1*minute), c1, edit("e2", 3*minute), c2)))
	r = detectCompactions(&f)
	if len(r.Findings) != 2 || r.Stats["in_change_window"] != 1 || r.Stats["in_change_window_ms"] != 5000 || !strings.Contains(r.Findings[0].Note, "between two edits") || strings.Contains(r.Findings[1].Note, "between two edits") {
		t.Fatalf("D11 %+v %+v", r.Stats, r.Findings)
	}
	// D1: a question from a turn that had already changed files; a question from a later turn
	// that changed nothing is a plain question even though the session had edits
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 20 * minute}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 3 * minute, Status: "completed", Trigger: "user"},
		{ID: "t2", Start: 5 * minute, End: 10 * minute, Status: "completed", Trigger: "user"},
		{ID: "t3", Start: 15 * minute, End: 20 * minute, Status: "completed", Trigger: "user"},
	}
	e := edit("e1", 1*minute)
	e.Turn = "t1"
	root.Ops = []*model.Operation{e}
	root.Markers = []model.Marker{{T: 2 * minute, Kind: "question", Lane: "R", Turn: "t1"}, {T: 9 * minute, Kind: "question", Lane: "R", Turn: "t2"}}
	f = Extract(session(t, root))
	r = detectWaitingOnAnswer(&f)
	if len(r.Findings) != 2 || r.Stats["after_changes"] != 1 || r.Stats["after_changes_ms"] != 2*minute || !strings.Contains(r.Findings[0].Note, "already changed files") || strings.Contains(r.Findings[1].Note, "already changed files") {
		t.Fatalf("D1 %+v %+v", r.Stats, r.Findings)
	}
	// the catalogue holds the two checks in the verification group
	n := 0
	for _, d := range Catalogue {
		if d.Group == GroupVerify && d.Class == ClassCheck {
			n++
		}
	}
	if n != 2 || len(Catalogue) != 21 {
		t.Fatalf("catalogue: %d checks, %d detectors", n, len(Catalogue))
	}
}
