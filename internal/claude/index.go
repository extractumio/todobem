// Package claude reads Claude Code session logs and turns them into the normalized model
// (internal/model) through the shared joiner (internal/source). Verified against CLI 2.1.226 →
// 2.1.270 (docs/DESIGN.md §1b).
//
// Layout of a Claude home (~/.claude): projects/<project>/<session-id>.jsonl is a root
// session; projects/<project>/<session-id>/subagents/agent-<id>.jsonl is a sub-agent spawned
// by it (agent-<id>.meta.json next to it names the agent); tool-results/ holds persisted tool
// outputs and is never read. Only that layout is walked, so a slip in the settings never turns
// into a walk of the whole disk.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/extractumio/todobem/internal/source"
)

// fileMeta is one session file: the shared Meta plus what the sub-agent meta file says.
type fileMeta struct {
	source.Meta
	Valid bool
	// sub-agent files: agent-<id>.meta.json
	AgentType   string
	AgentName   string
	Description string
	Depth       int
	ToolUseID   string
	// root files: whether the harness's own title (an ai-title line) was found; until it is,
	// Title is the first prompt and the head is scanned again when the file grows (the title
	// is written after the first exchange, so a file indexed at 3 KB would otherwise keep the
	// first prompt as its name forever).
	titled bool
}

// Bounds of the index's reads. The head scan stops at the first ai-title, so it normally
// ends within the first exchange; the caps keep a pathological file cheap. No line longer
// than maxMetaLine is decoded: the first message line, the first prompt and the ai-title are
// small, the multi-megabyte lines are tool results.
const (
	headScan    = 1 << 20
	maxMetaLine = 256 << 10
	tailScan    = 256 << 10
)

// readRoot indexes a root session file: identity from the file name, cwd / branch / version /
// start from the first message line, the title from the first ai-title line (else the first
// prompt), the model from the first assistant line, the last answer from the tail.
func readRoot(path string, st fs.FileInfo) fileMeta {
	fm := fileMeta{Meta: source.Meta{Source: source.Claude, Path: path, Size: st.Size(), ModTime: st.ModTime()}}
	fm.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return fm
	}
	defer f.Close()
	fm.Valid = scanHead(f, &fm)
	if fm.Valid {
		readTail(f, st.Size(), &fm)
	}
	return fm
}

// scanHead reads the first lines of a root file (bounded by headScan) and fills the meta.
// Valid when at least one message line was seen.
func scanHead(f *os.File, fm *fileMeta) bool {
	r := bufio.NewReaderSize(f, 256<<10)
	var read int64
	seen := false
	fallback, systemFallback, agentName := "", "", ""
	for read < headScan {
		line, err := r.ReadBytes('\n')
		read += int64(len(line))
		if len(line) == 0 {
			break
		}
		if err != nil && line[len(line)-1] != '\n' {
			break // a partial last line: not complete yet
		}
		line = bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(line, []byte(`{"type":"ai-title"`)) {
			if t := aiTitle(line); t != "" {
				fm.Title, fm.titled = t, true
			}
		} else if bytes.HasPrefix(line, []byte(`{"type":"agent-name"`)) {
			agentName = metaField(line, "agentName")
		} else if bytes.HasPrefix(line, []byte(`{"parentUuid"`)) && len(line) <= maxMetaLine {
			var env envelope
			if json.Unmarshal(line, &env) != nil {
				continue
			}
			if env.Type != "user" && env.Type != "assistant" && env.Type != "system" && env.Type != "attachment" {
				continue
			}
			if !seen {
				seen = true
				fm.CWD, fm.Branch, fm.CLI = env.CWD, env.GitBranch, env.Version
				fm.Started = source.ParseTS(env.Timestamp)
			}
			if fm.Model == "" && env.Type == "assistant" {
				var msg message
				if json.Unmarshal(env.Message, &msg) == nil {
					fm.Model = msg.Model
				}
			}
			if fallback == "" && env.Type == "user" && !env.IsMeta {
				var msg message
				if json.Unmarshal(env.Message, &msg) == nil {
					if text, results := contentText(msg.Content); len(results) == 0 {
						switch promptKind(env, text) {
						case promptHuman:
							fallback = promptTitle(text)
						case promptSystem:
							if systemFallback == "" {
								systemFallback = promptTitle(text)
							}
						}
					}
				}
			}
		}
		if fm.titled && seen && fm.Model != "" && fallback != "" {
			break
		}
		if err != nil {
			break
		}
	}
	if fm.Title == "" {
		// no title from the harness: the user's first words; a session that only ever received
		// harness messages (a teammate agent) is named by its agent name or its first message
		fm.Title = fallback
		if fm.Title == "" && agentName != "" {
			fm.Title = agentName
			if systemFallback != "" {
				fm.Title += " · " + systemFallback
			}
		}
		if fm.Title == "" {
			fm.Title = systemFallback
		}
	}
	return seen
}

