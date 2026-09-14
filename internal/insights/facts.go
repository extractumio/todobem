// Package insights turns parsed sessions into ranked, evidence-backed findings about where a
// harness loses time and tokens (docs/INSIGHTS-SPEC.md). Facts is the compact per-session
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
// file with another version is a miss.
const FactsVersion = 2

// Facts is everything the detectors need about one session, in a few KB.
type Facts struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
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
	LongOps     []OpFacts         `json:"long_ops"`    // longest verdict-phase ops, all lanes
	Failures    []OpFacts         `json:"failures"`    // failed steps in the code phase (edits, patches, scripts)
	Unknown     []HeadFacts       `json:"unknown"`     // unknown command heads
	UnknownOps  []OpFacts         `json:"unknown_ops"` // longest unknown commands (evidence for D13)
	LLMErrors   int               `json:"llm_errors"`
	LLMErrorAt  []ErrorAt         `json:"llm_errors_at,omitempty"`
	Cells       []CellFacts       `json:"cells"` // lane kind × model × effort × lifecycle
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
	// LLMByLifecycle is the model-output time of the turn by the stage it was attributed to
	// (the stage-bracket convention); the rest of ByLifecycle is tool and wait time.
	LLMByLifecycle map[model.Lifecycle]int64 `json:"llm_by_lifecycle,omitempty"`
}

// GapFacts is one root wait_user segment with what surrounded it.
type GapFacts struct {
	Start         int64             `json:"start"`
	End           int64             `json:"end"`
	AfterQuestion bool              `json:"after_question"` // the turn before it asked the user
	PrevTurn      string            `json:"prev_turn,omitempty"`
	NextTurn      string            `json:"next_turn,omitempty"`
	NextTrigger   string            `json:"next_trigger,omitempty"`
	NextFirst     *model.TokenUsage `json:"next_first,omitempty"` // first counted call of the next turn
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
// tokens of the turns that overlap it, pro rata by time.
type WindowFacts struct {
	Lane   int              `json:"lane"`
	Start  int64            `json:"start"`
	End    int64            `json:"end"`
	Tokens model.TokenUsage `json:"tokens"`
}

type CompactionFacts struct {
	Lane    int               `json:"lane"`
	Op      string            `json:"op"`
	Turn    string            `json:"turn,omitempty"`
	Start   int64             `json:"start"`
	End     int64             `json:"end"`
	Context int64             `json:"context,omitempty"`
	Reread  *model.TokenUsage `json:"reread,omitempty"`
}

type OpFacts struct {
	Lane   int         `json:"lane"`
	ID     string      `json:"id"`
	Phase  model.Phase `json:"phase"`
	Kind   string      `json:"kind"`
	Shape  string      `json:"shape,omitempty"`
	Title  string      `json:"title"`
	Start  int64       `json:"start"`
	End    int64       `json:"end"`
	Status string      `json:"status"`
	Exit   *int        `json:"exit,omitempty"`
	Turn   string      `json:"turn,omitempty"`
}

// ErrorAt is one invalid tool call (an llm_error marker): a point in time on a lane.
type ErrorAt struct {
	Lane int    `json:"lane"`
	T    int64  `json:"t"`
	Turn string `json:"turn,omitempty"`
}

type HeadFacts struct {
	Head  string `json:"head"`
	Count int    `json:"count"`
	Ms    int64  `json:"ms"`
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
	f := Facts{Version: FactsVersion, ID: s.ID, Title: s.Title, CWD: s.CWD, Branch: s.Branch, Model: s.Model, CLI: s.CLI, Started: s.Started, Ended: s.Ended, Live: s.Live}
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
			switch m.Kind {
			case "question":
				questions[m.Turn] = true
			case "llm_error":
				f.LLMErrors++
				f.LLMErrorAt = append(f.LLMErrorAt, ErrorAt{Lane: i, T: m.T, Turn: m.Turn})
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
	f.Gaps = gapFacts(root)
	f.Waits = waitFacts(s, opByID)
	f.Groups = groupFacts(s, laneIndex, opByID)
	for i, l := range s.Lanes {
		for _, o := range l.Ops {
			switch {
			case o.Phase == classify.Compaction:
				f.Compactions = append(f.Compactions, CompactionFacts{Lane: i, Op: o.ID, Turn: o.Turn, Start: o.Start, End: o.End, Context: o.Context, Reread: o.Tokens})
			case o.Background:
				f.Background = append(f.Background, opFacts(i, o))
			}
			if o.Phase == classify.Code && o.Failure() && !o.Background {
				f.Failures = append(f.Failures, opFacts(i, o))
			}
			if isVerdictPhase(o.Phase) && !o.Background && o.End > o.Start {
				f.LongOps = append(f.LongOps, opFacts(i, o))
			}
			if o.Phase == classify.Unknown && !o.Background && o.End > o.Start {
				f.UnknownOps = append(f.UnknownOps, opFacts(i, o))
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
	for _, c := range cells {
		f.Cells = append(f.Cells, *c)
	}
	sort.Slice(f.Cells, func(a, b int) bool { return f.Cells[a].Ms > f.Cells[b].Ms })
	return f
}

func isVerdictPhase(p model.Phase) bool {
	return p == classify.Test || p == classify.Build || p == classify.Release || p == classify.Infra
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

func gapFacts(root *model.Lane) []GapFacts {
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
		if sg.Phase != classify.WaitWorker {
			continue
		}
		w := WaitFacts{Start: sg.Start, End: sg.End, Kind: "harness"}
		if o := opByID[sg.Op]; o != nil {
			w.Kind = o.Kind
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
			w := WindowFacts{Lane: li, Start: prev.End, End: next.Start}
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
		// an unknown command is keyed by its head word: the thing a rule would match
		kind = o.Title
		if i := strings.IndexAny(kind, " \n"); i > 0 {
			kind = kind[:i]
		}
	}
	of := OpFacts{Lane: lane, ID: o.ID, Phase: o.Phase, Kind: kind, Title: o.Title, Start: o.Start, End: o.End, Status: o.Status, Exit: o.Exit, Turn: o.Turn}
	if o.Identity != "" {
		of.Shape = Shape(o.Phase, o.Identity)
	} else {
		of.Shape = Shape(o.Phase, "\n"+o.Title)
	}
	return of
}

func unknownHeads(s *model.Session) []HeadFacts {
	heads := map[string]*HeadFacts{}
	for _, l := range s.Lanes {
		for _, o := range l.Ops {
			if o.Phase != classify.Unknown || o.Background {
				continue
			}
			h := o.Title
			if i := strings.IndexAny(h, " \n"); i > 0 {
				h = h[:i]
			}
			e := heads[h]
			if e == nil {
				e = &HeadFacts{Head: h}
				heads[h] = e
			}
			e.Count++
			e.Ms += o.End - o.Start
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

// Shape is the cross-session key of a command: its phase, head word and subcommand, taken from
// the normalized identity ("<cwd>\n<command>"). Exact commands almost never recur across
// sessions (temp paths, MR numbers), shapes do; retry groups themselves stay exact.
func Shape(phase model.Phase, identity string) string {
	cmd := identity
	if i := strings.IndexByte(cmd, '\n'); i >= 0 {
		cmd = cmd[i+1:]
	}
	var words []string
	for _, w := range strings.Fields(cmd) {
		if len(words) == 0 && strings.Contains(w, "=") && !strings.HasPrefix(w, "=") {
			continue // env assignment prefix
		}
		if len(words) == 0 && (w == "env" || w == "sudo" || w == "nohup" || w == "time") {
			continue
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
