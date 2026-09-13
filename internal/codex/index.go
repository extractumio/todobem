package codex

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	Valid      bool
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
	fm.Started = parseTS(sm.Timestamp)
	if fm.Started == 0 {
		fm.Started = parseTS(rl.Timestamp)
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
	return fm
}

// Index knows every rollout file under the sessions root, keyed by thread id.
type Index struct {
	root   string // ~/.codex
	mu     sync.Mutex
	files  map[string]FileMeta // by path
	byID   map[string]FileMeta // by thread id
	names  map[string]string   // thread id -> thread_name (session_index.jsonl)
	scanAt time.Time
}

func NewIndex(codexHome string) *Index {
	return &Index{root: codexHome, files: map[string]FileMeta{}, byID: map[string]FileMeta{}, names: map[string]string{}}
}

// Scan walks the sessions tree. Files whose (size, mtime) are unchanged are not re-read.
// Only the first line is read for new/changed files, so a full scan of ~2000 files is cheap.
func (ix *Index) Scan() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	seen := map[string]bool{}
	dirs := []string{filepath.Join(ix.root, "sessions"), filepath.Join(ix.root, "archived_sessions")}
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
				// Only size/mtime changed: keep meta, refresh stats.
				old.Size, old.ModTime = st.Size(), st.ModTime()
				ix.files[path] = old
				ix.byID[old.ThreadID] = old
				return nil
			}
			fm := readMeta(path, st)
			ix.files[path] = fm
			if fm.Valid {
				ix.byID[fm.ThreadID] = fm
			}
			return nil
		})
	}
	for p, fm := range ix.files {
		if !seen[p] {
			delete(ix.files, p)
			if fm.Valid && ix.byID[fm.ThreadID].Path == p {
				delete(ix.byID, fm.ThreadID)
			}
		}
	}
	ix.loadNames()
	ix.scanAt = time.Now()
}

func (ix *Index) loadNames() {
	f, err := os.Open(filepath.Join(ix.root, "session_index.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && e.Name != "" {
			ix.names[e.ID] = e.Name // later lines win (file is append-only, newest last)
		}
	}
}

// Roots returns root-thread files, newest first.
func (ix *Index) Roots() []FileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []FileMeta
	for _, fm := range ix.files {
		if fm.Valid && fm.ParentID == "" {
			out = append(out, fm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// Get returns the file for a thread id.
func (ix *Index) Get(id string) (FileMeta, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	fm, ok := ix.byID[id]
	return fm, ok
}

// Descendants returns all sub-agent files whose parent chain leads to rootID, sorted by start.
func (ix *Index) Descendants(rootID string) []FileMeta {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	children := map[string][]FileMeta{}
	for _, fm := range ix.files {
		if fm.Valid && fm.ParentID != "" {
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
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return out
}

// Name returns the thread name recorded by Codex, if any.
func (ix *Index) Name(id string) string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.names[id]
}

// Subtree returns the total size of the root file plus descendants (for the list view).
func (ix *Index) SubtreeSize(rootID string) int64 {
	fm, ok := ix.Get(rootID)
	if !ok {
		return 0
	}
	n := fm.Size
	for _, d := range ix.Descendants(rootID) {
		n += d.Size
	}
	return n
}
