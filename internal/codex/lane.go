package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// laneParser holds the resumable state for one rollout file.
type laneParser struct {
	meta FileMeta
	lane *model.Lane
	off  int64 // bytes consumed (end of last complete line)

	turn      *model.Turn // open turn, if any
	lastTS    int64
	pending   map[string]*pendingCall // call_id -> call awaiting output
	procs     map[string]*model.Operation
	execCall  string // current `exec` custom tool call id (for parallel counting)
	execCmds  int    // CommandExecution items seen inside execCall
	execInput string
	execStart int64
	// provisional ops were synthesized from an `exec` envelope whose commands had not reported
	// yet (yield_time_ms elapsed); the real CommandExecution item replaces them when it arrives.
	provisional map[string]*model.Operation
	sawUserItem bool
	question    bool // a request_user_input is pending
	nextOp      int
	lastOrdinal int64
	Bytes       int64 // bytes read
	Decoded     int64 // bytes JSON-decoded
}

type pendingCall struct {
	id, name, args string
	ts             int64
	op             *model.Operation
}

func newLaneParser(fm FileMeta) *laneParser {
	l := &model.Lane{
		ID: fm.ThreadID, Path: fm.AgentPath, Parent: fm.ParentID, Role: fm.Role, Nickname: fm.Nickname,
		Model: fm.Model, Depth: fm.Depth, File: fm.Path, Started: fm.Started, ByPhase: map[model.Phase]int64{},
	}
	if l.Path == "" {
		if fm.ParentID == "" {
			l.Path = "/root"
		} else {
			// old rollouts carry no agent_path: name the lane by nickname, role or id
			name := fm.Nickname
			if name == "" {
				name = fm.Role
			}
			if name == "" && len(fm.ThreadID) >= 8 {
				name = fm.ThreadID[:8]
			}
			l.Path = "/root/" + name
		}
	}
	return &laneParser{meta: fm, lane: l, pending: map[string]*pendingCall{}, procs: map[string]*model.Operation{}, provisional: map[string]*model.Operation{}}
}

// ---- payload shapes (only the fields we use)

type itemCompleted struct {
	TurnID      string          `json:"turn_id"`
	Item        json.RawMessage `json:"item"`
	StartedAtMs int64           `json:"started_at_ms"`
	CompletedMs int64           `json:"completed_at_ms"`
}

type itemHead struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type cmdItem struct {
	ID        string   `json:"id"`
	Command   []string `json:"command"`
	CWD       string   `json:"cwd"`
	ParsedCmd []struct {
		Type string `json:"type"`
		Cmd  string `json:"cmd"`
	} `json:"parsed_cmd"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	Duration struct {
		Secs  int64 `json:"secs"`
		Nanos int64 `json:"nanos"`
	} `json:"duration"`
	Source string `json:"source"`
}

type fileChangeItem struct {
	ID      string `json:"id"`
	Changes map[string]struct {
		Type string `json:"type"`
	} `json:"changes"`
	Status string `json:"status"`
}

type textItem struct {
	ID      string `json:"id"`
	Phase   string `json:"phase"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type subAgentItem struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	AgentThreadID string `json:"agent_thread_id"`
	AgentPath     string `json:"agent_path"`
}

type extensionItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	DurationMs int64  `json:"durationMs"`
}

