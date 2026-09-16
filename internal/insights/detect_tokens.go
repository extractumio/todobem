package insights

import "fmt"

// Gap buckets shared by D2 and T1 (minutes; a display convention, printed on the card).
var gapBuckets = []struct {
	label string
	maxMs int64
}{
	{"under 5 min", 5 * 60e3},
	{"5-15 min", 15 * 60e3},
	{"15-60 min", 60 * 60e3},
	{"1-4 h", 4 * 3600e3},
	{"4 h or more", 1 << 62},
}

// GapBucket names the bucket a gap of ms falls in.
func GapBucket(ms int64) string {
	for _, b := range gapBuckets {
		if ms < b.maxMs {
			return b.label
		}
	}
	return gapBuckets[len(gapBuckets)-1].label
}

// GapBucketOrder lists the bucket labels in order for tables.
func GapBucketOrder() []string {
	out := make([]string, 0, len(gapBuckets))
	for _, b := range gapBuckets {
		out = append(out, b.label)
	}
	return out
}

// T1 · Cache after a break. Signal: a root turn that started after a wait for the user, and
// the usage of its first model call. Exposure per finding: the gap length (time) and the first
// call (tokens; its uncached input is what the model re-read). Key: the gap bucket. A turn with
// no usage record is counted as no data; a session with no usage records is not measurable.
func detectCacheAfterBreak(f *Facts) Result {
	r := Result{}
	for _, t := range f.Turns {
		if t.Lane == 0 && t.Tokens != nil {
			r.Measurable = true
			break
		}
	}
	if !r.Measurable {
		r.Reason = "no token usage records on the root lane"
		return r
	}
	for _, g := range f.Gaps {
		gap := g.End - g.Start
		if g.NextTurn == "" || g.NextTrigger == "system" || gap < MinReplyMs {
			continue // the session's tail, a harness-triggered turn or a queued message: not a break
		}
		if g.NextFirst == nil || g.NextFirst.Input <= 0 {
			r.NoData++
			continue
		}
		uncached := g.NextFirst.Input - g.NextFirst.Cached
		note := fmt.Sprintf("break of %s; first call after it: %d k input, %d k not cached", fmtDur(gap), g.NextFirst.Input/1000, uncached/1000)
		// a tokens card: the break itself is counted by D2 / D2b, so no time exposure here
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: g.Start, B: g.End, Tokens: g.NextFirst, Key: GapBucket(gap), Note: note})
	}
	return r
}

// fmtDur formats a duration for a note: 2h 10m, 25m, 40s.
func fmtDur(ms int64) string {
	sec := ms / 1000
	h, m := sec/3600, (sec%3600)/60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", sec)
}
