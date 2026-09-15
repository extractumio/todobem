package codex

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
	"time"

	"github.com/extractumio/todobem/internal/source"
)

// FileMeta is what we learn from line 1 (session_meta) of a rollout file.
type FileMeta struct {
	Path       string
	Size       int64
	ModTime    time.Time
	ThreadID   string
	ParentID   string // "" for root threads
	AgentPath  string // "/root/x" for sub-agents
	Nickname   string
	Role       string
	Depth      int
	CWD        string
	Branch     string
	CLIVersion string
	Model      string
	Started    int64 // ms
	Originator string
	// LastAnswer is the final message of the last completed turn recorded in the file
	// (task_complete.last_agent_message), verbatim, clipped; read from the file's tail so the
	// session list can describe a session that was never parsed. Root files only.
	LastAnswer string
	// Question is the time (ms) of a request_user_input call nothing has answered yet (no
	// output for its call_id, no user message after it); 0 when none. Root files only.
	Question int64
	Valid    bool
}

// meta is the file as the shared joiner and the list see it; title is the thread name.
func (fm FileMeta) meta(title string) source.Meta {
	return source.Meta{Source: source.Codex, ID: fm.ThreadID, ParentID: fm.ParentID, Path: fm.Path, Size: fm.Size, ModTime: fm.ModTime, Started: fm.Started, CWD: fm.CWD, Branch: fm.Branch, CLI: fm.CLIVersion, Model: fm.Model, Title: title, LastAnswer: fm.LastAnswer, Question: fm.Question}
}

type sessionMeta struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	ParentThreadID string          `json:"parent_thread_id"`
	Timestamp      string          `json:"timestamp"`
	CWD            string          `json:"cwd"`
	Originator     string          `json:"originator"`
	CLIVersion     string          `json:"cli_version"`
	ThreadSource   string          `json:"thread_source"`
	AgentPath      string          `json:"agent_path"`
	AgentNickname  string          `json:"agent_nickname"`
	AgentRole      string          `json:"agent_role"`
	Source         json.RawMessage `json:"source"`
	Git            struct {
		Branch string `json:"branch"`
	} `json:"git"`
	BaseInstructions struct {
		Provenance struct {
			Model string `json:"model"`
		} `json:"provenance"`
	} `json:"base_instructions"`
}

type subagentSource struct {
	Subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
			Depth          int    `json:"depth"`
			AgentPath      string `json:"agent_path"`
			AgentNickname  string `json:"agent_nickname"`
			AgentRole      string `json:"agent_role"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

// readMeta parses the first line of a rollout file.
func readMeta(path string, st fs.FileInfo) FileMeta {
	fm := FileMeta{Path: path, Size: st.Size(), ModTime: st.ModTime()}
	f, err := os.Open(path)
	if err != nil {
		return fm
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256<<10)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return fm
	}
	var rl rawLine
	if json.Unmarshal(line, &rl) != nil || rl.Type != "session_meta" {
		return fm
	}
	var sm sessionMeta
	if json.Unmarshal(rl.Payload, &sm) != nil {
		return fm
	}
	fm.ThreadID = sm.ID
	if fm.ThreadID == "" {
		fm.ThreadID = sm.SessionID
	}
	fm.ParentID = sm.ParentThreadID
	fm.AgentPath = sm.AgentPath
	fm.Nickname = sm.AgentNickname
	fm.Role = sm.AgentRole
	fm.CWD = sm.CWD
	fm.Branch = sm.Git.Branch
	fm.CLIVersion = sm.CLIVersion
	fm.Model = sm.BaseInstructions.Provenance.Model
	fm.Originator = sm.Originator
	fm.Started = source.ParseTS(sm.Timestamp)
	if fm.Started == 0 {
		fm.Started = source.ParseTS(rl.Timestamp)
	}
	if len(sm.Source) > 0 && sm.Source[0] == '{' {
		var ss subagentSource
		if json.Unmarshal(sm.Source, &ss) == nil && ss.Subagent.ThreadSpawn.ParentThreadID != "" {
			ts := ss.Subagent.ThreadSpawn
			fm.ParentID = ts.ParentThreadID
			fm.Depth = ts.Depth
			if fm.AgentPath == "" {
				fm.AgentPath = ts.AgentPath
			}
			if fm.Nickname == "" {
				fm.Nickname = ts.AgentNickname
			}
			if fm.Role == "" {
				fm.Role = ts.AgentRole
			}
		}
	}
	if fm.ParentID != "" && fm.Depth == 0 {
		fm.Depth = strings.Count(strings.TrimPrefix(fm.AgentPath, "/root"), "/")
		if fm.Depth == 0 {
			fm.Depth = 1
		}
	}
	fm.Valid = fm.ThreadID != ""
	if fm.Valid && fm.ParentID == "" {
		fm.LastAnswer, fm.Question = readTail(f, st.Size())
	}
	return fm
}

// lastAnswerTail bounds the tail read: task_complete lines are small and the last one is near
// the end of the file, and a pending question is by definition at the end. A multi-megabyte
// compacted line at the very end pushes them out of reach; then there is nothing to show,
// which is honest.
const lastAnswerTail = 256 << 10

// readTail finds, in the file's tail, the last complete task_complete line (the last answer:
// last_agent_message, clipped) and a question the agent asked the user that nothing has
// answered — the last request_user_input call with no function_call_output for its call_id
// and no user message after it (question = the call's time in ms; 0 when none). Only those
// lines are JSON-decoded.
func readTail(f *os.File, size int64) (answer string, question int64) {
	off := size - lastAnswerTail
	if off < 0 {
		off = 0
	}
	buf := make([]byte, size-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return "", 0
	}
	buf = buf[:n]
	if off > 0 {
		// drop the partial first line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return "", 0
		}
	}
	var last []byte
	pendingCall := "" // the call_id of the question still waiting for its answer
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		switch lineType(line) {
		case "event_msg":
			switch payloadType(line) {
			case "task_complete":
				last = line
			case "user_message":
				pendingCall, question = "", 0 // the user spoke: the question is no longer pending
			}
		case "response_item":
			switch payloadType(line) {
			case "function_call":
				if !bytes.Contains(line, []byte(`"name":"request_user_input`)) {
					continue
				}
				var rl rawLine
				var fc struct {
					Name   string `json:"name"`
					CallID string `json:"call_id"`
				}
				if json.Unmarshal(line, &rl) != nil || json.Unmarshal(rl.Payload, &fc) != nil {
					continue
				}
				if fc.Name == "request_user_input" || fc.Name == "request_user_input_async" {
					pendingCall, question = fc.CallID, source.ParseTS(rl.Timestamp)
				}
			case "function_call_output":
				if pendingCall != "" && prefixField(line, "call_id", 400) == pendingCall {
					pendingCall, question = "", 0 // answered
				}
			case "message":
				if prefixField(line, "role", 300) == "user" {
					pendingCall, question = "", 0
				}
			}
		}
	}
	if last == nil {
		return "", question
	}
	var rl rawLine
	var tc struct {
		Last string `json:"last_agent_message"`
	}
	if json.Unmarshal(last, &rl) != nil || json.Unmarshal(rl.Payload, &tc) != nil {
		return "", question
	}
	return source.Clip(strings.TrimSpace(tc.Last), 1200), question
}