type mcpItem struct {
	ID     string `json:"id"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
}

type funcCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	CallID    string `json:"call_id"`
	Status    string `json:"status"`
	Meta      struct {
		TurnID string `json:"turn_id"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type funcOutput struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

type userMsg struct {
	Role    string `json:"role"`
	Phase   string `json:"phase"`
	Channel string `json:"channel"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Meta struct {
		TurnID string   `json:"turn_id"`
		Kinds  []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type agentMsg struct {
	Author    string `json:"author"`
	Recipient string `json:"recipient"`
	Content   []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type taskEvent struct {
	TurnID      string `json:"turn_id"`
	LastMessage string `json:"last_agent_message"`
	Reason      string `json:"reason"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
}

// injectedPrefixes mark messages that the harness (not the human) put into the user role.
var injectedPrefixes = []string{"<codex_internal_context", "<subagent_notification", "<environment_context", "# AGENTS.md", "<INSTRUCTIONS>", "<permissions", "<skills_instructions", "<collaboration_mode", "<turn_aborted", "<user_action", "<app_context", "<system_notification"}

func injected(text string) string {
	t := strings.TrimSpace(text)
	for _, pfx := range injectedPrefixes {
		if strings.HasPrefix(t, pfx) {
			name := strings.TrimLeft(pfx, "<# ")
			if i := strings.IndexAny(name, " >"); i > 0 {
				name = name[:i]
			}
			return name
		}
	}
	return ""
}

// noteUserInput records a user-role message: real input or a harness injection. fallback
// marks markers built from raw `message` lines, which precede the authoritative UserMessage
// item (when the file has them) by a few milliseconds.
func (p *laneParser) noteUserInput(ts int64, turn, text string, src *model.Src, fallback bool) {
	if turn == "" {
		turn = p.turnID()
	}
	if tag := injected(text); tag != "" {
		if p.turn != nil && p.turn.Trigger == "" {
			p.turn.Trigger = "system"
		}
		p.addMarker(ts, "system_message", turn, clip(tag+"\n"+text, 1500), tag, src)
		return
	}
	if p.turn != nil && p.turn.Trigger == "" {
		p.turn.Trigger = "user"
	}
	p.question = false
	if !fallback {
		// the item is authoritative: drop a fallback marker for the same input
		for n := len(p.lane.Markers); n > 0; n-- {
			last := p.lane.Markers[n-1]
			if ts-last.T > 3000 {
				break
			}
			if last.Kind == "user_message" && last.Fallback {
				p.lane.Markers = append(p.lane.Markers[:n-1], p.lane.Markers[n:]...)
				break
			}
		}
	}
	p.addMarker(ts, "user_message", turn, text, "", src)
	p.lane.Markers[len(p.lane.Markers)-1].Fallback = fallback
}

// The same final text can appear in a response message, a timed AgentMessage
// item, and task_complete. Keep one marker per recorded text and turn, preferring
// the authoritative item's timestamp and source when it becomes available.
func (p *laneParser) noteFinalAnswer(ts int64, turn, text string, src *model.Src, fallback bool) {
	if text == "" {
		return
	}
	if turn == "" {
		turn = p.turnID()
	}
	if p.turn != nil && p.turn.ID == turn {
		p.turn.Final = text
	}
	if turn != "" {
		for i := len(p.lane.Markers) - 1; i >= 0; i-- {
			mk := &p.lane.Markers[i]
			if mk.Kind == "final_answer" && mk.Turn == turn && mk.Text == text {
				if mk.Fallback && !fallback {
					mk.T, mk.Text, mk.Src, mk.Fallback = ts, text, src, false
				}
				return
			}
		}
	}
	p.addMarker(ts, "final_answer", turn, text, "", src)
	p.lane.Markers[len(p.lane.Markers)-1].Fallback = fallback
}

var (
	reExited   = regexp.MustCompile(`Process exited with code (-?\d+)`)
	reRunning  = regexp.MustCompile(`Process running with session ID (\d+)`)
	reExecCmd  = regexp.MustCompile(`"?cmd"?\s*:\s*"((?:[^"\\]|\\.)*)"`)
	reCmdArg   = regexp.MustCompile(`"cmd"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	reSession  = regexp.MustCompile(`"session_id"\s*:\s*(\d+)`)
	reWorkdir  = regexp.MustCompile(`"workdir"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	reQuestion = regexp.MustCompile(`"title"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// consume reads all complete new lines from the file. Returns bytes consumed.
// If a line's ordinal goes backwards the file was rewritten: the caller restarts from 0.
func (p *laneParser) consume(now int64) (int64, error) {
	tr, err := openTail(p.meta.Path, p.off)
	if err != nil {
		return 0, err
	}
	defer tr.Close()
	var n int64
	for {
		line, start, skipped, err := tr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		p.Bytes += tr.off - start
		if skipped {
			p.touch(prefixTS(line))
		} else {
			if o := prefixField(line, "ordinal", 80); o != "" {
				if v, e := strconv.ParseInt(o, 10, 64); e == nil {
					if v < p.lastOrdinal {
						return n, errRewritten
					}
					p.lastOrdinal = v
				}
			}
			p.handle(line, start)
		}
		p.off = tr.off
		n += tr.off - start
	}
	p.lane.Ended = max(p.lane.Started, p.lastTS)
	return n, nil
}

var errRewritten = errors.New("rollout rewritten")

// touch records evidence that the process was alive at ts.
func (p *laneParser) touch(ts int64) {
	if ts <= 0 {
		return
	}
	p.lastTS = ts
	if p.turn != nil && p.turn.Status == "open" {
		p.turn.End = ts
	}
}

func (p *laneParser) src(start int64, line []byte) *model.Src {
	return &model.Src{File: p.meta.Path, Off: start, Len: len(line)}
}

func (p *laneParser) handle(line []byte, start int64) {
	if len(line) < 20 || line[0] != '{' {
		return
	}
	t := lineType(line)
	ts := prefixTS(line)
	if t == "session_meta" || t == "event_msg" && payloadType(line) == "task_started" {
		// A new turn/resume is evidence for the new activity, not for an orphaned
		// predecessor. Close that predecessor before advancing its last evidence.
		defer p.touch(ts)
	} else {
		p.touch(ts)
	}
	if skipTypes[t] {
		return
	}
	switch t {
	case "session_meta":
		if start > 0 { // a second meta line: resumed thread
			p.addMarker(ts, "resumed", "", "session resumed", "", p.src(start, line))
			if p.turn != nil && p.turn.Status == "open" {
				p.closeTurn(ts, "orphaned", "")
			}
		}
		return
	case "event_msg":
		p.handleEvent(line, start, ts)
	case "response_item":
		p.handleResponseItem(line, start, ts)
	case "turn_context":
		// small line: model id and reasoning effort for the turn
		var rl rawLine
		var tc struct {
			TurnID string `json:"turn_id"`
			Model  string `json:"model"`
			Effort string `json:"effort"`
			Collab struct {
				Settings struct {
					Model  string `json:"model"`
					Effort string `json:"reasoning_effort"`
				} `json:"settings"`
			} `json:"collaboration_mode"`
		}
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &tc) != nil {
			return
		}
		model, effort := tc.Model, tc.Effort
		if model == "" {
			model = tc.Collab.Settings.Model
		}
		if effort == "" {
			effort = tc.Collab.Settings.Effort
		}
		p.Decoded += int64(len(line))
		for i := len(p.lane.Turns) - 1; i >= 0; i-- {
			if p.lane.Turns[i].ID == tc.TurnID {
				p.lane.Turns[i].Model, p.lane.Turns[i].Effort = model, effort
				break
			}
		}
		if model != "" {
			p.lane.Model = model // the model actually used (may differ from base_instructions)
		}
	}
}

func (p *laneParser) handleEvent(line []byte, start int64, ts int64) {
	pt := payloadType(line)
	switch pt {
	case "thread_settings_applied":
		return
	case "token_count":
		// small line; keep the latest cumulative usage for this thread
		var rl rawLine
		var tc struct {
			Info *struct {
				Total struct {
					Input     int64 `json:"input_tokens"`
					Cached    int64 `json:"cached_input_tokens"`
					Output    int64 `json:"output_tokens"`
					Reasoning int64 `json:"reasoning_output_tokens"`
					Total     int64 `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		}
		if json.Unmarshal(line, &rl) == nil && json.Unmarshal(rl.Payload, &tc) == nil && tc.Info != nil && tc.Info.Total.Total > 0 {
			u := tc.Info.Total
			p.lane.Tokens = &model.TokenUsage{Input: u.Input, Cached: u.Cached, Output: u.Output, Reasoning: u.Reasoning, Total: u.Total}
		}
		return
	case "task_started":
		p.Decoded += int64(len(line))
		var rl rawLine
		var te taskEvent
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &te) != nil {
			return
		}
		if p.turn != nil && p.turn.Status == "open" {
			if p.meta.ParentID != "" && p.turnIsEmpty(p.turn) && ts >= p.turn.Start && ts-p.turn.Start < 5000 {
				// a task_started replayed from the parent's history (forked sub-agent): no tool
				// activity and superseded within seconds → replace it rather than leaving an orphan
				p.dropTurn(p.turn, te.TurnID)
			} else {
				p.closeTurn(ts, "orphaned", "")
			}
		}
		p.turn = &model.Turn{ID: te.TurnID, Start: ts, End: ts, Status: "open"}
		p.lane.Turns = append(p.lane.Turns, p.turn)
		p.addMarker(ts, "turn_start", te.TurnID, "", "", p.src(start, line))
	case "task_complete", "turn_aborted":
		p.Decoded += int64(len(line))
		var rl rawLine
		var te taskEvent
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &te) != nil {
			return
		}
		status := "completed"
		if pt == "turn_aborted" {
			status = "aborted"
			p.addMarker(ts, "interrupted", te.TurnID, te.Reason, "", p.src(start, line))
		}
		if p.turn == nil || p.turn.ID != te.TurnID {
			// a close event for a turn this file never opened (replayed parent history): ignore
			return
		}
		p.noteFinalAnswer(ts, te.TurnID, te.LastMessage, p.src(start, line), true)
		p.closeTurn(ts, status, te.LastMessage)
		p.execCall = ""
	case "item_completed":
		p.Decoded += int64(len(line))
		p.handleItem(line, start, ts)
	case "thread_goal_updated":
		p.addMarker(ts, "goal", p.turnID(), "goal updated", "", p.src(start, line))
	}
}

