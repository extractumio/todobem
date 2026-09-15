package model

import (
	"fmt"
	"sort"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
)

// Derive recomputes everything that depends on the full set of lanes:
// segments, stages, groups, totals. It is O(ops log ops) and runs only when a file grew.
func Derive(s *Session, now int64) {
	s.Now = now
	if s.Groups == nil {
		s.Groups = []Group{}
	}
	if len(s.Lanes) == 0 {
		return
	}
	for _, l := range s.Lanes {
		// JSON must carry [] not null for every list the UI iterates
		if l.Turns == nil {
			l.Turns = []*Turn{}
		}
		if l.Ops == nil {
			l.Ops = []*Operation{}
		}
		if l.Markers == nil {
			l.Markers = []Marker{}
		}
		if l.Segments == nil {
			l.Segments = []Segment{}
		}
	}
	root := s.Lanes[0]
	s.Live = false
	for _, l := range s.Lanes {
		l.Live = len(l.Turns) > 0 && l.Turns[len(l.Turns)-1].Status == "open"
		if l.Live && l.Ended < now {
			l.Ended = now
		}
	}
	s.Live = root.Live
	s.Started = root.Started
	s.Ended = root.Ended
	for _, l := range s.Lanes[1:] {
		if l.Ended > s.Ended && l.Live {
			s.Ended = l.Ended
		}
	}
	for _, l := range s.Lanes {
		// a review / cleanup skill invoked anywhere in the turn: Turn.Skill is the first skill the
		// adapter saw, a skill marker records every invocation (Claude Code can invoke several)
		reviewIn := map[string]bool{}
		for _, m := range l.Markers {
			if m.Kind == "skill" && classify.ReviewSkill(m.Ref) {
				reviewIn[m.Turn] = true
			}
		}
		for _, t := range l.Turns {
			t.Review = classify.ReviewSkill(t.Skill) || reviewIn[t.ID]
		}
		for _, o := range l.Ops {
			// a query kind's failure is an answer, not a failed step (before groups: the retry
			// roles read Operation.Failure). Codex records the exit code; Claude Code records only
			// that the tool errored (status "failed"): both are the harness's word.
			o.QueryMiss = classify.QueryKind(o.Phase, o.Kind) && (o.Status == "failed" || o.Exit != nil && *o.Exit != 0)
			o.Subgroup = classify.Subgroup(o.Phase, o.Kind)
		}
		extendPendingOperations(l, now)
		markBackground(l, now)
	}
	assignGroups(s)
	laneByID := map[string]*Lane{}
	for _, l := range s.Lanes { // parents come before their children (source.Session orders them)
		laneByID[l.ID] = l
		buildSegments(l, l.ID == root.ID, now)
		assignLifecycle(l, laneByID[l.Parent])
		l.Active = activeIntervals(l)
	}
	buildTotals(s)
}

// Open marks a tool envelope awaiting its output. Its displayed endpoint advances
// with the live lane; the adapter replaces it with the recorded output timestamp.
func extendPendingOperations(l *Lane, now int64) {
	for _, o := range l.Ops {
		if !o.Open {
			continue
		}
		end := l.Ended
		for _, t := range l.Turns {
			if o.Turn != "" && o.Turn != t.ID {
				continue
			}
			bound := t.End
			if t.Status == "open" {
				bound = now
			}
			if o.Turn == t.ID || o.Turn == "" && o.Start >= t.Start && o.Start <= bound {
				end = bound
				break
			}
		}
		o.End = max(o.Start, end)
	}
}

// markBackground flags ops that outlived the turn they started in (the turn completed while
// the process was still running) or that started outside any turn. The log structure proves
// the agent moved on, so such ops are drawn separately and never overlay the partition.
func markBackground(l *Lane, now int64) {
	for _, o := range l.Ops {
		o.Background = false
		var turn *Turn
		for _, t := range l.Turns {
			end := t.End
			if t.Status == "open" {
				end = now
			}
			if o.Start >= t.Start && (o.Start < end || o.Open && t.Status == "open" && o.Start == end) {
				turn = t
				break
			}
		}
		if turn == nil {
			o.Background = len(l.Turns) > 0
			continue
		}
		if turn.Status == "open" {
			continue
		}
		if o.End > turn.End+1000 { // crosses the end of its own turn (1 s tolerance for close-out writes)
			o.Background = true
		}
	}
}

// ---- exclusive partition

type edge struct {
	t     int64
	start bool
	op    *Operation
}

