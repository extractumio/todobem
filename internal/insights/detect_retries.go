package insights

import (
	"fmt"

	"github.com/extractumio/todobem/internal/model"
)

// D7 · Commands that fail and get retried. Signal: a retry group with at least one failed
// attempt (exact identity). Exposure: time in the retry, fix, recovery and queue roles, plus
// the tokens of the turns overlapping the failed-to-retry windows. Key: the command shape.
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
		for _, w := range g.Windows {
			tokens.Add(&w.Tokens)
		}
		lane := 0
		if len(g.Lanes) > 0 {
			lane = g.Lanes[0]
		}
		fd := Finding{Lane: f.lanePath(lane), LaneID: f.laneID(lane), A: g.Start, B: g.End, TimeMs: ms, Key: g.Shape, Note: fmt.Sprintf("%d attempts, %d failed: %s", g.Attempts, g.Failed, g.Title)}
		if tokens.Total > 0 {
			t := tokens
			fd.Tokens = &t
		}
		r.Findings = append(r.Findings, fd)
	}
	return r
}
