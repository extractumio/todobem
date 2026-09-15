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

// Context buckets for T2 (thousands of tokens; a display convention).
var contextBuckets = []struct {
	label string
	max   int64
}{
	{"under 50 k", 50000},
	{"50-100 k", 100000},
	{"100-150 k", 150000},
	{"150-200 k", 200000},
	{"200 k or more", 1 << 62},
}

func ContextBucket(peak int64) string {
	for _, b := range contextBuckets {
		if peak < b.max {
			return b.label
		}
	}
	return contextBuckets[len(contextBuckets)-1].label
}

func ContextBucketOrder() []string {
	out := make([]string, 0, len(contextBuckets))
	for _, b := range contextBuckets {
		out = append(out, b.label)
	}
	return out
}

// T2 · Context size. Signal: the largest input of one model call in each turn (the context it
// reached). Exposure: the turn's tokens, keyed by context bucket.
func detectContextSize(f *Facts) Result {
	r := Result{}
	for _, t := range f.Turns {
		if t.ContextPeak <= 0 {
			continue
		}
		r.Measurable = true
		perCall := int64(0)
		if t.Responses > 0 && t.Tokens != nil {
			perCall = t.Tokens.Input / int64(t.Responses)
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(t.Lane), LaneID: f.laneID(t.Lane), A: t.Start, B: t.End, Tokens: t.Tokens, Key: ContextBucket(t.ContextPeak), Note: fmt.Sprintf("turn of %s: peak %d k, %d calls, about %d k input each", fmtDur(t.End-t.Start), t.ContextPeak/1000, t.Responses, perCall/1000)})
	}
	if !r.Measurable {
		r.Reason = "no token usage records"
	}
	return r
}

func laneKindLabel(kind string) string {
	switch kind {
	case LaneRoot:
		return "main thread"
	case LaneReadOnly:
		return "read-only sub-agents"
	}
	return "worker sub-agents"
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

// T6 · Cost to start a sub-agent. Signal: the first model call of a sub-agent's first turn
// (the instructions and context it re-reads). Exposure: those tokens, keyed by agent type;
// the note compares them with the sub-agent's whole usage.
func detectSpawnCost(f *Facts) Result {
	r := Result{Stats: map[string]int64{}}
	for _, a := range f.Agents[min(1, len(f.Agents)):] {
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
		r.Reason = "no sub-agents with token usage records"
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
