package codex

import (
	"encoding/json"

	"github.com/extractumio/todobem/internal/model"
)

// Token accounting for one lane. A Codex thread reports usage in event_msg/token_count records:
// total_token_usage is the thread's cumulative counter and last_token_usage the usage of the
// model call just made. The record is written more often than once per call (a repeat carries
// the same total), the counter restarts when a thread is resumed, a forked child starts from its
// parent's total, and all-zero / info:null records surround compactions. A call is therefore
// COUNTED when the total is positive and differs from the last counted total; the lane's usage
// is the sum of the counted calls (docs/ARCHITECTURE.md §2.1).
//
// Every counted call is also attributed to the open turn (Turn.Tokens, Responses, First,
// ContextPeak) and, when a compaction is waiting for its first call, to that compaction op
// (Operation.Tokens: what the model re-read after it). A record outside a turn counts for the
// lane only (none seen in the corpus: every token_count sits inside a turn).

// tokenUsage is one usage figure of a token_count record (cumulative or per call).
type tokenUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (u tokenUsage) usage() *model.TokenUsage {
	return &model.TokenUsage{Input: u.Input, Cached: u.Cached, CacheWrite: u.CacheWrite, Output: u.Output, Reasoning: u.Reasoning, Total: u.Total}
}

// noteTokenCount handles one event_msg/token_count line (small; always decoded).
func (p *laneParser) noteTokenCount(line []byte) {
	var rl rawLine
	var tc struct {
		Info *struct {
			Total tokenUsage `json:"total_token_usage"`
			Last  tokenUsage `json:"last_token_usage"`
		} `json:"info"`
	}
	if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &tc) != nil || tc.Info == nil {
		return
	}
	total := tc.Info.Total.Total
	if total <= 0 || total == p.tokenTotal {
		return
	}
	use := tc.Info.Last
	if use.Total <= 0 && total > p.tokenTotal {
		// no per-call usage recorded: the counter's own increase is the only figure left
		use = tokenUsage{Total: total - p.tokenTotal}
	}
	p.tokenTotal = total
	p.countCall(use)
}

// countCall adds one counted model call to the lane, to the open turn and to a compaction
// waiting for the call that followed it.
func (p *laneParser) countCall(use tokenUsage) {
	if p.lane.Tokens == nil {
		p.lane.Tokens = &model.TokenUsage{}
	}
	p.lane.Tokens.Add(use.usage())
	if use.Input > 0 {
		p.lastCall = use
	}
	if c := p.compaction; c != nil {
		c.Tokens = use.usage()
		p.compaction = nil
	}
	t := p.turn
	if t == nil || t.Status != "open" {
		return
	}
	if t.Tokens == nil {
		t.Tokens = &model.TokenUsage{}
	}
	t.Tokens.Add(use.usage())
	t.Responses++
	if t.First == nil {
		t.First = use.usage()
	}
	if use.Input > t.ContextPeak {
		t.ContextPeak = use.Input
	}
}

// noteCompaction records the context a compaction started from (the input of the last counted
// call) and arms the op to receive the first counted call after it.
func (p *laneParser) noteCompaction(op *model.Operation) {
	op.Context = p.lastCall.Input
	p.compaction = op
}

// noteTokenUsageRecord reads root_turn_id from a token_usage_record (CLI >= 0.153): the
// harness's own link from a sub-agent's model call to the root turn that was running. A root
// lane needs nothing from it (turn_id == root_turn_id there), so only sub-agent files decode it.
func (p *laneParser) noteTokenUsageRecord(line []byte) {
	if p.meta.ParentID == "" || p.turn == nil || p.turn.RootTurn != "" {
		return
	}
	var rl rawLine
	var rec struct {
		TurnID     string `json:"turn_id"`
		RootTurnID string `json:"root_turn_id"`
	}
	if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &rec) != nil {
		return
	}
	p.Decoded += int64(len(line))
	if rec.RootTurnID != "" && rec.TurnID == p.turn.ID {
		p.turn.RootTurn = rec.RootTurnID
	}
}