func (p *laneParser) turnID() string {
	if p.turn != nil {
		return p.turn.ID
	}
	return ""
}

func (p *laneParser) closeTurn(ts int64, status, final string) {
	if p.turn == nil {
		return
	}
	if p.meta.ParentID != "" && status == "completed" && final == "" && ts >= p.turn.Start && ts-p.turn.Start < 1000 && p.turnIsEmpty(p.turn) {
		// replayed parent history in a forked sub-agent: start+complete at the same instant, no items
		p.dropTurn(p.turn, "")
		return
	}
	if status == "orphaned" {
		ts = p.turn.End
	}
	p.turn.End = ts
	p.turn.Status = status
	if final != "" {
		p.turn.Final = final
	}
	if status == "completed" {
		p.addMarker(ts, "turn_end", p.turn.ID, "", "", nil)
	}
	// close dangling ops of this turn
	for _, pc := range p.pending {
		if pc.op != nil && pc.op.Open {
			pc.op.End, pc.op.Open, pc.op.Status = ts, false, "aborted"
		}
	}
	p.pending = map[string]*pendingCall{}
	p.turn = nil
	p.execCall = ""
}

func (p *laneParser) handleItem(line []byte, start int64, ts int64) {
	var rl rawLine
	if json.Unmarshal(line, &rl) != nil {
		return
	}
	var ic itemCompleted
	if json.Unmarshal(rl.Payload, &ic) != nil {
		return
	}
	var head itemHead
	if json.Unmarshal(ic.Item, &head) != nil {
		return
	}
	s, e := ic.StartedAtMs, ic.CompletedMs
	if s == 0 {
		s = ts
	}
	if e < s {
		e = s
	}
	src := p.src(start, line)
	switch head.Type {
	case "UserMessage":
		var ti textItem
		json.Unmarshal(ic.Item, &ti)
		p.sawUserItem = true
		p.noteUserInput(ts, ic.TurnID, joinText(ti.Content), src, false)
	case "CommandExecution":
		var ci cmdItem
		if json.Unmarshal(ic.Item, &ci) != nil {
			return
		}
		cmd := ""
		if n := len(ci.Command); n > 0 {
			cmd = ci.Command[n-1]
		}
		codexKind := ""
		if len(ci.ParsedCmd) > 0 {
			all := true
			for _, pc := range ci.ParsedCmd {
				if pc.Type != "read" && pc.Type != "search" && pc.Type != "list_files" {
					all = false
				}
			}
			if all {
				codexKind = ci.ParsedCmd[0].Type
			}
		}
		if e == s && ci.Duration.Secs+ci.Duration.Nanos > 0 {
			e = s + ci.Duration.Secs*1000 + ci.Duration.Nanos/1e6
		}
		if prov, ok := p.provisional[strings.TrimSpace(cmd)]; ok {
			p.dropOp(prov)
			for k, v := range p.provisional {
				if v == prov {
					delete(p.provisional, k)
				}
			}
		}
		op := p.commandOp(head.ID, ic.TurnID, cmd, codexKind, s, e, src)
		if op.Identity != "" {
			op.Identity = strings.TrimPrefix(ci.CWD, "file://") + "\n" + op.Identity
		}
		op.Status = ci.Status
		if ci.ExitCode != nil {
			ec := *ci.ExitCode
			op.Exit = &ec
			if ec != 0 {
				op.Status = "failed"
			}
		}
		if p.execCall != "" {
			op.SetCall(p.execCall)
			p.execCmds++
		}
	case "FileChange":
		var fc fileChangeItem
		if json.Unmarshal(ic.Item, &fc) != nil {
			return
		}
		var names []string
		var detail []string
		for path, ch := range fc.Changes {
			names = append(names, filepath.Base(path))
			detail = append(detail, ch.Type+" "+path)
		}
		title := strings.Join(names, ", ")
		if len(names) > 3 {
			title = fmt.Sprintf("%s +%d files", strings.Join(names[:3], ", "), len(names)-3)
		}
		op := p.newOp(head.ID, ic.TurnID, classify.Code, "edit", s, e, src)
		op.Title = "edit " + title
		op.Detail = clip(strings.Join(detail, "\n"), 2000)
		op.Status = orDefault(fc.Status, "completed")
	case "Reasoning":
		op := p.newOp(head.ID, ic.TurnID, classify.LLM, "reasoning", s, e, src)
		op.Title = "reasoning"
		op.Status = "completed"
	case "AgentMessage":
		var ti textItem
		json.Unmarshal(ic.Item, &ti)
		text := joinText(ti.Content)
		op := p.newOp(head.ID, ic.TurnID, classify.LLM, "message", s, e, src)
		op.Title = "message: " + clip(firstLine(text), 90)
		op.Detail = clip(text, 2000)
		op.Status = "completed"
		if ti.Phase == "final_answer" {
			p.noteFinalAnswer(e, ic.TurnID, text, src, false)
		}
	case "SubAgentActivity":
		var sa subAgentItem
		json.Unmarshal(ic.Item, &sa)
		p.addMarker(ts, "agent_"+sa.Kind, ic.TurnID, sa.AgentPath, sa.AgentThreadID, src)
	case "ContextCompaction":
		op := p.newOp(head.ID, ic.TurnID, classify.Compaction, "compaction", s, e, src)
		op.Title = "context compaction"
		op.Status = "completed"
		p.addMarker(s, "compaction", ic.TurnID, "", op.ID, nil)
	case "McpToolCall":
		var mi mcpItem
		json.Unmarshal(ic.Item, &mi)
		op := p.newOp(head.ID, ic.TurnID, classify.Code, "mcp", s, e, src)
		op.Title = "mcp " + mi.Server + "." + mi.Tool
		op.Status = orDefault(mi.Status, "completed")
	case "WebSearch":
		op := p.newOp(head.ID, ic.TurnID, classify.Code, "web_search", s, e, src)
		op.Title = "web search"
		op.Status = "completed"
	case "ImageView":
		op := p.newOp(head.ID, ic.TurnID, classify.Code, "image", s, e, src)
		op.Title = "view image"
		op.Status = "completed"
	case "EnteredReviewMode", "ExitedReviewMode":
		p.addMarker(ts, strings.ToLower(head.Type), ic.TurnID, "", "", src)
	case "CollabAgentToolCall", "Extension":
		// covered by the function_call / output pair (wait_agent, sleep)
	}
}

