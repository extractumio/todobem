package insights

import (
	"strings"
	"testing"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
)

const hour = 3600e3

// rootWithGaps: three root turns; the first asks a question and is followed by a 20-minute
// wait, the second by a 5-hour break; the third is aborted.
func rootWithGaps(t *testing.T) *model.Lane {
	t.Helper()
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 8 * hour, Tokens: &model.TokenUsage{Input: 3500, Cached: 2800, Output: 250, Reasoning: 100, Total: 3750}}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 10 * minute, Status: "completed", Model: "m", Effort: "high", Tokens: usage(1000, 900, 100), First: usage(1000, 900, 100), Responses: 4, ContextPeak: 210000},
		{ID: "t2", Start: 30 * minute, End: 40 * minute, Status: "completed", Trigger: "user", Model: "m", Effort: "high", Tokens: usage(2000, 1500, 100), First: usage(200, 100, 10), Responses: 3, ContextPeak: 60000},
		{ID: "t3", Start: 5*hour + 40*minute, End: 7 * hour, Status: "aborted", Trigger: "user", Model: "m", Effort: "high", Tokens: usage(500, 400, 50), First: usage(500, 10, 50), Responses: 9, ContextPeak: 90000},
	}
	root.Markers = []model.Marker{
		{T: 9 * minute, Kind: "question", Lane: "R", Turn: "t1", Text: "which?"},
		{T: 2 * minute, Kind: "llm_error", Lane: "R", Turn: "t1", Text: "bad args"},
	}
	edit := op("e1", "R", classify.Code, "edit", 3*minute, 3*minute+1, "failed")
	edit.Turn, edit.Title = "t1", "edit x.go"
	unknown := op("u1", "R", classify.Unknown, "unknown", 4*minute, 6*minute, "completed")
	unknown.Turn, unknown.Title = "t1", "mytool --run"
	bg := op("b1", "R", classify.Infra, "docker", 31*minute, 3*hour, "completed")
	bg.Turn, bg.Title = "t2", "docker compose up"
	long := op("l1", "R", classify.Build, "build-flag", 33*minute, 38*minute, "completed")
	long.Turn, long.Identity, long.Title = "t2", "/proj\n./dev build --all", "./dev build --all"
	root.Ops = []*model.Operation{edit, unknown, bg, long}
	return root
}

func TestYouAndTheAgentDetectors(t *testing.T) {
	f := Extract(session(t, rootWithGaps(t)))
	if r := detectWaitingOnAnswer(&f); len(r.Findings) != 1 || r.Findings[0].TimeMs != 20*minute || r.Findings[0].Key != "15-60 min" {
		t.Fatalf("D1 %+v", r.Findings)
	}
	if r := detectReplyLatency(&f); len(r.Findings) != 1 || r.Findings[0].TimeMs != 20*minute {
		t.Fatalf("D2 %+v", r.Findings)
	}
	if r := detectLongBreaks(&f); len(r.Findings) != 1 || r.Findings[0].TimeMs != 5*hour || r.Findings[0].Key != "long break" {
		t.Fatalf("D2b %+v", r.Findings)
	}
	if r := detectStoppedTurns(&f); len(r.Findings) != 1 || r.Findings[0].TimeMs != 80*minute || r.Findings[0].Key != "main thread" || r.Findings[0].Tokens == nil {
		t.Fatalf("D3 %+v", r.Findings)
	}
	// the tail of the session (after t3) is a wait with no next turn: never a reply or a break
	root := rootWithGaps(t)
	root.Turns[2].Status = "completed"
	root.Markers = nil
	f = Extract(session(t, root))
	if r := detectWaitingOnAnswer(&f); len(r.Findings) != 0 {
		t.Fatalf("D1 without a question %+v", r.Findings)
	}
	if r := detectStoppedTurns(&f); len(r.Findings) != 0 {
		t.Fatalf("D3 without an aborted turn %+v", r.Findings)
	}
}

