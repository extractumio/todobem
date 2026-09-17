package insights

import (
	"fmt"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// D7 · Commands that fail and get retried. Signal: a retry group with at least one failed
// attempt (exact identity). Exposure: time in the retry, fix, recovery and queue roles, plus
// the tokens of the turns overlapping the failed-to-retry windows. Key: the command shape.
// Stats, per failed-to-retry window: what ran between the failure and the retry — only reads
// and queries (a blind retry, unless a user turn started in between), a fix on any lane, an
// infra step, a wait, another step (a stash, a checkout, a narrowed test, a build), or several
// of those — how many retries failed again after a recorded recovery, and how many blind test
// retries passed (the log shows nothing that changed the code between the failure and the pass).
func detectRetryLoops(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, g := range f.Groups {
		if g.Failed == 0 {
			continue
		}
		r.Stats["groups"]++
		r.Stats["attempts"] += int64(g.Attempts)
		ms := g.RoleMs["retry_after_failure"] + g.RoleMs["fix"] + g.RoleMs["infra_recovery"] + g.RoleMs["worker_queue"]
		var tokens model.TokenUsage
		paths := map[string]int{}
		for _, w := range g.Windows {
			tokens.Add(&w.Tokens)
			r.Stats["windows"]++
			path := windowPath(w)
			paths[path]++
			r.Stats["windows_"+path]++
			if w.RetryFailed && path != "blind" && path != "after_user" {
				r.Stats["retries_failed_again"]++
			}
			if path == "blind" && !w.RetryFailed && g.Phase == classify.Test {
				r.Stats["windows_blind_passed"]++ // a test failed, then passed with only reads between: the environment, not the code, changed — or nothing did
			}
		}
		lane := 0
		if len(g.Lanes) > 0 {
			lane = g.Lanes[0]
		}
		fd := Finding{Lane: f.lanePath(lane), LaneID: f.laneID(lane), A: g.Start, B: g.End, TimeMs: ms, Key: g.Shape, Note: fmt.Sprintf("%d attempts, %d failed: %s%s", g.Attempts, g.Failed, g.Title, pathsNote(paths))}
		if tokens.Total > 0 {
			t := tokens
			fd.Tokens = &t
		}
		r.Findings = append(r.Findings, fd)
	}
	return r
}

// windowPath names a failed-to-retry window by what ran inside it: blind (nothing, no user turn
// either), after_user (nothing recorded, but a user turn started in between), fix, infra, worker
// or mixed (the recorded recovery).
func windowPath(w WindowFacts) string {
	if w.Recovery == "none" || w.Recovery == "" {
		if w.HumanBoundary {
			return "after_user"
		}
		return "blind"
	}
	return w.Recovery
}

var pathWords = map[string]string{"blind": "retried with only reads recorded between", "after_user": "retried after your message", "fix": "retried after a fix", "infra": "retried after an infra step", "worker": "retried after a wait", "other": "retried after another step", "mixed": "retried after several kinds of step"}

// pathsNote words the recovery paths of a group's windows for the evidence row.
func pathsNote(paths map[string]int) string {
	out := ""
	for _, p := range []string{"blind", "after_user", "fix", "infra", "worker", "other", "mixed"} {
		if n := paths[p]; n > 0 {
			out += fmt.Sprintf("; %d× %s", n, pathWords[p])
		}
	}
	return out
}