func (p *laneParser) handleResponseItem(line []byte, start int64, ts int64) {
	pt := payloadType(line)
	switch pt {
	case "function_call", "custom_tool_call":
		p.Decoded += int64(len(line))
		var rl rawLine
		var fc funcCall
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &fc) != nil {
			return
		}
		p.onCall(fc, ts, start, line, pt == "custom_tool_call")
	case "function_call_output", "custom_tool_call_output":
		// call_id sits in the first ~200 bytes; the (often huge) output is decoded only
		// when the pending call needs its text (old-format process status lines).
		callID := prefixField(line, "call_id", 400)
		if callID == "" {
			return
		}
		fo := funcOutput{CallID: callID}
		needText := callID == p.execCall && p.execCmds == 0 // exec that produced no commands: inspect why
		if pc, ok := p.pending[callID]; ok && pc.name != "exec" {
			needText = true // small outputs; decode to catch harness errors ("failed to parse function arguments")
		}
		if needText {
			p.Decoded += int64(len(line))
			var rl rawLine
			if json.Unmarshal(line, &rl) == nil {
				json.Unmarshal(rl.Payload, &fo)
			}
			fo.CallID = callID
		}
		p.onOutput(fo, ts, start, line)
	case "message":
		if p.sawUserItem {
			// Preserve the existing fast path for duplicated user/context messages,
			// while still allowing legacy assistant finals to supply a marker.
			if role := prefixField(line, "role", 400); role != "" && role != "assistant" {
				return
			}
		}
		p.Decoded += int64(len(line))
		var rl rawLine
		var um userMsg
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &um) != nil {
			return
		}
		if um.Role == "assistant" {
			if um.Phase == "final" || um.Phase == "final_answer" || um.Channel == "final" {
				p.noteFinalAnswer(ts, um.Meta.TurnID, joinText(um.Content), p.src(start, line), true)
			}
			return
		}
		if um.Role != "user" || p.sawUserItem {
			return
		}
		isUser := len(um.Meta.Kinds) == 0
		for _, k := range um.Meta.Kinds {
			if k == "user.text" || k == "user.image" {
				isUser = true
			}
		}
		if !isUser {
			return
		}
		p.noteUserInput(ts, um.Meta.TurnID, joinText(um.Content), p.src(start, line), true)
	case "agent_message":
		p.Decoded += int64(len(line))
		var rl rawLine
		var am agentMsg
		if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &am) != nil {
			return
		}
		var sb strings.Builder
		for _, c := range am.Content {
			sb.WriteString(c.Text)
		}
		kind := "message_received"
		if am.Author == p.lane.Path {
			kind = "message_sent"
		} else if strings.Contains(sb.String(), "FINAL_ANSWER") {
			kind = "result_returned"
		}
		p.addMarker(ts, kind, p.turnID(), clip(sb.String(), 3000), am.Author, p.src(start, line))
	case "web_search_call", "tool_search_call":
		op := p.newOp(p.generatedOpID("ws", start), p.turnID(), classify.Code, "web_search", ts, ts, p.src(start, line))
		op.Title = strings.TrimSuffix(pt, "_call")
		op.Status = "completed"
	}
}

