package insights

import (
	"sort"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// DeliveryFacts is the session read as one delivery loop: the recorded order of change ops,
// successful test ops, review runs and stop hooks across all lanes. Session scope: a sub-agent's
// edit is the session's edit, and a sub-agent's test verifies it, so a parent checking a child's
// change in its next turn is verification. Every field is a count or a timestamp from the op
// order; nothing here says what a test covered or whether a review was thorough.
type DeliveryFacts struct {
	Changes        int    `json:"changes"` // change ops (classify.IsChangeOp), all lanes
	FirstChangeAt  int64  `json:"first_change_at,omitempty"`
	LastChangeAt   int64  `json:"last_change_at,omitempty"`
	LastChangeOp   string `json:"last_change_op,omitempty"`
	LastChangeLane int    `json:"last_change_lane,omitempty"`
	// Verified: a successful test op started at or after LastChangeAt, or a stop hook whose
	// command classifies as test and that ended without a hook error. VerifiedOp is the first
	// such op, VerifiedBy "agent" | "hook".
	Verified   bool   `json:"verified"`
	VerifiedAt int64  `json:"verified_at,omitempty"`
	VerifiedOp string `json:"verified_op,omitempty"`
	VerifiedBy string `json:"verified_by,omitempty"`
	// VerifiedKinds are the distinct kinds the rule table gave every verification at or after
	// the last change (go test, pytest, lint, syntax-check, …; a hook's command kind), in the
	// order they ran: what verified the session, by name, nothing about what it covered.
	VerifiedKinds []string `json:"verified_kinds,omitempty"`
	Tests         int      `json:"tests"`                  // test ops in the session, any result
	TestsFailed   int      `json:"tests_failed,omitempty"` // test ops that failed: a verdict, not a failed tool call (Failures is the code phase)
	// HookOps counts the stop hooks the session ran, whatever their command: the sessions that
	// ran hooks at all are the denominator of "verified by a hook".
	HookOps int `json:"hook_ops,omitempty"`
	// LastVerdict: the last test op by start, and whether it failed.
	LastVerdictFailed bool   `json:"last_verdict_failed,omitempty"`
	LastVerdictOp     string `json:"last_verdict_op,omitempty"`
	// Review: the end of the last turn carrying a review run (a review skill run, a
	// review-pinned turn, a review-role lane); ChangesAfterReview counts change ops started
	// after it.
	ReviewedAt         int64 `json:"reviewed_at,omitempty"`
	ChangesAfterReview int   `json:"changes_after_review,omitempty"`
	// Blind: unknown + no_telemetry time on the root lane between LastChangeAt and the session
	// end; when > 0 the walk may have missed a test, and a rule reading Verified reports the
	// session as no data instead.
	BlindAfterLastChangeMs int64 `json:"blind_after_last_change_ms,omitempty"`
	// Pushes: every `git push` op (any lane) that had a change op before it, with what ran
	// between that change and the push; ChangesAfterLastPush counts the change ops started after
	// the session's last push (the tail the next session inherits).
	Pushes               []PushFacts `json:"pushes,omitempty"`
	ChangesAfterLastPush int         `json:"changes_after_last_push,omitempty"`
	// Per-turn aggregates: turns with a change op, those with no successful test in the same
	// turn at or after their last change, and the unknown + no_telemetry time inside the root
	// lane's change windows.
	EditTurns           int   `json:"edit_turns"`
	EditTurnsUnverified int   `json:"edit_turns_unverified"`
	UnknownInWindowMs   int64 `json:"unknown_in_window_ms,omitempty"`
}

// PushFacts is one `git push` read against the change before it: whether a verification (a
// passing test op, a test stop hook without error) started between the last change op and the
// push, whether a test ran there at all and whether the last one before the push failed, and
// the root's unknown + no_telemetry time inside that window (when > 0 the walk may have missed
// the test: the push is not measurable). Session scope, like the rest of the walk: a sub-agent's
// edit is the session's edit and a sub-agent's test verifies it. CI after the push is not in the
// log; an edit to any file counts, docs included.
type PushFacts struct {
	Lane           int    `json:"lane"`
	Op             string `json:"op"`
	Start          int64  `json:"start"`
	LastChangeAt   int64  `json:"last_change_at"`
	Verified       bool   `json:"verified"`
	Tests          int    `json:"tests,omitempty"`
	LastTestFailed bool   `json:"last_test_failed,omitempty"`
	BlindMs        int64  `json:"blind_ms,omitempty"`
}

// verificationOp reports whether o is a recorded verification: a test op that completed
// without failing, or a stop hook whose command classified as test and raised no hook error.
// by is "agent" or "hook".
func verificationOp(o *model.Operation) (by string, ok bool) {
	if o.Background || o.Status != "completed" || o.Failure() {
		return "", false
	}
	if o.Phase == classify.Test {
		return "agent", true
	}
	if p, isHook := classify.HookPhase(o.Rule); isHook && p == classify.Test {
		return "hook", true
	}
	return "", false
}

// isPushOp reports whether o is a `git push` (the release kind; a PR comment or a CI cancel is a
// release op too and is not a push).
func isPushOp(o *model.Operation) bool {
	return o.Phase == classify.Release && classify.BaseKind(o.Kind) == "git push"
}

// verificationKind names what a verification ran: the base kind the rule table gave the test op
// (a heredoc classified by its inner command loses the "script→" prefix, as in classify.Subgroup),
// or the kind of a stop hook's command.
func verificationKind(o *model.Operation, by string) string {
	if by == "hook" {
		return classify.HookKind(o.Rule)
	}
	return strings.TrimPrefix(classify.BaseKind(o.Kind), "script→")
}

// appendDistinct appends s to list unless it is already there (first-seen order kept).
func appendDistinct(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// changeWindow is a turn's first and last change op (by start), the composition rule's window.
type changeWindow struct {
	first, last int64
	changes     int
}

// changeWindows lists the change windows of one lane by turn id (background ops excluded).
func changeWindows(l *model.Lane) map[string]*changeWindow {
	out := map[string]*changeWindow{}
	for _, o := range l.Ops {
		if o.Background || !classify.IsChangeOp(o.Phase, o.Kind) {
			continue
		}
		w := out[o.Turn]
		if w == nil {
			w = &changeWindow{first: o.Start, last: o.Start}
			out[o.Turn] = w
		}
		w.first, w.last = min(w.first, o.Start), max(w.last, o.Start)
		w.changes++
	}
	return out
}

// blindMs sums the lane's unknown and no_telemetry segments inside [from, to).
func blindMs(l *model.Lane, from, to int64) int64 {
	var ms int64
	for _, sg := range l.Segments {
		if sg.Phase != classify.Unknown && sg.Phase != classify.NoTelemetry {
			continue
		}
		if sg.End <= from || sg.Start >= to {
			continue
		}
		ms += min(sg.End, to) - max(sg.Start, from)
	}
	return ms
}

// deliveryFacts walks every non-background op of the session in start order.
func deliveryFacts(s *model.Session, laneIndex map[string]int) DeliveryFacts {
	var d DeliveryFacts
	root := s.Lanes[0]
	var all []*model.Operation
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			if !o.Background {
				all = append(all, o)
			}
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].Start < all[b].Start })
	var verified *model.Operation
	verifiedBy := ""
	var verifiedKinds []string
	var lastVerdict *model.Operation
	// the window since the last change, read at each push: tests in it and the last one's verdict
	testsSinceChange, lastTestSinceChangeFailed := 0, false
	changesSinceLastPush := 0
	for _, o := range all {
		if classify.IsChangeOp(o.Phase, o.Kind) {
			d.Changes++
			if d.FirstChangeAt == 0 {
				d.FirstChangeAt = o.Start
			}
			d.LastChangeAt, d.LastChangeOp, d.LastChangeLane = o.Start, o.ID, laneIndex[o.Lane]
			verified, verifiedBy, verifiedKinds = nil, "", nil // a later change invalidates the verification seen so far
			testsSinceChange, lastTestSinceChangeFailed = 0, false
			changesSinceLastPush++
			continue
		}
		if o.Phase == classify.Test {
			d.Tests++
			testsSinceChange++
			lastTestSinceChangeFailed = o.Failure()
			if lastTestSinceChangeFailed {
				d.TestsFailed++
			}
			lastVerdict = o
		}
		if isHookOp(o) {
			d.HookOps++
		}
		if by, ok := verificationOp(o); ok {
			if verified == nil {
				verified, verifiedBy = o, by
			}
			verifiedKinds = appendDistinct(verifiedKinds, verificationKind(o, by))
		}
		if isPushOp(o) && d.Changes > 0 {
			d.Pushes = append(d.Pushes, PushFacts{Lane: laneIndex[o.Lane], Op: o.ID, Start: o.Start, LastChangeAt: d.LastChangeAt, Verified: verified != nil, Tests: testsSinceChange, LastTestFailed: lastTestSinceChangeFailed, BlindMs: blindMs(root, d.LastChangeAt, o.Start)})
			changesSinceLastPush = 0
		}
	}
	if len(d.Pushes) > 0 {
		d.ChangesAfterLastPush = changesSinceLastPush
	}
	if d.Changes > 0 && verified != nil {
		d.Verified, d.VerifiedAt, d.VerifiedOp, d.VerifiedBy, d.VerifiedKinds = true, verified.Start, verified.ID, verifiedBy, verifiedKinds
	}
	if lastVerdict != nil {
		d.LastVerdictOp, d.LastVerdictFailed = lastVerdict.ID, lastVerdict.Failure()
	}
	for _, l := range s.Lanes {
		for _, t := range l.Turns {
			if !reviewTurn(t) {
				continue
			}
			d.ReviewedAt = max(d.ReviewedAt, t.End)
		}
	}
	if d.ReviewedAt > 0 {
		for _, o := range all {
			if classify.IsChangeOp(o.Phase, o.Kind) && o.Start > d.ReviewedAt {
				d.ChangesAfterReview++
			}
		}
	}
	if d.Changes > 0 {
		d.BlindAfterLastChangeMs = blindMs(root, d.LastChangeAt, s.Ended)
	}
	for _, l := range s.Lanes {
		windows := changeWindows(l)
		for turn, w := range windows {
			d.EditTurns++
			ok := false
			for _, o := range l.Ops {
				if o.Turn != turn || o.Start < w.last {
					continue
				}
				if _, isVerification := verificationOp(o); isVerification {
					ok = true
					break
				}
			}
			if !ok {
				d.EditTurnsUnverified++
			}
			if l == root {
				d.UnknownInWindowMs += blindMs(root, w.first, w.last)
			}
		}
	}
	return d
}

