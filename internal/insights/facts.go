// Package insights turns parsed sessions into ranked, evidence-backed findings about where a
// harness loses time and tokens (docs/ARCHITECTURE.md §10). Facts is the compact per-session
// record the detectors read; Extract builds it from a derived model.Session and is a pure
// function of it. Nothing here reads a rollout, infers a cause or uses a duration threshold.
package insights

import (
	"sort"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// FactsVersion is bumped whenever Extract's output for the same model changes; a cached facts
// file with another version is a miss. 2: the first cached shape. 3: Source; query misses
// without an exit code. 4: subgroups on ops, the tool-call mix, failures by subgroup, unknown
// time by subgroup. 5: the delivery walk (Delivery), recovery on retry windows, compactions
// inside a change window, gaps after the first change, stop hooks among the long ops. 6:
// unknown heads and shapes past env assignments and `export`. 7, 8: the same past a lone
// shell separator (intermediate builds of the same change wrote 6 and 7). 9, 10: shapes from
// the classifier's deciding segment (10: never a bare assignment), the root's no-telemetry
// intervals (Blind), a gap's TurnChangedFiles instead of the session-level AfterFirstChange.
// 11: the llm_error points dropped with the D14 card. 12: the delivery walk's pushes (D25), what
// verified the session by kind, failed test runs and stop hooks run; retry windows session-scoped
// with the retry op and the "other" recovery. 13: the tool-call mix per slice (a compound
// command's shares land in their categories) with the shared calls and time apart. 14: delivery
// facts count test-running hooks and ordered compound change, test and push shares. 15: failed
// compounds leave their per-share test verdict explicitly ambiguous.
const FactsVersion = 16

// Facts is everything the detectors need about one session, in a few KB.
type Facts struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Source  string `json:"source,omitempty"` // "codex" | "claude"
	Title   string `json:"title"`
	CWD     string `json:"cwd"`
	Branch  string `json:"branch,omitempty"`
	Model   string `json:"model,omitempty"`
	CLI     string `json:"cli,omitempty"`
	Started int64  `json:"started"`
	Ended   int64  `json:"ended"`
	Live    bool   `json:"live"`

	Root        RootFacts         `json:"root"`
	Agents      []AgentFacts      `json:"agents"` // index 0 is the root lane
	Turns       []TurnFacts       `json:"turns"`  // all lanes, by start
	Gaps        []GapFacts        `json:"gaps"`   // root wait_user segments
	Waits       []WaitFacts       `json:"waits"`  // root wait_worker segments
	Groups      []GroupFacts      `json:"groups"`
	Compactions []CompactionFacts `json:"compactions"`
	Background  []OpFacts         `json:"background"`
	LongOps     []OpFacts         `json:"long_ops"`        // longest verdict-phase ops, all lanes
	Failures    []OpFacts         `json:"failures"`        // failed steps in the code phase (edits, shell, git, network …); query misses and CI status waits excluded
	Unknown     []HeadFacts       `json:"unknown"`         // unknown command heads
	UnknownOps  []OpFacts         `json:"unknown_ops"`     // longest unknown commands (evidence for D13)
	UnknownMs   map[string]int64  `json:"unknown_ms"`      // unknown time by subgroup (script, tool, command), all lanes
	ToolCalls   []ToolCallFacts   `json:"tool_calls"`      // the main thread's tool calls by phase and subgroup
	Cells       []CellFacts       `json:"cells"`           // lane kind × model × effort × lifecycle
	Delivery    DeliveryFacts     `json:"delivery"`        // the session as one delivery loop (facts_delivery.go)
	Blind       []BlindFacts      `json:"blind,omitempty"` // the root's no_telemetry intervals, longest first (at most five)
}

// BlindFacts is one root no_telemetry segment: the time after a turn's last event that the log
// says nothing about — the turn never closed (status open when the log ends) or the next prompt
// arrived before it closed (orphaned). Neither work nor a wait: not measured (product rule 3).
type BlindFacts struct {
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Turn   string `json:"turn,omitempty"`
	Status string `json:"status,omitempty"` // open | orphaned
}

// RootFacts are the root lane's totals (the session's exclusive accounting).
type RootFacts struct {
	ElapsedMs    int64                     `json:"elapsed_ms"`
	InTurnMs     int64                     `json:"in_turn_ms"`
	ByPhase      map[model.Phase]int64     `json:"by_phase"`
	ByLifecycle  map[model.Lifecycle]int64 `json:"by_lifecycle"`
	Tokens       model.TokenUsage          `json:"tokens"`     // root lane
	AllTokens    model.TokenUsage          `json:"all_tokens"` // every lane
	Turns        int                       `json:"turns"`      // root turns
	Aborted      int                       `json:"aborted"`    // root turns the user stopped
	UserMessages int                       `json:"user_messages"`
	Questions    int                       `json:"questions"`
	FailedOps    int                       `json:"failed_ops"`
	QueryMisses  int                       `json:"query_misses"`
}

