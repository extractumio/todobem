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
	Key     string            `json:"key,omitempty"`  // aggregation key inside the rule (a shape, a bucket, a lane kind)
	Note    string            `json:"note,omitempty"` // plain-English detail for the evidence row
}

// Result is what one detector says about one session. Measurable is false when the session
// cannot carry the signal at all (CLI version, no usage records); NoData counts the
// occurrences that could not be measured inside an otherwise measurable session.
type Result struct {
	Findings   []Finding        `json:"findings"`
	Measurable bool             `json:"measurable"`
	Reason     string           `json:"reason,omitempty"`
	NoData     int              `json:"no_data,omitempty"`
	Stats      map[string]int64 `json:"stats,omitempty"` // rule-specific numbers, summed over sessions by the report
}

// Detector is one rule of the catalogue (docs/INSIGHTS-SPEC.md §5).
type Detector struct {
	ID    string
	Group string
	Title string
	// Info cards are measurements shown for the honesty of the picture (long breaks, counts,
	// shares): never ranked, never in a group's total or the top findings.
	Info bool
	Run  func(*Facts) Result
}

// Groups, in the order the page shows them when exposures tie; "not_measured" is always last.
const (
	GroupYou      = "you_and_the_agent"
	GroupAgents   = "sub_agents"
	GroupFailures = "failures_and_retries"
	GroupLongRuns = "long_tool_runs"
	GroupContext  = "context_size"
	GroupModels   = "models_and_effort"
	GroupUnseen   = "not_measured"
)

// Catalogue lists every detector in rule order.
var Catalogue = []Detector{
	{ID: "D1", Group: GroupYou, Title: "The agent waited for your answer", Run: detectWaitingOnAnswer},
	{ID: "D2", Group: GroupYou, Title: "Time to your reply", Run: detectReplyLatency},
	{ID: "D2b", Group: GroupYou, Title: "Long breaks (4 hours or more)", Info: true, Run: detectLongBreaks},
	{ID: "D3", Group: GroupYou, Title: "Turns you stopped", Run: detectStoppedTurns},
	{ID: "T1", Group: GroupYou, Title: "Cache after a break", Run: detectCacheAfterBreak},
	{ID: "D4", Group: GroupAgents, Title: "Sub-agents ran one after another", Run: detectSerialDelegation},
	{ID: "T6", Group: GroupAgents, Title: "Cost to start a sub-agent", Run: detectSpawnCost},
	{ID: "D7", Group: GroupFailures, Title: "Commands that fail and get retried", Run: detectRetryLoops},
	{ID: "D15", Group: GroupFailures, Title: "Edits that failed", Info: true, Run: detectFailedEdits},
	{ID: "D14", Group: GroupFailures, Title: "Invalid tool calls", Info: true, Run: detectInvalidToolCalls},
	{ID: "D9", Group: GroupLongRuns, Title: "Long tool runs", Run: detectLongRuns},
	{ID: "D12", Group: GroupLongRuns, Title: "Processes left running", Run: detectBackground},
	{ID: "D11", Group: GroupContext, Title: "Context compaction pauses", Run: detectCompactions},
	{ID: "T2", Group: GroupContext, Title: "Context size", Run: detectContextSize},
	{ID: "M1", Group: GroupModels, Title: "Model time by model, effort and stage", Run: detectModelTime},
	{ID: "T3", Group: GroupModels, Title: "Tokens by model, effort and agent type", Run: detectTokensByModel},
	{ID: "T7", Group: GroupModels, Title: "Reasoning tokens", Info: true, Run: detectReasoningShare},
	{ID: "D13", Group: GroupUnseen, Title: "Commands no rule matched", Info: true, Run: detectUnknown},
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
