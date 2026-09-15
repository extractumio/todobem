package server

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/extractumio/todobem/internal/settings"
	"github.com/extractumio/todobem/internal/source"
)

// settingsSvc serves /api/settings: the session folders the server reads, per source, and the
// file they are saved in (~/.todobem/settings.json). GET describes each folder as configured
// (as typed, resolved, present or not, sessions found); POST saves new lists and applies them
// live (Server.SetHomes). The route sits behind the auth gate like every other /api/* and takes
// only a JSON POST (jsonPost): it is the first UI write that outlives the process, and with
// -auth=off anyone on the port can point the server at another folder of session logs — the
// startup line and the SetHomes log line say which folders are in effect.
type settingsSvc struct {
	mu     sync.Mutex
	path   string            // the settings file; "" = none (the folders are read-only)
	typed  settings.Settings // the folders as configured, in each source's order
	pinned bool              // -codex / -claude on the command line: this run's folders cannot change from the page
}

// ConfigureSettings tells the server where the settings file is and how the folders were
// chosen. typed lists the folders as the user wrote them (the file's entries, or the flag
// values); pinned says the page must not change them. Without this call the folders are
// read-only.
func (s *Server) ConfigureSettings(path string, typed settings.Settings, pinned bool) {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.path = path
	s.settings.typed = settings.Settings{CodexHomes: append([]string(nil), typed.CodexHomes...), ClaudeHomes: append([]string(nil), typed.ClaudeHomes...)}
	s.settings.pinned = pinned
}

// settingsHome is one row of the page: the entry as typed next to what the index sees.
type settingsHome struct {
	Path      string `json:"path"`
	Resolved  string `json:"resolved"`
	Status    string `json:"status"` // ok | missing | no_sessions_dir
	Sessions  int    `json:"sessions"`
	Elsewhere int    `json:"elsewhere"` // sessions it holds that are read from an earlier folder
}

// settingsSource is one source's section of the page.
type settingsSource struct {
	Name  string         `json:"name"` // codex | claude
	Homes []settingsHome `json:"homes"`
}

type settingsState struct {
	Path    string           `json:"path"`
	Exists  bool             `json:"exists"`
	Pinned  bool             `json:"pinned"`
	Sources []settingsSource `json:"sources"`
}

func (s *Server) settingsSnapshot() settingsState {
	svc := s.settings
	svc.mu.Lock()
	path, typed, pinned := svc.path, svc.typed, svc.pinned
	svc.mu.Unlock()
	st := settingsState{Path: path, Pinned: pinned || path == "", Sources: []settingsSource{}}
	if path != "" {
		_, st.Exists, _ = settings.Load(path)
	}
	for _, src := range s.src.Sources() {
		typedHomes := typed.CodexHomes
		if src.Name() == source.Claude {
			typedHomes = typed.ClaudeHomes
		}
		sec := settingsSource{Name: src.Name(), Homes: []settingsHome{}}
		for i, h := range src.HomeStatuses() {
			row := settingsHome{Path: h.Path, Resolved: h.Path, Status: h.Status, Sessions: h.Sessions, Elsewhere: h.Elsewhere}
			if i < len(typedHomes) {
				row.Path = typedHomes[i]
			}
			sec.Homes = append(sec.Homes, row)
		}
		st.Sources = append(st.Sources, sec)
	}
	return st
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.settingsSnapshot())
	case http.MethodPost:
		if !jsonPost(w, r) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		svc := s.settings
		svc.mu.Lock()
		path, pinned := svc.path, svc.pinned
		svc.mu.Unlock()
		if pinned || path == "" {
			http.Error(w, "the folders are set by -codex / -claude on the command line for this run", http.StatusConflict)
			return
		}
		var body settings.Settings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		cfg, homes, err := settings.Resolve(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := settings.Save(path, cfg); err != nil {
			http.Error(w, "could not write "+path+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		svc.mu.Lock()
		svc.typed = cfg
		svc.mu.Unlock()
		s.SetHomes(homes)
		writeJSON(w, s.settingsSnapshot())
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
