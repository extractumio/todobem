package insights

import (
	"fmt"

	"github.com/extractumio/todobem/internal/classify"
)

// sourceLabel names a session's source the way the page does.
func sourceLabel(source string) string {
	switch source {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	}
	return source
}

// D17 · Final changes had no later successful test. Signal: the session changed a file and no
// test op (nor a stop hook running a test) started at or after its last change and completed
// without failing — the state at session end (a test after a push verifies the session; the
// push itself is D25's question). A check: one finding per session, keyed by source (the rate
// differed fourfold between harnesses on the first corpus); the note says which literal case
// applies. A session with an unknown command or a telemetry gap on the root lane after the
// last change is not measurable (the walk may have missed the test). A session with no change
// op is not applicable: it is in neither the denominator nor "no data". Stats: the per-turn
// measurement (turns with edits, those with no successful test after their last edit), test
// runs and how many failed, sessions whose last verdict failed, what verified the verified
// sessions by the kind the rule table gave it (`verified_kind:<kind>`; a lint, type check or
// syntax check alone is `verified_static_only`), sessions that ran stop hooks at all and those
// verified by one. Says nothing about what the test covered.
func detectUnverifiedChanges(f *Facts) Result {
	d := f.Delivery
	if d.Changes == 0 {
		return Result{NotApplicable: true}
	}
	if d.BlindAfterLastChangeMs > 0 {
		return Result{Reason: "an unknown command or a telemetry gap after the last edit"}
	}
	r := Result{Measurable: true, Key: sourceLabel(f.Source), Stats: map[string]int64{"edit_turns": int64(d.EditTurns), "edit_turns_unverified": int64(d.EditTurnsUnverified), "tests": int64(d.Tests), "tests_failed": int64(d.TestsFailed)}}
	if d.LastVerdictFailed {
		r.Stats["last_verdict_failed"]++
	}
	if d.HookOps > 0 {
		r.Stats["hook_sessions"]++
	}
	if d.Verified {
		r.Stats["verified"]++
		if d.VerifiedBy == "hook" {
			r.Stats["verified_by_hook"]++
		}
		if len(d.VerifiedKinds) > 0 {
			r.Stats["verified_kind:"+d.VerifiedKinds[0]]++
		}
		if staticOnly(d.VerifiedKinds) {
			r.Stats["verified_static_only"]++
		}
		return r
	}
	note := "no test ran in this session"
	switch {
	case d.Tests > 0 && d.LastVerdictFailed:
		note = "the last test after the last edit failed"
	case d.Tests > 0:
		note = "the last test ran before the last edit"
	}
	r.Findings = append(r.Findings, Finding{Lane: f.lanePath(d.LastChangeLane), LaneID: f.laneID(d.LastChangeLane), A: d.LastChangeAt, B: f.Ended, Op: d.LastChangeOp, Key: sourceLabel(f.Source), Note: fmt.Sprintf("%s; %s", plural(d.Changes, "edit"), note)})
	return r
}

// staticOnly reports whether every verification after the last change was a linter, a type
// check or a shell syntax check (classify.StaticCheckKinds): nothing ran the code.
func staticOnly(kinds []string) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, k := range kinds {
		if !classify.StaticCheckKinds[k] {
			return false
		}
	}
	return true
}

// plural words a count with its noun ("1 edit", "3 edits").
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// reviewGapBucket names how many edits followed the review (a display convention).
func reviewGapBucket(n int) string {
	switch {
	case n <= 1:
		return "1 edit after the review"
	case n <= 5:
		return "2-5 edits after the review"
	}
	return "more than 5 edits after the review"
}

// D24 · A review did not cover the last changes. Signal: a review run (a review skill the
// harness injected, Codex review mode, a review-role lane) whose turn ended before the
// session's last change op. A check: one finding per session, keyed by how many edits came
// after the review. A session with no review run is not applicable. Says nothing about what
// the review looked at.
func detectReviewNotCoveringLastChanges(f *Facts) Result {
	d := f.Delivery
	if d.ReviewedAt == 0 {
		return Result{NotApplicable: true}
	}
	r := Result{Measurable: true, Stats: map[string]int64{"edits_after_review": int64(d.ChangesAfterReview)}}
	if d.ChangesAfterReview == 0 {
		return r
	}
	r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: d.ReviewedAt, B: d.LastChangeAt, Op: d.LastChangeOp, Key: reviewGapBucket(d.ChangesAfterReview), Note: plural(d.ChangesAfterReview, "edit") + " after the last review run"})
	return r
}

// D25 · Pushed with no passing test between the last edit and the push. Signal: a `git push`
// op (any lane) with a change op before it and no verification (a passing test op, a test stop
// hook without error) started between that change and the push. Two keys: no test ran in the
// window; tests ran and none passed. A check: one finding per such push, the interval from the
// last change to the push. A session with no push after a change is not applicable. A push whose
// window holds unknown or no-telemetry root time is not measurable (the walk may have missed
// the test): counted as no data, and a session whose every push is blind is no data itself.
// Stats: pushes, unverified pushes, pushes after a failed last test, sessions with edits after
// their last push and those edits. CI after the push is not in the log; the card never says
// "untested": an edit to any file counts, docs included.
func detectPushWithoutTest(f *Facts) Result {
	d := f.Delivery
	if len(d.Pushes) == 0 {
		return Result{NotApplicable: true}
	}
	r := Result{Stats: map[string]int64{"pushes": int64(len(d.Pushes))}}
	if d.ChangesAfterLastPush > 0 {
		r.Stats["sessions_with_edits_after_last_push"]++
		r.Stats["edits_after_last_push"] += int64(d.ChangesAfterLastPush)
	}
	for _, p := range d.Pushes {
		if p.BlindMs > 0 {
			r.NoData++
			continue
		}
		r.Measurable = true
		if p.Verified {
			continue
		}
		r.Stats["pushes_unverified"]++
		key, note := "no test ran between the last edit and the push", "no test ran between the last edit and the push"
		if p.Tests > 0 {
			key = "tests ran between, none passed"
			note = fmt.Sprintf("%s between the last edit and the push, none passed", plural(p.Tests, "test run"))
		}
		if p.LastTestFailed {
			r.Stats["pushes_after_failed_test"]++
			note += "; the last test before the push failed"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(p.Lane), LaneID: f.laneID(p.Lane), A: p.LastChangeAt, B: p.Start, Op: p.Op, Key: key, Note: note})
	}
	if !r.Measurable {
		return Result{NotApplicable: false, NoData: r.NoData, Reason: "an unknown command or a telemetry gap between the last edit and the push", Stats: r.Stats}
	}
	return r
}