var trivialCalls = map[string]bool{"list_agents": true, "get_goal": true, "session_show_defaults": true, "session_set_defaults": true, "discover_projs": true, "list_schemes": true, "list_sims": true, "read_thread_terminal": true, "send_input": true, "update_goal": true, "close_agent": true, "interrupt_agent": true, "spawn_agent": true, "send_message": true, "followup_task": true, "js": true}

func (p *laneParser) onCall(fc funcCall, ts int64, start int64, line []byte, custom bool) {
	turn := fc.Meta.TurnID
	if turn == "" {
		turn = p.turnID()
	}
	src := p.src(start, line)
	pc := &pendingCall{id: fc.CallID, name: fc.Name, args: fc.Arguments, ts: ts}
	switch fc.Name {
	case "exec":
		p.execCall, p.execCmds, p.execInput, p.execStart = fc.CallID, 0, fc.Input, ts
	case "apply_patch":
		// FileChange item carries the edit; nothing here.
	case "exec_command":
		cmd := ""
		if m := reCmdArg.FindStringSubmatch(fc.Arguments); m != nil {
			cmd = unescapeJSON(m[1])
		}
		op := p.commandOp(fc.CallID, turn, cmd, "", ts, ts, src)
		if op.Identity != "" {
			if m := reWorkdir.FindStringSubmatch(fc.Arguments); m != nil {
				op.Identity = unescapeJSON(m[1]) + "\n" + op.Identity
			}
		}
		op.Open, op.Status = true, "running"
		pc.op = op
	case "write_stdin", "wait":
		// poll of a running process: extend that process's op
		if m := reSession.FindStringSubmatch(fc.Arguments); m != nil {
			if op := p.procs[m[1]]; op != nil {
				pc.op = op
				op.Open, op.Status = true, "running"
			}
		}
		if pc.op == nil && fc.Name == "wait" {
			op := p.newOp(fc.CallID, turn, classify.WaitWorker, "process", ts, ts, src)
			op.Title = "wait for process"
			op.Open, op.Status = true, "running"
			pc.op = op
		}
	case "wait_agent":
		op := p.newOp(fc.CallID, turn, classify.WaitWorker, "agent", ts, ts, src)
		op.Title = "wait for sub-agents"
		op.Detail = clip(fc.Arguments, 300)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "sleep":
		op := p.newOp(fc.CallID, turn, classify.WaitWorker, "sleep", ts, ts, src)
		op.Title = "sleep " + clip(fc.Arguments, 60)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "update_plan":
		p.addMarker(ts, "plan", turn, clip(planSummary(fc.Arguments), 1500), "", src)
	case "request_user_input_async", "request_user_input":
		var qs []string
		for _, m := range reQuestion.FindAllStringSubmatch(fc.Arguments, -1) {
			qs = append(qs, unescapeJSON(m[1]))
		}
		p.question = true
		p.addMarker(ts, "question", turn, clip(strings.Join(qs, "\n"), 2000), "", src)
	case "view_image":
		op := p.newOp(fc.CallID, turn, classify.Code, "image", ts, ts, src)
		op.Title = "view image"
		op.Open = true
		pc.op = op
	case "build_sim":
		op := p.newOp(fc.CallID, turn, classify.Build, "build_sim", ts, ts, src)
		op.Title = "build (simulator)"
		op.Open, op.Status = true, "running"
		pc.op = op
	case "test_sim":
		op := p.newOp(fc.CallID, turn, classify.Test, "test_sim", ts, ts, src)
		op.Title = "test (simulator)"
		op.Open, op.Status = true, "running"
		pc.op = op
	case "run":
		op := p.newOp(fc.CallID, turn, classify.Unknown, "run", ts, ts, src)
		op.Title = "run " + clip(fc.Arguments, 60)
		op.Open, op.Status = true, "running"
		pc.op = op
	default:
		if trivialCalls[fc.Name] {
			break
		}
		phase, kind := classify.Unknown, "tool:"+fc.Name
		if strings.HasPrefix(fc.Name, "_") && strings.Contains(fc.Name, "pull_request") || strings.Contains(fc.Name, "merge_pull") {
			phase, kind = classify.Release, "pr"
		}
		op := p.newOp(fc.CallID, turn, phase, kind, ts, ts, src)
		op.Title = fc.Name
		op.Detail = clip(fc.Arguments, 1000)
		op.Open, op.Status = true, "running"
		pc.op = op
	}
	if fc.CallID != "" {
		p.pending[fc.CallID] = pc
	}
}

