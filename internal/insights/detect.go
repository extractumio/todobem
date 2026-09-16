package insights

import (
	"sort"

	"github.com/extractumio/todobem/internal/model"
)

// A Finding is one row of evidence a detector produced: where in which session the pattern
// cost what. Findings are aggregated per rule (and per Key) by the report.
type Finding struct {
	Rule    string            `json:"rule"`
	Session string            `json:"session"`
	Lane    string            `json:"lane"` // lane path
	LaneID  string            `json:"lane_id"`
	A       int64             `json:"a"` // interval start
	B       int64             `json:"b"` // interval end
	Op      string            `json:"op,omitempty"`
	TimeMs  int64             `json:"time_ms"`
	Tokens  *model.TokenUsage `json:"tokens,omitempty"`
	Key     string            `json:"key,omitempty"`   // aggregation key inside the rule (a shape, a bucket, a lane kind)
	Note    string            `json:"note,omitempty"`  // plain-English detail for the evidence row
	N       int               `json:"n,omitempty"`     // occurrences this finding stands for (default 1): a per-session roll-up
	Value   int64             `json:"value,omitempty"` // a rule-specific number the report can rank or summarise (D11: the context before the compaction); never parsed from Note
}

// Result is what one detector says about one session. Measurable is false when the session
// cannot carry the signal at all (CLI version, no usage records); NoData counts the
// occurrences that could not be measured inside an otherwise measurable session.
// NotApplicable is a session the rule's precondition is absent from (no change op for a
// verification rule, no review run for a review rule): it did not happen and it could not have,
// so the session is neither in the card's denominator nor in "no data".
type Result struct {
	Findings      []Finding        `json:"findings"`
	Measurable    bool             `json:"measurable"`
	NotApplicable bool             `json:"not_applicable,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	NoData        int              `json:"no_data,omitempty"`
	Stats         map[string]int64 `json:"stats,omitempty"` // rule-specific numbers, summed over sessions by the report
}

// Card classes. An exposure card is ranked by the time or tokens the pattern consumed and adds
// to its group's total. A check card says that a recorded sequence happened in N of M sessions
// (a change with no test after it): it has no honest time exposure, is ranked by sessions
// affected among checks, and never adds to a total. An info card is a measurement shown for
// the honesty of the picture (long breaks, counts, shares): never ranked, never in a total.
const (
	ClassExposure = "exposure"
	ClassCheck    = "check"
	ClassInfo     = "info"
)

// Detector is one rule of the catalogue (docs/ARCHITECTURE.md §10.2).
type Detector struct {
	ID    string
	Group string
	Title string
	Class string // "" is ClassExposure
	Run   func(*Facts) Result
}

// IsInfo and IsCheck read the class; the zero value is an exposure card.
func (d Detector) IsInfo() bool  { return d.Class == ClassInfo }
func (d Detector) IsCheck() bool { return d.Class == ClassCheck }

// ClassRank orders cards inside a group: exposure cards first, then checks, measurements last.
func ClassRank(class string) int {
	switch class {
	case ClassCheck:
		return 1
	case ClassInfo:
		return 2
	}
	return 0
}

// Groups, in the order the page shows them when exposures tie; "not_measured" is always last.
const (
	GroupYou      = "you_and_the_agent"
	GroupAgents   = "sub_agents"
	GroupFailures = "failures_and_retries"
	GroupTools    = "tool_calls"
	GroupLongRuns = "long_tool_runs"
	GroupContext  = "context_size"
	GroupModels   = "models_and_effort"
	GroupUnseen   = "not_measured"
)

// Catalogue lists every detector in rule order.
var Catalogue = []Detector{
	{ID: "D1", Group: GroupYou, Title: "The agent waited for your answer", Run: detectWaitingOnAnswer},
	{ID: "D2", Group: GroupYou, Title: "Time to your reply", Run: detectReplyLatency},
	{ID: "D2b", Group: GroupYou, Title: "Long breaks (4 hours or more)", Class: ClassInfo, Run: detectLongBreaks},
	{ID: "D3", Group: GroupYou, Title: "Turns you stopped", Run: detectStoppedTurns},
	{ID: "T1", Group: GroupYou, Title: "Cache after a break", Run: detectCacheAfterBreak},
	{ID: "D4", Group: GroupAgents, Title: "Sub-agents ran one after another", Run: detectSerialDelegation},
	{ID: "T6", Group: GroupAgents, Title: "Cost to start a sub-agent", Run: detectSpawnCost},
	{ID: "D7", Group: GroupFailures, Title: "Commands that fail and get retried", Run: detectRetryLoops},
	{ID: "D15", Group: GroupFailures, Title: "Tool calls that failed", Class: ClassInfo, Run: detectFailedEdits},
	{ID: "D16", Group: GroupTools, Title: "What the tool calls did", Class: ClassInfo, Run: detectToolCalls},
	{ID: "D14", Group: GroupFailures, Title: "Invalid tool calls", Class: ClassInfo, Run: detectInvalidToolCalls},
	{ID: "D9", Group: GroupLongRuns, Title: "Long tool runs", Run: detectLongRuns},
	{ID: "D12", Group: GroupLongRuns, Title: "Processes left running", Run: detectBackground},
	{ID: "D11", Group: GroupContext, Title: "Context compaction pauses", Run: detectCompactions},
	{ID: "T2", Group: GroupContext, Title: "Context size", Run: detectContextSize},
	{ID: "M1", Group: GroupModels, Title: "Model time by model, effort and stage", Run: detectModelTime},
	{ID: "T3", Group: GroupModels, Title: "Tokens by model, effort and agent type", Run: detectTokensByModel},
	{ID: "T7", Group: GroupModels, Title: "Reasoning tokens", Class: ClassInfo, Run: detectReasoningShare},
	{ID: "D13", Group: GroupUnseen, Title: "Commands no rule matched", Class: ClassInfo, Run: detectUnknown},
}

// RunAll runs every detector of the catalogue on one session.
func RunAll(f *Facts) map[string]Result {
	out := map[string]Result{}
	for _, d := range Catalogue {
		r := d.Run(f)
		for i := range r.Findings {
			r.Findings[i].Rule = d.ID
			r.Findings[i].Session = f.ID
		}
		sort.SliceStable(r.Findings, func(a, b int) bool { return r.Findings[a].TimeMs > r.Findings[b].TimeMs })
		out[d.ID] = r
	}
	return out
}

// Summary is the per-rule roll-up of one Result (used by cmd/dump and the report).
type Summary struct {
	Count  int
	TimeMs int64
	Tokens model.TokenUsage
	ByKey  map[string]int
}

func Summarize(r Result) Summary {
	s := Summary{ByKey: map[string]int{}}
	for _, x := range r.Findings {
		s.Count++
		s.TimeMs += x.TimeMs
		s.Tokens.Add(x.Tokens)
		if x.Key != "" {
			s.ByKey[x.Key]++
		}
	}
	return s
}

func (f *Facts) lanePath(i int) string {
	if i >= 0 && i < len(f.Agents) {
		return f.Agents[i].Path
	}
	return ""
}

func (f *Facts) laneID(i int) string {
	if i >= 0 && i < len(f.Agents) {
		return f.Agents[i].ID
	}
	return ""
}
