package insights

import "fmt"

// LongBreakMs is the display convention that splits "time to your reply" (D2) from "long
// breaks" (D2b): 4 hours. It never explains anything; it is printed on both cards.
const LongBreakMs = 4 * 3600e3

// D1 · The agent waited for your answer. Signal: a wait for the user right after a turn in
// which the agent asked a question. Exposure: the wait. Stats: the questions asked after the
// session had already changed a file, and their waits (a decision that arrived mid-work).
func detectWaitingOnAnswer(f *Facts) Result {
	r := Result{Measurable: true, Stats: map[string]int64{}}
	for _, g := range f.Gaps {
		if !g.AfterQuestion || g.NextTurn == "" {
			continue
		}
		note := "the agent asked a question before this wait"
		if g.AfterFirstChange {
			note += "; files had already been changed"
			r.Stats["after_changes"]++
			r.Stats["after_changes_ms"] += g.End - g.Start
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: g.Start, B: g.End, TimeMs: g.End - g.Start, Key: GapBucket(g.End - g.Start), Note: note})
	}
	return r
}

// D2 · Time to your reply. Signal: a wait for the user shorter than the long-break convention,
// ended by a turn you started. Exposure: the wait; the card shows count, median and buckets.
func detectReplyLatency(f *Facts) Result {
	r := Result{Measurable: true}
	for _, g := range f.Gaps {
		gap := g.End - g.Start
		if g.NextTurn == "" || g.NextTrigger == "system" || gap >= LongBreakMs {
			continue
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: g.Start, B: g.End, TimeMs: gap, Key: GapBucket(gap)})
	}
	return r
}

// D2b · Long breaks. Signal: a wait for the user of 4 hours or more, ended by a turn you
// started. Listed for the honesty of the elapsed-time picture; never ranked (Info).
func detectLongBreaks(f *Facts) Result {
	r := Result{Measurable: true}
	for _, g := range f.Gaps {
		gap := g.End - g.Start
		if g.NextTurn == "" || g.NextTrigger == "system" || gap < LongBreakMs {
			continue
		}
		note := "the agent had no work during this break"
		if g.NextFirst != nil && g.NextFirst.Input > 0 {
			note = fmt.Sprintf("first call after the break: %d k input, %d k not cached", g.NextFirst.Input/1000, (g.NextFirst.Input-g.NextFirst.Cached)/1000)
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(0), LaneID: f.laneID(0), A: g.Start, B: g.End, TimeMs: gap, Key: "long break", Note: note})
	}
	return r
}

// D3 · Turns you stopped. Signal: a turn with status aborted. Exposure: the time inside it and
// its tokens; the main thread and sub-agents are separate rows.
func detectStoppedTurns(f *Facts) Result {
	r := Result{Measurable: true}
	for _, t := range f.Turns {
		if t.Status != "aborted" || t.End <= t.Start {
			continue
		}
		key := "sub-agents"
		if t.Lane == 0 {
			key = "main thread"
		}
		r.Findings = append(r.Findings, Finding{Lane: f.lanePath(t.Lane), LaneID: f.laneID(t.Lane), A: t.Start, B: t.End, TimeMs: t.End - t.Start, Tokens: t.Tokens, Key: key, Note: fmt.Sprintf("%d model calls before the stop", t.Responses)})
	}
	return r
}