func (p *laneParser) onOutput(fo funcOutput, ts int64, start int64, line []byte) {
	if fo.CallID != "" && fo.CallID == p.execCall {
		// end of an `exec` envelope; if no CommandExecution items were emitted (old versions),
		// synthesize one op from the JS input.
		if p.execCmds == 0 && strings.Contains(p.execInput, "exec_command") {
			var cmds []string
			for _, m := range reExecCmd.FindAllStringSubmatch(p.execInput, -1) {
				cmds = append(cmds, unescapeJSON(m[1]))
			}
			joined := strings.Join(cmds, "\n")
			if joined == "" {
				joined = clip(p.execInput, 500)
			}
			op := p.commandOp(fo.CallID, p.turnID(), joined, "", p.execStart, ts, p.src(start, line))
			op.Status = "completed"
			op.Parallel = len(cmds)
			out := outputText(fo.Output)
			if strings.HasPrefix(out, "Script failed") || strings.HasPrefix(out, "Error") {
				// the script itself failed before running anything
				op.Status = "failed"
				if len(cmds) == 0 {
					op.Phase = classify.Unknown
				}
				op.Kind = "exec-script-error"
				op.Title = "exec script failed (no commands ran): " + clip(firstLine(out), 80)
				p.addMarker(ts, "llm_error", op.Turn, clip(out, 300), op.ID, p.src(start, line))
			} else {
				// commands may still be running (yield_time_ms elapsed): keep the op only until the
				// real CommandExecution item reports the same command
				op.Status = "running"
				for _, c := range cmds {
					p.provisional[strings.TrimSpace(c)] = op
				}
			}
		}
		p.execCall = ""
		delete(p.pending, fo.CallID)
		return
	}
	pc, ok := p.pending[fo.CallID]
	if !ok {
		return
	}
	delete(p.pending, fo.CallID)
	if pc.op == nil {
		return
	}
	op := pc.op
	// A previous live derivation may have advanced End beyond this buffered output.
	// The recorded output timestamp is authoritative once the envelope closes.
	op.End = max(op.Start, ts)
	op.Open = false
	out := outputText(fo.Output)
	switch pc.name {
	case "exec_command", "write_stdin", "wait":
		if m := reExited.FindStringSubmatch(out); m != nil {
			code, _ := strconv.Atoi(m[1])
			op.Exit = &code
			op.Status = "completed"
			if code != 0 {
				op.Status = "failed"
			}
		} else if m := reRunning.FindStringSubmatch(out); m != nil {
			p.procs[m[1]] = op
			op.Status = "running"
		} else {
			op.Status = "completed"
		}
	default:
		if op.Status == "running" {
			op.Status = "completed"
		}
	}
	if strings.HasPrefix(out, "failed to parse function arguments") || strings.HasPrefix(out, "invalid function arguments") {
		// the model produced arguments the harness could not parse: an LLM-side failure
		op.Status = "failed"
		op.Kind = "llm-invalid-args"
		p.addMarker(ts, "llm_error", op.Turn, clip(out, 300), op.ID, p.src(start, line))
	}
}