// buildSegments partitions [lane.Started, lane.Ended] into non-overlapping segments.
// Base state comes from turns; ops overlay by classify.Priority.
func buildSegments(l *Lane, isRoot bool, now int64) {
	l.Segments = l.Segments[:0]
	if l.Ended <= l.Started {
		l.Ended = l.Started + 1
	}
	// 1. base timeline from turns
	type base struct {
		s, e  int64
		phase Phase
	}
	var bases []base
	cursor := l.Started
	idle := Phase(classify.WaitUser)
	if !isRoot {
		idle = classify.Idle
	}
	for i, t := range l.Turns {
		if t.Start > cursor {
			// idle before a harness-triggered turn (goal loop, notifications) is not the user's time
			ph := idle
			if t.Trigger == "system" {
				ph = classify.WaitWorker
			}
			bases = append(bases, base{cursor, t.Start, ph})
		}
		end := t.End
		switch t.Status {
		case "open":
			// think until last evidence, then no telemetry until now
			bases = append(bases, base{t.Start, t.End, classify.LLM})
			if now > t.End {
				bases = append(bases, base{t.End, now, classify.NoTelemetry})
			}
			end = now
		case "orphaned":
			bases = append(bases, base{t.Start, t.End, classify.LLM})
			next := l.Ended
			if i+1 < len(l.Turns) {
				next = l.Turns[i+1].Start
			}
			if next > t.End {
				bases = append(bases, base{t.End, next, classify.NoTelemetry})
			}
			end = next
		default:
			bases = append(bases, base{t.Start, t.End, classify.LLM})
		}
		if end > cursor {
			cursor = end
		}
	}
	if cursor < l.Ended {
		bases = append(bases, base{cursor, l.Ended, idle})
	}
	// 2. sweep ops
	// Instantaneous ops (file edits) get a 1 ms anchor so stage attribution sees them.
	edges := make([]edge, 0, 2*len(l.Ops))
	for _, o := range l.Ops {
		if o.Background {
			continue
		}
		e := o.End
		if e <= o.Start {
			e = o.Start + 1
		}
		start, end := max(o.Start, l.Started), min(e, l.Ended)
		if end > start {
			edges = append(edges, edge{start, true, o}, edge{end, false, o})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].t != edges[j].t {
			return edges[i].t < edges[j].t
		}
		return !edges[i].start && edges[j].start // ends before starts at the same instant
	})
	var active []*Operation
	best := func() *Operation {
		var b *Operation
		for _, o := range active {
			if b == nil || classify.Priority[o.Phase] > classify.Priority[b.Phase] || (classify.Priority[o.Phase] == classify.Priority[b.Phase] && o.Start > b.Start) {
				b = o
			}
		}
		return b
	}
	bi := 0
	basePhase := func(t int64) Phase {
		for bi < len(bases) && bases[bi].e <= t {
			bi++
		}
		if bi < len(bases) && bases[bi].s <= t {
			return bases[bi].phase
		}
		return classify.NoTelemetry
	}
	emit := func(s, e int64, phase Phase, op string) {
		if e <= s {
			return
		}
		if n := len(l.Segments); n > 0 {
			last := &l.Segments[n-1]
			if last.End == s && last.Phase == phase && last.Op == op {
				last.End = e
				return
			}
		}
		l.Segments = append(l.Segments, Segment{Start: s, End: e, Phase: phase, Op: op})
	}
	// fill from t0 to t1 with base phases (possibly several bases) or the winning op
	fill := func(t0, t1 int64) {
		if t1 <= t0 {
			return
		}
		if b := best(); b != nil {
			emit(t0, t1, b.Phase, b.ID)
			return
		}
		t := t0
		for t < t1 {
			ph := basePhase(t)
			end := t1
			if bi < len(bases) && bases[bi].e < end && bases[bi].s <= t {
				end = bases[bi].e
			}
			if end <= t {
				end = t1
			}
			emit(t, end, ph, "")
			t = end
		}
	}
	cur := l.Started
	for _, ed := range edges {
		if ed.t > cur {
			fill(cur, ed.t)
			cur = ed.t
		} else if ed.t < cur {
			// op starts before lane start: clamp
		}
		if ed.start {
			active = append(active, ed.op)
		} else {
			for i, o := range active {
				if o == ed.op {
					active = append(active[:i], active[i+1:]...)
					break
				}
			}
		}
	}
	if cur < l.Ended {
		fill(cur, l.Ended)
	}
	// per-lane phase totals: exclusive (partition) and raw (sum of op durations)
	l.ByPhase = map[Phase]int64{}
	for _, sg := range l.Segments {
		l.ByPhase[sg.Phase] += sg.End - sg.Start
	}
	l.RawByPhase = map[Phase]int64{}
	for _, o := range l.Ops {
		if o.End > o.Start && !o.Background {
			l.RawByPhase[o.Phase] += o.End - o.Start
		}
	}
	l.InTurnMs = 0
	for _, t := range l.Turns {
		end := t.End
		if t.Status == "open" {
			end = now
		}
		if end > t.Start {
			l.InTurnMs += end - t.Start
		}
	}
}

