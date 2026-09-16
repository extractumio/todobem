package insights

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

// factsWithWait builds a closed session whose root waited solo for one sub-agent for wait ms
// (no wait op at all when wait is 0).
func factsWithWait(t *testing.T, id string, ended int64, wait int64) Facts {
	t.Helper()
	root := &model.Lane{ID: id + "-R", Path: "/root", Started: ended - 10*minute, Ended: ended}
	root.Turns = []*model.Turn{{ID: "t1", Start: ended - 10*minute, End: ended, Status: "completed"}}
	if wait > 0 {
		w := op("w", root.ID, classify.WaitWorker, "agent", ended-9*minute, ended-9*minute+wait, "completed")
		w.Turn = "t1"
		root.Ops = []*model.Operation{w}
	}
	a := &model.Lane{ID: id + "-A", Path: "/root/a", Parent: root.ID, Depth: 1, Started: ended - 9*minute, Ended: ended}
	a.Turns = []*model.Turn{{ID: "a1", Start: ended - 9*minute, End: ended - 9*minute + wait, Status: "completed"}}
	// a second sub-agent after the wait: two sub-agents that never overlapped (D4 applies)
	b := &model.Lane{ID: id + "-B", Path: "/root/b", Parent: root.ID, Depth: 1, Started: ended - minute, Ended: ended}
	b.Turns = []*model.Turn{{ID: "b1", Start: ended - minute, End: ended, Status: "completed"}}
	s := &model.Session{ID: id, Source: "codex", Title: "session " + id, CWD: "/proj", Lanes: []*model.Lane{root, a, b}}
	model.Derive(s, ended)
	return Extract(s)
}

func TestPeriodMembershipAndResolution(t *testing.T) {
	now := int64(100 * day)
	p := Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}
	if p.Period.From != now-30*day || p.Period.To != now {
		t.Fatalf("resolved %+v", p.Period)
	}
	if !p.InPeriod("a", now-day, false) || p.InPeriod("b", now-31*day, false) || p.InPeriod("c", now-day, true) {
		t.Fatal("membership by last activity, live excluded")
	}
	one := Params{Period: Period{Kind: "session", Session: "x"}}
	if !one.InPeriod("x", 0, true) || one.InPeriod("y", now, false) {
		t.Fatal("one-session period selects that id only, live or not")
	}
	if d := (Period{Kind: "1d"}).Resolve(now); d.From != now-day || d.To != now {
		t.Fatalf("1d: %+v", d)
	}
	if (Period{Kind: "bogus"}).Resolve(now).Kind != "30d" {
		t.Fatal("unknown kinds fall back to 30 days")
	}
}

func TestBuildRanksGroupsAndKeepsEvidence(t *testing.T) {
	now := int64(100 * day)
	inputs := []Input{
		{Facts: factsWithWait(t, "s1", now-day, 4*minute), Fingerprint: "f1"},
		{Facts: factsWithWait(t, "s2", now-2*day, 2*minute), Fingerprint: "f2"},
		{Facts: factsWithWait(t, "s3", now-3*day, 0), Fingerprint: "f3"},
	}
	r := Build(Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}, inputs, now)
	if r.Fallback != "" || r.Scope.Sessions != 3 || len(r.Sources) != 3 || r.SourcesHash == "" {
		t.Fatalf("scope %+v fallback %q", r.Scope, r.Fallback)
	}
	var agents *Group
	for i := range r.Groups {
		if r.Groups[i].ID == GroupAgents {
			agents = &r.Groups[i]
		}
	}
	if agents == nil || len(agents.Cards) != 1 {
		t.Fatalf("groups %+v", r.Groups)
	}
	card := agents.Cards[0]
	if card.Rule != "D4" || card.Exposure.TimeMs != 6*minute || card.Sessions != 2 || card.Of != 3 || card.NoData != 0 {
		t.Fatalf("card %+v", card)
	}
	if card.Share == nil || card.Share.Of != "in_turn" || card.Share.OfMs != 30*minute {
		t.Fatalf("share %+v", card.Share)
	}
	if len(card.Evidence) != 2 || card.Evidence[0].Session != "s1" || card.Evidence[0].Title != "session s1" || card.Evidence[0].TimeMs != 4*minute {
		t.Fatalf("evidence %+v", card.Evidence)
	}
	top := false
	for _, id := range r.TopTime {
		top = top || id == "D4"
	}
	if !top || len(r.TopTokens) != 0 {
		t.Fatalf("top %v %v", r.TopTime, r.TopTokens)
	}
	// fewer than three sessions: the report says so but still carries the cards
	r = Build(Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}, inputs[:2], now)
	if r.Fallback != "fewer_than_3_sessions" || len(r.Groups) < 1 {
		t.Fatalf("fallback %q groups %d", r.Fallback, len(r.Groups))
	}
	if r = Build(Params{}, nil, now); r.Fallback != "no_sessions" || len(r.Groups) != 0 {
		t.Fatalf("empty report %+v", r)
	}
}

