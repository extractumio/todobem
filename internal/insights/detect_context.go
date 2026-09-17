package insights

import "fmt"

// D11 · Context compaction pauses. Signal: a compaction op. Exposure: its duration and the
// first model call after it (the re-read). Key: main thread or sub-agents, never mixed in one
// total. Stats: how many sat between two edits of one turn (the model lost its context in the
// middle of an implementation loop) and their time.
func detectCompactions(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, c := range f.Compactions {
		key := "sub-agents"
		if c.Lane == 0 {
			key = "main thread"
		}
		note := "context before: not recorded"
		if c.Context > 0 {
			note = fmt.Sprintf("context before: %d k tokens", c.Context/1000)
		}
		if c.Reread == nil {
			r.NoData++
		}
		if c.InChangeWindow {
			r.Stats["in_change_window"]++
			r.Stats["in_change_window_ms"] += c.End - c.Start
			note += "; between two edits of one turn"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(c.Lane), LaneID: f.laneID(c.Lane), A: c.Start, B: c.End, Op: c.Op, TimeMs: c.End - c.Start, Tokens: c.Reread, Key: key, Note: note, Value: c.Context})
	}
	return r
}

// Context buckets for T2 (thousands of tokens; a display convention). The scale continues past
// 200 k because 1M-context models put most turns there: on the first corpus 1,024 of 1,733 root
// turns sat in a single "200 k or more" bucket.
var contextBuckets = []struct {
	label string
	max   int64
}{
	{"under 50 k", 50000},
	{"50-100 k", 100000},
	{"100-150 k", 150000},
	{"150-200 k", 200000},
	{"200-500 k", 500000},
	{"500-750 k", 750000},
	{"750 k or more", 1 << 62},
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
