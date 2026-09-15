package claude

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/source"
)

// Tool calls → operations. Every Claude Code tool call is a literal tool_use block with a
// name and a JSON input; the mapping below is by name only (docs/ARCHITECTURE.md §2.2):
//
//	Bash                       → the command classifier (the same table as Codex commands)
//	Edit/Write/NotebookEdit    → code/edit (+ the edited path's lifecycle pin)
//	Read / Grep / Glob (LS)    → code/read, code/search, code/list_files (query kinds)
//	WebSearch / WebFetch       → code/web_search
//	Agent (Task)               → wait_worker/agent: the parent waits for the sub-agent
//	AskUserQuestion            → wait_user/question: the user is answering
//	TaskOutput/BashOutput/Monitor → a poll of a background task: extends its Bash op, else
//	                             wait_worker/process
//	Skill                      → a skill marker + Turn.Skill (review detection)
//	ExitPlanMode               → an instant llm/plan op pinned to the planning stage
//	TodoWrite / TaskCreate     → plan markers
//	mcp__<server>__<tool>      → code/mcp
//	bookkeeping tools          → nothing (trivialTools)
//	anything else              → unknown/"tool:<name>" — honest unknown, listed by `todobem unknown`
var trivialTools = map[string]bool{"ToolSearch": true, "ListAgents": true, "TaskList": true, "TaskStop": true, "SendMessage": true, "KillShell": true, "KillBash": true, "TaskUpdate": true, "TaskGet": true, "ListMcpResourcesTool": true, "ReadMcpResourceTool": true, "EnterWorktree": true, "ExitWorktree": true, "EnterPlanMode": true}

