package claude

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/source"
)

// laneParser turns one Claude Code session file into a lane. A Claude Code log records no turn
// events: a turn opens at a prompt line (the user's, or a harness-injected one) and closes at
// the last streamed block of the assistant message that stopped with end_turn — deferred until
// the next line proves the message is over, so the stop hooks the harness runs afterwards
// (system/stop_hook_summary) still land inside the turn. Tool calls are assistant tool_use
// blocks; their results come back as user tool_result blocks and end the op.
type laneParser struct {
	meta fileMeta
	lane *model.Lane
	off  int64

	turn    *model.Turn
	lastTS  int64
	pending map[string]*pendingCall     // tool_use id → call awaiting its result
	procs   map[string]*model.Operation // background task id → its Bash op (polls extend it)
	// ending is the id of the assistant message that stopped with end_turn: its later block
	// lines still belong to the turn; endTS is the last of them; endFinal its last text.
	ending   string
	endTS    int64
	endFinal string
	finalTS  int64      // when the final text was written (the marker's place; the turn ends later, after the hooks)
	finalSrc *model.Src // its line
	cmdTurn  bool       // the open turn was opened by a slash command line (dropped if nothing follows)
	// closedMsg / closedTurn: the end_turn message and turn closed at the end of the file (no
	// later line was there to prove the message over); a later block of that same message
	// re-opens the turn instead of starting a new one.
	closedMsg  string
	closedTurn *model.Turn
	// usage is recorded on every streamed block line of a message, identically; a message is
	// counted once (countedMsg), and a later line of it that differs adds only the delta.
	countedMsg string
	countedUse model.TokenUsage
	lastCall   model.TokenUsage
	compaction *model.Operation // a compaction op waiting for the first counted call after it
	nextOp     int
	Bytes      int64
	Decoded    int64
}

type pendingCall struct {
	id, name string
	op       *model.Operation
	marker   int // index of the agent_started marker to link once the result names the agent; -1 = none
}

func newLaneParser(fm fileMeta) *laneParser {
	l := &model.Lane{ID: fm.ID, Parent: fm.ParentID, Role: fm.AgentType, Nickname: fm.AgentName, Model: fm.Model, Depth: fm.Depth, File: fm.Path, Started: fm.Started, ByPhase: map[model.Phase]int64{}}
	if fm.ParentID == "" {
		l.Path = "/root"
		l.Depth = 0
	} else {
		name := fm.AgentName
		if name == "" {
			name = fm.AgentType
		}
		if name == "" {
			name = strings.TrimPrefix(fm.ID, "agent-")
			if len(name) > 8 {
				name = name[:8]
			}
		}
		l.Path = "/root/" + name
	}
	return &laneParser{meta: fm, lane: l, pending: map[string]*pendingCall{}, procs: map[string]*model.Operation{}}
}

// ---- line shapes (only the fields we use)

// envelope is a message line: user, assistant, system or attachment. The payload fields the
// timeline needs are small; the big ones (tool results, attachments) are never decoded.
type envelope struct {
	Type             string          `json:"type"`
	UUID             string          `json:"uuid"`
	Timestamp        string          `json:"timestamp"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	IsAPIError       bool            `json:"isApiErrorMessage"`
	Error            json.RawMessage `json:"error"`
	CWD              string          `json:"cwd"`
	GitBranch        string          `json:"gitBranch"`
	Version          string          `json:"version"`
	Effort           string          `json:"effort"`
	PermissionMode   string          `json:"permissionMode"`
	PromptSource     string          `json:"promptSource"` // typed | queued | system (CLI ≥ 2.1.26x)
	Origin           struct {
		Kind string `json:"kind"` // human | task-notification | auto-continuation | peer
	} `json:"origin"`
	Message       json.RawMessage `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	// system lines
	Subtype   string `json:"subtype"`
	Content   string `json:"content"`
	HookInfos []struct {
		Command    string `json:"command"`
		DurationMs int64  `json:"durationMs"`
	} `json:"hookInfos"`
	HookErrors      []json.RawMessage `json:"hookErrors"`
	CompactMetadata *struct {
		Trigger    string `json:"trigger"`
		PreTokens  int64  `json:"preTokens"`
		PostTokens int64  `json:"postTokens"`
		DurationMs int64  `json:"durationMs"`
	} `json:"compactMetadata"`
}

