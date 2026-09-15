package insights

import (
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
	if r := detectInvalidToolCalls(&f); len(r.Findings) != 1 || r.Findings[0].A != 2*minute {
		t.Fatalf("D14 %+v", r.Findings)
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
	if x := byKey["build"]; x.N != 1 || x.TimeMs != 5*minute {
		t.Fatalf("D16 build %+v", x)
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
	if r := detectContextSize(&f); !r.Measurable || len(r.Findings) != 3 || r.Findings[0].Key != "200 k or more" || r.Findings[1].Key != "50-100 k" {
		t.Fatalf("T2 %+v", r.Findings)
	}
	if r := detectTokensByModel(&f); !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "main thread · m / high" {
		t.Fatalf("T3 %+v", r.Findings)
	}
	if r := detectReasoningShare(&f); !r.Measurable || len(r.Findings) != 1 || r.Findings[0].Key != "high" {
		t.Fatalf("T7 %+v", r.Findings)
	}
	// no sub-agents: T6 is not measurable
	if r := detectSpawnCost(&f); r.Measurable || r.Reason == "" {
		t.Fatalf("T6 without sub-agents %+v", r)
	}
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
	if !ok || !lb.Info || lb.Exposure.TimeMs != 15*hour {
		t.Fatalf("D2b %+v", lb)
	}
	if you.TimeMs >= 15*hour || you.Cards[len(you.Cards)-1].Rule != "D2b" && !you.Cards[len(you.Cards)-1].Info {
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
	// invalid tool calls: three occurrences across sessions are shown, one is not
	if c, ok := cards["D14"]; !ok || c.Exposure.Count != 3 {
		t.Fatalf("D14 %+v", c)
	}
	r1 := Build(Params{CWD: "/proj", Period: Period{Kind: "30d"}.Resolve(now)}, []Input{mk("a")}, now)
	for _, g := range r1.Groups {
		for _, c := range g.Cards {
			if c.Rule == "D14" {
				t.Fatal("D14 shown below three occurrences")
			}
		}
	}
	// no-data rows: T6 cannot be measured without sub-agents
	found := false
	for _, nd := range r.NoData {
		if nd.Rule == "T6" && nd.Sessions == 3 && nd.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no-data rows %+v", r.NoData)
	}
	// D2 stats: median and p90 of the reply gaps
	if c := cards["D2"]; c.Stats["median"] != 20*minute || c.Stats["p90"] != 20*minute || c.Share == nil || c.Share.Of != "elapsed" {
		t.Fatalf("D2 %+v", c)
	}
}
