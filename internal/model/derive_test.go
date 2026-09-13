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
