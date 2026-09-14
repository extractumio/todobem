package model

import (
	"math"
	"sort"

	"github.com/extractumio/todobem/internal/classify"
)

// assignLifecycle fills the second partition of a lane: every op, turn and segment gets the
// SDLC stage it served (docs/SCHEMA.md "Lifecycle"). It runs after buildSegments, on the
// exclusive partition it produced, so sum(by_lifecycle) == sum(by_phase) by construction.
//
// Precedence, all literal signals:
//  1. lane: the sub-agent's spawn role (overlay `lifecycle.roles`) pins every turn;
//  2. turn: plan collaboration mode, an invoked skill, Codex review mode — the WHOLE turn,
//     model output and waits included, takes that stage;
//  3. op: a lifecycle pinned by the adapter (command kind, edited path), else the phase default.
//     The single order-dependent rule: an operations candidate that starts before this lane's
//     first release op is implementation (nothing is "after launch" before anything shipped);
//  4. segment: model output inside a turn takes the stage of the next tool call in that turn
//     (the stage-bracket convention); with no tool call after it, it stays `llm`. Waits, idle,
//     compaction and telemetry gaps pass through under their phase name.
func assignLifecycle(l *Lane) {
	laneLc, laneRule := Lifecycle(""), ""
	if lc, ok := classify.RoleLifecycle(l.Role); ok {
		laneLc, laneRule = lc, "agent role "+l.Role
	}
	l.Lifecycle = laneLc
	reviewMode := map[string]bool{}
	for _, m := range l.Markers {
		if m.Kind == "enteredreviewmode" {
			reviewMode[m.Turn] = true
		}
	}
	turnRule := map[string]string{}
	for _, t := range l.Turns {
		lc, rule := laneLc, laneRule
		if lc == "" && t.Mode == "plan" {
			lc, rule = classify.LcPlan, "plan mode"
		}
		if lc == "" && t.Skill != "" {
			if s, ok := classify.SkillLifecycle(t.Skill); ok {
				lc, rule = s, "skill "+t.Skill
			}
		}
		if lc == "" && reviewMode[t.ID] {
			lc, rule = classify.LcReview, "review mode"
		}
		t.Lifecycle = lc
		if lc != "" {
			turnRule[t.ID] = rule
		}
	}
	turnByID := map[string]*Turn{}
	for _, t := range l.Turns {
		turnByID[t.ID] = t
	}
	firstRelease := int64(math.MaxInt64)
	for _, o := range l.Ops {
		if o.Phase == classify.Release && o.Start < firstRelease {
			firstRelease = o.Start
		}
	}
	opByID := map[string]*Operation{}
	for _, o := range l.Ops {
		opByID[o.ID] = o
		if t := turnByID[o.Turn]; t != nil && t.Lifecycle != "" {
			o.Lifecycle, o.LifecycleRule = t.Lifecycle, turnRule[o.Turn]
			continue
		}
		switch {
		case o.Lifecycle == "":
			o.Lifecycle, o.LifecycleRule = classify.PhaseLifecycle(o.Phase), "phase "+string(o.Phase)
		case o.Lifecycle == classify.LcOperate && o.Start < firstRelease:
			o.Lifecycle, o.LifecycleRule = classify.LcImplement, "operations before this lane's first release → implementation"
		case o.LifecycleRule == "":
			o.LifecycleRule = "kind " + o.Kind
		}
	}
	splitSegmentsAtTurns(l)
	segs := l.Segments
	n := len(segs)
	turnOf := make([]*Turn, n) // segments never straddle a turn after the split
	ti := 0
	for i := range segs {
		for ti < len(l.Turns) && l.Turns[ti].End <= segs[i].Start {
			ti++
		}
		if ti < len(l.Turns) && l.Turns[ti].Start <= segs[i].Start {
			turnOf[i] = l.Turns[ti]
		}
	}
	for i := n - 1; i >= 0; i-- {
		sg := &segs[i]
		if t := turnOf[i]; t != nil && t.Lifecycle != "" {
			sg.Lifecycle = t.Lifecycle
			continue
		}
		var op *Operation
		if sg.Op != "" {
			op = opByID[sg.Op]
		}
		if op != nil && classify.IsWorkLifecycle(op.Lifecycle) {
			sg.Lifecycle = op.Lifecycle
			continue
		}
		if sg.Phase == classify.LLM {
			sg.Lifecycle = Lifecycle(classify.LLM)
			// later segments are already decided (the loop runs backwards): the first one
			// with a work stage decides; a non-work tool call (a wait, a compaction, an unknown
			// command) or the turn's end closes the bracket
			for j := i + 1; j < n; j++ {
				nx := &segs[j]
				if turnOf[j] != turnOf[i] || nx.Phase == classify.WaitUser || nx.Phase == classify.Idle || nx.Phase == classify.NoTelemetry {
					break
				}
				if classify.IsWorkLifecycle(nx.Lifecycle) {
					sg.Lifecycle = nx.Lifecycle
					break
				}
				if nx.Phase != classify.LLM {
					break
				}
			}
			continue
		}
		if op != nil {
			sg.Lifecycle = op.Lifecycle
		} else {
			sg.Lifecycle = Lifecycle(sg.Phase)
		}
	}
	l.ByLifecycle = map[Lifecycle]int64{}
	for _, sg := range segs {
		l.ByLifecycle[sg.Lifecycle] += sg.End - sg.Start
	}
}

// splitSegmentsAtTurns cuts every segment at turn starts and ends. buildSegments merges adjacent
// same-phase segments, so one model-output segment can span two back-to-back turns; a turn-level
// signal must not leak across that boundary. Cutting changes no duration.
func splitSegmentsAtTurns(l *Lane) {
	cuts := make([]int64, 0, 2*len(l.Turns))
	for _, t := range l.Turns {
		cuts = append(cuts, t.Start, t.End)
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })
	out := make([]Segment, 0, len(l.Segments)+len(cuts))
	ci := 0
	for _, sg := range l.Segments {
		for ci < len(cuts) && cuts[ci] <= sg.Start {
			ci++
		}
		for k := ci; k < len(cuts) && cuts[k] < sg.End; k++ {
			if cuts[k] > sg.Start {
				head := sg
				head.End = cuts[k]
				out = append(out, head)
				sg.Start = cuts[k]
			}
		}
		out = append(out, sg)
	}
	l.Segments = out
}
