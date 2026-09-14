package model

import (
	"testing"

	"github.com/extractumio/todobem/internal/classify"
)

func mkOp(id string, phase Phase, s, e int64, status string, identity string) *Operation {
	return &Operation{ID: id, Lane: "L", Phase: phase, Kind: "k", Start: s, End: e, Status: status, Identity: identity}
}

func fixture() *Session {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{{ID: "t1", Start: 100, End: 600, Status: "completed"}, {ID: "t2", Start: 800, End: 900, Status: "completed"}}
	l.Ops = []*Operation{
		mkOp("r1", classify.Code, 120, 130, "completed", ""),
		// two parallel commands inside one fan-out, overlapping: test wins by priority
		mkOp("c1", classify.Test, 200, 300, "failed", "swift test"),
		mkOp("c2", classify.Code, 250, 400, "completed", ""),
		mkOp("f1", classify.Code, 410, 420, "completed", ""),
		mkOp("c3", classify.Test, 450, 500, "completed", "swift test"),
		mkOp("m1", classify.LLM, 520, 590, "completed", ""),
	}
	return &Session{ID: "s", Lanes: []*Lane{l}}
}

func TestPartitionBalances(t *testing.T) {
	s := fixture()
	Derive(s, 2000)
	l := s.Lanes[0]
	var total int64
	for _, sg := range l.Segments {
		if sg.End <= sg.Start {
			t.Fatalf("empty segment %+v", sg)
		}
		total += sg.End - sg.Start
	}
	if total != l.Ended-l.Started {
		t.Fatalf("partition %d != elapsed %d", total, l.Ended-l.Started)
	}
	var sum int64
	for _, v := range s.Totals.ByPhase {
		sum += v
	}
	if sum != s.Totals.ElapsedMs {
		t.Fatalf("by_phase sum %d != elapsed %d", sum, s.Totals.ElapsedMs)
	}
	// exclusive vs raw: c1 and c2 overlap by 50 ms
	if s.Totals.RawOpsMs != 10+100+150+10+50+70 {
		t.Fatalf("raw ops %d", s.Totals.RawOpsMs)
	}
	if l.ByPhase[classify.Test] != 150 || l.ByPhase[classify.Code] != 10+100+10 { // r1 + c2 (after c1) + f1
		t.Fatalf("exclusive test=%d code=%d", l.ByPhase[classify.Test], l.ByPhase[classify.Code])
	}
	// outside turns: [0,100) + [600,800) + [900,1000) = 400 ms of wait_user
	if l.ByPhase[classify.WaitUser] != 400 {
		t.Fatalf("wait_user %d", l.ByPhase[classify.WaitUser])
	}
	// in-turn time (600) minus exclusive tool coverage (r1 10 + c1∪c2 200 + f1 10 + c3 50) is think
	if l.ByPhase[classify.LLM] != 600-10-200-10-50 {
		t.Fatalf("think %d", l.ByPhase[classify.LLM])
	}
	if s.Totals.InTurnMs != 600 {
		t.Fatalf("in-turn %d", s.Totals.InTurnMs)
	}
}

func TestGroupsAndRoles(t *testing.T) {
	s := fixture()
	Derive(s, 2000)
	if len(s.Groups) != 1 || s.Groups[0].Attempts != 2 || s.Groups[0].Failed != 1 {
		t.Fatalf("groups %+v", s.Groups)
	}
	get := func(id string) *Operation {
		for _, o := range s.Lanes[0].Ops {
			if o.ID == id {
				return o
			}
		}
		return nil
	}
	if RoleOf(get("c1").Kind) != "first" || RoleOf(get("c3").Kind) != "retry_after_failure" {
		t.Fatalf("attempt roles %s %s", get("c1").Kind, get("c3").Kind)
	}
	if RoleOf(get("f1").Kind) != "fix" || get("f1").Group != "G01" {
		t.Fatalf("fix role %s group %s", get("f1").Kind, get("f1").Group)
	}
	if RoleOf(get("r1").Kind) != "" {
		t.Fatalf("op before the failure must not be a fix: %s", get("r1").Kind)
	}
	// c2 overlaps the failed attempt (started before it ended) → not a fix
	if RoleOf(get("c2").Kind) != "" {
		t.Fatalf("overlapping op must not be a fix: %s", get("c2").Kind)
	}
}

func TestParallelSameIdentityIsNotRetry(t *testing.T) {
	s := fixture()
	l := s.Lanes[0]
	l.Ops = append(l.Ops, mkOp("c4", classify.Test, 460, 490, "completed", "swift test")) // overlaps c3
	Derive(s, 2000)
	for _, o := range l.Ops {
		if o.ID == "c4" && RoleOf(o.Kind) != "parallel" {
			t.Fatalf("c4 role %s", o.Kind)
		}
	}
}

