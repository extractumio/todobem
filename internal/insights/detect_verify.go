package insights

import "fmt"

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
// without failing. A check: one finding per session, keyed by source (the rate differed
// fourfold between harnesses on the first corpus); the note says which literal case applies.
// A session with an unknown command or a telemetry gap on the root lane after the last change
// is not measurable (the walk may have missed the test). A session with no change op is not
// applicable: it is in neither the denominator nor "no data". Stats: the per-turn measurement
// (turns with edits, those with no successful test after their last edit), sessions whose last
// verdict failed, sessions verified by a stop hook. Says nothing about what the test covered.
func detectUnverifiedChanges(f *Facts) Result {
	d := f.Delivery
	if d.Changes == 0 {
		return Result{NotApplicable: true}
	}
	if d.BlindAfterLastChangeMs > 0 {
		return Result{Reason: "an unknown command or a telemetry gap after the last edit"}
	}
	r := Result{Measurable: true, Key: sourceLabel(f.Source), Stats: map[string]int64{"edit_turns": int64(d.EditTurns), "edit_turns_unverified": int64(d.EditTurnsUnverified)}}
	if d.LastVerdictFailed {
		r.Stats["last_verdict_failed"]++
	}
	if d.Verified {
		r.Stats["verified"]++
		if d.VerifiedBy == "hook" {
			r.Stats["verified_by_hook"]++
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
