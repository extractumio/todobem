// Package source is the seam between the server and the session-log formats it reads. A Source
// (internal/codex, internal/claude) finds the session files under its configured homes, describes
// each as a Meta, and opens a root session as a Session: the shared joiner that parses the root
// file and its sub-agent files lane by lane (LaneParser) and re-derives the model when a file
// grows. Multi fans out over the configured sources so the server, the list, the cache and the
// command-line tools never name a format.
package source

import (
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// Source names: the value of model.Session.Source / SessionSummary.Source.
const (
	Codex  = "codex"
	Claude = "claude"
)

// Meta describes one session file the way the list, the cache fingerprint and the joiner need
// it: which source it belongs to, its identity in the thread tree, its size and mtime (the
// fingerprint), and what the index learned about it without parsing it.
type Meta struct {
	Source   string
	ID       string // thread / session id; a sub-agent file has its own
	ParentID string // "" for root sessions
	Path     string
	Size     int64
	ModTime  time.Time
	Started  int64 // ms
	CWD      string
	Branch   string
	CLI      string // harness version
	Model    string
	// Title is the name the harness itself recorded for the session (a Codex thread_name, a
	// Claude Code ai-title); "" when none — the list then falls back to the first user message.
	Title string
	// LastAnswer is the final message of the last completed turn, verbatim and clipped, read
	// from the file's tail so the list can describe a session that was never parsed.
	LastAnswer string
	// Question is the time (ms) of a question the agent asked the user that nothing has
	// answered yet — a Claude Code AskUserQuestion or ExitPlanMode call without its tool_result, a Codex
	// request_user_input call without its output or a later user message — read from the
	// file's tail like LastAnswer; 0 when none is pending.
	Question int64
}

// Home statuses for the settings page.
const (
	HomeOK            = "ok"              // present, with the source's session folder
	HomeMissing       = "missing"         // not a directory (an unmounted volume, a typo)
	HomeNoSessionsDir = "no_sessions_dir" // a directory, but not a home of this source
)

// HomeStatus is one configured home as the settings page shows it.
type HomeStatus struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Sessions  int    `json:"sessions"`  // root sessions read from it
	Elsewhere int    `json:"elsewhere"` // root sessions it holds that an earlier home also holds (read from there)
}

// Source is one session-log format: an index of the files under its homes plus the parser
// that turns a root session into the model. Every method is safe for concurrent use.
type Source interface {
	Name() string
	// Homes returns the configured homes in order; SetHomes replaces them, dropping the files
	// of a home no longer listed at once (the next Scan adds the new homes' files).
	Homes() []string
	SetHomes(homes []string)
	HomeStatuses() []HomeStatus
	// Dirs lists every directory the source reads session files from, across all homes; a
	// recorded source span is served only from a file under one of them.
	Dirs() []string
	// Scan walks the homes; unchanged files (size, mtime) are not re-read.
	Scan()
	// Roots returns the root sessions, newest first; IDs every id, roots and sub-agents.
	Roots() []Meta
	IDs() []string
	Get(id string) (Meta, bool)
	// Descendants returns the sub-agent files under a root, parents before children, by start.
	Descendants(rootID string) []Meta
	// Open creates the session for a root id (nothing is parsed until Refresh).
	Open(rootID string) (*Session, error)
}

// LaneParser is one lane file's resumable parser, supplied by the source.
type LaneParser interface {
	// Lane is the lane being built; the joiner reads it after every Consume.
	Lane() *model.Lane
	// Path is the file; Offset the bytes consumed (end of the last complete line).
	Path() string
	Offset() int64
	// Consume reads every complete new line. ErrRewritten means the file was rewritten from
	// the start (the joiner restarts the lane with a fresh parser).
	Consume(now int64) error
	// LastTS is the last timestamp seen: evidence the process was alive then.
	LastTS() int64
	// Stats reports bytes read and bytes JSON-decoded (cmd/dump).
	Stats() (read, decoded int64)
}
