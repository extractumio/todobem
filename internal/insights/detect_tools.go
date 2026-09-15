package insights

import "fmt"

// D16 · What the tool calls did. Signal: the main thread's tool calls by phase and subgroup —
// reading files, searching, editing, git, code hosting, network, shell; build, test, release,
// infrastructure; waits for sub-agents, polling, hooks; unknown scripts, tools and commands.
// Exposure: their exclusive time (the partition, so it sums with the model's time to the time
// in turns); each finding stands for a session's calls of one subgroup (N), with its query
// misses (a search that found nothing, a CI status still pending) and failed steps in the note.
// A measurement — the mix says what the agent spent its calls on, never why.
func detectToolCalls(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, t := range f.ToolCalls {
		if t.Calls == 0 {
			continue
		}
		key := string(t.Phase)
		if t.Sub != "" {
			key += ":" + t.Sub
		}
		r.Stats["calls_"+key] += int64(t.Calls)
		r.Stats["misses_"+key] += int64(t.Misses)
		r.Stats["failed_"+key] += int64(t.Failed)
		note := fmt.Sprintf("%d calls", t.Calls)
		if t.Calls == 1 {
			note = "1 call"
		}
		if t.Misses > 0 {
			note += fmt.Sprintf(", %d found nothing", t.Misses)
		}
		if t.Failed > 0 {
			note += fmt.Sprintf(", %d failed", t.Failed)
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: f.Started, B: f.Ended, TimeMs: t.Ms, Key: key, Note: note, N: t.Calls})
	}
	return r
}
