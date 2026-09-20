package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
)

// agentFixture runs an agent over a synthetic Codex home behind pinned TLS and returns its
// address, pin, key path and server.
func agentFixture(t *testing.T) (agent *Server, addr, pin, keyPath, home string) {
	t.Helper()
	home = t.TempDir()
	writeRolloutAt(t, home, "aaaa-1111", 1_700_000_000_000, "go test ./...")
	writeRolloutAt(t, home, "bbbb-2222", 1_700_000_100_000, "npm run build")
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "agent.key")
	certPath, tlsKey := fleet.CertPaths(keyPath)
	cert, pin, err := fleet.LoadOrCreateCert(certPath, tlsKey)
	if err != nil {
		t.Fatal(err)
	}
	agent = NewWithCache(settings.Homes{Codex: []string{home}}, nil, filepath.Join(dir, "cache"))
	agent.Scan()
	handler, err := agent.AgentHandler(keyPath, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = fleet.ServerTLS(cert)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return agent, strings.TrimPrefix(ts.URL, "https://"), pin, keyPath, home
}

// hubFixture pairs a hub with the agent: an agents file, a snapshot dir, a hub server with no
// local sessions, polled once.
func hubFixture(t *testing.T, addr, pin, keyPath string) (hub *Server, fl *fleet.Fleet, agentsPath string) {
	t.Helper()
	pairing, err := PairingString(keyPath, pin, "7789", false, AgentBearerTTL, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	agentsPath = filepath.Join(dir, "agents.json")
	fl = fleet.New(agentsPath, filepath.Join(dir, "fleet"))
	hub = NewWithCache(settings.Homes{}, fstest.MapFS{}, filepath.Join(dir, "cache"))
	hub.SetFleet(fl) // the facts sink is wired before anything polls
	if err := fl.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fl.Stop)
	if err := fl.Add(pairing, "a1", addr); err != nil {
		t.Fatalf("pair: %v", err)
	}
	return hub, fl, agentsPath
}

// get is call for a GET whose JSON answer lands in v.
func get(t *testing.T, s *Server, path string, v any) int {
	t.Helper()
	w := call(s.Handler(), http.MethodGet, path, "", "")
	if v != nil && w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v: %s", path, err, w.Body.String())
		}
	}
	return w.Code
}

// TestHubServesRemoteSessions: the hub lists the agent's sessions under composite ids and
// serves the model, the op detail (with its source span through the agent), the version poll
// and an event span through the same /api/* routes the page uses.
func TestHubServesRemoteSessions(t *testing.T) {
	_, addr, pin, keyPath, _ := agentFixture(t)
	hub, _, _ := hubFixture(t, addr, pin, keyPath)

	var rows []model.SessionSummary
	if code := get(t, hub, "/api/sessions", &rows); code != 200 || len(rows) != 2 {
		t.Fatalf("list: %d rows, code %d", len(rows), code)
	}
	for _, r := range rows {
		if r.Host != "a1" || !strings.HasSuffix(r.ID, "@a1") {
			t.Fatalf("row not marked remote: %+v", r)
		}
	}
	id := "aaaa-1111@a1"
	var m model.Session
	if code := get(t, hub, "/api/sessions/"+id, &m); code != 200 {
		t.Fatalf("model: %d", code)
	}
	if m.ID != id || m.Host != "a1" || len(m.Lanes) == 0 || len(m.Lanes[0].Ops) == 0 {
		t.Fatalf("model not rewritten or empty: id=%s host=%s", m.ID, m.Host)
	}
	// second open: served from the hub's cache (the fingerprint still matches the row)
	if code := get(t, hub, "/api/sessions/"+id, &m); code != 200 || m.ID != id {
		t.Fatalf("cached model: %d %s", code, m.ID)
	}
	var op struct {
		Detail string          `json:"detail"`
		Source json.RawMessage `json:"source"`
	}
	opID := m.Lanes[0].Ops[0].ID
	for _, o := range m.Lanes[0].Ops {
		if o.Src != nil {
			opID = o.ID
		}
	}
	if code := get(t, hub, "/api/sessions/"+id+"/op/"+opID, &op); code != 200 {
		t.Fatalf("op: %d", code)
	}
	if !strings.Contains(op.Detail, "go test") || len(op.Source) == 0 {
		t.Fatalf("op detail through the agent: detail=%q source=%d bytes", op.Detail, len(op.Source))
	}
	var ver struct {
		Version string `json:"version"`
	}
	if code := get(t, hub, "/api/sessions/"+id+"/version", &ver); code != 200 || ver.Version != m.Version {
		t.Fatalf("version: %d %q vs %q", code, ver.Version, m.Version)
	}
	var src *model.Src
	for _, mk := range m.Lanes[0].Markers {
		if mk.Kind == "user_message" && mk.Src != nil {
			src = mk.Src
		}
	}
	if src == nil {
		t.Fatal("no user message span in the remote model")
	}
	var ev map[string]any
	q := fmt.Sprintf("/api/event?session=%s&file=%s&off=%d&len=%d", id, src.File, src.Off, src.Len)
	if code := get(t, hub, q, &ev); code != 200 || ev["type"] != "event_msg" {
		t.Fatalf("event through the agent: %d %v", code, ev)
	}
}