// Index knows every rollout file under the configured Codex homes, keyed by thread id. It is
// the Codex source.Source.
type Index struct {
	mu     sync.Mutex
	homes  []string            // Codex homes (each contains sessions/), in configured order, absolute
	files  map[string]FileMeta // by path
	byID   map[string]FileMeta // by thread id: a rollout present in two homes is the first home's
	names  map[string]string   // thread id -> thread_name (session_index.jsonl)
	scanAt time.Time
}

// NewIndex indexes the rollouts of one or more Codex homes: <home>/sessions and
// <home>/archived_sessions, names from <home>/session_index.jsonl. Homes are searched in the
// given order; a thread present in several (a tree copied from another machine) is one session,
// the first home's copy.
func NewIndex(homes ...string) *Index {
	ix := &Index{files: map[string]FileMeta{}, byID: map[string]FileMeta{}, names: map[string]string{}}
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
func (ix *Index) Name() string { return source.Codex }

// Homes returns the configured Codex homes in order.
func (ix *Index) Homes() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append([]string(nil), ix.homes...)
}

// SetHomes replaces the configured homes. Files of a home that is no longer listed leave the
// index at once, in the same critical section, so nothing of a dropped folder can be opened
// between this call and the next Scan (which adds the new homes' files).
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
	ix.names = map[string]string{}
}

// rolloutDirs lists the directories of one home that hold rollouts. Only the Codex home layout
// is read (sessions/ and archived_sessions/); a folder pointed at directly is not walked, so a
// slip in the settings ("/" or "~") never turns into a walk of the whole disk.
func rolloutDirs(home string) []string {
	return []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")}
}

// Dirs returns every directory the index reads rollouts from, across all homes, in order.
// The server's source reader serves a span only from a file under one of them.
func (ix *Index) Dirs() []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []string
	for _, h := range ix.homes {
		out = append(out, rolloutDirs(h)...)
	}
	return out
}

// homeOf returns the position of the home a path lies under, or -1. The separator is part of
// the test: a sibling folder that shares a name prefix (…/codex vs …/codex2) is not under it.
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
		if ix.byID[fm.ThreadID].Path == p {
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
	for _, d := range rolloutDirs(home) {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return source.HomeOK
		}
	}
	return source.HomeNoSessionsDir
}

