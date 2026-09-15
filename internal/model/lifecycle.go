package model

import (
	"math"
	"sort"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
)

// assignLifecycle fills the second partition of a lane: every op, turn and segment gets the
// SDLC stage it served (docs/SCHEMA.md "Lifecycle"). It runs after buildSegments, on the
// exclusive partition it produced, so sum(by_lifecycle) == sum(by_phase) by construction.
// parent is the lane this one was spawned from (nil for the root); its turns are already
// assigned because Derive walks the lanes parents first.
//
// Precedence, all literal signals:
//  1. lane: the sub-agent's spawn role (overlay `lifecycle.roles`) pins every turn;
//  2. turn: plan collaboration mode, an invoked skill, Codex review mode — the WHOLE turn,
//     model output and waits included, takes that stage. A sub-agent turn with no signal of
//     its own inherits the stage of the parent turn it ran inside (parentTurnOf: a recorded
//     link only, never bare time overlap): the sub-agent is that turn's tool call, so its work
//     is that turn's work (the origin lane is named in the rule);
//  3. op: a lifecycle pinned by the adapter (command kind, edited path), else the phase default.
//     The single order-dependent rule: an operations candidate that starts before this lane's
//     first release op is implementation (nothing is "after launch" before anything shipped);
//  4. segment: model output inside a turn takes the stage of the nearest tool call in that
//     turn — the next one first (the call it prepared: the activity-bracket convention), else
//     the previous one (the answer that reported on it). Compactions and telemetry gaps inside
//     the turn are transparent: skipped over, they keep their own name for their own duration.
//     A wait for workers (a spawn, sleep or poll the model chose), an unknown command, waiting
//     for the user, idle time and the turn's end each close the bracket. Model output whose
//     nearest call is unknown is `unknown` (a wrong stage is worse than an honest unknown); a
//     turn with no tool call at all keeps its model output as `llm`.
func assignLifecycle(l, parent *Lane) {
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
		if lc == "" && parent != nil {
			if pt := parentTurnOf(parent, l, t); pt != nil && pt.Lifecycle != "" {
				lc, rule = pt.Lifecycle, pt.LifecycleRule
				if !strings.HasPrefix(rule, "inherited from ") { // name the origin lane, not the chain
					rule = "inherited from " + parent.Path + " (" + rule + ")"
				}
			}
		}
		t.Lifecycle, t.LifecycleRule = lc, rule
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
			o.Lifecycle, o.LifecycleRule = t.Lifecycle, t.LifecycleRule
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
	// what each segment is to the model output around it: a tool call with a stage (an
	// anchor), an unknown command, a transparent interval, or model output still to decide
	const (
		segDecided = iota
		segAnchor
		segUnknown
		segTransparent
		segModel
	)
	kind := make([]int8, n)
	for i := range segs {
		sg := &segs[i]
		if t := turnOf[i]; t != nil && t.Lifecycle != "" {
			sg.Lifecycle = t.Lifecycle
			continue
		}
		var op *Operation
		if sg.Op != "" {
			op = opByID[sg.Op]
		}
		switch {
		case op != nil && classify.IsWorkLifecycle(op.Lifecycle):
			sg.Lifecycle, kind[i] = op.Lifecycle, segAnchor
		case sg.Phase == classify.LLM: // model output, or a message op without a stage of its own
			kind[i] = segModel
		case sg.Phase == classify.Unknown:
			sg.Lifecycle, kind[i] = Lifecycle(classify.Unknown), segUnknown
		case sg.Phase == classify.Compaction || sg.Phase == classify.NoTelemetry:
			// a harness interruption, not a decision the model made: transparent to the bracket
			sg.Lifecycle, kind[i] = Lifecycle(sg.Phase), segTransparent
		case op != nil:
			sg.Lifecycle = op.Lifecycle
		default:
			sg.Lifecycle = Lifecycle(sg.Phase)
		}
	}
	// nearest walks from i in one direction inside i's turn to the first tool call that
	// decides model output: an anchor gives its stage, an unknown command gives unknown,
	// anything else (a wait for a worker or the user, idle time) closes the bracket.
	nearest := func(i, dir int) (Lifecycle, bool) {
		for j := i + dir; j >= 0 && j < n && turnOf[j] == turnOf[i]; j += dir {
			switch kind[j] {
			case segAnchor:
				return segs[j].Lifecycle, true
			case segUnknown:
				return Lifecycle(classify.Unknown), true
			case segModel, segTransparent:
				continue
			}
			return "", false
		}
		return "", false
	}
	for i := range segs {
		if kind[i] != segModel {
			continue
		}
		lc := Lifecycle(classify.LLM)
		if turnOf[i] != nil {
			if s, ok := nearest(i, +1); ok {
				lc = s
			} else if s, ok := nearest(i, -1); ok {
				lc = s
			}
		}
		segs[i].Lifecycle = lc
	}
	l.ByLifecycle = map[Lifecycle]int64{}
	for _, sg := range segs {
		l.ByLifecycle[sg.Lifecycle] += sg.End - sg.Start
	}
}

// parentTurnOf links a sub-agent turn to the parent-lane turn it ran inside, by a recorded
// signal only — never bare time overlap, which would wrongly attribute an agent re-used across
// parent turns. In priority: the harness's own root_turn_id (Codex >= 0.153, resolved on the
// immediate parent — set only for a direct child); else the spawn / message marker that started
// or last addressed this lane (the parent's `agent_*` marker with this lane as its Ref); else an
// open worker-wait op on the parent whose window contains the child turn's start. No link → no
// inheritance (product rule 3). Parents are assigned before children, so a returned turn already
// carries any stage it inherited itself; the chain reaches every depth.
func parentTurnOf(parent, child *Lane, ct *Turn) *Turn {
	byID := func(id string) *Turn {
		if id == "" {
			return nil
		}
		for _, t := range parent.Turns {
			if t.ID == id {
				return t
			}
		}
		return nil
	}
	if t := byID(ct.RootTurn); t != nil {
		return t
	}
	linkTurn, linkT := "", int64(math.MinInt64)
	for _, m := range parent.Markers {
		if strings.HasPrefix(m.Kind, "agent_") && m.Ref == child.ID && m.T <= ct.Start && m.T > linkT {
			linkTurn, linkT = m.Turn, m.T
		}
	}
	if t := byID(linkTurn); t != nil {
		return t
	}
	// last resort: the agent was spawned during one of the parent's worker waits. Only for the
	// child's first turn (its spawn) — a bare wait window cannot say which of several concurrent
	// agents it belongs to, so a re-used agent's later turn must come from a message marker, not
	// from whatever wait happens to contain its start.
	if len(child.Turns) > 0 && ct == child.Turns[0] {
		for _, o := range parent.Ops {
			if o.Phase == classify.WaitWorker && o.Start <= ct.Start && ct.Start < o.End {
				if t := byID(o.Turn); t != nil {
					return t
				}
			}
		}
	}
	return nil
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
