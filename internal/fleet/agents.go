package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/extractumio/todobem/internal/atomicfile"
	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/settings"
)

// Agent is one paired agent as the hub's agents file records it. The file holds bearers — the
// read credentials to every paired host's sessions — so it is 0600 and never shown by the UI.
type Agent struct {
	Name    string `json:"name"`
	Addr    string `json:"addr"` // host:port the hub dials
	Pin     string `json:"pin"`  // the agent certificate's SHA-256, 64 hex
	Bearer  string `json:"bearer"`
	Expires int64  `json:"expires"` // ms, the bearer's expiry
}

// DefaultAgentsPath is the hub's list of paired agents: $TODOBEM_AGENTS, else ~/.todobem/agents.json.
func DefaultAgentsPath() string {
	if p := os.Getenv("TODOBEM_AGENTS"); p != "" {
		return p
	}
	return under("agents.json")
}

// DefaultSnapshotDir holds one snapshot per agent: ~/.todobem/fleet.
func DefaultSnapshotDir() string { return under("fleet") }

// DefaultAgentKeyPath is the agent's root of trust: $TODOBEM_AGENT_KEY, else ~/.todobem/agent.key.
// The certificate and its key sit next to it (CertPaths).
func DefaultAgentKeyPath() string {
	if p := os.Getenv("TODOBEM_AGENT_KEY"); p != "" {
		return p
	}
	return under("agent.key")
}

func under(name string) string {
	if d := settings.Dir(); d != "" {
		return filepath.Join(d, name)
	}
	return ""
}

// CertPaths derives the certificate and TLS key files from the agent key path.
func CertPaths(agentKeyPath string) (certPath, keyPath string) {
	dir := filepath.Dir(agentKeyPath)
	return filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent-tls.key")
}

// ExpiresOn prints a bearer's expiry the way the CLI and the log name it.
func ExpiresOn(ms int64) string { return time.UnixMilli(ms).Format(time.DateOnly) }

// LoadAgents reads the agents file; a missing file is an empty fleet. mod is the file's mtime
// (zero when absent) so a running hub can notice a change made by the command line.
func LoadAgents(path string) (agents []Agent, mod time.Time, err error) {
	if path == "" {
		return nil, time.Time{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, nil
		}
		return nil, time.Time{}, err
	}
	if err := auth.CheckPrivate(path); err != nil {
		return nil, time.Time{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := json.Unmarshal(b, &agents); err != nil {
		return nil, time.Time{}, fmt.Errorf("%s: %w", path, err)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, st.ModTime(), nil
}

// SaveAgents writes the agents file atomically, 0600, creating its directory (0700).
func SaveAgents(path string, agents []Agent) error {
	if path == "" {
		return errors.New("no agents file path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if agents == nil {
		agents = []Agent{}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	b, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// Record writes an agent into the file, replacing one of the same name.
func Record(path string, a Agent) error {
	return edit(path, func(agents []Agent) []Agent {
		for i := range agents {
			if agents[i].Name == a.Name {
				agents[i] = a
				return agents
			}
		}
		return append(agents, a)
	})
}

// Forget drops the agent of that name from the file (a name not there is fine).
func Forget(path, name string) error {
	return edit(path, func(agents []Agent) []Agent {
		out := agents[:0:0]
		for _, a := range agents {
			if a.Name != name {
				out = append(out, a)
			}
		}
		return out
	})
}

func edit(path string, fn func([]Agent) []Agent) error {
	agents, _, err := LoadAgents(path)
	if err != nil {
		return err
	}
	return SaveAgents(path, fn(agents))
}