// Scan walks the sessions trees. Files whose (size, mtime) are unchanged are not re-read.
// Only the first line is read for new/changed files, so a full scan of ~2000 files is cheap.
func (ix *Index) Scan() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	seen := map[string]bool{}
	var dirs []string
	for _, h := range ix.homes {
		dirs = append(dirs, rolloutDirs(h)...)
	}
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return nil
			}
			st, err := d.Info()
			if err != nil || !st.Mode().IsRegular() {
				return nil
			}
			seen[path] = true
			if old, ok := ix.files[path]; ok && old.Valid && old.Size == st.Size() && old.ModTime.Equal(st.ModTime()) {
				return nil
			}
			if old, ok := ix.files[path]; ok && old.Valid {
				// Only size/mtime changed: keep meta, refresh stats, the last answer and the
				// pending question.
				old.Size, old.ModTime = st.Size(), st.ModTime()
				if old.ParentID == "" {
					if f, err := os.Open(path); err == nil {
						old.LastAnswer, old.Question = readTail(f, st.Size())
						f.Close()
					}
				}
				ix.files[path] = old
				return nil
			}
			ix.files[path] = readMeta(path, st)
			return nil
		})
	}
	for p := range ix.files {
		if !seen[p] {
			delete(ix.files, p)
		}
	}
	ix.rebuildByID()
	ix.loadNames()
	ix.scanAt = time.Now()
}

// rebuildByID derives the thread-id map from the files, deterministically: homes in configured
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
	ix.byID = make(map[string]FileMeta, len(paths))
	for _, p := range paths {
		fm := ix.files[p]
		if !fm.Valid {
			continue
		}
		if _, dup := ix.byID[fm.ThreadID]; !dup {
			ix.byID[fm.ThreadID] = fm
		}
	}
}

// loadNames reads every home's session_index.jsonl. Within a file later lines win (it is
// append-only, newest last); across homes the first home wins, like the files.
func (ix *Index) loadNames() {
	ix.names = map[string]string{}
	for _, h := range ix.homes {
		f, err := os.Open(filepath.Join(h, "session_index.jsonl"))
		if err != nil {
			continue
		}
		local := map[string]string{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e struct {
				ID   string `json:"id"`
				Name string `json:"thread_name"`
			}
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && e.Name != "" {
				local[e.ID] = e.Name
			}
		}
		f.Close()
		for id, name := range local {
			if _, ok := ix.names[id]; !ok {
				ix.names[id] = name
			}
		}
	}
}

// Roots returns root-thread files, newest first: one per thread id, whichever home it is in.
func (ix *Index) Roots() []source.Meta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []source.Meta
	for _, fm := range ix.byID {
		if fm.ParentID == "" {
			out = append(out, fm.meta(ix.names[fm.ThreadID]))
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

// IDs returns every thread id the index knows, roots and sub-agents alike (a sub-agent
// can be opened as a session of its own).
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

// Get returns the file for a thread id.
func (ix *Index) Get(id string) (source.Meta, bool) {
	fm, ok := ix.fileMeta(id)
	if !ok {
		return source.Meta{}, false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return fm.meta(ix.names[id]), true
}

// fileMeta is Get with the Codex-only fields (agent path, nickname, role, depth).
func (ix *Index) fileMeta(id string) (FileMeta, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	fm, ok := ix.byID[id]
	return fm, ok
}

// Descendants returns all sub-agent files whose parent chain leads to rootID, sorted by start
// (then path, so the order is stable between refreshes).
func (ix *Index) Descendants(rootID string) []source.Meta {
	files := ix.descendants(rootID)
	out := make([]source.Meta, len(files))
	for i, fm := range files {
		out[i] = fm.meta("")
	}
	return out
}

func (ix *Index) descendants(rootID string) []FileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	children := map[string][]FileMeta{}
	for _, fm := range ix.byID {
		if fm.ParentID != "" {
			children[fm.ParentID] = append(children[fm.ParentID], fm)
		}
	}
	var out []FileMeta
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if depth > 8 {
			return
		}
		for _, c := range children[id] {
			out = append(out, c)
			walk(c.ThreadID, depth+1)
		}
	}
	walk(rootID, 0)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started != out[j].Started {
			return out[i].Started < out[j].Started
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// SubtreeSize returns the total size of the root file plus descendants.
func (ix *Index) SubtreeSize(rootID string) int64 {
	fm, ok := ix.fileMeta(rootID)
	if !ok {
		return 0
	}
	n := fm.Size
	for _, d := range ix.descendants(rootID) {
		n += d.Size
	}
	return n
}

// Open creates the session for a root thread id (nothing is parsed until Refresh). Every lane
// file, root or sub-agent, gets a laneParser built from the index's own record of it.
func (ix *Index) Open(rootID string) (*source.Session, error) {
	fm, ok := ix.Get(rootID)
	if !ok {
		return nil, fmt.Errorf("unknown thread %s", rootID)
	}
	return source.NewSession(ix, fm, func(m source.Meta) source.LaneParser {
		if f, ok := ix.fileMeta(m.ID); ok {
			return newLaneParser(f)
		}
		return newLaneParser(FileMeta{Path: m.Path, ThreadID: m.ID, ParentID: m.ParentID, Started: m.Started, CWD: m.CWD})
	}), nil
}