// commandOp creates a classified command operation.
func (p *laneParser) commandOp(id, turn, cmd, codexKind string, s, e int64, src *model.Src) *model.Operation {
	res := classify.Command(cmd, codexKind)
	op := p.newOp(id, turn, res.Phase, res.Kind, s, e, src)
	op.Title = res.Title
	op.Detail = clip(cmd, 2000)
	op.Identity = res.Identity
	op.Remote = res.Remote
	op.Queued = res.Queued
	op.Rule = res.Rule
	return op
}

// The separator cannot occur in the encoded lane ID, so both lane and kind have
// unambiguous namespaces even when recorded lane IDs contain URL delimiters.
func (p *laneParser) generatedOpID(kind string, serial int64) string {
	return fmt.Sprintf("%s.%s.%d", kind, base64.RawURLEncoding.EncodeToString([]byte(p.lane.ID)), serial)
}

func (p *laneParser) newOp(id, turn string, phase model.Phase, kind string, s, e int64, src *model.Src) *model.Operation {
	if id == "" {
		p.nextOp++
		id = p.generatedOpID("op", int64(p.nextOp))
	}
	if turn == "" {
		turn = p.turnID()
	}
	op := &model.Operation{ID: id, Lane: p.lane.ID, Turn: turn, Phase: phase, Kind: kind, Start: s, End: e, Src: src}
	p.lane.Ops = append(p.lane.Ops, op)
	return op
}

