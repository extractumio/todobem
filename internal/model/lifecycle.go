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
// Precedence, all literal signals or the order of literal signals inside one turn:
//  1. lane: the sub-agent's spawn role (overlay `lifecycle.roles`) pins every turn;
//  2. turn: plan collaboration mode, Codex review mode — the WHOLE turn, model output and waits
//     included, takes that stage. A sub-agent turn with no signal of its own inherits the stage
//     in force on the parent turn it ran inside when it started (parentTurnOf: a recorded link
//     only, never bare time overlap): the sub-agent is that turn's tool call, so its work is
//     that turn's work (the origin lane is named in the rule). A skill the harness actually
//     injected pins a RUN: from the skill marker to the turn's end or the next stage-bearing
//     skill marker. Nothing ran before the marker → the run is the whole turn (Codex injects
//     at the turn start; promoted to the turn's Lifecycle); otherwise (Claude Code's mid-turn
//     Skill call) the ops before it keep their own composition (Turn.Runs). A plan-mode turn
//     keeps planning whatever skill it invokes: the skill reviews the plan;
//  3. op: a lifecycle pinned by the adapter (command kind, edited path, plan anchor) — the
//     operations guard demotes an operate pin before this lane's first release op to
//     implementation ("nothing is after launch before anything shipped");
//  4. op, composition — the turn's ops read as a group: a tool call before the turn's first
//     plan anchor (a plan created with update_plan / ExitPlanMode) that changed nothing is
//     planning; every code, build, test and infra call between the turn's first and last change
//     op (classify.ChangeKinds) is implementation — a test between two edits is the loop, not
//     QA; else the phase default. Release ops keep release wherever they sit;
//  5. segment: model output inside a turn takes the stage of the nearest tool call in that
//     turn — the next one first (the call it prepared: the activity-bracket convention), else
//     the previous one (the answer that reported on it). Compactions and telemetry gaps inside
//     the turn are transparent: skipped over, they keep their own name for their own duration.
//     A wait for workers (a spawn, sleep or poll the model chose), an unknown command, waiting
//     for the user, idle time and the turn's end each close the bracket. Model output whose
//     nearest call is unknown is `unknown` (a wrong stage is worse than an honest unknown); a
//     turn with no tool call at all keeps its model output as `llm`. A model-output op then
//     takes the stage of the segment that covers it, so the op an operator clicks and the time
//     it stands on agree.
func assignLifecycle(l, parent *Lane) {
	laneLc, laneRule := Lifecycle(""), ""
	if lc, ok := classify.RoleLifecycle(l.Role); ok {
		laneLc, laneRule = lc, "agent role "+l.Role
	}
	l.Lifecycle = laneLc
	reviewMode := map[string]bool{}
	skillRuns := map[string][]StageRun{}
	for _, m := range l.Markers {
		switch m.Kind {
		case "enteredreviewmode":
			reviewMode[m.Turn] = true
		case "skill":
			if lc, ok := classify.SkillLifecycle(m.Ref); ok && m.Turn != "" {
				skillRuns[m.Turn] = append(skillRuns[m.Turn], StageRun{From: m.T, Lifecycle: lc, Rule: "skill " + m.Ref})
			}
		}
	}
	firstOpAt := map[string]int64{} // per turn: when its first op started
	for _, o := range l.Ops {
		if o.Background {
			continue
		}
		if v, ok := firstOpAt[o.Turn]; !ok || o.Start < v {
			firstOpAt[o.Turn] = o.Start
		}
	}
	for _, t := range l.Turns {
		lc, rule := laneLc, laneRule
		if lc == "" && t.Mode == "plan" {
			lc, rule = classify.LcPlan, "plan mode"
		}
		runs := skillRuns[t.ID]
		if len(runs) == 0 && t.Skill != "" { // no marker recorded: the injection sits at the turn start
			if s, ok := classify.SkillLifecycle(t.Skill); ok {
				runs = []StageRun{{From: t.Start, Lifecycle: s, Rule: "skill " + t.Skill}}
			}
		}
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].From < runs[j].From })
		if len(runs) > 0 {
			if first, ok := firstOpAt[t.ID]; !ok || first >= runs[0].From {
				runs[0].From = t.Start // nothing ran before the skill was invoked: it covers the turn
			}
			if laneLc != "" || t.Mode == "plan" {
				// the lane's role is the stronger signal; and a skill invoked in a plan-mode turn
				// reviews the plan, so the turn stays planning
				runs = nil
			} else if runs[0].From <= t.Start && lc == "" {
				lc, rule = runs[0].Lifecycle, runs[0].Rule // promoted: the whole turn
				runs = runs[1:]
			}
		}
		if lc == "" && reviewMode[t.ID] {
			lc, rule = classify.LcReview, "review mode"
		}
		if lc == "" && parent != nil {
			if pt := parentTurnOf(parent, l, t); pt != nil {
				if plc, prule, ok := stageAt(pt, t.Start); ok {
					lc, rule = plc, prule
					if !strings.HasPrefix(rule, "inherited from ") { // name the origin lane, not the chain
						rule = "inherited from " + parent.Path + " (" + rule + ")"
					}
				}
			}
		}
		t.Lifecycle, t.LifecycleRule, t.Runs = lc, rule, runs
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
	// the composition of every turn, read before any op of it is decided: the first plan
	// anchor and the change window (first and last change op)
	type composition struct{ planAt, first, last int64 }
	comp := map[string]*composition{}
	for _, o := range l.Ops {
		if o.Background {
			continue
		}
		c := comp[o.Turn]
		if c == nil {
			c = &composition{planAt: math.MinInt64, first: math.MaxInt64, last: math.MinInt64} // planAt MinInt64 = no plan anchor
			comp[o.Turn] = c
		}
		if o.Phase == classify.LLM && classify.BaseKind(o.Kind) == "plan" && o.Lifecycle == classify.LcPlan && (c.planAt == math.MinInt64 || o.Start < c.planAt) {
			c.planAt = o.Start
		}
		if classify.IsChangeOp(o.Phase, o.Kind) {
			c.first = min(c.first, o.Start)
			c.last = max(c.last, o.Start)
		}
	}
	toolPhase := func(p Phase) bool {
		return p == classify.Code || p == classify.Build || p == classify.Test || p == classify.Infra
	}
	opByID := map[string]*Operation{}
	for _, o := range l.Ops {
		opByID[o.ID] = o
		if t := turnByID[o.Turn]; t != nil {
			if lc, rule, ok := stageAt(t, o.Start); ok {
				o.Lifecycle, o.LifecycleRule = lc, rule
				continue
			}
		}
		switch {
		case o.Phase == classify.LLM && o.Lifecycle == "":
			// decided below, from the segment that covers it
		case o.Lifecycle == "":
			o.Lifecycle, o.LifecycleRule = classify.PhaseLifecycle(o.Phase), "phase "+string(o.Phase)
			c := comp[o.Turn]
			switch {
			case c == nil || o.Background || !toolPhase(o.Phase):
			case o.Start < c.planAt && !classify.IsChangeOp(o.Phase, o.Kind):
				o.Lifecycle, o.LifecycleRule = classify.LcPlan, "before the turn's plan was submitted"
			case o.Start >= c.first && o.Start <= c.last:
				o.Lifecycle, o.LifecycleRule = classify.LcImplement, "inside the turn's change window (first edit … last edit)"
			}
		case o.Lifecycle == classify.LcOperate && o.Start < firstRelease:
			o.Lifecycle, o.LifecycleRule = classify.LcImplement, "operations before this lane's first release → implementation"
		case o.LifecycleRule == "":
			o.LifecycleRule = "kind " + o.Kind
		}
	}
	splitSegmentsAtTurns(l)
	segs := l.Segments
	n := len(segs)
	turnOf := make([]*Turn, n) // segments never straddle a turn or a run after the split
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
		if t := turnOf[i]; t != nil {
			if lc, _, ok := stageAt(t, sg.Start); ok {
				sg.Lifecycle = lc
				continue
			}
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
	// model-output ops (Codex Reasoning / AgentMessage items, Claude Code text blocks) take the
	// stage of the segment covering their start, so the row an operator clicks and the time it
	// stands on agree
	for _, o := range l.Ops {
		if o.Phase != classify.LLM || o.Lifecycle != "" {
			continue
		}
		i := sort.Search(n, func(i int) bool { return segs[i].End > o.Start })
		if i < n && segs[i].Start <= o.Start {
			o.Lifecycle, o.LifecycleRule = segs[i].Lifecycle, modelOutputRule(segs[i].Lifecycle)
		} else {
			o.Lifecycle, o.LifecycleRule = Lifecycle(classify.LLM), "model output outside the lane's partition"
		}
	}
	l.ByLifecycle = map[Lifecycle]int64{}
	for _, sg := range segs {
		l.ByLifecycle[sg.Lifecycle] += sg.End - sg.Start
	}
}

