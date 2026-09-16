package insights

import (
	"fmt"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

func modelKey(m, effort string) string {
	if m == "" {
		m = "unknown model"
	}
	if effort == "" {
		return m
	}
	return m + " / " + effort
}

// M1 · Model time by model, effort and stage. Signal: the model-output segments of the main
// agent's turns. Exposure: that time (the largest in-turn phase), keyed by model and effort;
// the split by the stage each segment served is in the card's stats ("stage_<lc>"), and the
// output of turns that made no tool call — not a stage — is "no_tool_call_ms". Model output
// nearest an unknown command counts as "unknown_ms". A measurement, actionable through
// harness settings.
func detectModelTime(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, t := range f.Turns {
		if t.Lane != 0 || t.LLMMs <= 0 {
			continue
		}
		for lc, ms := range t.LLMByLifecycle {
			switch {
			case classify.IsWorkLifecycle(lc):
				r.Stats["stage_"+string(lc)] += ms
			case lc == model.Lifecycle(classify.LLM):
				r.Stats["no_tool_call_ms"] += ms
			default:
				r.Stats["unknown_ms"] += ms // model output nearest an unknown command, or a gap's edge
			}
		}
		top, topMs := model.Lifecycle(""), int64(0)
		for lc, ms := range t.ByLifecycle {
			if classify.IsWorkLifecycle(lc) && ms > topMs {
				top, topMs = lc, ms
			}
		}
		note := fmt.Sprintf("%d model calls", t.Responses)
		if top != "" {
			note += ", mostly " + string(top)
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: t.Start, B: t.End, TimeMs: t.LLMMs, Tokens: t.Tokens, Key: modelKey(t.Model, t.Effort), Note: note})
	}
	return r
}

// T3 · Tokens by model, effort and agent type. Signal: each lane's tokens with its model and
// effort and what its ops literally did. Exposure: the lane's tokens, keyed by agent type,
// model and effort; read-only sub-agents on the default model are the usual candidate for a
// cheaper model. No substitution value is computed.
func detectTokensByModel(f *Facts) Result {
	r := Result{}
	for i, a := range f.Agents {
		if a.Tokens == nil || a.Tokens.Total <= 0 {
			continue
		}
		r.Measurable = true
		mdl, effort := a.Model, ""
		for _, t := range f.Turns {
			if t.Lane != i {
				continue
			}
			if effort == "" && t.Effort != "" {
				effort = t.Effort
			}
			if mdl == "" && t.Model != "" {
				mdl = t.Model // the model actually used by the lane's turns
			}
		}
		key := laneKindLabel(a.Kind) + " · " + modelKey(mdl, effort)
		r.Findings = append(r.Findings, Finding{Lane: a.Path, LaneID: a.ID, A: a.Started, B: a.Ended, Tokens: a.Tokens, Key: key, Note: fmt.Sprintf("%d ops, %d turns, %s in turns", a.Ops, a.Turns, fmtDur(a.InTurnMs))})
	}
	if !r.Measurable {
		r.Reason = "no token usage records"
	}
	return r
}

// T7 · Reasoning tokens. Signal: each lane's reasoning share of its output tokens, by effort.
// A measurement (Info), keyed by effort.
func detectReasoningShare(f *Facts) Result {
	r := Result{}
	for i, a := range f.Agents {
		if a.Tokens == nil || a.Tokens.Output <= 0 {
			continue
		}
		r.Measurable = true
		effort := ""
		for _, t := range f.Turns {
			if t.Lane == i && t.Effort != "" {
				effort = t.Effort
				break
			}
		}
		if effort == "" {
			effort = "unknown effort"
		}
		r.Findings = append(r.Findings, Finding{Lane: a.Path, LaneID: a.ID, A: a.Started, B: a.Ended, Tokens: a.Tokens, Key: effort, Note: fmt.Sprintf("reasoning %d %% of output", a.Tokens.Reasoning*100/max(1, a.Tokens.Output))})
	}
	if !r.Measurable {
		r.Reason = "no token usage records"
	}
	return r
}