// toolInput holds every input field the mapping reads, whatever the tool.
type toolInput struct {
	Command         string `json:"command"`
	Description     string `json:"description"`
	RunInBackground bool   `json:"run_in_background"`
	FilePath        string `json:"file_path"`
	NotebookPath    string `json:"notebook_path"`
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Query           string `json:"query"`
	URL             string `json:"url"`
	Prompt          string `json:"prompt"`
	Skill           string `json:"skill"`
	SubagentType    string `json:"subagent_type"`
	Name            string `json:"name"`
	TaskID          string `json:"task_id"`
	Subject         string `json:"subject"`
	Plan            string `json:"plan"`
	Offset          int64  `json:"offset"`
	Limit           int64  `json:"limit"`
	Questions       []struct {
		Question string `json:"question"`
	} `json:"questions"`
	Todos []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"todos"`
}

func (p *laneParser) onCall(b block, ts int64, src *model.Src) {
	turn := p.turnID()
	var in toolInput
	json.Unmarshal(b.Input, &in)
	pc := &pendingCall{id: b.ID, name: b.Name, marker: -1}
	switch b.Name {
	case "Bash":
		op := p.commandOp(b.ID, turn, in.Command, ts, src)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		path := source.OrDefault(in.FilePath, in.NotebookPath)
		op := p.newOp(b.ID, turn, classify.Code, "edit", ts, ts, src)
		op.Title = "edit " + filepath.Base(path)
		op.Detail = strings.ToLower(b.Name) + " " + path
		op.Open, op.Status = true, "running"
		if lc, ok := classify.PathLifecycle([]string{path}); ok {
			op.Lifecycle, op.LifecycleRule = lc, "edited path"
		}
		pc.op = op
	case "Read":
		op := p.newOp(b.ID, turn, classify.Code, "read", ts, ts, src)
		op.Title = "read " + filepath.Base(in.FilePath)
		op.Detail = "read " + in.FilePath
		if in.Offset > 0 || in.Limit > 0 {
			op.Detail += " (offset " + strconv.FormatInt(in.Offset, 10) + ", limit " + strconv.FormatInt(in.Limit, 10) + ")"
		}
		op.Open, op.Status = true, "running"
		pc.op = op
	case "Grep":
		op := p.newOp(b.ID, turn, classify.Code, "search", ts, ts, src)
		op.Title = "search " + source.Clip(in.Pattern, 60)
		op.Detail = strings.TrimSpace("grep " + in.Pattern + " " + in.Path)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "Glob", "LS":
		op := p.newOp(b.ID, turn, classify.Code, "list_files", ts, ts, src)
		op.Title = strings.TrimSpace("list " + source.Clip(source.OrDefault(in.Pattern, in.Path), 60))
		op.Detail = strings.TrimSpace(strings.ToLower(b.Name) + " " + in.Pattern + " " + in.Path)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "WebSearch", "WebFetch":
		op := p.newOp(b.ID, turn, classify.Code, "web_search", ts, ts, src)
		if b.Name == "WebFetch" {
			op.Title = "web fetch " + hostOf(in.URL)
			op.Detail = "fetch " + in.URL
		} else {
			op.Title = "web search: " + source.Clip(in.Query, 80)
			op.Detail = "search " + in.Query
		}
		op.Open, op.Status = true, "running"
		pc.op = op
	case "Agent", "Task":
		who := source.OrDefault(in.Description, source.OrDefault(in.Name, in.SubagentType))
		op := p.newOp(b.ID, turn, classify.WaitWorker, "agent", ts, ts, src)
		op.Title = "sub-agent · " + source.Clip(who, 80)
		op.Detail = source.Clip(in.Prompt, 2000)
		op.Open, op.Status = true, "running"
		pc.op = op
		p.addMarker(ts, "agent_started", turn, who, "", src)
		pc.marker = len(p.lane.Markers) - 1
	case "AskUserQuestion":
		var qs []string
		for _, q := range in.Questions {
			qs = append(qs, q.Question)
		}
		text := source.Clip(strings.Join(qs, "\n"), 2000)
		op := p.newOp(b.ID, turn, classify.WaitUser, "question", ts, ts, src)
		op.Title = "question to user"
		op.Detail = text
		op.Open, op.Status = true, "running"
		pc.op = op
		p.addMarker(ts, "question", turn, text, "", src)
	case "TaskOutput", "BashOutput", "Monitor":
		if op := p.procs[in.TaskID]; op != nil {
			// a poll of a background process: the process op grows to the poll's answer
			op.Open, op.Status = true, "running"
			pc.op = op
			break
		}
		op := p.newOp(b.ID, turn, classify.WaitWorker, "process", ts, ts, src)
		op.Title = "wait for task output"
		if b.Name == "Monitor" {
			op.Title = "monitor " + source.Clip(source.OrDefault(in.Description, in.Command), 60)
		}
		op.Detail = source.Clip(string(b.Input), 500)
		op.Open, op.Status = true, "running"
		pc.op = op
	case "Skill":
		p.noteSkill(ts, in.Skill, src)
	case "ExitPlanMode":
		// the plan is written: an instant op (model output) pinned to the planning stage, so
		// the reasoning before it is attributed to planning — like Codex's update_plan
		op := p.newOp(b.ID, turn, classify.LLM, "plan", ts, ts, src)
		op.Title = "plan"
		op.Status = "completed"
		op.Lifecycle, op.LifecycleRule = classify.LcPlan, "plan submitted (ExitPlanMode)"
		p.addMarker(ts, "plan", turn, source.Clip(source.OrDefault(in.Plan, "plan submitted"), 1500), "", src)
	case "TodoWrite":
		p.addMarker(ts, "plan", turn, todoSummary(in), "", src)
	case "TaskCreate":
		p.addMarker(ts, "plan", turn, "☐ "+source.Clip(in.Subject, 300), "", src)
	default:
		if trivialTools[b.Name] {
			break
		}
		if strings.HasPrefix(b.Name, "mcp__") {
			op := p.newOp(b.ID, turn, classify.Code, "mcp", ts, ts, src)
			op.Title = "mcp " + strings.ReplaceAll(strings.TrimPrefix(b.Name, "mcp__"), "__", ".")
			op.Detail = source.Clip(string(b.Input), 1000)
			op.Open, op.Status = true, "running"
			pc.op = op
			break
		}
		op := p.newOp(b.ID, turn, classify.Unknown, "tool:"+b.Name, ts, ts, src)
		op.Title = b.Name
		op.Detail = source.Clip(string(b.Input), 1000)
		op.Open, op.Status = true, "running"
		pc.op = op
	}
	if b.ID != "" {
		p.pending[b.ID] = pc
	}
}

// onResult ends the op of a tool call: the result line's timestamp is the end, is_error the
// verdict, a Bash "Exit code N" line the exit code.
func (p *laneParser) onResult(b block, raw json.RawMessage, ts int64, start int64, line []byte) {
	pc, ok := p.pending[b.ToolUseID]
	if !ok {
		return
	}
	delete(p.pending, b.ToolUseID)
	var res toolResult
	if len(raw) > 0 && raw[0] == '{' {
		json.Unmarshal(raw, &res)
	}
	src := p.src(start, line)
	if res.AgentID != "" {
		lane := "agent-" + res.AgentID
		if pc.marker >= 0 && pc.marker < len(p.lane.Markers) {
			p.lane.Markers[pc.marker].Ref = lane
		}
		if !res.IsAsync {
			p.addMarker(ts, "agent_completed", p.turnID(), "", lane, src)
		}
	}
	op := pc.op
	if op == nil {
		return
	}
	op.End = max(op.Start, ts)
	op.Open = false
	head := ""
	if b.IsError || pc.name == "Bash" {
		head = strings.TrimSpace(source.Clip(resultText(b.Content), 200))
	}
	switch {
	case res.Interrupted:
		op.Status = "aborted"
	case b.IsError:
		op.Status = "failed"
		if m := reExitCode.FindStringSubmatch(head); m != nil {
			if code, err := strconv.Atoi(m[1]); err == nil {
				op.Exit = &code
				if code == 0 {
					op.Status = "completed"
				}
			}
		}
		if strings.HasPrefix(head, "The user doesn't want") {
			op.Status = "aborted" // the tool call was denied
		}
	case res.Task != nil && res.Task.Status == "running":
		op.Status = "running"
	default:
		op.Status = "completed"
	}
	if res.BackgroundTaskID != "" && pc.name == "Bash" {
		// the command keeps running in the background: later polls extend this op
		p.procs[res.BackgroundTaskID] = op
		op.Status = "running"
	}
}

// commandOp creates a classified command operation (the Codex adapter's twin).
func (p *laneParser) commandOp(id, turn, cmd string, ts int64, src *model.Src) *model.Operation {
	res := classify.Command(cmd, "")
	op := p.newOp(id, turn, res.Phase, res.Kind, ts, ts, src)
	op.Title = res.Title
	op.Detail = source.Clip(cmd, 2000)
	if res.Identity != "" {
		op.Identity = p.meta.CWD + "\n" + res.Identity
	}
	op.Remote = res.Remote
	op.Queued = res.Queued
	op.Rule = res.Rule
	op.Lifecycle = res.Lifecycle
	return op
}

// noteSkill records that a skill was invoked in the current turn (the Skill tool call is the
// harness's own record of it): the turn's Skill, for review detection, and a point marker.
func (p *laneParser) noteSkill(ts int64, name string, src *model.Src) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if p.turn != nil && p.turn.Skill == "" {
		p.turn.Skill = name
	}
	p.addMarker(ts, "skill", p.turnID(), name, name, src)
}

func todoSummary(in toolInput) string {
	var sb strings.Builder
	for _, t := range in.Todos {
		mark := "☐"
		switch t.Status {
		case "completed":
			mark = "☑"
		case "in_progress":
			mark = "▶"
		}
		sb.WriteString(mark + " " + t.Content + "\n")
	}
	return source.Clip(strings.TrimSpace(sb.String()), 1500)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return source.Clip(raw, 60)
}