type message struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"` // a string, or content blocks
	Usage      *struct {
		Input       int64 `json:"input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		Output      int64 `json:"output_tokens"`
	} `json:"usage"`
}

type block struct {
	Type      string          `json:"type"` // text | thinking | tool_use | tool_result | image | document
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result: a string, or text blocks
}

// toolResult is the harness's own record next to a tool_result (toolUseResult), when it is an
// object (AskUserQuestion's is a string).
type toolResult struct {
	AgentID          string `json:"agentId"`
	IsAsync          bool   `json:"isAsync"`
	Interrupted      bool   `json:"interrupted"`
	BackgroundTaskID string `json:"backgroundTaskId"`
	TimedOutAfterMs  int64  `json:"timedOutAfterMs"`
	Task             *struct {
		Status string `json:"status"`
	} `json:"task"`
}

// blocksOf decodes message content: a plain string is one text block.
func blocksOf(raw json.RawMessage) []block {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []block{{Type: "text", Text: s}}
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// contentText joins the text blocks of a user message (an image block is named, a
// system-reminder block the harness attached is not the user's words and is left out) and
// returns the tool_result blocks apart.
func contentText(raw json.RawMessage) (text string, results []block) {
	var sb strings.Builder
	for _, b := range blocksOf(raw) {
		switch b.Type {
		case "tool_result":
			results = append(results, b)
		case "text":
			if strings.HasPrefix(strings.TrimSpace(b.Text), "<system-reminder>") {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Text)
		case "image", "document":
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString("[" + b.Type + "]")
		}
	}
	return sb.String(), results
}

// resultText is the text of a tool_result block (a string or text blocks).
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	var sb strings.Builder
	for _, b := range blocksOf(raw) {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// Prompt kinds of a user line that is not a tool result.
const (
	promptHuman  = "human"  // the user's own words: opens a turn
	promptSystem = "system" // a harness-injected message the model answers: opens a system turn
	promptNoise  = "noise"  // echoes and attachments that start nothing
)

// Tags the harness wraps its own user-role messages in.
var (
	systemTags = map[string]bool{"task-notification": true, "teammate-message": true, "system-reminder": true}
	noiseTags  = map[string]bool{"local-command-stdout": true, "local-command-caveat": true, "bash-input": true, "bash-stdout": true}
	reTag      = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9_-]*)[\s>]`)
	reExitCode = regexp.MustCompile(`^Exit code (-?\d+)`)
)

// tagOf returns the harness tag a message starts with ("" when none).
func tagOf(text string) string {
	if m := reTag.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
		return m[1]
	}
	return ""
}

// promptKind classifies a user line that is not a tool result. Newer CLIs say it themselves
// (promptSource, origin.kind); older ones are read from the tags and isMeta.
func promptKind(env envelope, text string) string {
	if env.IsCompactSummary {
		return promptNoise
	}
	if tag := tagOf(text); tag != "" {
		switch {
		case tag == "command-name":
			return promptHuman // a slash command the user typed
		case systemTags[tag]:
			return promptSystem
		case noiseTags[tag]:
			return promptNoise
		}
	}
	if env.PromptSource == "system" || env.Origin.Kind != "" && env.Origin.Kind != "human" {
		return promptSystem
	}
	if env.IsMeta {
		return promptNoise // an image attachment, a caveat
	}
	return promptHuman
}

// ---- source.LaneParser

func (p *laneParser) Lane() *model.Lane            { return p.lane }
func (p *laneParser) Path() string                 { return p.meta.Path }
func (p *laneParser) Offset() int64                { return p.off }
func (p *laneParser) LastTS() int64                { return p.lastTS }
func (p *laneParser) Stats() (read, decoded int64) { return p.Bytes, p.Decoded }

