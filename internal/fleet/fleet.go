package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/store"
)

// Fleet is the hub's view of its paired agents: the agents file (re-read when it changes), one
// poller per agent keeping a snapshot of its session list, models fetched on demand for the
// server to cache, facts handed to a sink after every delta. The server merges Summaries into
// its list, routes the ids Split recognises here and owns every cache write
// (internal/server/remote.go); nothing here knows a session-log format or a cache directory.
type Fleet struct {
	path  string // the agents file
	dir   string // the snapshots
	facts FactsSink

	mu      sync.Mutex
	members map[string]*member
	fileMod time.Time
	touched time.Time // the last request a browser made for the list or a report
	stop    chan struct{}
	started bool
}

// FactsSink receives the facts of one remote session under its hub id and cache key.
type FactsSink func(id string, f insights.Facts, fp store.Fingerprint)

// AgentError is a request the agent did not answer well: unreachable, a refused bearer, an
// error status. The server answers it as a bad gateway, not as an unknown session.
type AgentError struct {
	Agent string
	Err   error
}

func (e *AgentError) Error() string { return "agent " + e.Agent + ": " + e.Err.Error() }
func (e *AgentError) Unwrap() error { return e.Err }

// Polling (docs/AGENT-MODE.md §6.7): 60 s per agent while a browser is looking, 10 minutes
// otherwise, phase-shifted by the agent's name; three failures in a row drop to the idle rate.
const (
	activeWindow   = 10 * time.Minute
	activeInterval = 60 * time.Second
	idleInterval   = 10 * time.Minute
	pendingRetry   = 5 * time.Minute // a session the agent has not digested is asked for again after this
	maxFailures    = 3
	// FileCheck is how often a running hub looks for a changed agents file.
	FileCheck = 5 * time.Second
)

type member struct {
	f      *Fleet
	agent  Agent
	client *Client
	kick   chan struct{}
	stop   chan struct{}

	mu   sync.Mutex
	snap Snapshot
	rows map[string]model.SessionSummary // by the agent's own id
	want map[string]int64                // uuid → not before (ms): facts wanted
	seen map[string]string               // uuid → the version the last Follow-mode poll answered
}

// New prepares a fleet over the agents file at path and snapshots under dir. Nothing is read or
// dialled until Start; facts go nowhere until OnFacts.
func New(path, dir string) *Fleet {
	return &Fleet{path: path, dir: dir, members: map[string]*member{}, stop: make(chan struct{})}
}

// OnFacts sets where fetched facts go (the server's sidecar and memo).
func (f *Fleet) OnFacts(sink FactsSink) { f.facts = sink }

// Path is the agents file.
func (f *Fleet) Path() string { return f.path }

// Start loads the agents file and the snapshots and begins polling; a change to the file made
// by `todobem hub` is picked up within FileCheck.
func (f *Fleet) Start() error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return nil
	}
	f.started = true
	f.mu.Unlock()
	if err := f.Reload(); err != nil {
		return err
	}
	go f.watch()
	return nil
}

// Stop ends every poller (tests).
func (f *Fleet) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
	for name, m := range f.members {
		close(m.stop)
		m.client.Close()
		delete(f.members, name)
	}
}

func (f *Fleet) watch() {
	t := time.NewTicker(FileCheck)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
			st, err := os.Stat(f.path)
			f.mu.Lock()
			mod := f.fileMod
			f.mu.Unlock()
			if err == nil && !st.ModTime().Equal(mod) || err != nil && !mod.IsZero() {
				if err := f.Reload(); err != nil {
					log.Printf("fleet: %v", err)
				}
			}
		}
	}
}