// Lane kinds, from literal op content: a read-only sub-agent never edited, built, tested,
// released or touched infrastructure.
const (
	LaneRoot     = "root"
	LaneReadOnly = "read_only"
	LaneWorker   = "worker"
)

type AgentFacts struct {
	ID       string            `json:"id"`
	Path     string            `json:"path"`
	Role     string            `json:"role,omitempty"`
	Model    string            `json:"model,omitempty"`
	Depth    int               `json:"depth"`
	Kind     string            `json:"kind"` // root | read_only | worker
	Started  int64             `json:"started"`
	Ended    int64             `json:"ended"`
	InTurnMs int64             `json:"in_turn_ms"`
	Turns    int               `json:"turns"`
	Ops      int               `json:"ops"`
	Tokens   *model.TokenUsage `json:"tokens,omitempty"`
	First    *model.TokenUsage `json:"first,omitempty"` // first counted call of the first turn (spawn cost)
	Active   []model.Interval  `json:"active,omitempty"`
}

type TurnFacts struct {
	Lane        int                       `json:"lane"` // index into Agents
	ID          string                    `json:"id"`
	Start       int64                     `json:"start"`
	End         int64                     `json:"end"`
	Status      string                    `json:"status"`
	Trigger     string                    `json:"trigger,omitempty"`
	Model       string                    `json:"model,omitempty"`
	Effort      string                    `json:"effort,omitempty"`
	Lifecycle   model.Lifecycle           `json:"lc,omitempty"`
	Question    bool                      `json:"question,omitempty"`
	Tokens      *model.TokenUsage         `json:"tokens,omitempty"`
	Responses   int                       `json:"responses,omitempty"`
	First       *model.TokenUsage         `json:"first,omitempty"`
	ContextPeak int64                     `json:"context_peak,omitempty"`
	RootTurn    string                    `json:"root_turn,omitempty"`
	LLMMs       int64                     `json:"llm_ms"` // model-output segments inside the turn
	ByLifecycle map[model.Lifecycle]int64 `json:"by_lifecycle,omitempty"`
	// LLMByLifecycle is the model-output time of the turn by the stage it served (the nearest
	// tool call's stage, or the turn's signal); the rest of ByLifecycle is tool and wait time.
	LLMByLifecycle map[model.Lifecycle]int64 `json:"llm_by_lifecycle,omitempty"`
}

// GapFacts is one root wait_user segment with what surrounded it.
type GapFacts struct {
	Start            int64             `json:"start"`
	End              int64             `json:"end"`
	AfterQuestion    bool              `json:"after_question"`               // the turn before it asked the user
	TurnChangedFiles bool              `json:"turn_changed_files,omitempty"` // the turn before it had already changed a file (its own edit, or a sub-agent's inside it)
	PrevTurn         string            `json:"prev_turn,omitempty"`
	NextTurn         string            `json:"next_turn,omitempty"`
	NextTrigger      string            `json:"next_trigger,omitempty"`
	NextFirst        *model.TokenUsage `json:"next_first,omitempty"` // first counted call of the next turn
}

// WaitFacts is one root wait_worker segment. SoloMs is the part of it during which at most one
// sub-agent lane was inside a turn; Lanes lists the sub-agent lanes active at any point of it.
type WaitFacts struct {
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Kind   string `json:"kind"` // agent | wait | sleep | ci | poll-loop | process | tail-f | harness
	Turn   string `json:"turn,omitempty"`
	SoloMs int64  `json:"solo_ms"`
	Lanes  []int  `json:"lanes,omitempty"`
}

type GroupFacts struct {
	ID       string           `json:"id"`
	Identity string           `json:"identity"`
	Shape    string           `json:"shape"` // phase + head word + subcommand (cross-session key)
	Phase    model.Phase      `json:"phase"`
	Title    string           `json:"title"`
	Start    int64            `json:"start"`
	End      int64            `json:"end"`
	Attempts int              `json:"attempts"`
	Failed   int              `json:"failed"`
	Lanes    []int            `json:"lanes"`
	RoleMs   map[string]int64 `json:"role_ms"` // first, retry_after_failure, rerun, parallel, fix, infra_recovery, worker_queue
	Windows  []WindowFacts    `json:"windows,omitempty"`
}

