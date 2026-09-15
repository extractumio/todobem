package insights

import "fmt"

// D13 · Not measured. Signal: a command no rule matched (phase unknown). Exposure: its time,
// keyed by the command head; the card lists the heads so a user can add overlay rules, and says
// how much of it was an opaque script, a Claude Code tool without a mapping, or a command no rule
// matched (the unknown subgroups). The session's time with no telemetry is reported as a number,
// not as findings (it has no op).
func detectUnknown(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{"no_telemetry_ms": f.Root.ByPhase["no_telemetry"], "unknown_ms": f.Root.ByPhase["unknown"]}}
	for sub, ms := range f.UnknownMs {
		if sub != "" {
			r.Stats["unknown_"+sub+"_ms"] += ms
		}
	}
	for _, o := range f.UnknownOps {
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(o.Lane), LaneID: f.laneID(o.Lane), A: o.Start, B: o.End, Op: o.ID, TimeMs: o.End - o.Start, Key: o.Kind, Note: o.Title})
	}
	return r
}

// D14 · Invalid tool calls. Signal: the model produced arguments the harness could not parse
// (an llm_error marker). Exposure: the count; each is a point in time.
func detectInvalidToolCalls(f *Facts) Result {
	r := Result{Measurable: true}
	for _, e := range f.LLMErrorAt {
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(e.Lane), LaneID: f.laneID(e.Lane), A: e.T, B: e.T, Key: "invalid tool call", Note: "the harness could not parse the model's tool call"})
	}
	return r
}

// D15 · Tool calls that failed. Signal: a failed step in the code phase — an edit, a patch, a
// script, a shell, git, code-hosting or network call (query misses and CI status waits are
// answers, excluded by Operation.Failure). Exposure: the count, keyed by what the call did (the
// subgroup: "code:edit", "code:shell", …); the note names the command and its kind.
func detectFailedEdits(f *Facts) Result {
	r := Result{Measurable: true}
	for _, o := range f.Failures {
		note := o.Title
		if o.Exit != nil {
			note = fmt.Sprintf("%s (exit %d)", o.Title, *o.Exit)
		}
		if note == "" {
			note = o.Kind
		} else {
			note += " · " + o.Kind
		}
		key := o.Kind
		if o.Sub != "" {
			key = string(o.Phase) + ":" + o.Sub
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(o.Lane), LaneID: f.laneID(o.Lane), A: o.Start, B: o.End, Op: o.ID, TimeMs: o.End - o.Start, Key: key, Note: note})
	}
	return r
}