// activeIntervals: for sub-agents, the turns are the active periods.
func activeIntervals(l *Lane) []Interval {
	out := []Interval{}
	for _, t := range l.Turns {
		if t.End > t.Start {
			out = append(out, Interval{t.Start, t.End})
		}
	}
	return out
}

// ---- retry groups (explicit identity only)

func assignGroups(s *Session) {
	s.Groups = []Group{}
	byID := map[string][]*Operation{}
	var all []*Operation
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			o.Group, o.Attempt = "", 0
			if o.Identity != "" && !o.Background {
				byID[o.Identity] = append(byID[o.Identity], o)
			}
			all = append(all, o)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start < all[j].Start })
	var ids []string
	for id, ops := range byID {
		if len(ops) < 2 {
			continue
		}
		// Infra groups are retries only: an identical infra command repeated with every run
		// succeeding is a poll or a routine (`ssh host 'cat lease.json'` ×7), not an attempt
		// sequence. Test/build/release reruns after a pass are re-verification and stay grouped.
		if ops[0].Phase == classify.Infra {
			failed := false
			for _, o := range ops {
				if o.Failure() {
					failed = true
					break
				}
			}
			if !failed {
				continue
			}
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return byID[ids[i]][0].Start < byID[ids[j]][0].Start })
	roleOf := map[string]string{} // op id -> group role for non-attempt members
	type window struct {
		g      *Group
		lo, hi int64
		lane   string
	}
	var windows []window // failed attempt -> next attempt, per group
	for gi, id := range ids {
		ops := byID[id]
		sort.Slice(ops, func(i, j int) bool { return ops[i].Start < ops[j].Start })
		g := Group{ID: fmt.Sprintf("G%02d", gi+1), Identity: id, Phase: ops[0].Phase, Title: ops[0].Title, Start: ops[0].Start, End: ops[0].End}
		lanes := map[string]bool{}
		prevFailed := false
		for i, o := range ops {
			o.Group, o.Attempt = g.ID, i+1
			lanes[o.Lane] = true
			g.Members = append(g.Members, o.ID)
			if o.End > g.End {
				g.End = o.End
			}
			failed := o.Failure()
			if failed {
				g.Failed++
			}
			role := "first"
			if i > 0 {
				prev := ops[i-1]
				switch {
				case o.Start < prev.End: // started before the previous attempt finished: fan-out, not a retry
					role = "parallel"
				case prevFailed:
					role = "retry_after_failure"
				default:
					role = "rerun"
				}
				if prevFailed && role == "retry_after_failure" {
					windows = append(windows, window{&g, prev.End, o.Start, prev.Lane})
				}
			}
			roleOf[o.ID] = role
			prevFailed = failed
		}
		g.Attempts = len(ops)
		for ln := range lanes {
			g.Lanes = append(g.Lanes, ln)
		}
		sort.Strings(g.Lanes)
		s.Groups = append(s.Groups, g)
	}
	// Ops between a failed attempt and its retry become fix / infra_recovery / worker_queue —
	// only when exactly one group is in that state at the time and the op is on the same lane
	// as the failed attempt. Ambiguous cases get no role.
	for _, x := range all {
		// attempts of a group are never also the recovery between two other attempts
		if x.Group != "" || x.Phase == classify.LLM || x.Phase == classify.Compaction {
			continue
		}
		var hit *window
		n := 0
		for i := range windows {
			w := &windows[i]
			if x.Start >= w.lo && x.Start < w.hi && x.Lane == w.lane {
				n++
				hit = w
			}
		}
		if n != 1 {
			continue
		}
		role := ""
		switch x.Phase {
		case classify.Code:
			role = "fix"
		case classify.Infra:
			role = "infra_recovery"
		case classify.WaitWorker:
			role = "worker_queue"
		}
		if role == "" {
			continue
		}
		roleOf[x.ID] = role
		x.Group = hit.g.ID
		for i := range s.Groups {
			if s.Groups[i].ID == hit.g.ID {
				s.Groups[i].Members = append(s.Groups[i].Members, x.ID)
			}
		}
	}
	for _, o := range all {
		if r, ok := roleOf[o.ID]; ok {
			o.Kind = withRole(o.Kind, r)
		} else {
			o.Kind = withRole(o.Kind, "")
		}
	}
}