// metaField reads one string field of a small metadata line.
func metaField(line []byte, key string) string {
	var v map[string]json.RawMessage
	if json.Unmarshal(line, &v) != nil {
		return ""
	}
	var s string
	json.Unmarshal(v[key], &s)
	return strings.TrimSpace(s)
}

// promptTitle renders a prompt's first line as a title: a slash command as the user typed it,
// a harness-wrapped message without its tag.
func promptTitle(text string) string {
	t := strings.TrimSpace(text)
	if tag := tagOf(t); tag != "" {
		if tag == "command-name" {
			return source.Clip(commandText(t), 80)
		}
		if i := strings.IndexByte(t, '>'); i >= 0 {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "</"+tag+">"))
	}
	return source.Clip(source.FirstLine(t), 80)
}

// readTail finds, in the file's tail, the last completed answer — the last assistant text block
// of a message that stopped with end_turn — and a question the agent asked the user that nothing
// has answered: the last AskUserQuestion (a question) or ExitPlanMode (a plan awaiting approval)
// tool_use with no tool_result for its id, no later model output and no later typed prompt
// (fm.Question = its time; 0 when none). Only candidate lines are decoded, none above
// maxMetaLine (a final answer is small; a huge tail line is a tool result).
func readTail(f *os.File, size int64, fm *fileMeta) {
	off := size - tailScan
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return
	}
	buf = buf[:n]
	if off > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return
		}
	}
	fm.Question = 0
	pending := "" // the tool_use id of the question still waiting for its answer
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		if !fm.titled && bytes.HasPrefix(line, []byte(`{"type":"ai-title"`)) {
			if t := aiTitle(line); t != "" {
				fm.Title, fm.titled = t, true
			}
			continue
		}
		if pending != "" && bytes.Contains(line, []byte(`"tool_use_id":"`+pending+`"`)) {
			pending, fm.Question = "", 0 // the answer (or the harness's own result) arrived
		}
		if len(line) > maxMetaLine || !bytes.HasPrefix(line, []byte(`{"parentUuid"`)) {
			continue
		}
		assistant := bytes.Contains(line, []byte(`"type":"assistant"`))
		if assistant {
			pending, fm.Question = "", 0 // the model spoke again: nothing before this line is pending
		}
		// decode only what can change the result: a final answer, a question, and — while one
		// is pending — a user line that may be the typed prompt superseding it
		candidate := assistant && (bytes.Contains(line, []byte(`"stop_reason":"end_turn"`)) || bytes.Contains(line, []byte(`"name":"AskUserQuestion"`)) || bytes.Contains(line, []byte(`"name":"ExitPlanMode"`)))
		if !candidate && !(pending != "" && bytes.Contains(line, []byte(`"type":"user"`))) {
			continue
		}
		var env envelope
		var msg message
		if json.Unmarshal(line, &env) != nil || json.Unmarshal(env.Message, &msg) != nil {
			continue
		}
		if env.Type == "user" {
			if !env.IsMeta && len(msg.Content) > 0 && msg.Content[0] == '"' {
				pending, fm.Question = "", 0 // a typed prompt: the user moved on without answering
			}
			continue
		}
		if env.Type != "assistant" {
			continue
		}
		if fm.Model == "" {
			fm.Model = msg.Model
		}
		for _, b := range blocksOf(msg.Content) {
			switch {
			case b.Type == "tool_use" && (b.Name == "AskUserQuestion" || b.Name == "ExitPlanMode"):
				pending, fm.Question = b.ID, source.ParseTS(env.Timestamp)
			case b.Type == "text" && msg.StopReason == "end_turn" && strings.TrimSpace(b.Text) != "":
				fm.LastAnswer = source.Clip(strings.TrimSpace(b.Text), 1200)
			}
		}
	}
}

func aiTitle(line []byte) string {
	var v struct {
		Title string `json:"aiTitle"`
	}
	if json.Unmarshal(line, &v) != nil {
		return ""
	}
	return strings.TrimSpace(v.Title)
}

