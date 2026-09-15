// Package model is the source-agnostic normalized schema (see docs/ARCHITECTURE.md §3).
package model

import "github.com/extractumio/todobem/internal/classify"

type Phase = classify.Phase

// Lifecycle is the SDLC stage a segment served (classify.Lifecycle): the second exclusive
// partition of a lane's time, orthogonal to Phase. See docs/ARCHITECTURE.md §6.
type Lifecycle = classify.Lifecycle

// Src points at the exact source line of an event so the inspector can show it.
type Src struct {
	File string `json:"file"`
	Off  int64  `json:"off"`
	Len  int    `json:"len"`
}

type Operation struct {
	ID     string `json:"id"`
	Lane   string `json:"lane"`
	Turn   string `json:"turn"`
	Phase  Phase  `json:"phase"`
	Kind   string `json:"kind"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Open   bool   `json:"open,omitempty"`
	Status string `json:"status"` // completed | failed | aborted | running | recorded
	Exit   *int   `json:"exit,omitempty"`
	// QueryMiss: the harness recorded a failure (status "failed", or a non-zero exit) for a
	// query kind — a search with no match, a read or listing of a path that is not there,
	// `git diff --quiet` saying "there are changes", `test`/`which` probes. Status and exit stay
	// the literal record; the op is not counted as a failure (classify.QueryKind; set by Derive).
	QueryMiss bool   `json:"query_miss,omitempty"`
	Title     string `json:"title"`
	Detail    string `json:"-"` // full command / file list; served per-op, not in the model payload
	Identity  string `json:"identity,omitempty"`
	Group     string `json:"group,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Parallel  int    `json:"parallel,omitempty"`
	Remote    bool   `json:"remote,omitempty"`
	Queued    bool   `json:"queued,omitempty"` // a polling loop preceded the work inside this command
	// Background: the op outlived the turn it started in (or started outside any turn), i.e.
	// the agent moved on while the process kept running. Excluded from the partition.
	Background bool   `json:"background,omitempty"`
	Rule       string `json:"rule,omitempty"`
	// Lifecycle is the SDLC stage this op served; LifecycleRule says which literal signal decided
	// it (a turn's skill or mode, the lane's agent role, the command's kind, the phase default).
	// An adapter may pre-pin it from the classifier or an edited path; Derive fills the rest.
	Lifecycle     Lifecycle `json:"lc"`
	LifecycleRule string    `json:"lc_rule,omitempty"`
	// Subgroup is the finer breakdown row of the phase (classify.Subgroup): "" when the phase has none.
	Subgroup string `json:"sub,omitempty"`
	Src      *Src   `json:"src,omitempty"`
	// Compaction ops only: Context is the input of the last counted model call before the
	// compaction (the context it started from); Tokens the first counted call after it (what
	// the model re-read). Zero / nil = no usage record around it.
	Context int64       `json:"context,omitempty"`
	Tokens  *TokenUsage `json:"tokens,omitempty"`
	// call links this op to a tool-call envelope (old-format process polling).
	call string
}

// Failure reports whether the op counts as a failed step: a recorded failure or non-zero exit
// that is not a query miss.
func (o *Operation) Failure() bool {
	return !o.QueryMiss && (o.Status == "failed" || (o.Exit != nil && *o.Exit != 0))
}

// SetCall/CallID are used by adapters to stitch polls to their originating command.
func (o *Operation) SetCall(id string) { o.call = id }
func (o *Operation) CallID() string    { return o.call }

type Segment struct {
	Start     int64     `json:"s"`
	End       int64     `json:"e"`
	Phase     Phase     `json:"p"`
	Lifecycle Lifecycle `json:"lc"`
	Op        string    `json:"op,omitempty"`
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
	Skill   string `json:"skill,omitempty"`  // name of a skill actually invoked in this turn (harness injection)
	Review  bool   `json:"review,omitempty"` // Skill is a code-review / cleanup skill (classify.ReviewSkill)
	Mode    string `json:"mode,omitempty"`   // harness collaboration mode from turn_context ("plan"); "" = default
	// Lifecycle is the stage a harness-level signal pinned on the WHOLE turn (plan mode, an
	// invoked skill, review mode, the lane's agent role, or — for a sub-agent turn with no
	// signal of its own — the parent turn it ran inside); "" = decided per op. LifecycleRule
	// names the signal, the origin lane included when inherited.
	Lifecycle     Lifecycle `json:"lc,omitempty"`
	LifecycleRule string    `json:"lc_rule,omitempty"`
	// Runs are the skill runs layered on the turn: a skill invoked mid-turn (Claude Code's Skill
	// tool call) pins its stage from that moment to the turn's end or the next run. A skill
	// invoked before anything ran (Codex injects it at the turn start) is promoted to Lifecycle
	// instead, so Runs is empty for every Codex turn. Ops and segments carry the stage in force.
	Runs []StageRun `json:"lc_runs,omitempty"`
	// Token accounting of the turn's counted model calls (codex/tokens.go): Tokens is their
	// sum, Responses their number, First the usage of the first one (its uncached input is what
	// the model re-read after a gap; a sub-agent's first turn: the cost of being spawned),
	// ContextPeak the largest input of one call. Nil / zero = no usage record in the turn.
	Tokens      *TokenUsage `json:"tokens,omitempty"`
	Responses   int         `json:"responses,omitempty"`
	First       *TokenUsage `json:"first,omitempty"`
	ContextPeak int64       `json:"context_peak,omitempty"`
	// RootTurn is the root turn a sub-agent turn ran inside, from the harness's own
	// token_usage_record.root_turn_id (CLI >= 0.153); "" = not recorded. Never inferred.
	RootTurn string `json:"root_turn,omitempty"`
}

