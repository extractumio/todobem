// Package settings is the user's durable configuration: ~/.todobem/settings.json, written by the
// Settings page and read at start. It holds paths only (no secrets) and knows nothing about
// sessions: the folders it names are handed to the session sources.
//
// File format (JSON):
//
//	{"codex_homes": ["~/.codex", "/Volumes/work/codex-from-laptop"], "claude_homes": ["~/.claude"]}
//
// Every codex_homes entry is a Codex home — the folder that contains sessions/ (and
// archived_sessions/, session_index.jsonl); every claude_homes entry a Claude Code home — the
// folder that contains projects/. Entries are kept as typed ("~" allowed) so the file stays
// readable and portable; they are expanded when loaded. A key that is absent means that
// source's default home; an empty list turns the source off. Without the file todobem reads
// ~/.codex and ~/.claude.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/extractumio/todobem/internal/atomicfile"
)

// Settings is the file's content: the homes of each source, as typed. Both lists are always
// present once loaded (an absent key was filled with the source's default); an empty list is a
// source that is off.
type Settings struct {
	CodexHomes  []string `json:"codex_homes"`
	ClaudeHomes []string `json:"claude_homes"`
}

// Homes is the resolved answer to "which folders hold the sessions": absolute folders per
// source, in the settings' order. A source with no folders is off.
type Homes struct {
	Codex  []string
	Claude []string
}

// Dir is todobem's own folder, ~/.todobem: the settings, the auth and agent keys, the cache, the
// agents file and the fleet snapshots live under it. "" when the home directory is unknown.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".todobem")
}

// DefaultPath is where the settings live: $TODOBEM_SETTINGS, else ~/.todobem/settings.json.
func DefaultPath() string {
	if env := os.Getenv("TODOBEM_SETTINGS"); env != "" {
		return env
	}
	if d := Dir(); d != "" {
		return filepath.Join(d, "settings.json")
	}
	return ""
}

// The folders read when no settings file exists (or a key is absent), as typed.
const (
	DefaultCodexHome  = "~/.codex"
	DefaultClaudeHome = "~/.claude"
)

// Defaults is the configuration without a file: both default homes.
func Defaults() Settings {
	return Settings{CodexHomes: []string{DefaultCodexHome}, ClaudeHomes: []string{DefaultClaudeHome}}
}

// Load reads the file at path. A missing file is not an error (exists=false: the defaults
// apply); a malformed one is, so a broken config fails loudly instead of silently reading the
// wrong folders. An empty path reads nothing.
func Load(path string) (s Settings, exists bool, err error) {
	if path == "" {
		return Defaults(), false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Defaults(), false, nil
		}
		return Settings{}, false, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // tolerate a UTF-8 BOM
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	// a key that is absent keeps the source's default; a key that is present, even as [],
	// is what the user chose (a file written before claude_homes existed keeps Claude on)
	var file struct {
		Codex  *[]string `json:"codex_homes"`
		Claude *[]string `json:"claude_homes"`
	}
	if err := dec.Decode(&file); err != nil {
		return Settings{}, true, fmt.Errorf("%s: %w", path, err)
	}
	s = Defaults()
	if file.Codex != nil {
		s.CodexHomes = *file.Codex
	}
	if file.Claude != nil {
		s.ClaudeHomes = *file.Claude
	}
	return s, true, nil
}

// Save writes s to path atomically (temp file in the same directory, then rename), creating the
// directory. Mode 0600 like the rest of ~/.todobem; the file holds paths, not secrets.
func Save(path string, s Settings) error {
	if path == "" {
		return errors.New("no settings path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// both keys are always written: an absent key would read as the default next time
	if s.CodexHomes == nil {
		s.CodexHomes = []string{}
	}
	if s.ClaudeHomes == nil {
		s.ClaudeHomes = []string{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0600)
}

// Resolve normalizes both lists (Normalize) and requires at least one folder overall: a source
// may be off, todobem with nothing to read is a mistake. The returned Settings is what to save
// (entries as typed, cleaned); Homes are the folders to scan, in the same order.
func Resolve(s Settings) (Settings, Homes, error) {
	codexTyped, codex, err := Normalize(s.CodexHomes)
	if err != nil {
		return Settings{}, Homes{}, fmt.Errorf("codex_homes: %w", err)
	}
	claudeTyped, claude, err := Normalize(s.ClaudeHomes)
	if err != nil {
		return Settings{}, Homes{}, fmt.Errorf("claude_homes: %w", err)
	}
	if len(codex)+len(claude) == 0 {
		return Settings{}, Homes{}, errors.New("at least one folder is needed (a Codex or a Claude Code home)")
	}
	return Settings{CodexHomes: codexTyped, ClaudeHomes: claudeTyped}, Homes{Codex: codex, Claude: claude}, nil
}

// Normalize trims the entries of one list, drops blank ones and resolves each to an absolute
// folder ("~" is the home directory). It rejects a relative entry (relative to what? the
// server's working directory is not something the user sees) and a folder listed twice —
// through a symlink too, when the folder exists. An empty list is allowed (the source is off).
// typed is what to save (entries as typed, cleaned, never nil); homes are the folders to
// scan, in the same order.
func Normalize(entries []string) (typed []string, homes []string, err error) {
	typed = []string{}
	seen := map[string]int{}
	for _, raw := range entries {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		home, err := expand(t)
		if err != nil {
			return nil, nil, err
		}
		if !filepath.IsAbs(home) {
			return nil, nil, fmt.Errorf("%q: use an absolute path or one starting with ~/", t)
		}
		key := home
		if real, err := filepath.EvalSymlinks(home); err == nil {
			key = real
		}
		if i, dup := seen[key]; dup {
			return nil, nil, fmt.Errorf("%q is the same folder as %q", t, typed[i])
		}
		seen[key] = len(homes)
		typed = append(typed, t)
		homes = append(homes, home)
	}
	return typed, homes, nil
}

// expand turns "~" and "~/x" into the user's home directory and cleans the path.
func expand(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("%q: cannot resolve ~ (no home directory)", p)
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Clean(p), nil
}