func TestRunsUnseenAndModelDetectors(t *testing.T) {
	f := Extract(session(t, rootWithGaps(t)))
	if r := detectBackground(&f); len(r.Findings) != 1 || r.Findings[0].Key != "infra docker compose" {
		t.Fatalf("D12 %+v", r.Findings)
	}
	if r := detectLongRuns(&f); len(r.Findings) != 1 || r.Findings[0].Key != "build ./dev build" || r.Findings[0].TimeMs != 5*minute {
		t.Fatalf("D9 %+v", r.Findings)
	}
	if r := detectUnknown(&f); len(r.Findings) != 1 || r.Findings[0].Key != "mytool" || r.Stats["unknown_ms"] != 2*minute {
		t.Fatalf("D13 %+v stats %v", r.Findings, r.Stats)
	}

	if r := detectFailedEdits(&f); len(r.Findings) != 1 || r.Findings[0].Key != "code:edit" || r.Findings[0].Note != "edit x.go · edit" {
		t.Fatalf("D15 %+v", r.Findings)
	}
	// D16: every tool call of the main thread by phase and subgroup; the edit failed, the docker
	// run is a background op (left out), the build and the unknown command count
	r16 := detectToolCalls(&f)
	byKey := map[string]Finding{}
	for _, x := range r16.Findings {
		byKey[x.Key] = x
	}
	if x := byKey["code:edit"]; x.N != 1 || x.Note != "1 call, 1 failed" || x.TimeMs != 1 {
		t.Fatalf("D16 code:edit %+v (all %+v)", x, r16.Findings)
	}
	if x := byKey["build:compile"]; x.N != 1 || x.TimeMs != 5*minute {
		t.Fatalf("D16 build:compile %+v (all %+v)", x, r16.Findings)
	}
	if x := byKey["unknown:command"]; x.N != 1 || x.TimeMs != 2*minute {
		t.Fatalf("D16 unknown:command %+v", x)
	}
	if _, ok := byKey["infra"]; ok {
		t.Fatalf("D16 must leave background ops out: %+v", r16.Findings)
	}
	if r16.Stats["calls_code:edit"] != 1 || r16.Stats["failed_code:edit"] != 1 || r16.Stats["misses_code:edit"] != 0 {
		t.Fatalf("D16 stats %v", r16.Stats)
	}
	if r := detectUnknown(&f); r.Stats["unknown_command_ms"] != 2*minute || r.Stats["unknown_script_ms"] != 0 {
		t.Fatalf("D13 subgroup stats %v", r.Stats)
	}
	r := detectModelTime(&f)
	if len(r.Findings) != 3 || r.Findings[0].Key != "m / high" || r.Findings[0].TimeMs <= 0 {
		t.Fatalf("M1 %+v", r.Findings)
	}
	// the stage split plus the output of turns without a tool call (not a stage) plus the output
	// nearest an unknown command is the whole model time
	var stage int64
	for k, v := range r.Stats {
		if len(k) > 6 && k[:6] == "stage_" {
			if k == "stage_llm" || k == "stage_unknown" {
				t.Fatalf("M1 reports a non-stage as a stage: %s", k)
			}
			stage += v
		}
	}
	var llm int64
	for _, x := range r.Findings {
		llm += x.TimeMs
	}
	if stage+r.Stats["no_tool_call_ms"]+r.Stats["unknown_ms"] != llm {
		t.Fatalf("M1 split %d + %d + %d != llm time %d", stage, r.Stats["no_tool_call_ms"], r.Stats["unknown_ms"], llm)
	}
	if r := detectContextSize(&f); !r.Measurable || len(r.Findings) != 3 || r.Findings[0].Key != "200-500 k" || r.Findings[1].Key != "50-100 k" {
		t.Fatalf("T2 %+v", r.Findings)
	}
	// the scale continues past 200 k: 1M-context models put most turns there
	for peak, want := range map[int64]string{120000: "100-150 k", 320000: "200-500 k", 600000: "500-750 k", 900000: "750 k or more"} {
		if got := ContextBucket(peak); got != want {
			t.Errorf("ContextBucket(%d) = %q, want %q", peak, got, want)
		}
	}
	if r := detectTokensByModel(&f); !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "main thread · m / high" {
		t.Fatalf("T3 %+v", r.Findings)
	}
	if r := detectReasoningShare(&f); !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "high" {
		t.Fatalf("T7 %+v", r.Findings)
	}
	// no sub-agents: T6 and D4 do not apply; a Claude Code session cannot carry T7
	if r := detectSpawnCost(&f); !r.NotApplicable || r.Measurable {
		t.Fatalf("T6 without sub-agents %+v", r)
	}
	if r := detectSerialDelegation(&f); !r.NotApplicable {
		t.Fatalf("D4 without sub-agents %+v", r)
	}
	if f.Source = "claude"; detectReasoningShare(&f).Measurable {
		t.Fatal("T7 must not measure a Claude Code session")
	}
	f.Source = "codex"
	// a sub-agent with a first call: spawn cost; one without: no data
	a := &model.Lane{ID: "A", Path: "/root/a", Parent: "R", Depth: 1, Started: minute, Ended: 5 * minute, Tokens: usage(1000, 500, 100)}
	a.Turns = []*model.Turn{{ID: "a1", Start: minute, End: 5 * minute, Status: "completed", Tokens: usage(1000, 500, 100), First: usage(900, 450, 10)}}
	b := &model.Lane{ID: "B", Path: "/root/b", Parent: "R", Depth: 1, Started: minute, Ended: 5 * minute, Tokens: usage(1000, 500, 100)}
	b.Turns = []*model.Turn{{ID: "b1", Start: minute, End: 5 * minute, Status: "completed"}}
	f = Extract(session(t, rootWithGaps(t), a, b))
	r = detectSpawnCost(&f)
	if !r.Measurable || len(r.Findings) != 1 || r.NoData != 1 || r.Stats["more_to_start"] != 1 || r.Findings[0].Tokens.Input != 900 {
		t.Fatalf("T6 %+v", r)
	}
}