// StageRun is a span of a turn pinned to a stage by a skill invocation (Turn.Runs).
type StageRun struct {
	From      int64     `json:"from"`
	Lifecycle Lifecycle `json:"lc"`
	Rule      string    `json:"lc_rule"`
}

type Interval struct {
	Start int64 `json:"s"`
	End   int64 `json:"e"`
}

// TokenUsage is usage reported by the harness: one model call, or a sum of calls (a turn, a
// thread, a session). Cached is the part of Input served from the prompt cache; CacheWrite the
// part written to it; Reasoning the part of Output spent on reasoning.
type TokenUsage struct {
	Input      int64 `json:"input"`
	Cached     int64 `json:"cached"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	Total      int64 `json:"total"`
}

func (t *TokenUsage) Add(o *TokenUsage) {
	if o == nil {
		return
	}
	t.Input += o.Input
	t.Cached += o.Cached
	t.CacheWrite += o.CacheWrite
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
	Markers    []Marker        `json:"markers"`
	Active     []Interval      `json:"active,omitempty"` // the lane's own turns (Derive); a sub-agent is active inside them
	Tokens     *TokenUsage     `json:"tokens,omitempty"` // usage consumed by this thread: per-call usage summed on every change of the cumulative counter (survives counter restarts and a forked child's inherited counter)
	ByPhase    map[Phase]int64 `json:"by_phase"`         // exclusive partition
	RawByPhase map[Phase]int64 `json:"raw_by_phase"`     // plain sum of op durations (overlaps counted)
	// ByLifecycle is the same exclusive partition keyed by SDLC stage (sums to the same total).
	ByLifecycle map[Lifecycle]int64 `json:"by_lifecycle"`
	// Lifecycle is the stage the lane's agent role pins on every turn (overlay roles); "" = none.
	Lifecycle Lifecycle `json:"lc,omitempty"`
	InTurnMs  int64     `json:"in_turn_ms"`
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
	ElapsedMs  int64           `json:"elapsed_ms"`
	InTurnMs   int64           `json:"in_turn_ms"` // time inside turns (agent responsible)
	RawOpsMs   int64           `json:"raw_ops_ms"` // sum of op durations, overlaps counted
	ByPhase    map[Phase]int64 `json:"by_phase"`
	RawByPhase map[Phase]int64 `json:"raw_by_phase"`
	// ByLifecycle is the root lane's exclusive partition by SDLC stage: sum(by_lifecycle) ==
	// sum(by_phase) == elapsed_ms. LLM time inside a turn is attributed to the stage of the tool
	// call that followed it (or the turn's signal); the two partitions are not comparable per key.
	ByLifecycle  map[Lifecycle]int64 `json:"by_lifecycle"`
	ByKind       map[string]int64    `json:"by_kind"` // test sub-kinds
	Ops          int                 `json:"ops"`
	Turns        int                 `json:"turns"`
	UserMessages int                 `json:"user_messages"`
	SystemMsgs   int                 `json:"system_messages"` // harness-injected (goal loop, notifications)
	Questions    int                 `json:"questions"`
	Compactions  int                 `json:"compactions"`
	Failed       int                 `json:"failed_ops"`    // failed steps (Operation.Failure): query misses excluded
	QueryMisses  int                 `json:"query_misses"`  // non-zero exits of query kinds (Operation.QueryMiss)
	BackgroundMs int64               `json:"background_ms"` // sum of background process durations (root lane)
	Background   int                 `json:"background_ops"`
	Tokens       TokenUsage          `json:"tokens"`        // sum over all lanes
	CompactionMs int64               `json:"compaction_ms"` // summed compaction durations, all lanes (may overlap tests)
	// Reviews counts the turns, across all lanes, in which a review/cleanup skill was actually
	// invoked (Turn.Review). The time is in by_lifecycle["review"].
	Reviews int `json:"reviews"`
}

type Parallel struct {
	AgentMs int64 `json:"agent_ms"` // sum of sub-agent active time
	WallMs  int64 `json:"wall_ms"`  // union of sub-agent active intervals
	Agents  int   `json:"agents"`
}

type Session struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"` // "codex" | "claude"
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
	Source  string `json:"source"` // "codex" | "claude"
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
	// LastAnswer is the final message of the last completed root turn, verbatim (clipped):
	// the only recorded description of what a session ended on. Never generated.
	LastAnswer string `json:"last_answer,omitempty"`
	// Question is the time (ms) of a question the agent asked the user that nothing has answered
	// yet — a literal harness record (AskUserQuestion, ExitPlanMode or request_user_input without
	// its answer),
	// never read from prose; absent when none is pending. The list marks the session and shows
	// how long it has been waiting.
	Question int64 `json:"question,omitempty"`
	// Filled when the session has been parsed at least once.
	Totals *Totals `json:"totals,omitempty"`
}