// Reload re-reads the agents file: new agents start polling, removed ones stop, a changed
// bearer or address takes effect in place (the snapshot stays).
func (f *Fleet) Reload() error {
	agents, mod, err := LoadAgents(f.path)
	if err != nil {
		return err
	}
	// snapshots of new agents are read before the lock: a hundred gunzips need not stall a list
	f.mu.Lock()
	fresh := map[string]Snapshot{}
	for _, a := range agents {
		if _, ok := f.members[a.Name]; !ok {
			fresh[a.Name] = Snapshot{}
		}
	}
	f.mu.Unlock()
	for name := range fresh {
		snap, err := LoadSnapshot(f.dir, name)
		if err != nil {
			log.Printf("fleet: snapshot of %s unreadable, starting from nothing: %v", name, err)
		}
		fresh[name] = snap
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileMod = mod
	seen := map[string]bool{}
	for _, a := range agents {
		seen[a.Name] = true
		if m, ok := f.members[a.Name]; ok {
			if m.agent.Addr != a.Addr || m.agent.Pin != a.Pin {
				m.client.Close()
				m.client = NewClient(a.Addr, a.Pin, a.Bearer)
			} else if m.agent.Bearer != a.Bearer {
				m.client.SetBearer(a.Bearer)
			}
			m.agent = a
			continue
		}
		m := &member{f: f, agent: a, client: NewClient(a.Addr, a.Pin, a.Bearer), kick: make(chan struct{}, 1), stop: make(chan struct{}), rows: map[string]model.SessionSummary{}, want: map[string]int64{}, seen: map[string]string{}}
		m.snap = fresh[a.Name]
		for _, r := range m.snap.Rows {
			m.rows[r.ID] = r
		}
		if m.snap.Facts == nil {
			m.snap.Facts = map[string]int64{}
		}
		m.wantStale(nil)
		f.members[a.Name] = m
		go m.loop()
	}
	for name, m := range f.members {
		if !seen[name] {
			close(m.stop)
			m.client.Close()
			delete(f.members, name)
		}
	}
	return nil
}

// Touch records that a browser is looking (a list or report request). Coming back from idle
// wakes every poller at once so the list is fresh within seconds, not minutes.
func (f *Fleet) Touch() {
	f.mu.Lock()
	idle := time.Since(f.touched) > activeWindow
	f.touched = time.Now()
	members := f.membersLocked()
	f.mu.Unlock()
	if idle {
		for _, m := range members {
			m.wake()
		}
	}
}

func (f *Fleet) active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Since(f.touched) <= activeWindow
}

func (f *Fleet) membersLocked() []*member {
	out := make([]*member, 0, len(f.members))
	for _, m := range f.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].agent.Name < out[j].agent.Name })
	return out
}

func (f *Fleet) member(name string) (*member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.members[name]
	if !ok {
		return nil, fmt.Errorf("no agent named %s", name)
	}
	return m, nil
}

// Names lists the paired agents, sorted.
func (f *Fleet) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, m := range f.membersLocked() {
		names = append(names, m.agent.Name)
	}
	return names
}

// ---- the poller -----------------------------------------------------------------------------

func (m *member) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *member) loop() {
	h := fnv.New32a()
	h.Write([]byte(m.agent.Name))
	wait := time.Duration(h.Sum32()%uint32(activeInterval/time.Second)) * time.Second
	for {
		force := false
		select {
		case <-m.stop:
			return
		case <-m.kick:
			force = true // an explicit poll asks for the pending facts again too
		case <-time.After(wait):
		}
		m.poll(force)
		wait = m.interval()
	}
}

func (m *member) interval() time.Duration {
	m.mu.Lock()
	failures := m.snap.Attempts
	m.mu.Unlock()
	if failures >= maxFailures || !m.f.active() {
		return idleInterval
	}
	return activeInterval
}

// poll is one round: hello when needed, the list synced and reconciled, then the facts the
// delta made stale (force: the pending ones too, before their retry time). The snapshot is
// written when the rows or the facts changed and when reachability flipped.
func (m *member) poll(force bool) {
	now := time.Now().UnixMilli()
	m.mu.Lock()
	needHello := m.snap.Hello.Protocol == 0 || m.snap.Attempts > 0
	wasDown := m.snap.Attempts > 0
	m.mu.Unlock()
	if needHello {
		if err := m.hello(); err != nil {
			m.fail(now, err)
			return
		}
	}
	changed, restarted, err := m.syncList()
	if err != nil {
		m.fail(now, err)
		return
	}
	if restarted && !needHello {
		_ = m.hello() // a new boot: its build, rules and homes may have changed with it
	}
	m.mu.Lock()
	m.snap.LastOK, m.snap.LastErr, m.snap.Since, m.snap.Attempts = now, "", 0, 0
	m.mu.Unlock()
	fetched := m.fetchFacts(now, force)
	if changed || fetched || wasDown {
		m.save()
	}
}

