package insights

import "fmt"

// D9 · Long tool runs. Signal: a test, build, release or infra command. Exposure: its time,
// keyed by command shape; the report keeps only shapes that ran at least twice in the period
// (a repeated long command is where an incremental or remote variant pays). Listed, never
// explained (product rule 1).
func detectLongRuns(f *Facts) Result {
	r := Result{Measurable: true}
	for _, o := range f.LongOps {
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(o.Lane), LaneID: f.laneID(o.Lane), A: o.Start, B: o.End, Op: o.ID, TimeMs: o.End - o.Start, Key: o.Shape, Note: o.Title})
	}
	return r
}

// D12 · Processes left running. Signal: an op that outlived its turn (a server, a watcher).
// Exposure: how long it kept running.
func detectBackground(f *Facts) Result {
	r := Result{Measurable: true}
	for _, o := range f.Background {
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(o.Lane), LaneID: f.laneID(o.Lane), A: o.Start, B: o.End, Op: o.ID, TimeMs: o.End - o.Start, Key: o.Shape, Note: fmt.Sprintf("%s: kept running after its turn ended", o.Title)})
	}
	return r
}

// D26 · Sleeps and polling loops. Signal: a root wait the agent wrote itself — a `sleep N`, a
// `while … sleep` / `until … sleep` loop, a `tail -f` — as opposed to the harness's own wait
// for a sub-agent, a CI watch or a stop hook. Exposure: the time of each such wait, keyed by
// its kind; the per-run average on the card says which intervals the agent chose. The agent
// picks N; the harness has no say beyond the tool's timeout — what it waited for is not
// inferred (product rule 1).
func detectSleeps(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, w := range f.Waits {
		key, ok := sleepKinds[w.Kind]
		if !ok {
			continue
		}
		r.Stats[w.Kind]++
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: w.Start, B: w.End, TimeMs: w.End - w.Start, Key: key, Note: key + " · " + durationText(w.End-w.Start)})
	}
	return r
}

// sleepKinds are the wait kinds the agent's own command produced, with their card labels.
var sleepKinds = map[string]string{"sleep": "sleep", "poll-loop": "poll loop", "tail-f": "tail -f"}

// durationText prints a wait's length the way the notes do: seconds under two minutes,
// minutes and seconds otherwise.
func durationText(ms int64) string {
	s := ms / 1000
	if s < 120 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %d s", s/60, s%60)
}
