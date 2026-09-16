package insights

import (
	"fmt"
	"strconv"
)

// D4 · Sub-agents ran one after another. Signal: a root wait for sub-agents (wait_agent or the
// harness's wait tool) during which at most one sub-agent lane was inside a turn. Exposure:
// that part of the wait. Whether the sub-agents depended on each other is not on disk.
func detectSerialDelegation(f *Facts) Result {
	if len(f.Agents) < 3 {
		return Result{NotApplicable: true} // fewer than two sub-agents: nothing could have run in parallel
	}
	r := Result{Measurable: true}
	for _, w := range f.Waits {
		if w.Kind != "agent" && w.Kind != "wait" || w.SoloMs <= 0 {
			continue
		}
		key := "no sub-agent inside a turn"
		note := "no sub-agent was inside a turn"
		if len(w.Lanes) == 1 {
			key = "one sub-agent at a time"
			note = "one sub-agent at a time: " + f.lanePath(w.Lanes[0])
		} else if len(w.Lanes) > 1 {
			key = "several, one at a time"
			note = strconv.Itoa(len(w.Lanes)) + " sub-agents, at most one inside a turn at any moment"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: w.Start, B: w.End, TimeMs: w.SoloMs, Key: key, Note: note})
	}
	return r
}

// T6 · Cost to start a sub-agent. Signal: the first model call of a sub-agent's first turn
// (the instructions and context it re-reads). Exposure: those tokens, keyed by agent type;
// the note compares them with the sub-agent's whole usage.
func detectSpawnCost(f *Facts) Result {
	if len(f.Agents) <= 1 {
		return Result{NotApplicable: true} // no sub-agent was started
	}
	r := Result{Stats: map[string]int64{}}
	for _, a := range f.Agents[1:] {
		if a.Tokens == nil {
			continue
		}
		r.Measurable = true
		if a.First == nil || a.First.Input <= 0 {
			r.NoData++
			continue
		}
		work := a.Tokens.Total - a.First.Total
		note := fmt.Sprintf("start %d k of %d k tokens", a.First.Total/1000, a.Tokens.Total/1000)
		if a.First.Total > work {
			note += " — more to start than to work"
			r.Stats["more_to_start"]++
		}
		r.Findings = append(r.Findings, Finding{Lane: a.Path, LaneID: a.ID, A: a.Started, B: a.Ended, Tokens: a.First, Key: laneKindLabel(a.Kind), Note: note})
	}
	if !r.Measurable {
		r.Reason = "the sub-agents have no token usage records"
	}
	return r
}