type fakeLoader struct {
	mu    sync.Mutex
	calls []string
	fail  string
}

func (l *fakeLoader) Parse(id string) (*model.Session, error) {
	l.mu.Lock()
	l.calls = append(l.calls, id)
	l.mu.Unlock()
	if id == l.fail {
		return nil, errors.New("boom")
	}
	time.Sleep(5 * time.Millisecond)
	root := &model.Lane{ID: id, Path: "/root", Started: 0, Ended: minute}
	root.Turns = []*model.Turn{{ID: "t", Start: 0, End: minute, Status: "completed"}}
	s := &model.Session{ID: id, Lanes: []*model.Lane{root}}
	model.Derive(s, minute)
	return s, nil
}

func TestScannerRunsOnceCountsErrorsAndCancels(t *testing.T) {
	loader := &fakeLoader{fail: "bad"}
	var mu sync.Mutex
	got := map[string]bool{}
	sc := NewScanner(loader, 2, func(id string, f Facts) {
		mu.Lock()
		got[id] = true
		mu.Unlock()
	})
	if !sc.Start([]string{"a", "bad", "c"}) {
		t.Fatal("start refused")
	}
	if sc.Start([]string{"d"}) {
		t.Fatal("a second scan must be refused while one runs")
	}
	deadline := time.Now().Add(2 * time.Second)
	for sc.Progress().Running && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	p := sc.Progress()
	if p.Running || p.Done != 3 || p.Errors != 1 || p.LastError == "" || p.FinishedAt == 0 {
		t.Fatalf("progress %+v", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if !got["a"] || !got["c"] || got["bad"] {
		t.Fatalf("sink got %v", got)
	}
	// cancel: a long queue stops early
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = "x"
	}
	sc2 := NewScanner(&fakeLoader{}, 1, nil)
	sc2.Start(ids)
	sc2.Cancel()
	deadline = time.Now().Add(2 * time.Second)
	for sc2.Progress().Running && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if p := sc2.Progress(); p.Running || !p.Cancelled || p.Done >= 200 {
		t.Fatalf("cancelled progress %+v", p)
	}
}

func TestOneSessionReportHasNoFallback(t *testing.T) {
	now := int64(100 * day)
	in := []Input{{Facts: factsWithWait(t, "s1", now-day, 4*minute), Fingerprint: "f1"}}
	r := Build(Params{CWD: "/elsewhere", Period: Period{Kind: "session", Session: "s1"}.Resolve(now)}, in, now)
	if r.Fallback != "" || r.Scope.Sessions != 1 {
		t.Fatalf("fallback %q scope %+v", r.Fallback, r.Scope)
	}
}

// A check card is ranked by sessions affected, never totalled; a session the rule does not
// apply to is in neither the denominator nor "no data"; headline stats come from the detector,
// not from the wording of a note.
func TestCheckCardsRankByAffectedSessionsAndSkipNotApplicable(t *testing.T) {
	check := Detector{ID: "X1", Group: GroupFailures, Title: "A check", Class: ClassCheck}
	results := []Result{
		{Measurable: true, Key: "Codex", Findings: []Finding{{Session: "s1", Key: "Codex", TimeMs: 0, N: 1}}},
		{NotApplicable: true},
		{Measurable: false, Reason: "no records"},
		{Measurable: true, Key: "Claude Code"},
	}
	titles := map[string]string{"s1": "one"}
	card, nd := buildCard(check, results, &Scope{RootInTurn: minute}, titles)
	if card == nil || card.Sessions != 1 || card.Of != 2 || card.NoData != 1 || card.NotApplicable != 1 || nd.Sessions != 1 {
		t.Fatalf("card %+v nd %+v", card, nd)
	}
	// the row's own denominator: the measurable sessions of its key, with or without a finding
	if len(card.Distribution) != 1 || card.Distribution[0].Label != "Codex" || card.Distribution[0].Of != 1 {
		t.Fatalf("rows %+v", card.Distribution)
	}
	// D7's headline numbers are stats the detector emitted, D11's the finding's Value
	if s := statsProbe(t, "D7", []Finding{{Note: "anything"}}, map[string]int64{"groups": 2, "attempts": 5}); s["groups"] != 2 || s["attempts"] != 5 {
		t.Fatalf("D7 stats %+v", s)
	}
	if s := statsProbe(t, "D11", []Finding{{Key: "main thread", Value: 90_000}, {Key: "main thread", Value: 120_000}, {Key: "sub-agents", Value: 30_000}}, nil); s["context_median"] != 90_000 || s["context_min"] != 30_000 || s["context_max"] != 120_000 {
		t.Fatalf("D11 stats %+v", s)
	}
	// through Build: a check card sits after the exposure cards of its group, before the
	// measurements, adds nothing to the group's time and reaches TopChecks
	saved := Catalogue
	defer func() { Catalogue = saved }()
	Catalogue = append([]Detector{{ID: "X1", Group: GroupAgents, Title: "A check", Class: ClassCheck, Run: func(f *Facts) Result {
		return Result{Measurable: true, Findings: []Finding{{Lane: "/root", A: f.Started, B: f.Ended, N: 1}}}
	}}}, saved...)
	now := int64(100 * day)
	inputs := []Input{
		{Facts: factsWithWait(t, "s1", now-day, 4*minute), Fingerprint: "f1"},
		{Facts: factsWithWait(t, "s2", now-2*day, 2*minute), Fingerprint: "f2"},
		{Facts: factsWithWait(t, "s3", now-3*day, 0), Fingerprint: "f3"},
	}
	r := Build(Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}, inputs, now)
	var agents Group
	for _, g := range r.Groups {
		if g.ID == GroupAgents {
			agents = g
		}
	}
	if len(agents.Cards) < 2 || agents.Cards[0].Rule != "D4" || agents.Cards[1].Rule != "X1" || agents.Cards[1].Class != ClassCheck || agents.Cards[1].Sessions != 3 {
		t.Fatalf("agents group cards %+v", agents.Cards)
	}
	if agents.TimeMs != 6*minute {
		t.Fatalf("a check adds nothing to the group's time: %d", agents.TimeMs)
	}
	if len(r.TopChecks) != 1 || r.TopChecks[0] != "X1" {
		t.Fatalf("top checks %v", r.TopChecks)
	}
	for _, id := range r.TopTime {
		if id == "X1" {
			t.Fatal("a check card reached the time ranking")
		}
	}
}

// statsProbe runs statsFor on a card with the given findings and pre-summed stats.
func statsProbe(t *testing.T, rule string, all []Finding, stats map[string]int64) map[string]int64 {
	t.Helper()
	c := &Card{Rule: rule, Stats: map[string]int64{}}
	for k, v := range stats {
		c.Stats[k] = v
	}
	for _, x := range all {
		key := x.Key
		if key == "" {
			key = "all"
		}
		c.Distribution = append(c.Distribution, Row{Label: key, N: 1})
	}
	statsFor(rule, c, all)
	return c.Stats
}
