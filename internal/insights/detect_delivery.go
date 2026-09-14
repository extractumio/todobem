package insights

import (
	"fmt"
	"strconv"

	"github.com/extractumio/todobem/internal/model"
)

// D4 · Sub-agents ran one after another. Signal: a root wait for sub-agents (wait_agent or the
// harness's wait tool) during which at most one sub-agent lane was inside a turn. Exposure:
// that part of the wait. Whether the sub-agents depended on each other is not on disk.
func detectSerialDelegation(f *Facts) Result {
	r := Result{Measurable: true}
	for _, w := range f.Waits {
		if w.Kind != "agent" && w.Kind != "wait" || w.SoloMs <= 0 {
			continue
		}
		key := "none"
		note := "no sub-agent was inside a turn"
		if len(w.Lanes) == 1 {
			key = f.lanePath(w.Lanes[0])
			note = "one sub-agent at a time: " + key
		} else if len(w.Lanes) > 1 {
			key = "several, one at a time"
			note = strconv.Itoa(len(w.Lanes)) + " sub-agents, at most one inside a turn at any moment"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: w.Start, B: w.End, TimeMs: w.SoloMs, Key: key, Note: note})
	}
	return r
}

// D7 · Commands that fail and get retried. Signal: a retry group with at least one failed
// attempt (exact identity). Exposure: time in the retry, fix, recovery and queue roles, plus
// the tokens of the turns overlapping the failed-to-retry windows. Key: the command shape.
func detectRetryLoops(f *Facts) Result {
	r := Result{Measurable: true}
	for _, g := range f.Groups {
		if g.Failed == 0 {
			continue
		}
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

// D11 · Context compaction pauses. Signal: a compaction op. Exposure: its duration and the
// first model call after it (the re-read). Key: main thread or sub-agents, never mixed in one total.
func detectCompactions(f *Facts) Result {
	r := Result{Measurable: true}
	for _, c := range f.Compactions {
		key := "sub-agents"
		if c.Lane == 0 {
			key = "main thread"
		}
		note := "context before: not recorded"
		if c.Context > 0 {
			note = fmt.Sprintf("context before: %d k tokens", c.Context/1000)
		}
		if c.Reread == nil {
			r.NoData++
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(c.Lane), LaneID: f.laneID(c.Lane), A: c.Start, B: c.End, Op: c.Op, TimeMs: c.End - c.Start, Tokens: c.Reread, Key: key, Note: note})
	}
	return r
}
