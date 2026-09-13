// Package model is the source-agnostic normalized schema (see docs/SCHEMA.md).
package model

import "github.com/extractumio/todobem/internal/classify"

type Phase = classify.Phase

// Src points at the exact source line of an event so the inspector can show it.
type Src struct {
	File string `json:"file"`
	Off  int64  `json:"off"`
	Len  int    `json:"len"`
}

type Operation struct {
	ID       string `json:"id"`
	Lane     string `json:"lane"`
	Turn     string `json:"turn"`
	Phase    Phase  `json:"phase"`
	Kind     string `json:"kind"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Open     bool   `json:"open,omitempty"`
	Status   string `json:"status"` // completed | failed | aborted | running | recorded
	Exit     *int   `json:"exit,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"-"` // full command / file list; served per-op, not in the model payload
	Identity string `json:"identity,omitempty"`
	Group    string `json:"group,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Parallel int    `json:"parallel,omitempty"`
	Remote   bool   `json:"remote,omitempty"`
	Queued   bool   `json:"queued,omitempty"` // a polling loop preceded the work inside this command
	// Background: the op outlived the turn it started in (or started outside any turn), i.e.
	// the agent moved on while the process kept running. Excluded from the partition.
	Background bool   `json:"background,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Src        *Src   `json:"src,omitempty"`
	// call links this op to a tool-call envelope (old-format process polling).
	call string
}

// SetCall/CallID are used by adapters to stitch polls to their originating command.
func (o *Operation) SetCall(id string) { o.call = id }
func (o *Operation) CallID() string    { return o.call }

type Segment struct {
	Start int64  `json:"s"`
	End   int64  `json:"e"`
	Phase Phase  `json:"p"`
	Op    string `json:"op,omitempty"`
}

type Stage struct {
	Start int64  `json:"s"`
	End   int64  `json:"e"`
	Phase Phase  `json:"p"`
	Ops   int    `json:"n"`
	Turn  string `json:"turn,omitempty"`
}

type Marker struct {
	Fallback bool   `json:"-"` // user_message reconstructed from a raw message line; dropped when an item arrives
	T        int64  `json:"t"`
	Kind     string `json:"kind"`
	Lane     string `json:"lane"`
	Turn     string `json:"turn,omitempty"`
	Text     string `json:"text,omitempty"`
	Ref      string `json:"ref,omitempty"` // op id / agent lane id
	Src      *Src   `json:"src,omitempty"`
}

type Turn struct {
	ID      string `json:"id"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Status  string `json:"status"`            // completed | aborted | open | orphaned
	Trigger string `json:"trigger,omitempty"` // user | system (harness-injected message) | ""
	Model   string `json:"model,omitempty"`   // model id from turn_context
	Effort  string `json:"effort,omitempty"`  // reasoning effort from turn_context
	Final   string `json:"final,omitempty"`
}

type Interval struct {
	Start int64 `json:"s"`
	End   int64 `json:"e"`
}

// TokenUsage is the cumulative usage reported by the harness for one thread.
type TokenUsage struct {
	Input     int64 `json:"input"`
	Cached    int64 `json:"cached"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Total     int64 `json:"total"`
}

func (t *TokenUsage) Add(o *TokenUsage) {
	if o == nil {
		return
	}
	t.Input += o.Input
	t.Cached += o.Cached
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.Total += o.Total
}

type Lane struct {
	ID         string          `json:"id"`
	Path       string          `json:"path"`
	Parent     string          `json:"parent,omitempty"`
	Role       string          `json:"role,omitempty"`
	Nickname   string          `json:"nickname,omitempty"`
	Model      string          `json:"model,omitempty"`
	Depth      int             `json:"depth"`
	File       string          `json:"file"`
	Started    int64           `json:"started"`
	Ended      int64           `json:"ended"`
	Live       bool            `json:"live"`
	Turns      []*Turn         `json:"turns"`
	Ops        []*Operation    `json:"ops"`
	Segments   []Segment       `json:"segments"`
	Stages     []Stage         `json:"stages"`
	Markers    []Marker        `json:"markers"`
	Active     []Interval      `json:"active,omitempty"`
	Tokens     *TokenUsage     `json:"tokens,omitempty"` // latest cumulative usage reported for this thread
	ByPhase    map[Phase]int64 `json:"by_phase"`         // exclusive partition
	RawByPhase map[Phase]int64 `json:"raw_by_phase"`     // plain sum of op durations (overlaps counted)
	InTurnMs   int64           `json:"in_turn_ms"`
}

type Group struct {
	ID       string   `json:"id"`
	Identity string   `json:"identity"`
	Phase    Phase    `json:"phase"`
	Title    string   `json:"title"`
	Start    int64    `json:"start"`
	End      int64    `json:"end"`
	Attempts int      `json:"attempts"`
	Failed   int      `json:"failed"`
	Members  []string `json:"members"`
	Lanes    []string `json:"lanes"`
}

type Totals struct {
	ElapsedMs    int64            `json:"elapsed_ms"`
	InTurnMs     int64            `json:"in_turn_ms"` // time inside turns (agent responsible)
	RawOpsMs     int64            `json:"raw_ops_ms"` // sum of op durations, overlaps counted
	ByPhase      map[Phase]int64  `json:"by_phase"`
	RawByPhase   map[Phase]int64  `json:"raw_by_phase"`
	ByKind       map[string]int64 `json:"by_kind"` // test sub-kinds
	Ops          int              `json:"ops"`
	Turns        int              `json:"turns"`
	UserMessages int              `json:"user_messages"`
	SystemMsgs   int              `json:"system_messages"` // harness-injected (goal loop, notifications)
	Questions    int              `json:"questions"`
	Compactions  int              `json:"compactions"`
	Failed       int              `json:"failed_ops"`
	BackgroundMs int64            `json:"background_ms"` // sum of background process durations (root lane)
	Background   int              `json:"background_ops"`
	Tokens       TokenUsage       `json:"tokens"`        // sum over all lanes
	CompactionMs int64            `json:"compaction_ms"` // summed compaction durations, all lanes (may overlap tests)
}

type Parallel struct {
	AgentMs int64 `json:"agent_ms"` // sum of sub-agent active time
	WallMs  int64 `json:"wall_ms"`  // union of sub-agent active intervals
	Agents  int   `json:"agents"`
}

type Session struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Title    string   `json:"title"`
	CWD      string   `json:"cwd"`
	Branch   string   `json:"branch,omitempty"`
	Model    string   `json:"model,omitempty"`
	Version  string   `json:"version"`
	CLI      string   `json:"cli,omitempty"`
	Started  int64    `json:"started"`
	Ended    int64    `json:"ended"`
	Live     bool     `json:"live"`
	Now      int64    `json:"now"`
	Lanes    []*Lane  `json:"lanes"`
	Groups   []Group  `json:"groups"`
	Totals   Totals   `json:"totals"`
	Parallel Parallel `json:"parallel"`
	Bytes    int64    `json:"bytes"`
}

// SessionSummary is the list-view row.
type SessionSummary struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	CWD     string `json:"cwd"`
	Branch  string `json:"branch,omitempty"`
	Started int64  `json:"started"`
	Updated int64  `json:"updated"`
	Bytes   int64  `json:"bytes"`
	Agents  int    `json:"agents"`
	Live    bool   `json:"live"`
	CLI     string `json:"cli,omitempty"`
	Model   string `json:"model,omitempty"`
	// Filled when the session has been parsed at least once.
	Totals *Totals `json:"totals,omitempty"`
}