// Consume reads all complete new lines from the file. Nothing is skipped by prefix: a Claude
// Code line carries its type after the payload, so every message line is decoded (the big
// fields stay RawMessage and are never parsed further).
func (p *laneParser) Consume(now int64) error {
	tr, err := source.OpenTail(p.meta.Path, p.off, nil)
	if err != nil {
		return err
	}
	defer tr.Close()
	for {
		line, start, _, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		p.Bytes += tr.Offset() - start
		p.handle(line, start)
		p.off = tr.Offset()
	}
	// the file ends right after an end_turn message: its block lines are written together, so
	// the message is over — close the turn now rather than leave it open until the next line
	if p.ending != "" && p.turn != nil && len(p.pending) == 0 {
		p.closedMsg, p.closedTurn = p.ending, p.turn
		p.finishEnding()
	}
	p.lane.Ended = max(p.lane.Started, p.lastTS)
	return nil
}

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
	if !bytes.HasPrefix(line, []byte(`{"parentUuid"`)) {
		return // a metadata line (mode, ai-title, last-prompt, …): no timestamp, nothing for the timeline
	}
	if isAttachment(line) {
		// harness context attached to the conversation (hook output, reminders): evidence only
		p.finishEnding()
		p.touch(suffixTS(line))
		return
	}
	p.Decoded += int64(len(line))
	var env envelope
	if json.Unmarshal(line, &env) != nil {
		return
	}
	ts := source.ParseTS(env.Timestamp)
	if p.lane.Started == 0 && ts > 0 {
		p.lane.Started = ts
	}
	switch env.Type {
	case "assistant":
		p.onAssistant(env, ts, start, line)
	case "user":
		p.onUser(env, ts, start, line)
	case "system":
		p.onSystem(env, ts, start, line)
	default:
		p.finishEnding()
	}
	p.touch(ts)
}

// isAttachment: the attachment object comes right after the two fixed leading fields.
func isAttachment(line []byte) bool {
	head := line
	if len(head) > 120 {
		head = head[:120]
	}
	return bytes.Contains(head, []byte(`"attachment":{`))
}

// suffixTS reads the timestamp of an assistant / attachment line, which sits after the payload
// among short trailing fields, without decoding the line.
func suffixTS(line []byte) int64 {
	tail := line
	if len(tail) > 1024 {
		tail = tail[len(tail)-1024:]
	}
	i := bytes.LastIndex(tail, []byte(`"timestamp":"`))
	if i < 0 {
		return 0
	}
	rest := tail[i+13:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return 0
	}
	return source.ParseTS(string(rest[:j]))
}

// ---- turns

func (p *laneParser) turnID() string {
	if p.turn != nil {
		return p.turn.ID
	}
	return ""
}

// openTurn starts a turn at a prompt line. A turn still open is orphaned: the file recorded no
// end for it.
func (p *laneParser) openTurn(id string, ts int64, trigger string, src *model.Src) {
	p.finishEnding()
	if p.turn != nil && p.cmdTurn && p.turnIsEmpty(p.turn) {
		p.dropTurn(p.turn) // a slash command nothing answered: not a turn
	}
	if p.turn != nil {
		p.closeTurn(ts, "orphaned", "")
	}
	p.turn = &model.Turn{ID: id, Start: ts, End: ts, Status: "open", Trigger: trigger}
	p.lane.Turns = append(p.lane.Turns, p.turn)
	p.cmdTurn = false
	p.addMarker(ts, "turn_start", id, "", "", src)
}

func (p *laneParser) closeTurn(ts int64, status, final string) {
	if p.turn == nil {
		return
	}
	if status == "orphaned" {
		ts = p.turn.End
	}
	p.turn.End = max(ts, p.turn.Start)
	p.turn.Status = status
	if final != "" {
		p.turn.Final = final
	}
	if status == "completed" {
		p.addMarker(p.turn.End, "turn_end", p.turn.ID, "", "", nil)
	}
	// close dangling tool calls of this turn
	for _, pc := range p.pending {
		if pc.op != nil && pc.op.Open {
			pc.op.End, pc.op.Open, pc.op.Status = p.turn.End, false, "aborted"
		}
	}
	p.pending = map[string]*pendingCall{}
	p.turn = nil
	p.ending, p.endFinal, p.cmdTurn = "", "", false
}

// finishEnding closes the turn whose end_turn message is over: any line that is not a later
// block of that message proves it. A tool call still pending keeps the turn open (its result
// is still to come).
func (p *laneParser) finishEnding() {
	if p.ending == "" || p.turn == nil {
		return
	}
	if len(p.pending) > 0 {
		return
	}
	final := p.endFinal
	if final != "" {
		p.addMarker(p.finalTS, "final_answer", p.turn.ID, final, "", p.finalSrc)
	}
	p.closeTurn(p.endTS, "completed", final)
}