// Kind carries an optional "|role" suffix so the UI can show both the tool kind and the group role.
func withRole(kind, role string) string {
	if i := strings.IndexByte(kind, '|'); i >= 0 {
		kind = kind[:i]
	}
	if role == "" {
		return kind
	}
	return kind + "|" + role
}

func RoleOf(kind string) string {
	if i := strings.IndexByte(kind, '|'); i >= 0 {
		return kind[i+1:]
	}
	return ""
}

// ---- totals

func buildTotals(s *Session) {
	root := s.Lanes[0]
	t := Totals{ByPhase: map[Phase]int64{}, ByKind: map[string]int64{}}
	t.ElapsedMs = root.Ended - root.Started
	t.InTurnMs = root.InTurnMs
	for _, sg := range root.Segments {
		t.ByPhase[sg.Phase] += sg.End - sg.Start
	}
	// LLM split: segments backed by a Reasoning/AgentMessage item are verified; the rest is
	// the documented convention "inside a turn, no tool running → model output pending".
	for _, sg := range root.Segments {
		if sg.Phase == classify.LLM {
			if sg.Op != "" {
				t.ByKind["llm_verified"] += sg.End - sg.Start
			} else {
				t.ByKind["llm_gap"] += sg.End - sg.Start
			}
		}
	}
	t.RawByPhase = map[Phase]int64{}
	for k, v := range root.RawByPhase {
		t.RawByPhase[k] = v
		t.RawOpsMs += v
	}
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			if o.Background {
				t.Background++
				t.BackgroundMs += o.End - o.Start
			}
		}
	}
	t.ByLifecycle = map[Lifecycle]int64{}
	for k, v := range root.ByLifecycle {
		t.ByLifecycle[k] = v
	}
	// Review/cleanup skill invocations across all lanes; their time is in by_lifecycle.
	for _, l := range s.Lanes {
		for _, tn := range l.Turns {
			if tn.Review {
				t.Reviews++
			}
		}
	}
	for _, l := range s.Lanes {
		t.Tokens.Add(l.Tokens)
		t.Ops += len(l.Ops)
		t.Turns += len(l.Turns)
		for _, o := range l.Ops {
			if o.Failure() {
				t.Failed++
			} else if o.QueryMiss {
				t.QueryMisses++
			}
			if o.Phase == classify.Compaction {
				t.Compactions++
				t.CompactionMs += o.End - o.Start
			}
		}
		for _, m := range l.Markers {
			switch m.Kind {
			case "user_message":
				if l == root {
					t.UserMessages++
				}
			case "question":
				t.Questions++
			case "system_message":
				t.SystemMsgs++
			}
		}
	}
	// test/build/release sub-kinds on the root lane (op time, not partitioned)
	for _, o := range root.Ops {
		if r := RoleOf(o.Kind); r != "" {
			t.ByKind[r] += o.End - o.Start
		} else if o.Phase == classify.Test || o.Phase == classify.Build || o.Phase == classify.Release {
			t.ByKind["single"] += o.End - o.Start
		}
		if o.Remote && (o.Phase == classify.Test || o.Phase == classify.Build) {
			t.ByKind["remote"] += o.End - o.Start
		}
	}
	s.Totals = t
	// parallel sub-agent time
	var ivs []Interval
	p := Parallel{}
	for _, l := range s.Lanes[1:] {
		p.Agents++
		for _, iv := range l.Active {
			p.AgentMs += iv.End - iv.Start
			ivs = append(ivs, iv)
		}
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].Start < ivs[j].Start })
	var cs, ce int64 = 0, -1
	for _, iv := range ivs {
		if iv.Start > ce {
			if ce > cs {
				p.WallMs += ce - cs
			}
			cs, ce = iv.Start, iv.End
		} else if iv.End > ce {
			ce = iv.End
		}
	}
	if ce > cs {
		p.WallMs += ce - cs
	}
	s.Parallel = p
}