// TestFleetDeltaAndReconciliation: a new session arrives as a delta row; a deleted one is
// healed by the hash mismatch and a full fetch; a restarted agent (new boot) is a full list.
func TestFleetDeltaAndReconciliation(t *testing.T) {
	agent, addr, pin, keyPath, home := agentFixture(t)
	hub, fl, _ := hubFixture(t, addr, pin, keyPath)
	writeRolloutAt(t, home, "cccc-3333", 1_700_000_200_000, "make lint")
	agent.Scan()
	if err := fl.PollNow("a1"); err != nil {
		t.Fatal(err)
	}
	var rows []model.SessionSummary
	get(t, hub, "/api/sessions", &rows)
	if len(rows) != 3 {
		t.Fatalf("after a new session: %d rows", len(rows))
	}
	if err := os.Remove(filepath.Join(home, "sessions", "day", "rollout-bbbb-2222.jsonl")); err != nil {
		t.Fatal(err)
	}
	agent.Scan()
	if err := fl.PollNow("a1"); err != nil {
		t.Fatal(err)
	}
	get(t, hub, "/api/sessions", &rows)
	if len(rows) != 2 {
		t.Fatalf("after a deletion: %d rows", len(rows))
	}
	for _, r := range rows {
		if strings.HasPrefix(r.ID, "bbbb") {
			t.Fatal("deleted session still listed")
		}
	}
	// the tracker's page: a cursor from another boot is a full list
	page := agent.agent.tracker.Page("deadbeef:1", agent.summaries())
	if !page.Full || len(page.Rows) != 2 {
		t.Fatalf("foreign boot: full=%v rows=%d", page.Full, len(page.Rows))
	}
	page = agent.agent.tracker.Page(page.Cursor, agent.summaries())
	if page.Full || len(page.Rows) != 0 {
		t.Fatalf("current cursor: full=%v rows=%d", page.Full, len(page.Rows))
	}
}

// TestAgentRefusesWithoutBearer: no bearer, a wrong bearer and a token minted before boot are
// all refused; the agent serves nothing outside /agent/v1/.
func TestAgentRefusesWithoutBearer(t *testing.T) {
	_, addr, pin, keyPath, _ := agentFixture(t)
	c := fleet.NewClient(addr, pin, "")
	defer c.Close()
	if _, err := c.Hello(); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("no bearer: %v", err)
	}
	c.SetBearer("bm9wZQ")
	if _, err := c.Sessions(""); err == nil {
		t.Fatal("garbage bearer accepted")
	}
	old, err := PairingString(keyPath, pin, "7789", false, AgentBearerTTL, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fleet.Pair(old, "x", addr); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale token accepted: %v", err)
	}
	good, _ := PairingString(keyPath, pin, "7789", false, AgentBearerTTL, time.Now())
	if _, _, err := fleet.Pair(good, "x", addr); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
	if _, _, err := fleet.Pair(good, "x", addr); err == nil || !strings.Contains(err.Error(), "used") {
		t.Fatalf("token reused: %v", err)
	}
	wrongPin := fleet.NewClient(addr, strings.Repeat("0", 64), "")
	defer wrongPin.Close()
	if _, err := wrongPin.Hello(); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("wrong pin accepted: %v", err)
	}
}