// reopenTurn undoes the end-of-file close of a turn whose message turned out to continue: the
// turn is open again, its end markers are gone, and the message is the ending one once more.
func (p *laneParser) reopenTurn(t *model.Turn, msgID string) {
	t.Status = "open"
	for i := len(p.lane.Markers) - 1; i >= 0; i-- {
		mk := p.lane.Markers[i]
		if mk.Turn == t.ID && (mk.Kind == "turn_end" || mk.Kind == "final_answer") {
			p.lane.Markers = append(p.lane.Markers[:i], p.lane.Markers[i+1:]...)
		}
	}
	p.turn = t
	p.ending, p.endTS, p.endFinal, p.finalTS = msgID, t.End, t.Final, t.End
}

// dropTurn removes a turn that ran nothing (a local slash command): its markers stay, unowned.
func (p *laneParser) dropTurn(t *model.Turn) {
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
		mk.Turn = ""
	}
	if p.turn == t {
		p.turn = nil
	}
	p.cmdTurn = false
}

// turnIsEmpty: nothing but the start marker and the prompt itself.
func (p *laneParser) turnIsEmpty(t *model.Turn) bool {
	for _, o := range p.lane.Ops {
		if o.Turn == t.ID {
			return false
		}
	}
	for _, mk := range p.lane.Markers {
		if mk.Turn == t.ID && mk.Kind != "turn_start" && mk.Kind != "user_message" && mk.Kind != "system_message" {
			return false
		}
	}
	return true
}

// ---- lines

func (p *laneParser) onUser(env envelope, ts int64, start int64, line []byte) {
	var msg message
	json.Unmarshal(env.Message, &msg)
	text, results := contentText(msg.Content)
	if len(results) > 0 {
		for _, b := range results {
			p.onResult(b, env.ToolUseResult, ts, start, line)
		}
		return
	}
	src := p.src(start, line)
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "[Request interrupted by user") {
		if p.turn != nil && p.turn.Trigger == "system" && p.turnIsEmpty(p.turn) {
			// a harness message the model never got to answer (delivered as the session was
			// closed): no turn ran, the message stays a marker
			p.dropTurn(p.turn)
			return
		}
		p.addMarker(ts, "interrupted", p.turnID(), trimmed, "", src)
		if p.turn != nil {
			p.closeTurn(ts, "aborted", "")
		}
		return
	}
	kind := promptKind(env, text)
	tag := tagOf(text)
	switch {
	case kind == promptNoise:
		p.finishEnding()
		if tag == "local-command-stdout" && p.turn != nil && p.cmdTurn && p.turnIsEmpty(p.turn) {
			// a local slash command (/cost, /model): the harness answered it, no model ran
			p.dropTurn(p.turn)
		}
	case tag == "command-name":
		p.openTurn(env.UUID, ts, "user", src)
		p.addMarker(ts, "user_message", p.turn.ID, commandText(trimmed), "", src)
		p.cmdTurn = true
	case kind == promptSystem:
		p.openTurn(env.UUID, ts, "system", src)
		ref := source.OrDefault(tag, env.Origin.Kind)
		p.addMarker(ts, "system_message", p.turn.ID, source.Clip(ref+"\n"+text, 1500), ref, src)
	default:
		p.openTurn(env.UUID, ts, "user", src)
		if env.PermissionMode == "plan" {
			p.turn.Mode = "plan"
		}
		p.addMarker(ts, "user_message", p.turn.ID, text, "", src)
	}
}

var (
	reCommandName = regexp.MustCompile(`<command-name>\s*([^<]*?)\s*</command-name>`)
	reCommandArgs = regexp.MustCompile(`<command-args>\s*([^<]*?)\s*</command-args>`)
)

// commandText renders a slash-command line as the user typed it.
func commandText(text string) string {
	name, args := "", ""
	if m := reCommandName.FindStringSubmatch(text); m != nil {
		name = m[1]
	}
	if m := reCommandArgs.FindStringSubmatch(text); m != nil {
		args = m[1]
	}
	if name == "" {
		return source.Clip(text, 300)
	}
	return strings.TrimSpace(name + " " + args)
}