func (m *member) hello() error {
	h, err := m.client.Hello()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.snap.Hello, m.snap.Incompatible = h, false
	m.mu.Unlock()
	return nil
}

func (m *member) fail(now int64, err error) {
	m.mu.Lock()
	m.snap.LastErr = err.Error()
	if m.snap.Since == 0 {
		m.snap.Since = now
	}
	m.snap.Attempts++
	first := m.snap.Attempts == 1
	if errors.Is(err, ErrIncompatible) {
		m.snap.Incompatible = true
	}
	m.mu.Unlock()
	if first {
		log.Printf("fleet: %s unreachable: %v", m.agent.Name, err)
		m.save()
	}
}

// syncList fetches the delta since the cursor, merges it, and proves the rows against the
// agent's count and hash — one full fetch heals a deleted rollout, a restarted agent or a lost
// update. restarted says the agent answered a full page to a known cursor (a new boot).
func (m *member) syncList() (changed, restarted bool, err error) {
	m.mu.Lock()
	cursor := m.snap.Cursor
	m.mu.Unlock()
	page, err := m.client.Sessions(cursor)
	if err != nil {
		return false, false, err
	}
	restarted = page.Full && cursor != ""
	changed = m.apply(page)
	if !m.consistent(page) {
		full, err := m.client.Sessions("")
		if err != nil {
			return changed, restarted, err
		}
		full.Full = true
		changed = m.apply(full) || changed
		if !m.consistent(full) {
			log.Printf("fleet: %s: the list does not add up even after a full fetch (count %d, hash %s)", m.agent.Name, full.Count, full.IDsHash)
		}
	}
	return changed, restarted, nil
}

// apply merges a page into the rows and queues the facts of the rows it changed. It reports
// whether any row changed.
func (m *member) apply(page SessionsPage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	if page.Full {
		fresh := make(map[string]model.SessionSummary, len(page.Rows))
		for _, r := range page.Rows {
			fresh[r.ID] = r
		}
		for id := range m.rows {
			if _, ok := fresh[id]; !ok {
				changed = true
				delete(m.want, id)
				delete(m.snap.Facts, id)
				delete(m.seen, id)
			}
		}
		for id, r := range fresh {
			if old, ok := m.rows[id]; !ok || !sameRow(old, r) {
				changed = true
			}
		}
		m.rows = fresh
		m.wantStale(nil)
	} else {
		for _, r := range page.Rows {
			if old, ok := m.rows[r.ID]; !ok || !sameRow(old, r) {
				changed = true
			}
			m.rows[r.ID] = r
		}
		m.wantStale(page.Rows)
	}
	m.snap.Cursor = page.Cursor
	return changed
}

// wantStale queues the facts of the given rows (nil: every row) whose held facts are not the
// row's current ones. Called under m.mu.
func (m *member) wantStale(rows []model.SessionSummary) {
	if rows == nil {
		for _, r := range m.rows {
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		if m.snap.Facts[r.ID] != r.Updated {
			if _, queued := m.want[r.ID]; !queued {
				m.want[r.ID] = 0
			}
		}
	}
}

// sameRow compares the fields a row is displayed and keyed by (Totals is a pointer: identity
// would say "changed" on every decode).
func sameRow(a, b model.SessionSummary) bool {
	return a.ID == b.ID && a.Updated == b.Updated && a.Bytes == b.Bytes && a.Title == b.Title && a.Question == b.Question &&
		a.LastAnswer == b.LastAnswer && a.Live == b.Live && a.Agents == b.Agents && a.Started == b.Started && (a.Totals == nil) == (b.Totals == nil)
}

// consistent proves the rows against the page's count and hash.
func (m *member) consistent(page SessionsPage) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.rows) != page.Count {
		return false
	}
	ids := make([]string, 0, len(m.rows))
	for id := range m.rows {
		ids = append(ids, id)
	}
	return IDsHash(ids) == page.IDsHash
}