func TestOpenTurnTailIsNoTelemetry(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 500}
	l.Turns = []*Turn{{ID: "t1", Start: 100, End: 300, Status: "open"}}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 900)
	if !s.Live || l.Ended != 900 {
		t.Fatalf("live %v ended %d", s.Live, l.Ended)
	}
	if l.ByPhase[classify.NoTelemetry] != 600 || l.ByPhase[classify.LLM] != 200 {
		t.Fatalf("no_telemetry %d think %d", l.ByPhase[classify.NoTelemetry], l.ByPhase[classify.LLM])
	}
}

func TestBackgroundProcessDoesNotOverlayPartition(t *testing.T) {
	const k = 1000 // work in seconds so the 1 s close-out tolerance is realistic
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000 * k}
	l.Turns = []*Turn{{ID: "t1", Start: 100 * k, End: 300 * k, Status: "completed"}, {ID: "t2", Start: 600 * k, End: 800 * k, Status: "completed"}}
	l.Ops = []*Operation{
		mkOp("srv", classify.Infra, 150*k, 950*k, "failed", ""), // dev server: outlives turn t1
		mkOp("r1", classify.Code, 200*k, 220*k, "completed", ""),
	}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000*k)
	if !l.Ops[0].Background || l.Ops[1].Background {
		t.Fatalf("background flags: %v %v", l.Ops[0].Background, l.Ops[1].Background)
	}
	if l.ByPhase[classify.Infra] != 0 || l.ByPhase[classify.WaitUser] != (100+300+200)*k || l.ByPhase[classify.Code] != 20*k {
		t.Fatalf("by_phase %v", l.ByPhase)
	}
	if s.Totals.BackgroundMs != 800*k || s.Totals.Background != 1 {
		t.Fatalf("background totals %d %d", s.Totals.BackgroundMs, s.Totals.Background)
	}
}

func TestPendingOperationsFillLiveEnvelope(t *testing.T) {
	for _, phase := range []Phase{classify.Test, classify.WaitWorker} {
		t.Run(string(phase), func(t *testing.T) {
			turn := &Turn{ID: "turn", Start: 100, End: 300, Status: "open"}
			op := &Operation{ID: "pending", Lane: "L", Turn: "turn", Phase: phase, Start: 300, End: 300, Open: true, Status: "running"}
			lane := &Lane{ID: "L", Started: 0, Ended: 300, Turns: []*Turn{turn}, Ops: []*Operation{op}}
			s := &Session{Lanes: []*Lane{lane}}
			for _, now := range []int64{900, 1200} {
				Derive(s, now)
				if op.End != now || s.Totals.ByPhase[phase] != now-op.Start || s.Totals.RawByPhase[phase] != now-op.Start {
					t.Fatalf("pending endpoint/totals disagree: end=%d totals=%+v", op.End, s.Totals)
				}
				if s.Totals.ByPhase[classify.NoTelemetry] != 0 || turn.End != 300 {
					t.Fatalf("pending wait became missing telemetry or changed recorded turn end: %+v", s.Totals)
				}
			}
		})
	}
}

func TestPendingOperationAtLiveBoundaryPreservesPartition(t *testing.T) {
	op := &Operation{ID: "pending", Lane: "L", Turn: "turn", Phase: classify.Test, Start: 900, End: 900, Open: true}
	turn := &Turn{ID: "turn", Start: 100, End: 900, Status: "open"}
	lane := &Lane{ID: "L", Started: 0, Ended: 900, Turns: []*Turn{turn}, Ops: []*Operation{op}}
	s := &Session{Lanes: []*Lane{lane}}
	Derive(s, 900)
	var partition int64
	for _, sg := range lane.Segments {
		partition += sg.End - sg.Start
		if sg.End > lane.Ended {
			t.Fatalf("segment exceeds live boundary: %+v", sg)
		}
	}
	if partition != s.Totals.ElapsedMs || op.Background || s.Totals.ByPhase[classify.Test] != 0 {
		t.Fatalf("instant pending op changed accounting: partition=%d totals=%+v background=%v", partition, s.Totals, op.Background)
	}
	Derive(s, 1200)
	if s.Totals.ByPhase[classify.Test] != 300 || op.End != 1200 {
		t.Fatalf("pending operation did not advance: %+v", s.Totals)
	}
	// Adapters replace display bounds with the authoritative buffered output/close.
	op.End, op.Open, op.Status = 1000, false, "completed"
	turn.End, turn.Status, lane.Ended = 1100, "completed", 1100
	Derive(s, 1400)
	if op.End != 1000 || s.Ended != 1100 || s.Totals.RawByPhase[classify.Test] != 100 || s.Totals.ByPhase[classify.Test] != 100 {
		t.Fatalf("recorded completion retained live bounds: op=%+v session=%+v", op, s)
	}
}