func (p *laneParser) onAssistant(env envelope, ts int64, start int64, line []byte) {
	var msg message
	if json.Unmarshal(env.Message, &msg) != nil {
		return
	}
	if p.ending != "" && msg.ID != p.ending {
		p.finishEnding()
	}
	src := p.src(start, line)
	if p.turn == nil && msg.ID != "" && msg.ID == p.closedMsg && p.closedTurn != nil {
		// a later block of the message the file ended on last time: the turn goes on
		p.reopenTurn(p.closedTurn, msg.ID)
	}
	p.closedMsg, p.closedTurn = "", nil
	if p.turn == nil {
		// the model is running with no prompt recorded before it (a stop hook that kept the
		// turn going, a resumed thread): a harness-triggered turn from the last evidence
		st := p.lastTS
		if st <= 0 || st > ts {
			st = ts
		}
		p.openTurn("sys-"+env.UUID, st, "system", src)
	}
	if msg.Model != "" {
		p.lane.Model = msg.Model
		p.turn.Model = msg.Model
	}
	if env.Effort != "" {
		p.turn.Effort = env.Effort
	}
	p.countUsage(msg)
	if env.IsAPIError {
		p.addMarker(ts, "llm_error", p.turn.ID, source.Clip(errorText(env, msg), 300), "", src)
	}
	final := ""
	for _, b := range blocksOf(msg.Content) {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(b.Text)
			if text == "" {
				continue
			}
			op := p.newOp("", p.turn.ID, classify.LLM, "message", ts, ts, src)
			op.Title = "message: " + source.Clip(source.FirstLine(text), 90)
			op.Detail = source.Clip(text, 2000)
			op.Status = "completed"
			final = text
		case "tool_use":
			p.onCall(b, ts, src)
			p.ending = "" // a message that calls a tool is not the turn's last
		}
	}
	if msg.StopReason == "end_turn" && p.ending == "" && len(p.pending) == 0 {
		p.ending, p.endTS, p.endFinal, p.finalTS, p.finalSrc = msg.ID, ts, final, ts, src
	} else if msg.ID == p.ending {
		p.endTS = ts
		if final != "" {
			p.endFinal, p.finalTS, p.finalSrc = final, ts, src
		}
	}
}

func errorText(env envelope, msg message) string {
	if len(env.Error) > 0 && env.Error[0] == '"' {
		var s string
		if json.Unmarshal(env.Error, &s) == nil && s != "" {
			return s
		}
	}
	for _, b := range blocksOf(msg.Content) {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return strings.TrimSpace(b.Text)
		}
	}
	return "API error"
}

func (p *laneParser) onSystem(env envelope, ts int64, start int64, line []byte) {
	src := p.src(start, line)
	switch env.Subtype {
	case "stop_hook_summary":
		// the harness's stop hooks ran after the model's last message: harness time inside the
		// turn, and the turn ends when they are done. One op per hook (the summary records each
		// hook's command and duration, and they end together at the summary line): the op is
		// wait_worker/hook — the activity partition does not change — but it carries the
		// command's own classification in Rule and its retry identity, so a hook that re-runs
		// the agent's test command joins that command's group, and a hook whose command is a
		// test is a recorded verification the Insights read. The summary lists errors without
		// saying which hook raised them: with any error every hook of the summary is failed.
		if p.turn != nil {
			floor := p.lastTS
			if p.ending != "" {
				floor = p.endTS
			}
			for _, h := range env.HookInfos {
				p.hookOp(h.Command, max(floor, ts-h.DurationMs), ts, len(env.HookErrors), src)
			}
		}
		if p.ending != "" {
			p.endTS = ts
		}
		p.finishEnding()
	case "compact_boundary":
		p.finishEnding()
		s := ts
		var pre int64
		title := "context compaction"
		if md := env.CompactMetadata; md != nil {
			pre = md.PreTokens
			if md.DurationMs > 0 {
				// never before the last evidence: that time belongs to what was recorded there
				s = min(ts, max(p.lastTS, ts-md.DurationMs))
			}
			if md.Trigger != "" {
				title += " (" + md.Trigger + ")"
			}
		}
		op := p.newOp("", p.turnID(), classify.Compaction, "compaction", s, ts, src)
		op.Title = title
		op.Status = "completed"
		op.Context = pre
		p.compaction = op
		p.addMarker(s, "compaction", op.Turn, "", op.ID, nil)
	case "model_refusal_fallback", "model_refusal_no_fallback":
		p.finishEnding()
		p.addMarker(ts, "llm_error", p.turnID(), source.Clip(source.OrDefault(env.Content, env.Subtype), 300), "", src)
	case "local_command":
		// the harness ran a slash command itself (/clear, /cost): no model turn
		p.finishEnding()
		if p.turn != nil && p.cmdTurn && p.turnIsEmpty(p.turn) {
			p.dropTurn(p.turn)
		}
	default:
		// turn_duration, away_summary, informational, agents_killed: evidence only
		p.finishEnding()
	}
}