// TestNoTelemetryIntervals: D18 lists the root's no_telemetry segments with the turn that never
// closed; a session whose turns all closed has none.
func TestNoTelemetryIntervals(t *testing.T) {
	root := &model.Lane{ID: "R", Path: "/root", Started: 0, Ended: 3 * hour}
	root.Turns = []*model.Turn{
		{ID: "t1", Start: 0, End: 10 * minute, Status: "orphaned", Trigger: "user"}, // the next prompt arrived 2 h later
		{ID: "t2", Start: 2*hour + 10*minute, End: 2*hour + 20*minute, Status: "completed", Trigger: "user"},
		{ID: "t3", Start: 2*hour + 30*minute, End: 2*hour + 40*minute, Status: "open", Trigger: "user"}, // the log ends inside it
	}
	f := Extract(session(t, root))
	if len(f.Blind) != 2 || f.Blind[0].Turn != "t1" || f.Blind[0].Status != "orphaned" || f.Blind[0].End-f.Blind[0].Start != 2*hour || f.Blind[1].Turn != "t3" || f.Blind[1].Status != "open" {
		t.Fatalf("blind %+v", f.Blind)
	}
	r := detectNoTelemetry(&f)
	if len(r.Findings) != 2 || r.Findings[0].Key != "Codex" || r.Findings[0].TimeMs != 2*hour || !strings.Contains(r.Findings[0].Note, "next prompt") || !strings.Contains(r.Findings[1].Note, "log ends") {
		t.Fatalf("D18 %+v", r.Findings)
	}
	if f.Root.ByPhase["no_telemetry"] != r.Findings[0].TimeMs+r.Findings[1].TimeMs {
		t.Fatalf("D18 findings %d ms, root no_telemetry %d ms", r.Findings[0].TimeMs+r.Findings[1].TimeMs, f.Root.ByPhase["no_telemetry"])
	}
	root.Turns[0].Status, root.Turns[2].Status = "completed", "completed"
	if f = Extract(session(t, root)); len(f.Blind) != 0 {
		t.Fatalf("closed turns leave no blind interval: %+v", f.Blind)
	}
}

