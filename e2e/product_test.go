//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

const agentName = "e2e-agent"

// product runs the installed binary the way a user does — a viewer (the hub) and an agent,
// each on its own free port — and checks that it works: the agent is paired over TLS (when pair
// is set; otherwise the pairing must have survived), a login from `todobem token` opens the
// API, both local fixture sessions are listed and parse with an exact partition, and the
// agent's sessions arrive through the hub. Both processes are stopped on return.
func (h *host) product(t *testing.T, pair bool) {
	t.Helper()
	agent := h.start(t, "agent", "-agent", "-addr", fmt.Sprintf("127.0.0.1:%d", h.agentPort), "-codex", h.codex, "-claude", h.claude)
	defer agent.stop(t)
	viewer := h.start(t, "viewer", "-open=false", "-addr", h.viewerAddr(), "-codex", h.codex, "-claude", h.claude)
	defer viewer.stop(t)
	if pair {
		s := strings.TrimSpace(h.run(t, 0, h.bin(), "agent", "pair", "-q", "-port", fmt.Sprint(h.agentPort)))
		out := h.run(t, 0, h.bin(), "hub", "add", "-name", agentName, "-addr", fmt.Sprintf("127.0.0.1:%d", h.agentPort), s)
		mustContain(t, out, "paired "+agentName)
	}
	c := h.login(t)

	var rows []struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Host   string `json:"host"`
	}
	wait(t, "the hub lists its own and the agent's sessions", 90*time.Second, func() error {
		c.post(t, "/api/fleet/poll", map[string]string{"name": agentName})
		if err := c.get("/api/sessions", &rows); err != nil {
			return err
		}
		local, remote := 0, 0
		var seen []string
		for _, r := range rows {
			seen = append(seen, r.Source+"@"+r.Host)
			if r.Host == "" {
				local++
			} else if r.Host == agentName {
				remote++
			}
		}
		if local != 2 || remote != 2 {
			return fmt.Errorf("%d local and %d remote rows, want 2 and 2 (rows: %v)", local, remote, seen)
		}
		return nil
	})
	for _, r := range rows {
		var sess struct {
			Totals struct {
				ElapsedMs int64            `json:"elapsed_ms"`
				ByPhase   map[string]int64 `json:"by_phase"`
			} `json:"totals"`
		}
		if err := c.get("/api/sessions/"+r.ID, &sess); err != nil {
			t.Fatalf("open %s (%s): %v", r.ID, r.Host, err)
		}
		var sum int64
		for _, ms := range sess.Totals.ByPhase {
			sum += ms
		}
		if sess.Totals.ElapsedMs <= 0 || sum != sess.Totals.ElapsedMs {
			t.Fatalf("%s: partition %d of elapsed %d", r.ID, sum, sess.Totals.ElapsedMs)
		}
	}
	var fleet struct {
		Agents []struct {
			Name    string `json:"name"`
			Reached bool   `json:"reached"`
			Version string `json:"version"`
		} `json:"agents"`
	}
	if err := c.get("/api/fleet", &fleet); err != nil {
		t.Fatal(err)
	}
	if len(fleet.Agents) != 1 || !fleet.Agents[0].Reached || fleet.Agents[0].Version != h.info(t, h.bin()).Version {
		t.Fatalf("fleet: %+v", fleet.Agents)
	}
}

func (h *host) viewerAddr() string { return fmt.Sprintf("127.0.0.1:%d", h.viewerPort) }

// proc is a running todobem.
type proc struct {
	name string
	cmd  *exec.Cmd
	log  string
	done chan error
}

// start runs the installed binary in the background and waits for its port to accept.
func (h *host) start(t *testing.T, name string, args ...string) *proc {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(h.bin(), args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = h.env(), h.home, f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	p := &proc{name: name, cmd: cmd, log: logPath, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	port := h.viewerPort
	if name == "agent" {
		port = h.agentPort
	}
	wait(t, name+" listening", 30*time.Second, func() error {
		select {
		case err := <-p.done:
			p.done <- err
			t.Fatalf("%s exited: %v\n%s", name, err, readFile(t, logPath))
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
		}
		return err
	})
	return p
}

// stop ends the process (SIGTERM, then SIGKILL) and shows its log when the test failed.
func (p *proc) stop(t *testing.T) {
	t.Helper()
	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
	}
	if t.Failed() {
		t.Logf("%s log:\n%s", p.name, readFile(t, p.log))
	}
}

// client is a logged-in browser: the session cookie from a one-time token.
type client struct {
	base string
	http *http.Client
}

var tokenLine = regexp.MustCompile(`(?m)^token:\s+(\S+)`)

func (h *host) login(t *testing.T) *client {
	t.Helper()
	out := h.run(t, 0, h.bin(), "token", "-addr", h.viewerAddr())
	m := tokenLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no token in:\n%s", out)
	}
	jar, _ := cookiejar.New(nil)
	c := &client{base: "http://" + h.viewerAddr(), http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	if code := c.post(t, "/api/login", map[string]string{"token": m[1]}); code != http.StatusNoContent {
		t.Fatalf("login: HTTP %d", code)
	}
	return c
}

func (c *client) post(t *testing.T, path string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func (c *client) get(path string, v any) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: HTTP %d %s", path, resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
