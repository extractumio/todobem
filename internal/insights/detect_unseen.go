package insights

import "fmt"

// D13 · Not measured. Signal: a command no rule matched (phase unknown). Exposure: its time,
// keyed by the command head; the card lists the heads so a user can add overlay rules, and says
// how much of it was an opaque script, a Claude Code tool without a mapping, or a command no rule
// matched (the unknown subgroups), and how much sat inside the main thread's change windows —
// the unknown time a rule would pay off first. The session's time with no telemetry is reported
// as a number, not as findings (it has no op).
func detectUnknown(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{"no_telemetry_ms": f.Root.ByPhase["no_telemetry"], "unknown_ms": f.Root.ByPhase["unknown"], "unknown_in_change_window_ms": f.Delivery.UnknownInWindowMs}}
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

// D18 · Time with no telemetry. Signal: a root turn that never closed — the log ends inside it
// (status open) or the next prompt arrived before it closed (orphaned) — and the time after its
// last event that the log says nothing about (the no_telemetry phase). A measurement (Info) so
// the reader can find it: one finding per interval, keyed by source. Says nothing about what
// happened then: a crash, a kill and a closed laptop leave the same record (product rule 1).
func detectNoTelemetry(f *Facts) Result {
	r := Result{Measurable: true}
	for _, b := range f.Blind {
		note := "the log ends inside this turn"
		if b.Status == "orphaned" {
			note = "the next prompt arrived before this turn closed"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: b.Start, B: b.End, TimeMs: b.End - b.Start, Key: sourceLabel(f.Source), Note: note})
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