// reviewTurn reports whether a turn carried a review run: the harness injected a review skill,
// the whole turn is review (Codex review mode, a review-role lane) or a run inside it is.
func reviewTurn(t *model.Turn) bool {
	if t.Review || t.Lifecycle == classify.LcReview {
		return true
	}
	for _, r := range t.Runs {
		if r.Lifecycle == classify.LcReview {
			return true
		}
	}
	return false
}

// windowRecovery names what ran between the failed attempt and the retry of group: fix (a
// change op on any lane — session scope, a sub-agent's edit is the session's edit), and on the
// failing lane infra (an infra op), worker (a wait for workers), other (anything else that ran
// and could have changed the outcome: a stash, a checkout, a narrowed test, a build, an unknown
// command — every op that is not a query, not model output, not a wait for the user, not a
// compaction and not an attempt of this group), none (reads and queries only), or mixed when
// several of those ran.
func windowRecovery(s *model.Session, l *model.Lane, group string, from, to int64) string {
	kinds := map[string]bool{}
	inside := func(o *model.Operation) bool { return !o.Background && o.Start >= from && o.Start < to }
	for _, lane := range s.Lanes {
		for _, o := range lane.Ops {
			if inside(o) && classify.IsChangeOp(o.Phase, o.Kind) {
				kinds["fix"] = true
			}
		}
	}
	for _, o := range l.Ops {
		if !inside(o) || o.Group == group && o.Attempt > 0 {
			continue // this group's own attempts are the window's ends, never a step inside it
		}
		switch {
		case classify.IsChangeOp(o.Phase, o.Kind): // counted above, on every lane
		case o.Phase == classify.Infra:
			kinds["infra"] = true
		case o.Phase == classify.WaitWorker:
			kinds["worker"] = true
		case o.Phase == classify.LLM || o.Phase == classify.WaitUser || o.Phase == classify.Compaction || o.Phase == classify.NoTelemetry || o.Phase == classify.Idle:
		case classify.QueryKind(o.Phase, o.Kind):
		default:
			kinds["other"] = true
		}
	}
	switch len(kinds) {
	case 0:
		return "none"
	case 1:
		for k := range kinds {
			return k
		}
	}
	return "mixed"
}

// userTurnInside reports whether a user-triggered root turn started inside (from, to].
func userTurnInside(root *model.Lane, from, to int64) bool {
	for _, t := range root.Turns {
		if t.Trigger != "system" && t.Start > from && t.Start <= to {
			return true
		}
	}
	return false
}