func TestBuildAppliesInfoAndCrossSessionFilters(t *testing.T) {
	now := int64(100 * day)
	mk := func(id string) Input {
		root := rootWithGaps(t)
		root.ID = id + "-R"
		for _, o := range root.Ops {
			o.Lane = root.ID
		}
		s := &model.Session{ID: id, Title: "s " + id, CWD: "/proj", Lanes: []*model.Lane{root}}
		model.Derive(s, root.Ended)
		f := Extract(s)
		f.Ended = now - day
		return Input{Facts: f, Fingerprint: id}
	}
	r := Build(Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}, []Input{mk("a"), mk("b"), mk("c")}, now)
	cards := map[string]Card{}
	var you Group
	for _, g := range r.Groups {
		if g.ID == GroupYou {
			you = g
		}
		for _, c := range g.Cards {
			cards[c.Rule] = c
		}
	}
	// long breaks are Info: shown, never in the group total nor in the top findings
	lb, ok := cards["D2b"]
	if !ok || lb.Class != ClassInfo || lb.Exposure.TimeMs != 15*hour {
		t.Fatalf("D2b %+v", lb)
	}
	if you.TimeMs >= 15*hour || you.Cards[len(you.Cards)-1].Rule != "D2b" && you.Cards[len(you.Cards)-1].Class != ClassInfo {
		t.Fatalf("group you: time %d cards %v", you.TimeMs, you.Cards)
	}
	for _, id := range r.TopTime {
		if id == "D2b" {
			t.Fatal("an Info card reached the top findings")
		}
	}
	// long runs: the same shape in three sessions passes the "2 or more" filter
	if c, ok := cards["D9"]; !ok || c.Exposure.Count != 3 || c.Distribution[0].Sessions != 3 {
		t.Fatalf("D9 %+v", c)
	}

	// without sub-agents T6 and D4 do not apply: no card, no no-data row
	for _, nd := range r.NoData {
		if nd.Rule == "T6" || nd.Rule == "D4" {
			t.Fatalf("a not-applicable rule reached the no-data rows: %+v", nd)
		}
	}
	if _, ok := cards["T6"]; ok {
		t.Fatal("T6 card without sub-agents")
	}
	// measurements of totals and of the user's own pace are info cards: never in the top findings
	for _, id := range append(append([]string{}, r.TopTime...), r.TopTokens...) {
		if id == "M1" || id == "T2" || id == "T3" || id == "D2" {
			t.Fatalf("a measurement reached the top findings: %v %v", r.TopTime, r.TopTokens)
		}
	}
	if cards["M1"].Class != ClassInfo || cards["T2"].Class != ClassInfo || cards["T3"].Class != ClassInfo || cards["D2"].Class != ClassInfo {
		t.Fatal("M1, T2, T3 and D2 are measurements")
	}
	// D2 stats: median and p90 of the reply gaps
	if c := cards["D2"]; c.Stats["median"] != 20*minute || c.Stats["p90"] != 20*minute || c.Share == nil || c.Share.Of != "elapsed" {
		t.Fatalf("D2 %+v", c)
	}
	// T1 stats: the 20-minute break before t2 and the 5-hour break before t3 are starts after
	// 15 minutes; their first calls read 100 + 490 uncached input tokens (a T1 finding carries
	// the break as its interval, never as time exposure)
	if c := cards["T1"]; c.Stats["starts_after_15m"] != 6 || c.Stats["uncached_after_15m"] != 3*590 {
		t.Fatalf("T1 stats %v", c.Stats)
	}
	// D3: the share is the main thread's stopped turns over its time in turns
	if c := cards["D3"]; c.Share == nil || c.Share.Of != "in_turn" || c.Share.OfMs != 3*(10*minute+10*minute+80*minute) || c.Stats["time_main"] != 3*80*minute {
		t.Fatalf("D3 %+v", c)
	}
}