// WindowFacts is the span from a failed attempt to the next attempt on the same lane, with the
// tokens of the turns that overlap it, pro rata by time. RetryOp is the attempt that ends the
// window. Recovery names what ran in between (none: reads and queries only; fix: a change op
// on any lane; infra; worker; other: any other step on the lane; mixed); RetryFailed says the
// retry failed too; HumanBoundary that a user-triggered root turn started inside the window
// (the user may have changed something the log does not show).
type WindowFacts struct {
	Lane          int              `json:"lane"`
	Start         int64            `json:"start"`
	End           int64            `json:"end"`
	RetryOp       string           `json:"retry_op,omitempty"`
	Tokens        model.TokenUsage `json:"tokens"`
	Recovery      string           `json:"recovery,omitempty"`
	RetryFailed   bool             `json:"retry_failed,omitempty"`
	HumanBoundary bool             `json:"human_boundary,omitempty"`
}

type CompactionFacts struct {
	Lane           int               `json:"lane"`
	Op             string            `json:"op"`
	Turn           string            `json:"turn,omitempty"`
	Start          int64             `json:"start"`
	End            int64             `json:"end"`
	Context        int64             `json:"context,omitempty"`
	Reread         *model.TokenUsage `json:"reread,omitempty"`
	InChangeWindow bool              `json:"in_change_window,omitempty"` // between its turn's first and last edit on the same lane
}

type OpFacts struct {
	Lane   int         `json:"lane"`
	ID     string      `json:"id"`
	Phase  model.Phase `json:"phase"`
	Sub    string      `json:"sub,omitempty"` // classify.Subgroup of the op ("" for a phase without subgroups)
	Kind   string      `json:"kind"`
	Shape  string      `json:"shape,omitempty"`
	Title  string      `json:"title"`
	Start  int64       `json:"start"`
	End    int64       `json:"end"`
	Status string      `json:"status"`
	Exit   *int        `json:"exit,omitempty"`
	Turn   string      `json:"turn,omitempty"`
}

type HeadFacts struct {
	Head  string `json:"head"`
	Count int    `json:"count"`
	Ms    int64  `json:"ms"`
}

// ToolCallFacts is what the main thread's tool calls of one phase and subgroup did in a session:
// how many, their exclusive time (the partition, not the raw op sum), how many were query misses
// (a search that found nothing, a CI status still pending) and how many failed steps.
type ToolCallFacts struct {
	Phase  model.Phase `json:"phase"`
	Sub    string      `json:"sub,omitempty"` // classify.Subgroup; "" for a phase without subgroups
	Calls  int         `json:"calls"`
	Ms     int64       `json:"ms"`
	Misses int         `json:"misses"`
	Failed int         `json:"failed"`
	// Shared counts the compound calls whose equal share landed here (their call is counted
	// once, under the dominant phase); SharedMs is that estimated part of Ms.
	Shared   int   `json:"shared,omitempty"`
	SharedMs int64 `json:"shared_ms,omitempty"`
}

// CellFacts is time and tokens for one (lane kind, model, effort, lifecycle) cell. Tokens are a
// turn's tokens spread over its lifecycle segments pro rata by time.
type CellFacts struct {
	LaneKind  string           `json:"lane_kind"`
	Model     string           `json:"model,omitempty"`
	Effort    string           `json:"effort,omitempty"`
	Lifecycle model.Lifecycle  `json:"lc"`
	Ms        int64            `json:"ms"`
	Tokens    model.TokenUsage `json:"tokens"`
}

