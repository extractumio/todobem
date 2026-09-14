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
