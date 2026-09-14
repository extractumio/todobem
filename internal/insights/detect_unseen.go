package insights

import "fmt"

// D13 · Not measured. Signal: a command no rule matched (phase unknown). Exposure: its time,
// keyed by the command head; the card lists the heads so a user can add overlay rules. The
// session's time with no telemetry is reported as a number, not as findings (it has no op).
func detectUnknown(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{"no_telemetry_ms": f.Root.ByPhase["no_telemetry"], "unknown_ms": f.Root.ByPhase["unknown"]}}
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

// D15 · Edits that failed. Signal: a failed step in the code phase — an edit, a patch, a
// script, a shell command (query misses excluded by Operation.Failure). Exposure: the count,
// keyed by kind.
func detectFailedEdits(f *Facts) Result {
	r := Result{Measurable: true}
	for _, o := range f.Failures {
		note := o.Title
		if o.Exit != nil {
			note = fmt.Sprintf("%s (exit %d)", o.Title, *o.Exit)
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(o.Lane), LaneID: f.laneID(o.Lane), A: o.Start, B: o.End, Op: o.ID, TimeMs: o.End - o.Start, Key: o.Kind, Note: note})
	}
	return r
}