// TestRotateIsTwoPhase: after a rotation the old bearer still works until the new one is used
// once; then only the new one does. A lost answer therefore never locks the hub out.
func TestRotateIsTwoPhase(t *testing.T) {
	_, addr, pin, keyPath, _ := agentFixture(t)
	_, fl, agentsPath := hubFixture(t, addr, pin, keyPath)
	agents, _, _ := fleet.LoadAgents(agentsPath)
	oldBearer := agents[0].Bearer
	if err := fl.Rotate("a1"); err != nil {
		t.Fatal(err)
	}
	agents, _, _ = fleet.LoadAgents(agentsPath)
	newBearer := agents[0].Bearer
	if newBearer == oldBearer {
		t.Fatal("rotate recorded no new bearer")
	}
	oldClient := fleet.NewClient(addr, pin, oldBearer)
	defer oldClient.Close()
	if _, err := oldClient.Hello(); err != nil {
		t.Fatalf("old bearer refused before the new one was used: %v", err)
	}
	if _, err := os.Stat(auth.NextPath(keyPath)); err != nil {
		t.Fatal("no staged key")
	}
	newClient := fleet.NewClient(addr, pin, newBearer)
	defer newClient.Close()
	if _, err := newClient.Hello(); err != nil {
		t.Fatalf("new bearer refused: %v", err)
	}
	if _, err := os.Stat(auth.NextPath(keyPath)); err == nil {
		t.Fatal("staged key not promoted on first use")
	}
	if _, err := oldClient.Hello(); err == nil {
		t.Fatal("old bearer still works after promotion")
	}
	if _, err := newClient.Hello(); err != nil {
		t.Fatalf("new bearer after promotion: %v", err)
	}
}

// TestFactsFlowToTheHub: the agent's digest parses the closed sessions, the hub fetches their
// facts into its sidecar under composite ids, and the Insights report covers them (host filter).
func TestFactsFlowToTheHub(t *testing.T) {
	agent, addr, pin, keyPath, _ := agentFixture(t)
	hub, fl, _ := hubFixture(t, addr, pin, keyPath)
	agent.agent.digest()
	deadline := time.Now().Add(10 * time.Second)
	for agent.insights.scanner.Progress().Running && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	page, err := fleet.NewClient(addr, pin, currentBearer(t, fl)).Facts([]string{"aaaa-1111", "bbbb-2222", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 2 || len(page.Unknown) != 1 || page.Unknown[0] != "nope" {
		t.Fatalf("facts page: %d facts, pending %v, unknown %v", len(page.Facts), page.Pending, page.Unknown)
	}
	if err := fl.PollNow("a1"); err != nil {
		t.Fatal(err)
	}
	st := fl.Status()[0]
	if st.Digested != 2 || !st.FactsOK || !st.ModelsOK {
		t.Fatalf("status: %+v", st)
	}
	if _, ok := hub.insights.factsFor("aaaa-1111@a1"); !ok {
		t.Fatal("hub has no facts for the remote session")
	}
	var rep struct {
		Scope struct {
			Sessions int `json:"sessions"`
		} `json:"scope"`
	}
	if code := get(t, hub, "/api/insights/report?period=all&hosts=a1", &rep); code != 200 || rep.Scope.Sessions != 2 {
		t.Fatalf("report over the remote host: %d, %d sessions", code, rep.Scope.Sessions)
	}
	if code := get(t, hub, "/api/insights/report?period=all&hosts=.", &rep); code != 200 || rep.Scope.Sessions != 0 {
		t.Fatalf("report over this machine only: %d sessions", rep.Scope.Sessions)
	}
}

func currentBearer(t *testing.T, fl *fleet.Fleet) string {
	t.Helper()
	agents, _, err := fleet.LoadAgents(fl.Path())
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents file: %v", err)
	}
	return agents[0].Bearer
}

// TestFleetAPI: the Servers section's routes on the hub.
func TestFleetAPI(t *testing.T) {
	_, addr, pin, keyPath, _ := agentFixture(t)
	hub, _, _ := hubFixture(t, addr, pin, keyPath)
	var st struct {
		Agents []fleet.AgentStatus `json:"agents"`
	}
	if code := get(t, hub, "/api/fleet", &st); code != 200 || len(st.Agents) != 1 || st.Agents[0].Sessions != 2 || !st.Agents[0].Reached {
		t.Fatalf("fleet status: %d %+v", code, st.Agents)
	}
	var doc map[string]any
	if code := get(t, hub, "/api/fleet/doctor?name=a1", &doc); code != 200 || doc["hello"] == nil {
		t.Fatalf("doctor: %d %v", code, doc)
	}
	w := call(hub.Handler(), http.MethodPost, "/api/fleet/remove", "application/json", `{"name":"a1"}`)
	if w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	get(t, hub, "/api/fleet", &st)
	if len(st.Agents) != 0 {
		t.Fatalf("agent still listed after removal: %+v", st.Agents)
	}
	var rows []model.SessionSummary
	get(t, hub, "/api/sessions", &rows)
	if len(rows) != 0 {
		t.Fatalf("rows of a removed agent still listed: %d", len(rows))
	}
}