// fetchFacts asks for the facts of the rows that want them, in batches, and hands each to the
// sink under the row's cache key — outside the member lock. Pending ids are asked again later.
func (m *member) fetchFacts(now int64, force bool) bool {
	m.mu.Lock()
	var ids []string
	for id, notBefore := range m.want {
		if force || notBefore <= now {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	m.mu.Unlock()
	fetched := false
	for len(ids) > 0 {
		n := min(MaxFactsIDs, len(ids))
		batch := ids[:n]
		ids = ids[n:]
		page, err := m.client.Facts(batch)
		if err != nil {
			log.Printf("fleet: %s: facts: %v", m.agent.Name, err)
			return fetched
		}
		type delivery struct {
			id string
			f  insights.Facts
			fp store.Fingerprint
		}
		var deliveries []delivery
		m.mu.Lock()
		if page.FactsVersion != insights.FactsVersion {
			// facts of another build are not comparable; ask again much later, the agent may be upgraded
			for _, id := range batch {
				m.want[id] = now + int64(idleInterval/time.Millisecond)
			}
			m.mu.Unlock()
			return fetched
		}
		for _, fact := range page.Facts {
			row, ok := m.rows[fact.ID]
			if !ok {
				continue
			}
			uuid := fact.ID
			fact.ID = Join(uuid, m.agent.Name)
			deliveries = append(deliveries, delivery{fact.ID, fact, m.fingerprintLocked(row)})
			m.snap.Facts[uuid] = row.Updated
			delete(m.want, uuid)
			fetched = true
		}
		for _, id := range page.Pending {
			m.want[id] = now + int64(pendingRetry/time.Millisecond)
		}
		for _, id := range page.Unknown {
			delete(m.want, id)
		}
		m.mu.Unlock()
		if m.f.facts != nil {
			for _, d := range deliveries {
				m.f.facts(d.id, d.f, d.fp)
			}
		}
	}
	return fetched
}

func (m *member) save() {
	m.mu.Lock()
	rows := make([]model.SessionSummary, 0, len(m.rows))
	for _, r := range m.rows {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	m.snap.Rows = rows
	snap := m.snap
	m.mu.Unlock()
	if err := SaveSnapshot(m.f.dir, m.agent.Name, snap); err != nil {
		log.Printf("fleet: %s: snapshot: %v", m.agent.Name, err)
	}
}

// fingerprintLocked is a remote row's cache key on the hub. Called under m.mu.
func (m *member) fingerprintLocked(row model.SessionSummary) store.Fingerprint {
	return store.RowFingerprint(m.snap.Hello.RulesFingerprint, row.ID, row.Bytes, row.Updated)
}

// ---- what the server asks -------------------------------------------------------------------

// Summaries lists every agent's rows with the host set and the id composite, unsorted (the
// server merges and sorts them with its own).
func (f *Fleet) Summaries() []model.SessionSummary {
	f.mu.Lock()
	members := f.membersLocked()
	f.mu.Unlock()
	var out []model.SessionSummary
	for _, m := range members {
		m.mu.Lock()
		if out == nil {
			out = make([]model.SessionSummary, 0, len(m.rows)*len(members))
		}
		for _, r := range m.rows {
			r.ID = Join(r.ID, m.agent.Name)
			r.Host = m.agent.Name
			out = append(out, r)
		}
		m.mu.Unlock()
	}
	return out
}

// lookup resolves a composite id to its member and row.
func (f *Fleet) lookup(id string) (*member, model.SessionSummary, string, error) {
	uuid, host, ok := Split(id)
	if !ok {
		return nil, model.SessionSummary{}, "", fmt.Errorf("not a remote session: %s", id)
	}
	m, err := f.member(host)
	if err != nil {
		return nil, model.SessionSummary{}, "", err
	}
	m.mu.Lock()
	row, ok := m.rows[uuid]
	m.mu.Unlock()
	if !ok {
		return nil, model.SessionSummary{}, "", fmt.Errorf("agent %s does not list session %s", host, uuid)
	}
	return m, row, uuid, nil
}

// Fingerprint is a remote session's cache key on the hub (zero for an id no agent lists).
func (f *Fleet) Fingerprint(id string) store.Fingerprint {
	m, row, _, err := f.lookup(id)
	if err != nil {
		return store.Fingerprint{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fingerprintLocked(row)
}

// Current says whether a model of that version is still what the agent serves, as far as the
// hub knows: false once a Follow-mode poll answered another version, so the next open asks the
// agent instead of trusting a cache key the pollers have not refreshed yet.
func (f *Fleet) Current(id, version string) bool {
	m, _, uuid, err := f.lookup(id)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	last, ok := m.seen[uuid]
	return !ok || last == version
}

// Model fetches a remote session's cache file from its agent: with etag (the version the hub
// holds) a 304 says the hub's copy is current (notModified, no model); refresh asks the agent
// to re-parse. The model comes back with the composite id and the host set; nothing is cached
// here.
func (f *Fleet) Model(id string, refresh bool, etag string) (m *model.Session, notModified bool, err error) {
	mem, _, uuid, err := f.lookup(id)
	if err != nil {
		return nil, false, err
	}
	file, notModified, err := mem.client.Model(uuid, etag, refresh)
	if err != nil {
		return nil, false, &AgentError{mem.agent.Name, err}
	}
	if notModified {
		return nil, true, nil
	}
	sess, _, ok := store.Decode(file)
	if !ok {
		mem.mu.Lock()
		theirs := mem.snap.Hello.CacheVersion
		mem.mu.Unlock()
		return nil, false, fmt.Errorf("%s: model unavailable, the agent's build differs (cache version %d, this hub %d): upgrade one of them", mem.agent.Name, theirs, store.CacheVersion())
	}
	sess.ID = id
	sess.Host = mem.agent.Name
	mem.mu.Lock()
	mem.seen[uuid] = sess.Version
	mem.mu.Unlock()
	return sess, false, nil
}

// Version proxies the Follow-mode poll and remembers the version it answered (Current).
func (f *Fleet) Version(id string) (json.RawMessage, error) {
	m, _, uuid, err := f.lookup(id)
	if err != nil {
		return nil, err
	}
	raw, err := m.client.Version(uuid)
	if err != nil {
		return nil, &AgentError{m.agent.Name, err}
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &v) == nil && v.Version != "" {
		m.mu.Lock()
		m.seen[uuid] = v.Version
		m.mu.Unlock()
	}
	return raw, nil
}

// Event proxies one recorded source span.
func (f *Fleet) Event(id string, src model.Src) ([]byte, error) {
	m, _, uuid, err := f.lookup(id)
	if err != nil {
		return nil, err
	}
	b, err := m.client.Event(uuid, src)
	if err != nil {
		return nil, &AgentError{m.agent.Name, err}
	}
	return b, nil
}

// AgentStatus is one row of the Servers table.
type AgentStatus struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol"` // 0: never reached; -1: does not speak v1
	Sessions int    `json:"sessions"`
	Digested int    `json:"digested"` // rows whose facts the hub holds
	LastOK   int64  `json:"last_ok,omitempty"`
	Since    int64  `json:"since,omitempty"`
	Error    string `json:"error,omitempty"`
	Expires  int64  `json:"expires"`
	ModelsOK bool   `json:"models_ok"` // the agent's cache version equals this hub's
	FactsOK  bool   `json:"facts_ok"`  // likewise the facts version
	Reached  bool   `json:"reached"`
}

// Status describes every agent.
func (f *Fleet) Status() []AgentStatus {
	f.mu.Lock()
	members := f.membersLocked()
	f.mu.Unlock()
	out := []AgentStatus{}
	for _, m := range members {
		m.mu.Lock()
		s := m.snap
		digested := 0
		for id, r := range m.rows {
			if s.Facts[id] == r.Updated {
				digested++
			}
		}
		reached := s.Hello.Protocol != 0
		st := AgentStatus{Name: m.agent.Name, Addr: m.agent.Addr, Version: s.Hello.Version, Protocol: s.Hello.Protocol, Sessions: len(m.rows), Digested: digested, LastOK: s.LastOK, Since: s.Since, Error: s.LastErr, Expires: m.agent.Expires, Reached: reached}
		if s.Incompatible {
			st.Protocol = -1
		}
		st.ModelsOK = reached && s.Hello.CacheVersion == store.CacheVersion()
		st.FactsOK = reached && s.Hello.FactsVersion == insights.FactsVersion
		m.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// ---- pairing and control (the Settings page and `todobem hub`) --------------------------------

// Pair dials an agent from its pairing string, redeems the token and returns the agent record
// to save. name "" takes the agent's own proposal (its hostname); addr "" the pairing string's.
func Pair(pairing, name, addr string) (Agent, Hello, error) {
	p, err := ParsePairing(pairing)
	if err != nil {
		return Agent{}, Hello{}, err
	}
	if addr != "" {
		p.Addr = addr
	}
	c := NewClient(p.Addr, p.Pin, "")
	defer c.Close()
	resp, err := c.Pair(p.Token)
	if err != nil {
		return Agent{}, Hello{}, fmt.Errorf("pairing with %s: %w", p.Addr, err)
	}
	if name == "" {
		name = DefaultName(resp.Hello.Hostname)
	}
	if err := CheckName(name); err != nil {
		return Agent{}, Hello{}, err
	}
	return Agent{Name: name, Addr: p.Addr, Pin: p.Pin, Bearer: resp.Bearer, Expires: resp.Expires}, resp.Hello, nil
}

// Add pairs an agent, records it in the agents file and polls it once. An existing name is
// replaced (a re-pairing after a lost bearer).
func (f *Fleet) Add(pairing, name, addr string) error {
	a, _, err := Pair(pairing, name, addr)
	if err != nil {
		return err
	}
	if err := Record(f.path, a); err != nil {
		return err
	}
	if err := f.Reload(); err != nil {
		return err
	}
	return f.PollNow(a.Name)
}

// Remove forgets an agent — its record and its snapshot — and returns the hub ids of its rows
// so the caller can drop their cache entries. With revoke the key on the agent is rotated first
// and the new bearer discarded, so nobody holds one.
func (f *Fleet) Remove(name string, revoke bool) ([]string, error) {
	m, err := f.member(name)
	if err != nil {
		return nil, err
	}
	if revoke {
		if _, err := m.client.Rotate(); err != nil {
			return nil, fmt.Errorf("revoke on %s: %w", name, err)
		}
	}
	if err := Forget(f.path, name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.rows))
	for uuid := range m.rows {
		ids = append(ids, Join(uuid, name))
	}
	m.mu.Unlock()
	if err := f.Reload(); err != nil {
		return nil, err
	}
	return ids, RemoveSnapshot(f.dir, name)
}

// Rotate stages a new key on the agent and records the bearer under it; the next poll retires
// the old key. A lost answer costs nothing: the old bearer keeps working until then.
func (f *Fleet) Rotate(name string) error {
	m, err := f.member(name)
	if err != nil {
		return err
	}
	resp, err := m.client.Rotate()
	if err != nil {
		return fmt.Errorf("rotate on %s: %w", name, err)
	}
	a := m.agent
	a.Bearer, a.Expires = resp.Bearer, resp.Expires
	if err := Record(f.path, a); err != nil {
		return err
	}
	log.Printf("fleet: %s: key rotated, new bearer recorded (expires %s)", name, ExpiresOn(a.Expires))
	return f.Reload()
}

// Doctor fetches an agent's health report.
func (f *Fleet) Doctor(name string) (json.RawMessage, error) {
	m, err := f.member(name)
	if err != nil {
		return nil, err
	}
	return m.client.Doctor()
}

// PollNow runs one poll of an agent synchronously (the Servers page's Poll now, pairing, tests)
// and reports how it went.
func (f *Fleet) PollNow(name string) error {
	m, err := f.member(name)
	if err != nil {
		return err
	}
	m.poll(true)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snap.LastErr != "" {
		return errors.New(m.snap.LastErr)
	}
	return nil
}