func TestInfraAttemptsFormGroupsAndLoneInfraStaysRecovery(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{{ID: "t", Start: 0, End: 1000, Status: "completed"}}
	l.Ops = []*Operation{
		// a test attempt fails, a lone infra command runs in between, the test is retried
		mkOp("t1", classify.Test, 100, 150, "failed", "go test"),
		mkOp("d0", classify.Infra, 160, 170, "completed", "docker compose up"),
		mkOp("t2", classify.Test, 200, 250, "completed", "go test"),
		// an infra command fails and is retried with the identical command: a group of its own
		mkOp("d1", classify.Infra, 300, 390, "failed", "docker run --rm img cmd"),
		mkOp("e1", classify.Code, 400, 410, "completed", ""),
		mkOp("d2", classify.Infra, 500, 520, "completed", "docker run --rm img cmd"),
		// a kill that exits 1 (nothing matched) carries no identity from the classifier
		// (infraAttemptKinds) and never becomes an attempt, whatever its status
		mkOp("k1", classify.Infra, 560, 561, "failed", ""),
		mkOp("k2", classify.Infra, 570, 571, "failed", ""),
		// an identical infra command repeated with every run succeeding is a poll, not retries
		mkOp("p1", classify.Infra, 600, 610, "completed", "ssh host cat lease.json"),
		mkOp("p2", classify.Infra, 700, 710, "completed", "ssh host cat lease.json"),
		mkOp("p3", classify.Infra, 800, 810, "completed", "ssh host cat lease.json"),
	}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	get := func(id string) *Operation {
		for _, o := range l.Ops {
			if o.ID == id {
				return o
			}
		}
		return nil
	}
	if len(s.Groups) != 2 {
		t.Fatalf("groups %+v", s.Groups)
	}
	if get("k1").Group != "" || get("k2").Group != "" || get("p1").Group != "" || get("p3").Group != "" || RoleOf(get("p2").Kind) != "" {
		t.Fatalf("all-successful infra repeats must not group: %s/%s", get("p1").Group, get("p2").Kind)
	}
	if RoleOf(get("d1").Kind) != "first" || RoleOf(get("d2").Kind) != "retry_after_failure" || get("d2").Group != get("d1").Group {
		t.Fatalf("infra attempts: %s %s (%s/%s)", get("d1").Kind, get("d2").Kind, get("d1").Group, get("d2").Group)
	}
	if RoleOf(get("e1").Kind) != "fix" || get("e1").Group != get("d1").Group {
		t.Fatalf("code between infra attempts is a fix: %s %s", get("e1").Kind, get("e1").Group)
	}
	// d0 has an identity but no second occurrence: not an attempt, so it is still the recovery
	// between the two test attempts
	if RoleOf(get("d0").Kind) != "infra_recovery" || get("d0").Group == "" {
		t.Fatalf("lone infra op between test attempts: %s group=%q", get("d0").Kind, get("d0").Group)
	}
}

func TestQueryMissesAreNotFailures(t *testing.T) {
	// the harness records status "failed" and the exit code for a search with no match or a
	// read of a missing path; the record stays literal, the failure count does not include it
	one, two := 1, 2
	s := fixture()
	l := s.Lanes[0]
	miss := mkOp("q1", classify.Code, 132, 134, "failed", "")
	miss.Kind, miss.Exit = "search", &one
	missing := mkOp("q2", classify.Code, 136, 138, "failed", "")
	missing.Kind, missing.Exit = "read", &two
	edit := mkOp("e1", classify.Code, 140, 142, "failed", "")
	edit.Kind, edit.Exit = "sed -i", &one
	probe := mkOp("p1", classify.Code, 150, 152, "failed", "")
	probe.Kind, probe.Exit = "probe", &one
	l.Ops = append(l.Ops, miss, missing, edit, probe)
	Derive(s, 2000)
	if !miss.QueryMiss || !missing.QueryMiss || edit.QueryMiss {
		t.Fatalf("query_miss: search=%v read=%v sed -i=%v", miss.QueryMiss, missing.QueryMiss, edit.QueryMiss)
	}
	if miss.Status != "failed" || *miss.Exit != 1 {
		t.Fatalf("the literal record changed: %+v", miss)
	}
	// c1 (test, failed) + e1 (edit, exit 1) are failures; the three query misses are counted apart
	if s.Totals.Failed != 2 || s.Totals.QueryMisses != 3 {
		t.Fatalf("failed=%d query_misses=%d", s.Totals.Failed, s.Totals.QueryMisses)
	}
	if probe.Failure() || !edit.Failure() {
		t.Fatalf("Failure: probe=%v edit=%v", probe.Failure(), edit.Failure())
	}
}

func TestTotalsTokensSumLanes(t *testing.T) {
	s := fixture()
	s.Lanes[0].Tokens = &TokenUsage{Input: 100, Cached: 40, Output: 10, Reasoning: 5, Total: 110}
	child := &Lane{ID: "C", Path: "/root/c", Parent: "L", Depth: 1, Started: 100, Ended: 500, Tokens: &TokenUsage{Input: 30, Output: 3, Total: 33}}
	child.Turns = []*Turn{{ID: "ct", Start: 100, End: 500, Status: "completed"}}
	s.Lanes = append(s.Lanes, child)
	Derive(s, 2000)
	want := TokenUsage{Input: 130, Cached: 40, Output: 13, Reasoning: 5, Total: 143}
	if s.Totals.Tokens != want {
		t.Fatalf("tokens %+v, want %+v", s.Totals.Tokens, want)
	}
}