// turnIsEmpty permits only the start marker and injected context. Any other
// marker records activity, including tools such as plans and questions that do
// not create operations.
func (p *laneParser) turnIsEmpty(t *model.Turn) bool {
	if t.Final != "" {
		return false
	}
	for _, o := range p.lane.Ops {
		if o.Turn == t.ID {
			return false
		}
	}
	for _, mk := range p.lane.Markers {
		if mk.Turn == t.ID && mk.Kind != "turn_start" && mk.Kind != "system_message" {
			return false
		}
	}
	return true
}

// dropTurn removes a replayed turn; its markers (the spawn prompt) move to the real turn.
func (p *laneParser) dropTurn(t *model.Turn, newTurn string) {
	for i, x := range p.lane.Turns {
		if x == t {
			p.lane.Turns = append(p.lane.Turns[:i], p.lane.Turns[i+1:]...)
			break
		}
	}
	for i := len(p.lane.Markers) - 1; i >= 0; i-- {
		mk := &p.lane.Markers[i]
		if mk.Turn != t.ID {
			continue
		}
		if mk.Kind == "turn_start" {
			p.lane.Markers = append(p.lane.Markers[:i], p.lane.Markers[i+1:]...)
			continue
		}
		mk.Turn = newTurn
	}
	p.turn = nil
}

// dropOp removes an op from the lane (rare: provisional ops superseded by real items).
func (p *laneParser) dropOp(target *model.Operation) {
	for i, o := range p.lane.Ops {
		if o == target {
			p.lane.Ops = append(p.lane.Ops[:i], p.lane.Ops[i+1:]...)
			return
		}
	}
}

func (p *laneParser) addMarker(ts int64, kind, turn, text, ref string, src *model.Src) {
	p.lane.Markers = append(p.lane.Markers, model.Marker{T: ts, Kind: kind, Lane: p.lane.ID, Turn: turn, Text: text, Ref: ref, Src: src})
}

// ---- helpers

// prefixTS reads the timestamp from the line prefix without decoding JSON.
func prefixTS(line []byte) int64 {
	const key = `{"timestamp":"`
	if len(line) < len(key)+20 || string(line[:len(key)]) != key {
		return 0
	}
	rest := line[len(key):]
	for i := 0; i < len(rest) && i < 40; i++ {
		if rest[i] == '"' {
			return parseTS(string(rest[:i]))
		}
	}
	return 0
}

func joinText(c []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var sb strings.Builder
	for _, x := range c {
		if x.Text != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(x.Text)
		}
	}
	return sb.String()
}

func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		return sb.String()
	}
	return string(raw)
}

func unescapeJSON(s string) string {
	var out string
	if json.Unmarshal([]byte(`"`+s+`"`), &out) == nil {
		return out
	}
	return s
}

func planSummary(args string) string {
	var v struct {
		Plan []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
		Explanation string `json:"explanation"`
	}
	if json.Unmarshal([]byte(args), &v) != nil {
		return clip(args, 500)
	}
	var sb strings.Builder
	for _, s := range v.Plan {
		mark := "☐"
		switch s.Status {
		case "completed":
			mark = "☑"
		case "in_progress":
			mark = "▶"
		}
		sb.WriteString(mark + " " + s.Step + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// cut on a rune boundary
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