// Extract builds the Facts of a derived session. It never modifies the model.
func Extract(s *model.Session) Facts {
	f := Facts{Version: FactsVersion, ID: s.ID, Source: s.Source, Title: s.Title, CWD: s.CWD, Branch: s.Branch, Model: s.Model, CLI: s.CLI, Started: s.Started, Ended: s.Ended, Live: s.Live}
	if len(s.Lanes) == 0 {
		return f
	}
	root := s.Lanes[0]
	f.Root = RootFacts{
		ElapsedMs: s.Totals.ElapsedMs, InTurnMs: s.Totals.InTurnMs,
		ByPhase: copyPhases(s.Totals.ByPhase), ByLifecycle: copyLifecycles(s.Totals.ByLifecycle),
		AllTokens: s.Totals.Tokens, Turns: len(root.Turns), UserMessages: s.Totals.UserMessages,
		Questions: s.Totals.Questions, FailedOps: s.Totals.Failed, QueryMisses: s.Totals.QueryMisses,
	}
	if root.Tokens != nil {
		f.Root.Tokens = *root.Tokens
	}
	laneIndex := map[string]int{}
	opByID := map[string]*model.Operation{}
	for i, l := range s.Lanes {
		laneIndex[l.ID] = i
		f.Agents = append(f.Agents, agentFacts(l, i == 0))
		for _, o := range l.Ops {
			opByID[o.ID] = o
		}
	}
	cells := map[cellKey]*CellFacts{}
	for i, l := range s.Lanes {
		questions := map[string]bool{}
		for _, m := range l.Markers {
			if m.Kind == "question" {
				questions[m.Turn] = true
			}
		}
		for _, t := range l.Turns {
			tf := TurnFacts{Lane: i, ID: t.ID, Start: t.Start, End: t.End, Status: t.Status, Trigger: t.Trigger, Model: t.Model, Effort: t.Effort, Lifecycle: t.Lifecycle,
				Question: questions[t.ID], Tokens: t.Tokens, Responses: t.Responses, First: t.First, ContextPeak: t.ContextPeak, RootTurn: t.RootTurn}
			if i == 0 && t.Status == "aborted" {
				f.Root.Aborted++
			}
			tf.LLMMs, tf.ByLifecycle, tf.LLMByLifecycle = turnSplit(l, t)
			f.Turns = append(f.Turns, tf)
			addCells(cells, f.Agents[i].Kind, &tf)
		}
	}
	sort.SliceStable(f.Turns, func(a, b int) bool { return f.Turns[a].Start < f.Turns[b].Start })
	f.Delivery = deliveryFacts(s, laneIndex)
	f.Gaps = gapFacts(root, changedRootTurns(s))
	f.Blind = blindFacts(root)
	f.Waits = waitFacts(s, opByID)
	f.Groups = groupFacts(s, laneIndex, opByID)
	for i, l := range s.Lanes {
		windows := changeWindows(l)
		for _, o := range l.Ops {
			switch {
			case o.Phase == classify.Compaction:
				c := CompactionFacts{Lane: i, Op: o.ID, Turn: o.Turn, Start: o.Start, End: o.End, Context: o.Context, Reread: o.Tokens}
				if w := windows[o.Turn]; w != nil && o.Start >= w.first && o.Start <= w.last {
					c.InChangeWindow = true
				}
				f.Compactions = append(f.Compactions, c)
			case o.Background:
				f.Background = append(f.Background, opFacts(i, o))
			}
			if o.Phase == classify.Code && o.Failure() && !o.Background {
				f.Failures = append(f.Failures, opFacts(i, o))
			}
			if (isVerdictPhase(o.Phase) || isHookOp(o)) && !o.Background && o.End > o.Start {
				f.LongOps = append(f.LongOps, opFacts(i, o))
			}
			if !o.Background && o.End > o.Start {
				if len(o.Shares) == 0 && o.Phase == classify.Unknown {
					f.UnknownOps = append(f.UnknownOps, opFacts(i, o))
				} else {
					f.UnknownOps = append(f.UnknownOps, unknownShareFacts(i, o)...)
				}
			}
		}
	}
	sort.SliceStable(f.UnknownOps, func(a, b int) bool {
		return f.UnknownOps[a].End-f.UnknownOps[a].Start > f.UnknownOps[b].End-f.UnknownOps[b].Start
	})
	if len(f.UnknownOps) > 30 {
		f.UnknownOps = f.UnknownOps[:30]
	}
	sort.SliceStable(f.LongOps, func(a, b int) bool { return f.LongOps[a].End-f.LongOps[a].Start > f.LongOps[b].End-f.LongOps[b].Start })
	if len(f.LongOps) > 40 {
		f.LongOps = f.LongOps[:40]
	}
	f.Unknown = unknownHeads(s)
	f.UnknownMs = map[string]int64{}
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			if o.Background {
				continue
			}
			if _, ms, ok := o.Booked(classify.Unknown); ok {
				sub := o.Subgroup
				if len(o.Shares) > 0 {
					sub = classify.Subgroup(classify.Unknown, "unknown")
					for _, sh := range o.Shares {
						if sh.Phase == classify.Unknown {
							sub = sh.Sub
						}
					}
				}
				f.UnknownMs[sub] += ms
			}
		}
	}
	f.ToolCalls = toolCallFacts(root)
	for _, c := range cells {
		f.Cells = append(f.Cells, *c)
	}
	sort.Slice(f.Cells, func(a, b int) bool { return f.Cells[a].Ms > f.Cells[b].Ms })
	return f
}