// modelOutputRule words, for the inspector, why a model-output op carries a stage.
func modelOutputRule(lc Lifecycle) string {
	switch {
	case classify.IsWorkLifecycle(lc):
		return "model output: the stage of the nearest tool call in the turn"
	case lc == Lifecycle(classify.LLM):
		return "model output in a turn without tool calls"
	case lc == Lifecycle(classify.Unknown):
		return "model output: the nearest command is unknown"
	}
	return "model output alongside " + string(lc)
}

// stageAt is the turn-level stage in force at ts: the latest skill run started at or before ts,
// else the whole-turn signal, else none.
func stageAt(t *Turn, ts int64) (Lifecycle, string, bool) {
	for i := len(t.Runs) - 1; i >= 0; i-- {
		if t.Runs[i].From <= ts {
			return t.Runs[i].Lifecycle, t.Runs[i].Rule, true
		}
	}
	if t.Lifecycle != "" {
		return t.Lifecycle, t.LifecycleRule, true
	}
	return "", "", false
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

// splitSegmentsAtTurns cuts every segment at turn starts and ends and at the start of every
// skill run. buildSegments merges adjacent same-phase segments, so one model-output segment can
// span two back-to-back turns; a turn-level signal must not leak across that boundary, and a run
// must start on a segment edge. Cutting changes no duration.
func splitSegmentsAtTurns(l *Lane) {
	cuts := make([]int64, 0, 2*len(l.Turns))
	for _, t := range l.Turns {
		cuts = append(cuts, t.Start, t.End)
		for _, r := range t.Runs {
			cuts = append(cuts, r.From)
		}
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
