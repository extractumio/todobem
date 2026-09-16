package insights

import (
	"sort"

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
	Tests      int    `json:"tests"` // test ops in the session, any result
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
	// Per-turn aggregates: turns with a change op, those with no successful test in the same
	// turn at or after their last change, and the unknown + no_telemetry time inside the root
	// lane's change windows.
	EditTurns           int   `json:"edit_turns"`
	EditTurnsUnverified int   `json:"edit_turns_unverified"`
	UnknownInWindowMs   int64 `json:"unknown_in_window_ms,omitempty"`
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
	var lastVerdict *model.Operation
	for _, o := range all {
		if classify.IsChangeOp(o.Phase, o.Kind) {
			d.Changes++
			if d.FirstChangeAt == 0 {
				d.FirstChangeAt = o.Start
			}
			d.LastChangeAt, d.LastChangeOp, d.LastChangeLane = o.Start, o.ID, laneIndex[o.Lane]
			verified, verifiedBy = nil, "" // a later change invalidates the verification seen so far
			continue
		}
		if o.Phase == classify.Test {
			d.Tests++
			lastVerdict = o
		}
		if by, ok := verificationOp(o); ok && verified == nil {
			verified, verifiedBy = o, by
		}
	}
	if d.Changes > 0 && verified != nil {
		d.Verified, d.VerifiedAt, d.VerifiedOp, d.VerifiedBy = true, verified.Start, verified.ID, verifiedBy
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

// windowRecovery names what the failed attempt's lane ran between the failure and the retry:
// none, fix (a change op), infra (an infra op), worker (a wait for workers), or mixed.
func windowRecovery(l *model.Lane, from, to int64) string {
	kinds := map[string]bool{}
	for _, o := range l.Ops {
		if o.Background || o.Start < from || o.Start >= to || o.Attempt > 0 {
			continue // an attempt of any group is never also a recovery step (as in model.assignGroups)
		}
		switch {
		case classify.IsChangeOp(o.Phase, o.Kind):
			kinds["fix"] = true
		case o.Phase == classify.Infra:
			kinds["infra"] = true
		case o.Phase == classify.WaitWorker:
			kinds["worker"] = true
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