// ---- tokens

// countUsage counts one model call per assistant message: the lane's usage, the open turn's,
// and the re-read after an armed compaction (the same accounting as codex/tokens.go).
func (p *laneParser) countUsage(msg message) {
	if msg.Usage == nil {
		return
	}
	u := model.TokenUsage{Input: msg.Usage.Input + msg.Usage.CacheRead + msg.Usage.CacheCreate, Cached: msg.Usage.CacheRead, CacheWrite: msg.Usage.CacheCreate, Output: msg.Usage.Output}
	u.Total = u.Input + u.Output
	if u.Total <= 0 {
		return
	}
	if msg.ID != "" && msg.ID == p.countedMsg {
		d := model.TokenUsage{Input: u.Input - p.countedUse.Input, Cached: u.Cached - p.countedUse.Cached, CacheWrite: u.CacheWrite - p.countedUse.CacheWrite, Output: u.Output - p.countedUse.Output, Total: u.Total - p.countedUse.Total}
		if d == (model.TokenUsage{}) {
			return
		}
		p.countedUse = u
		p.lane.Tokens.Add(&d)
		if p.turn != nil && p.turn.Tokens != nil {
			p.turn.Tokens.Add(&d)
		}
		return
	}
	p.countedMsg, p.countedUse = msg.ID, u
	if p.lane.Tokens == nil {
		p.lane.Tokens = &model.TokenUsage{}
	}
	p.lane.Tokens.Add(&u)
	p.lastCall = u
	if c := p.compaction; c != nil {
		re := u
		c.Tokens = &re
		p.compaction = nil
	}
	t := p.turn
	if t == nil || t.Status != "open" {
		return
	}
	if t.Tokens == nil {
		t.Tokens = &model.TokenUsage{}
	}
	t.Tokens.Add(&u)
	t.Responses++
	if t.First == nil {
		first := u
		t.First = &first
	}
	if u.Input > t.ContextPeak {
		t.ContextPeak = u.Input
	}
}

// ---- ops and markers

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
	if e < s {
		e = s
	}
	op := &model.Operation{ID: id, Lane: p.lane.ID, Turn: turn, Phase: phase, Kind: kind, Start: s, End: e, Src: src}
	p.lane.Ops = append(p.lane.Ops, op)
	return op
}

// hookOp records one stop hook as a wait_worker/hook op inside the current turn, classified by
// its command text like a shell call (Rule "hook · <phase>/<kind>", the retry identity with the
// session's cwd) so it groups with the same command run by the agent.
func (p *laneParser) hookOp(cmd string, s, e int64, errors int, src *model.Src) *model.Operation {
	res := classify.Command(cmd, "")
	op := p.newOp("", p.turn.ID, classify.WaitWorker, "hook", s, e, src)
	op.Title = "stop hook · " + res.Title
	op.Detail = source.Clip(cmd, 2000)
	op.Rule = classify.HookRule(res.Phase, res.Kind)
	if res.Identity != "" {
		op.Identity = p.meta.CWD + "\n" + res.Identity
	}
	op.Status = "completed"
	if errors > 0 {
		op.Status = "failed"
		op.Detail += fmt.Sprintf("\n(%d hook error(s) recorded on this summary; the harness does not say which hook)", errors)
	}
	return op
}

func (p *laneParser) addMarker(ts int64, kind, turn, text, ref string, src *model.Src) {
	p.lane.Markers = append(p.lane.Markers, model.Marker{T: ts, Kind: kind, Lane: p.lane.ID, Turn: turn, Text: text, Ref: ref, Src: src})
}