// agentMeta is agent-<id>.meta.json: who the sub-agent is.
type agentMeta struct {
	AgentType       string `json:"agentType"`
	CustomAgentType string `json:"customAgentType"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Model           string `json:"model"`
	SpawnDepth      int    `json:"spawnDepth"`
	ToolUseID       string `json:"toolUseId"`
}

// readAgent indexes a sub-agent file: identity from the file name (agent-<id>, which the
// parent's Agent tool result names as agentId), the parent from the directory, who it is from
// the meta file, the start from its first line.
func readAgent(path string, st fs.FileInfo, parent string) fileMeta {
	fm := fileMeta{Meta: source.Meta{Source: source.Claude, Path: path, Size: st.Size(), ModTime: st.ModTime(), ParentID: parent}}
	fm.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil {
		var am agentMeta
		if json.Unmarshal(data, &am) == nil {
			fm.AgentType = source.OrDefault(am.CustomAgentType, am.AgentType)
			fm.AgentName, fm.Description, fm.Model, fm.Depth, fm.ToolUseID = am.Name, am.Description, am.Model, am.SpawnDepth, am.ToolUseID
		}
	}
	if fm.Depth < 1 {
		fm.Depth = 1
	}
	f, err := os.Open(path)
	if err != nil {
		return fm
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256<<10)
	for i := 0; i < 8; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 {
			break
		}
		if bytes.HasPrefix(line, []byte(`{"parentUuid"`)) && len(line) <= maxMetaLine {
			var env envelope
			if json.Unmarshal(line, &env) == nil && env.Timestamp != "" {
				fm.CWD, fm.Branch, fm.CLI = env.CWD, env.GitBranch, env.Version
				fm.Started = source.ParseTS(env.Timestamp)
				fm.Valid = true
				break
			}
		}
		if err != nil {
			break
		}
	}
	return fm
}

// Index knows every session file under the configured Claude homes. It is the Claude Code
// source.Source.
type Index struct {
	mu    sync.Mutex
	homes []string            // absolute, in configured order
	files map[string]fileMeta // by path
	byID  map[string]fileMeta // by id: a session present in two homes is the first home's
}

// NewIndex indexes the sessions of one or more Claude homes (each contains projects/). Homes
// are searched in the given order; a session present in several is one session, the first
// home's copy.
func NewIndex(homes ...string) *Index {
	ix := &Index{files: map[string]fileMeta{}, byID: map[string]fileMeta{}}
	ix.homes = cleanHomes(homes)
	return ix
}

func cleanHomes(homes []string) []string {
	out := make([]string, 0, len(homes))
	for _, h := range homes {
		if abs, err := filepath.Abs(h); err == nil {
			h = abs
		}
		out = append(out, filepath.Clean(h))
	}
	return out
}

// Name is the source name.
func (ix *Index) Name() string { return source.Claude }

// Homes returns the configured homes in order.
func (ix *Index) Homes() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append([]string(nil), ix.homes...)
}

// SetHomes replaces the configured homes; files of a dropped home leave the index at once.
func (ix *Index) SetHomes(homes []string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.homes = cleanHomes(homes)
	for p := range ix.files {
		if ix.homeOf(p) < 0 {
			delete(ix.files, p)
		}
	}
	ix.rebuildByID()
}

func projectsDir(home string) string { return filepath.Join(home, "projects") }

// Dirs returns the projects/ folder of every home: the only place session files are read from.
func (ix *Index) Dirs() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]string, 0, len(ix.homes))
	for _, h := range ix.homes {
		out = append(out, projectsDir(h))
	}
	return out
}

func (ix *Index) homeOf(path string) int {
	for i, h := range ix.homes {
		if strings.HasPrefix(path, h+string(filepath.Separator)) {
			return i
		}
	}
	return -1
}

// HomeStatuses describes every configured home, in order.
func (ix *Index) HomeStatuses() []source.HomeStatus {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]source.HomeStatus, len(ix.homes))
	for i, h := range ix.homes {
		out[i] = source.HomeStatus{Path: h, Status: homeLayout(h)}
	}
	for p, fm := range ix.files {
		if !fm.Valid || fm.ParentID != "" {
			continue
		}
		i := ix.homeOf(p)
		if i < 0 {
			continue
		}
		if ix.byID[fm.ID].Path == p {
			out[i].Sessions++
		} else {
			out[i].Elsewhere++
		}
	}
	return out
}

func homeLayout(home string) string {
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return source.HomeMissing
	}
	if st, err := os.Stat(projectsDir(home)); err == nil && st.IsDir() {
		return source.HomeOK
	}
	return source.HomeNoSessionsDir
}

// Scan walks projects/<project>/ of every home. Unchanged files (size, mtime) are not
// re-read; a grown root file gets its tail re-read (and its head, while it has no ai-title
// yet and is still small); a grown sub-agent file only refreshes its size.
func (ix *Index) Scan() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	seen := map[string]bool{}
	for _, h := range ix.homes {
		projects, err := os.ReadDir(projectsDir(h))
		if err != nil {
			continue
		}
		for _, proj := range projects {
			if !proj.IsDir() {
				continue
			}
			dir := filepath.Join(projectsDir(h), proj.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					ix.scanAgents(filepath.Join(dir, e.Name(), "subagents"), e.Name(), seen)
					continue
				}
				if !strings.HasSuffix(e.Name(), ".jsonl") {
					continue
				}
				st, err := e.Info()
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
				path := filepath.Join(dir, e.Name())
				seen[path] = true
				ix.scanRoot(path, st)
			}
		}
	}
	for p := range ix.files {
		if !seen[p] {
			delete(ix.files, p)
		}
	}
	ix.rebuildByID()
}

func (ix *Index) scanRoot(path string, st fs.FileInfo) {
	old, ok := ix.files[path]
	if ok && old.Valid && old.Size == st.Size() && old.ModTime.Equal(st.ModTime()) {
		return
	}
	if ok && old.Valid {
		old.Size, old.ModTime = st.Size(), st.ModTime()
		if f, err := os.Open(path); err == nil {
			if !old.titled && st.Size() <= headScan {
				scanHead(f, &old)
				f.Seek(0, 0)
			}
			readTail(f, st.Size(), &old)
			f.Close()
		}
		ix.files[path] = old
		return
	}
	ix.files[path] = readRoot(path, st)
}

func (ix *Index) scanAgents(dir, parent string, seen map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		st, err := e.Info()
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		seen[path] = true
		if old, ok := ix.files[path]; ok && old.Valid {
			if old.Size != st.Size() || !old.ModTime.Equal(st.ModTime()) {
				old.Size, old.ModTime = st.Size(), st.ModTime()
				ix.files[path] = old
			}
			continue
		}
		ix.files[path] = readAgent(path, st, parent)
	}
}

// rebuildByID derives the id map from the files, deterministically: homes in configured
// order, paths sorted within a home, the first valid file for an id wins.
func (ix *Index) rebuildByID() {
	paths := make([]string, 0, len(ix.files))
	for p := range ix.files {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		hi, hj := ix.homeOf(paths[i]), ix.homeOf(paths[j])
		if hi != hj {
			return hi < hj
		}
		return paths[i] < paths[j]
	})
	ix.byID = make(map[string]fileMeta, len(paths))
	for _, p := range paths {
		fm := ix.files[p]
		if !fm.Valid {
			continue
		}
		if _, dup := ix.byID[fm.ID]; !dup {
			ix.byID[fm.ID] = fm
		}
	}
}

// Roots returns the root sessions, newest first.
func (ix *Index) Roots() []source.Meta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []source.Meta
	for _, fm := range ix.byID {
		if fm.ParentID == "" {
			out = append(out, fm.Meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.After(out[j].ModTime)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// IDs returns every id the index knows, roots and sub-agents alike.
func (ix *Index) IDs() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]string, 0, len(ix.byID))
	for id := range ix.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Get returns the file for an id.
func (ix *Index) Get(id string) (source.Meta, bool) {
	fm, ok := ix.fileMeta(id)
	return fm.Meta, ok
}

func (ix *Index) fileMeta(id string) (fileMeta, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	fm, ok := ix.byID[id]
	return fm, ok
}

// Descendants returns the sub-agent files of a root session, by start then path. Every agent
// file sits directly under the root's subagents/ folder, a nested agent's too: the tree is
// flat (docs/DESIGN.md §1b).
func (ix *Index) Descendants(rootID string) []source.Meta {
	files := ix.descendants(rootID)
	out := make([]source.Meta, len(files))
	for i, fm := range files {
		out[i] = fm.Meta
	}
	return out
}

func (ix *Index) descendants(rootID string) []fileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []fileMeta
	for _, fm := range ix.byID {
		if fm.ParentID == rootID {
			out = append(out, fm)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started < out[j].Started
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Open creates the session for a root id (nothing is parsed until Refresh).
func (ix *Index) Open(rootID string) (*source.Session, error) {
	fm, ok := ix.fileMeta(rootID)
	if !ok || fm.ParentID != "" {
		return nil, fmt.Errorf("unknown session %s", rootID)
	}
	return source.NewSession(ix, fm.Meta, func(m source.Meta) source.LaneParser {
		if f, ok := ix.fileMeta(m.ID); ok {
			return newLaneParser(f)
		}
		return newLaneParser(fileMeta{Meta: m, Depth: 1})
	}), nil
}