// toolCallFacts counts the main thread's tool calls by phase and subgroup: every non-background
// op that is not model output, with the exclusive time of the segments it won.
func toolCallFacts(root *model.Lane) []ToolCallFacts {
	type key struct {
		phase model.Phase
		sub   string
	}
	acc := map[key]*ToolCallFacts{}
	get := func(p model.Phase, sub string) *ToolCallFacts {
		k := key{p, sub}
		if acc[k] == nil {
			acc[k] = &ToolCallFacts{Phase: p, Sub: sub}
		}
		return acc[k]
	}
	byID := map[string]*model.Operation{}
	for _, o := range root.Ops {
		byID[o.ID] = o
		if o.Background || o.Phase == classify.LLM || o.Phase == classify.WaitUser || o.Phase == classify.Compaction {
			continue
		}
		t := get(o.Phase, o.Subgroup)
		t.Calls++
		if o.QueryMiss {
			t.Misses++
		} else if o.Failure() {
			t.Failed++
		}
		for _, sh := range o.Shares {
			if !sh.Literal && (sh.Phase != o.Phase || sh.Sub != o.Subgroup) {
				get(sh.Phase, sh.Sub).Shared++
			}
		}
	}
	for _, sg := range root.Segments {
		o := byID[sg.Op]
		if o == nil || o.Background || o.Phase == classify.LLM || o.Phase == classify.WaitUser || o.Phase == classify.Compaction {
			continue
		}
		// the slice's own phase and sub-row: a compound command's shares land in their categories
		t := get(sg.Phase, sg.Sub)
		t.Ms += sg.End - sg.Start
		if sg.Shared {
			t.SharedMs += sg.End - sg.Start
		}
	}
	out := make([]ToolCallFacts, 0, len(acc))
	for _, t := range acc {
		out = append(out, *t)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Calls > out[b].Calls || out[a].Calls == out[b].Calls && string(out[a].Phase)+out[a].Sub < string(out[b].Phase)+out[b].Sub
	})
	return out
}

func isVerdictPhase(p model.Phase) bool {
	return p == classify.Test || p == classify.Build || p == classify.Release || p == classify.Infra
}

// isHookOp reports whether o is a harness stop hook (wait_worker/hook): harness time whose
// command is recorded, so a slow hook is listed among the long runs under its own shape.
func isHookOp(o *model.Operation) bool {
	return o.Phase == classify.WaitWorker && classify.BaseKind(o.Kind) == "hook"
}

func copyPhases(m map[model.Phase]int64) map[model.Phase]int64 {
	out := map[model.Phase]int64{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyLifecycles(m map[model.Lifecycle]int64) map[model.Lifecycle]int64 {
	out := map[model.Lifecycle]int64{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func agentFacts(l *model.Lane, isRoot bool) AgentFacts {
	a := AgentFacts{ID: l.ID, Path: l.Path, Role: l.Role, Model: l.Model, Depth: l.Depth, Kind: laneKind(l, isRoot), Started: l.Started, Ended: l.Ended, InTurnMs: l.InTurnMs, Turns: len(l.Turns), Ops: len(l.Ops), Tokens: l.Tokens, Active: l.Active}
	if len(l.Turns) > 0 {
		a.First = l.Turns[0].First
	}
	return a
}

// laneKind classifies a lane by what its ops literally did. A sub-agent whose every op only
// read state (query kinds, web/MCP lookups, images) or was model output / waiting is read-only.
func laneKind(l *model.Lane, isRoot bool) string {
	if isRoot {
		return LaneRoot
	}
	for _, o := range l.Ops {
		if !readOnlyOp(o) {
			return LaneWorker
		}
	}
	return LaneReadOnly
}

func readOnlyOp(o *model.Operation) bool {
	switch o.Phase {
	case classify.LLM, classify.WaitWorker, classify.Compaction, classify.Idle:
		return true
	case classify.Code:
		kind := o.Kind
		if i := strings.IndexByte(kind, '|'); i >= 0 {
			kind = kind[:i]
		}
		return classify.QueryKind(o.Phase, kind) || kind == "web_search" || kind == "mcp" || kind == "image"
	}
	return false
}

// turnSplit measures a turn's model-output time and its time by lifecycle from the lane's
// segments (already cut at turn boundaries by model.Derive).
func turnSplit(l *model.Lane, t *model.Turn) (llm int64, byLc, llmByLc map[model.Lifecycle]int64) {
	byLc = map[model.Lifecycle]int64{}
	llmByLc = map[model.Lifecycle]int64{}
	for _, sg := range l.Segments {
		if sg.End <= t.Start || sg.Start >= t.End {
			continue
		}
		s, e := max(sg.Start, t.Start), min(sg.End, t.End)
		if e <= s {
			continue
		}
		byLc[sg.Lifecycle] += e - s
		if sg.Phase == classify.LLM {
			llm += e - s
			llmByLc[sg.Lifecycle] += e - s
		}
	}
	return llm, byLc, llmByLc
}

type cellKey struct {
	kind, model, effort string
	lc                  model.Lifecycle
}

// addCells spreads a turn's time and tokens over its lifecycle stages. Only work stages and
// model output carry tokens (waits and gaps have none).
func addCells(cells map[cellKey]*CellFacts, laneKind string, t *TurnFacts) {
	var total int64
	for lc, ms := range t.ByLifecycle {
		if classify.IsWorkLifecycle(lc) || lc == model.Lifecycle(classify.LLM) {
			total += ms
		}
	}
	for lc, ms := range t.ByLifecycle {
		if !(classify.IsWorkLifecycle(lc) || lc == model.Lifecycle(classify.LLM)) {
			continue
		}
		k := cellKey{laneKind, t.Model, t.Effort, lc}
		c := cells[k]
		if c == nil {
			c = &CellFacts{LaneKind: laneKind, Model: t.Model, Effort: t.Effort, Lifecycle: lc}
			cells[k] = c
		}
		c.Ms += ms
		if t.Tokens != nil && total > 0 {
			c.Tokens.Add(scale(t.Tokens, ms, total))
		}
	}
}

// scale returns u × num/den (integer, rounded down).
func scale(u *model.TokenUsage, num, den int64) *model.TokenUsage {
	if u == nil || den <= 0 {
		return nil
	}
	return &model.TokenUsage{Input: u.Input * num / den, Cached: u.Cached * num / den, CacheWrite: u.CacheWrite * num / den, Output: u.Output * num / den, Reasoning: u.Reasoning * num / den, Total: u.Total * num / den}
}

// changedRootTurns lists the root turns that changed a file: by their own change ops, or by a
// sub-agent turn that ran inside them (session scope, as in the delivery walk).
func changedRootTurns(s *model.Session) map[string]bool {
	changed := map[string]bool{}
	for i, l := range s.Lanes {
		windows := changeWindows(l)
		for _, t := range l.Turns {
			if windows[t.ID] == nil {
				continue
			}
			if i == 0 {
				changed[t.ID] = true
			} else if t.RootTurn != "" {
				changed[t.RootTurn] = true
			}
		}
	}
	return changed
}

// blindFacts lists the root lane's no_telemetry segments with the turn each one follows (the
// last turn to close at or before it that never closed properly), longest first, at most five.
func blindFacts(root *model.Lane) []BlindFacts {
	var out []BlindFacts
	for _, sg := range root.Segments {
		if sg.Phase != classify.NoTelemetry || sg.End <= sg.Start {
			continue
		}
		b := BlindFacts{Start: sg.Start, End: sg.End}
		for _, t := range root.Turns {
			if t.End <= sg.Start && (t.Status == "open" || t.Status == "orphaned") {
				b.Turn, b.Status = t.ID, t.Status
			}
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].End-out[a].Start > out[b].End-out[b].Start })
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func gapFacts(root *model.Lane, changed map[string]bool) []GapFacts {
	questions := map[string]bool{}
	for _, m := range root.Markers {
		if m.Kind == "question" {
			questions[m.Turn] = true
		}
	}
	var out []GapFacts
	for _, sg := range root.Segments {
		if sg.Phase != classify.WaitUser {
			continue
		}
		g := GapFacts{Start: sg.Start, End: sg.End}
		for _, t := range root.Turns {
			if t.End <= sg.Start+1 && t.Start < sg.Start {
				g.PrevTurn = t.ID
				g.AfterQuestion = questions[t.ID]
				g.TurnChangedFiles = changed[t.ID]
			}
			if g.NextTurn == "" && t.Start >= sg.End-1 {
				g.NextTurn, g.NextTrigger, g.NextFirst = t.ID, t.Trigger, t.First
			}
		}
		out = append(out, g)
	}
	return out
}

// waitFacts lists the root lane's wait_worker segments and, for each, how much of it passed
// with at most one sub-agent inside a turn (a sweep over the sub-agents' turn intervals).
func waitFacts(s *model.Session, opByID map[string]*model.Operation) []WaitFacts {
	root := s.Lanes[0]
	type edge struct {
		t    int64
		d    int
		lane int
	}
	var edges []edge
	for i, l := range s.Lanes[1:] {
		for _, iv := range l.Active {
			edges = append(edges, edge{iv.Start, 1, i + 1}, edge{iv.End, -1, i + 1})
		}
	}
	sort.Slice(edges, func(a, b int) bool {
		if edges[a].t != edges[b].t {
			return edges[a].t < edges[b].t
		}
		return edges[a].d < edges[b].d
	})
	var out []WaitFacts
	for _, sg := range root.Segments {
		if sg.Phase != classify.WaitWorker || sg.Shared {
			continue // a wait is measured; an equal share of a compound command is not one
		}
		w := WaitFacts{Start: sg.Start, End: sg.End, Kind: "harness"}
		if o := opByID[sg.Op]; o != nil {
			w.Kind = o.Kind
			for _, sh := range o.Shares { // a literal `sleep N` slice of a compound command
				if sh.Phase == sg.Phase {
					w.Kind = sh.Kind
				}
			}
			if i := strings.IndexByte(w.Kind, '|'); i >= 0 {
				w.Kind = w.Kind[:i]
			}
			w.Turn = o.Turn
		}
		active := 0
		lanes := map[int]bool{}
		cur := sg.Start
		for _, e := range edges {
			if e.t <= sg.Start {
				active += e.d
				if e.d > 0 {
					lanes[e.lane] = true
				} else if active == 0 {
					lanes = map[int]bool{}
				}
				continue
			}
			if e.t >= sg.End {
				break
			}
			if active <= 1 {
				w.SoloMs += e.t - cur
			}
			cur = e.t
			active += e.d
			if e.d > 0 {
				lanes[e.lane] = true
			}
		}
		if active <= 1 {
			w.SoloMs += sg.End - cur
		}
		for ln := range lanes {
			w.Lanes = append(w.Lanes, ln)
		}
		sort.Ints(w.Lanes)
		out = append(out, w)
	}
	return out
}

func groupFacts(s *model.Session, laneIndex map[string]int, opByID map[string]*model.Operation) []GroupFacts {
	var out []GroupFacts
	for _, g := range s.Groups {
		gf := GroupFacts{ID: g.ID, Identity: g.Identity, Shape: Shape(g.Phase, g.Identity), Phase: g.Phase, Title: g.Title, Start: g.Start, End: g.End, Attempts: g.Attempts, Failed: g.Failed, RoleMs: map[string]int64{}}
		for _, id := range g.Lanes {
			if i, ok := laneIndex[id]; ok {
				gf.Lanes = append(gf.Lanes, i)
			}
		}
		var attempts []*model.Operation
		for _, id := range g.Members {
			o := opByID[id]
			if o == nil {
				continue
			}
			if r := model.RoleOf(o.Kind); r != "" {
				gf.RoleMs[r] += o.End - o.Start
			}
			if o.Attempt > 0 {
				attempts = append(attempts, o)
			}
		}
		sort.Slice(attempts, func(a, b int) bool { return attempts[a].Start < attempts[b].Start })
		for i := 1; i < len(attempts); i++ {
			prev, next := attempts[i-1], attempts[i]
			if !prev.Failure() || next.Start < prev.End || next.Lane != prev.Lane {
				continue
			}
			li := laneIndex[prev.Lane]
			w := WindowFacts{Lane: li, Start: prev.End, End: next.Start, RetryOp: next.ID, Recovery: windowRecovery(s, s.Lanes[li], g.ID, prev.End, next.Start), RetryFailed: next.Failure(), HumanBoundary: userTurnInside(s.Lanes[0], prev.End, next.Start)}
			for _, t := range s.Lanes[li].Turns {
				if t.Tokens == nil || t.End <= w.Start || t.Start >= w.End || t.End <= t.Start {
					continue
				}
				ov := min(t.End, w.End) - max(t.Start, w.Start)
				w.Tokens.Add(scale(t.Tokens, ov, t.End-t.Start))
			}
			gf.Windows = append(gf.Windows, w)
		}
		out = append(out, gf)
	}
	return out
}

func opFacts(lane int, o *model.Operation) OpFacts {
	kind := o.Kind
	if i := strings.IndexByte(kind, '|'); i >= 0 {
		kind = kind[:i]
	}
	if o.Phase == classify.Unknown {
		kind = unknownHead(o.Title)
	}
	of := OpFacts{Lane: lane, ID: o.ID, Phase: o.Phase, Kind: kind, Sub: o.Subgroup, Title: o.Title, Start: o.Start, End: o.End, Status: o.Status, Exit: o.Exit, Turn: o.Turn}
	switch {
	case isHookOp(o):
		of.Shape = Shape("hook", "\n"+o.Detail) // the hook's command, not the "stop hook" title
	case o.Identity != "":
		of.Shape = Shape(o.Phase, o.Identity)
	default:
		of.Shape = Shape(o.Phase, "\n"+o.Title)
	}
	return of
}

func unknownShareFacts(lane int, o *model.Operation) []OpFacts {
	var out []OpFacts
	at := o.Start
	for _, sh := range o.Shares {
		end := at + sh.Ms
		if sh.Phase == classify.Unknown && end > at {
			title := sh.Segment
			out = append(out, OpFacts{
				Lane: lane, ID: o.ID, Phase: classify.Unknown, Sub: sh.Sub,
				Kind: unknownHead(title), Shape: Shape(classify.Unknown, "\n"+title), Title: title,
				Start: at, End: end, Status: o.Status, Exit: o.Exit, Turn: o.Turn,
			})
		}
		at = end
	}
	return out
}

func unknownHeads(s *model.Session) []HeadFacts {
	heads := map[string]*HeadFacts{}
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			if o.Background {
				continue
			}
			text, ms, ok := o.Booked(classify.Unknown) // the op, or the unknown share of a compound one
			if !ok {
				continue
			}
			if len(o.Shares) == 0 {
				text = o.Title
			}
			h := unknownHead(text)
			e := heads[h]
			if e == nil {
				e = &HeadFacts{Head: h}
				heads[h] = e
			}
			e.Count++
			e.Ms += ms
		}
	}
	var out []HeadFacts
	for _, e := range heads {
		out = append(out, *e)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Ms > out[b].Ms || out[a].Ms == out[b].Ms && out[a].Head < out[b].Head
	})
	return out
}

// unknownHead is the word a rule would match for an unknown command: the first word of its
// title past any env assignments and wrappers (the same skip Shape applies), so `S=/tmp/x go
// run ./probe` is keyed `go`, not `S=/tmp/x`. A command that is nothing but an assignment
// keeps its first word.
func unknownHead(title string) string {
	fields := strings.Fields(title)
	for _, w := range fields {
		if prefixWord(w) {
			continue
		}
		return w
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return title
}

// prefixWord reports whether w, at the head of a command, is not the command: an env
// assignment (`FOO=bar`, `export PATH=…;`) or a wrapper (`env`, `sudo`, `nohup`, `time`,
// `export`).
func prefixWord(w string) bool {
	if strings.Contains(w, "=") && !strings.HasPrefix(w, "=") {
		return true
	}
	switch w {
	case "env", "sudo", "nohup", "time", "export", ";", "&&", "||", "|":
		return true
	}
	return false
}

// Shape is the cross-session key of a command: its phase, head word and subcommand, taken from
// the normalized identity ("<cwd>\n<command>"). The words come from the classifier: the
// top-level segment that decided the phase, past shell keywords, wrappers, env prefixes and
// `bash -c` (`set -euo pipefail; run-tests.sh app` is `test run-tests.sh app`, not `test set`).
// Exact commands almost never recur across sessions (temp paths, MR numbers), shapes do; retry
// groups themselves stay exact.
func Shape(phase model.Phase, identity string) string {
	cmd := identity
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = cmd[i+1:]
	}
	res := classify.Command(cmd, "")
	seg := res.Segment
	if seg == "" || phase != "hook" && res.Phase != phase {
		// a regex rule or a heredoc decided, or the rules have moved since the op was
		// classified (an overlay change re-derives the session anyway): the text's first words
		return shapeWords(phase, cmd)
	}
	if head, sub := classify.HeadWords(seg); head != "" {
		if sub != "" {
			return string(phase) + " " + head + " " + sub
		}
		return string(phase) + " " + head
	}
	return shapeWords(phase, cmd)
}

// shapeWords is the fallback key when the classifier sees no command in the text (a heredoc
// body, a bare assignment): the first two words past env assignments and wrappers.
func shapeWords(phase model.Phase, cmd string) string {
	var words []string
	for _, w := range strings.Fields(cmd) {
		if len(words) == 0 && prefixWord(w) {
			continue // env assignment or wrapper prefix
		}
		if len(words) == 1 && strings.HasPrefix(w, "-") {
			break
		}
		words = append(words, w)
		if len(words) == 2 {
			break
		}
	}
	if len(words) == 0 {
		return string(phase)
	}
	return string(phase) + " " + strings.Join(words, " ")
}
