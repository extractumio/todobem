package model

import (
	"strings"
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
	// an edit the harness completed on the same millisecond its turn closed names that turn and
	// is the turn's close-out write, not a background op (15 such patches in one real session)
	closeOut := mkOp("e1", classify.Code, 300*k, 300*k, "completed", "")
	closeOut.Kind, closeOut.Turn = "edit", "t1"
	stray := mkOp("e2", classify.Code, 300*k, 300*k, "completed", "")
	stray.Kind = "edit" // no turn named: outside every turn, background as before
	l.Ops = append(l.Ops, closeOut, stray)
	Derive(s, 2000*k)
	if closeOut.Background || !stray.Background {
		t.Fatalf("close-out edit background=%v, stray edit background=%v", closeOut.Background, stray.Background)
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

// TestKilledCommandsAreNotFailures: a command that ended with SIGINT (130) or SIGTERM (143)
// was stopped, not judged; the record stays literal, the failure count leaves it out.
func TestKilledCommandsAreNotFailures(t *testing.T) {
	one, sigint, sigterm := 1, 130, 143
	s := fixture()
	l := s.Lanes[0]
	Derive(s, 200)
	before := s.Totals.Failed
	failed := mkOp("f1", classify.Code, 132, 134, "failed", "")
	failed.Kind, failed.Exit = "shell", &one
	killed := mkOp("f2", classify.Code, 136, 138, "failed", "")
	killed.Kind, killed.Exit = "shell", &sigterm
	interrupted := mkOp("f3", classify.Test, 140, 142, "failed", "")
	interrupted.Kind, interrupted.Exit = "go test", &sigint
	l.Ops = append(l.Ops, failed, killed, interrupted)
	Derive(s, 200)
	if !failed.Failure() || killed.Failure() || interrupted.Failure() || killed.Status != "failed" {
		t.Fatalf("failure: exit 1=%v exit 143=%v exit 130=%v status=%q", failed.Failure(), killed.Failure(), interrupted.Failure(), killed.Status)
	}
	if s.Totals.Failed != before+1 {
		t.Fatalf("failed count %d, want %d", s.Totals.Failed, before+1)
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
	// a harness that records no exit code (Claude Code's is_error): a failed read is still a
	// query miss, a failed edit is still a failure
	noExitRead := mkOp("q3", classify.Code, 154, 156, "failed", "")
	noExitRead.Kind = "read"
	noExitEdit := mkOp("e2", classify.Code, 158, 160, "failed", "")
	noExitEdit.Kind = "edit"
	l.Ops = append(l.Ops, miss, missing, edit, probe, noExitRead, noExitEdit)
	Derive(s, 2000)
	if !miss.QueryMiss || !missing.QueryMiss || edit.QueryMiss || !noExitRead.QueryMiss || noExitEdit.QueryMiss {
		t.Fatalf("query_miss: search=%v read=%v sed -i=%v read(no exit)=%v edit(no exit)=%v", miss.QueryMiss, missing.QueryMiss, edit.QueryMiss, noExitRead.QueryMiss, noExitEdit.QueryMiss)
	}
	if miss.Status != "failed" || *miss.Exit != 1 {
		t.Fatalf("the literal record changed: %+v", miss)
	}
	// c1 (test, failed) + e1 (edit, exit 1) + e2 (edit, no exit) are failures; the four query
	// misses are counted apart
	if s.Totals.Failed != 3 || s.Totals.QueryMisses != 4 {
		t.Fatalf("failed=%d query_misses=%d", s.Totals.Failed, s.Totals.QueryMisses)
	}
	if probe.Failure() || !edit.Failure() || noExitRead.Failure() || !noExitEdit.Failure() {
		t.Fatalf("Failure: probe=%v edit=%v read(no exit)=%v edit(no exit)=%v", probe.Failure(), edit.Failure(), noExitRead.Failure(), noExitEdit.Failure())
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

// TestPendingUserQuestionLiveTailIsWaitUser: when the agent has handed control to the user (an
// open wait_user op — Claude Code AskUserQuestion, Codex request_user_input), the live tail of
// the open turn up to now reads "waiting for user", never "no telemetry". This is the shared
// derive guarantee both adapters rely on; the alternative (an open turn with no covering op)
// stays no_telemetry, because then nothing was recorded and nothing is inferred.
func TestPendingUserQuestionLiveTailIsWaitUser(t *testing.T) {
	mk := func(op *Operation) *Session {
		l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000, Live: true}
		l.Turns = []*Turn{{ID: "t1", Start: 100, End: 500, Status: "open"}}
		if op != nil {
			l.Ops = []*Operation{op}
		}
		return &Session{ID: "s", Lanes: []*Lane{l}}
	}
	q := &Operation{ID: "q", Lane: "L", Turn: "t1", Phase: classify.WaitUser, Kind: "question", Start: 500, End: 500, Status: "running", Open: true}
	s := mk(q)
	Derive(s, 100000)
	l := s.Lanes[0]
	if l.ByPhase[classify.NoTelemetry] != 0 {
		t.Fatalf("a pending user question must not read as no telemetry: %v", l.ByPhase)
	}
	tail := l.Segments[len(l.Segments)-1]
	if tail.Phase != classify.WaitUser || tail.End != 100000 {
		t.Fatalf("live tail = %+v; want wait_user to now", tail)
	}
	// control: an open turn with no covering op is honestly no_telemetry
	s = mk(nil)
	Derive(s, 100000)
	if got := s.Lanes[0].ByPhase[classify.NoTelemetry]; got == 0 {
		t.Fatal("an open turn with no recorded activity should stay no_telemetry")
	}
}

// A sub-agent whose file stopped mid-turn after the root closed is not live: its turn is
// orphaned at its last evidence, the session's end is the root's, and no telemetry gap grows
// until now. While the root is live, the sub-agent stays live.
func TestSubAgentOpenTurnAfterRootClosedIsOrphaned(t *testing.T) {
	mk := func(rootStatus string) *Session {
		root := &Lane{ID: "R", Path: "/root", Started: 0, Ended: 1000, Turns: []*Turn{{ID: "t1", Start: 0, End: 1000, Status: rootStatus}}}
		sub := &Lane{ID: "S", Path: "/root/a", Parent: "R", Depth: 1, Started: 100, Ended: 600, Turns: []*Turn{{ID: "u1", Start: 100, End: 600, Status: "open"}}}
		sub.Ops = []*Operation{{ID: "g", Lane: "S", Turn: "u1", Phase: classify.Code, Kind: "read", Start: 200, End: 300, Status: "completed"}}
		return &Session{ID: "s", Lanes: []*Lane{root, sub}}
	}
	s := mk("completed")
	Derive(s, 100000)
	sub := s.Lanes[1]
	if sub.Live || sub.Turns[0].Status != "orphaned" || sub.Ended != 600 || s.Ended != 1000 || s.Live {
		t.Fatalf("closed root: live=%v status=%q ended=%d session ended=%d live=%v", sub.Live, sub.Turns[0].Status, sub.Ended, s.Ended, s.Live)
	}
	if sub.ByPhase["no_telemetry"] != 0 {
		t.Fatalf("no telemetry gap must not grow after the root closed: %v", sub.ByPhase)
	}
	s = mk("open")
	Derive(s, 100000)
	sub = s.Lanes[1]
	if !sub.Live || sub.Turns[0].Status != "open" || sub.Ended != 100000 || !s.Live {
		t.Fatalf("live root: live=%v status=%q ended=%d session live=%v", sub.Live, sub.Turns[0].Status, sub.Ended, s.Live)
	}
}

// A compound command shares its wall clock among its categories: a sleep takes its literal
// seconds, the rest is split equally, the slices sit back to back, the partition and the raw
// sum follow the split, the estimate is summed apart, and the stages follow each slice.
func TestCompoundCommandSharesItsWallClock(t *testing.T) {
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{{ID: "t1", Start: 100, End: 900, Status: "completed"}}
	res := classify.Command("sleep 0.1 && go build ./... && go test ./...", "")
	op := mkOp("c1", res.Phase, 200, 700, "completed", res.Identity)
	op.Turn, op.Kind = "t1", res.Kind
	op.Shares = SharesOf(res.Parts)
	single := mkOp("c2", classify.Build, 750, 800, "completed", "")
	single.Kind = "go build"
	l.Ops = []*Operation{op, single}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	if op.Phase != classify.Test || len(op.Shares) != 3 {
		t.Fatalf("dominant phase %s, shares %+v", op.Phase, op.Shares)
	}
	// wall 500: sleep 100 (literal), build 200, test 200
	want := []Share{{Phase: classify.WaitWorker, Ms: 100, Literal: true}, {Phase: classify.Build, Ms: 200}, {Phase: classify.Test, Ms: 200}}
	for i, w := range want {
		g := op.Shares[i]
		if g.Phase != w.Phase || g.Ms != w.Ms || g.Literal != w.Literal {
			t.Fatalf("share %d = %+v, want %+v", i, g, w)
		}
	}
	if op.Shares[1].Sub != "compile" || op.Shares[1].Kind != "go build" || op.Shares[2].Kind != "go test" {
		t.Fatalf("share kinds %+v", op.Shares)
	}
	var total int64
	for _, sg := range l.Segments {
		total += sg.End - sg.Start
	}
	if total != 1000 {
		t.Fatalf("partition %d", total)
	}
	if l.ByPhase[classify.WaitWorker] != 100 || l.ByPhase[classify.Build] != 250 || l.ByPhase[classify.Test] != 200 {
		t.Fatalf("by_phase %+v", l.ByPhase)
	}
	if l.ByPhaseShared[classify.Build] != 200 || l.ByPhaseShared[classify.Test] != 200 || l.ByPhaseShared[classify.WaitWorker] != 0 {
		t.Fatalf("by_phase_shared %+v (the sleep is literal, c2 is whole)", l.ByPhaseShared)
	}
	if l.RawByPhase[classify.Build] != 250 || l.RawByPhase[classify.Test] != 200 || l.RawByPhase[classify.WaitWorker] != 100 {
		t.Fatalf("raw by phase %+v", l.RawByPhase)
	}
	// the slices in order, each carrying its sub-row and the shared mark
	var slices []Segment
	for _, sg := range l.Segments {
		if sg.Op == "c1" {
			slices = append(slices, sg)
		}
	}
	if len(slices) != 3 || slices[0].Phase != classify.WaitWorker || slices[0].Start != 200 || slices[0].End != 300 || slices[0].Shared ||
		slices[1].Phase != classify.Build || slices[1].End != 500 || !slices[1].Shared || slices[1].Sub != "compile" ||
		slices[2].Phase != classify.Test || slices[2].End != 700 || !slices[2].Shared {
		t.Fatalf("slices %+v", slices)
	}
	// stages: the build slice serves implementation, the test slice verification
	if slices[1].Lifecycle != classify.LcImplement || slices[2].Lifecycle != classify.LcTest || op.Shares[1].Lifecycle != classify.LcImplement || op.Shares[2].Lifecycle != classify.LcTest {
		t.Fatalf("stages %s %s (shares %+v)", slices[1].Lifecycle, slices[2].Lifecycle, op.Shares)
	}
	var lc, ph int64
	for _, v := range l.ByLifecycle {
		lc += v
	}
	for _, v := range l.ByPhase {
		ph += v
	}
	if lc != ph || s.Totals.ByPhaseShared[classify.Build] != 200 {
		t.Fatalf("lifecycle %d vs phase %d, totals shared %+v", lc, ph, s.Totals.ByPhaseShared)
	}
	// a single-category command is never split
	if single.Shares != nil || SharesOf(classify.Command("gofmt -w x.go && go vet ./... && go test ./...", "").Parts) != nil {
		t.Fatal("single-category commands must not be split")
	}
	// a skill's stage covers every slice
	l2 := &Lane{ID: "L2", Path: "/root", Started: 0, Ended: 1000}
	l2.Turns = []*Turn{{ID: "t1", Start: 100, End: 900, Status: "completed", Skill: "code-review-cc"}}
	op2 := mkOp("c1", res.Phase, 200, 700, "completed", "")
	op2.Turn, op2.Shares = "t1", SharesOf(res.Parts)
	l2.Ops = []*Operation{op2}
	Derive(&Session{ID: "s2", Lanes: []*Lane{l2}}, 2000)
	if op2.Shares[1].Lifecycle != classify.LcReview || op2.Shares[2].Lifecycle != classify.LcReview {
		t.Fatalf("skill turn shares %+v", op2.Shares)
	}
}

// The invariants a split op must keep whatever surrounds it: the categories are (phase,
// sub-row); the slices survive the turn-boundary split; a slice competes by its own phase
// with concurrent ops; a live op's shares move with its growing wall clock.
func TestCompoundCommandInvariants(t *testing.T) {
	// deps and compile are two categories, not one build share named by the first
	res := classify.Command("npm ci && npm run build && npm test", "")
	shares := SharesOf(res.Parts)
	if len(shares) != 3 || shares[0].Sub != "deps" || shares[1].Sub != "compile" || shares[2].Phase != classify.Test {
		t.Fatalf("shares %+v", shares)
	}
	// a split op crossing a turn boundary keeps its slices' sub-row and estimate mark
	l := &Lane{ID: "L", Path: "/root", Started: 0, Ended: 1000}
	l.Turns = []*Turn{{ID: "t1", Start: 100, End: 400, Status: "completed"}, {ID: "t2", Start: 400, End: 900, Status: "completed"}}
	op := mkOp("c1", res.Phase, 200, 800, "completed", "")
	op.Turn, op.Kind, op.Shares = "t1", res.Kind, SharesOf(res.Parts)
	// a concurrent measured build of lower priority than the test slice it overlaps
	other := mkOp("c2", classify.Build, 700, 850, "completed", "")
	other.Turn, other.Kind = "t2", "go build"
	l.Ops = []*Operation{op, other}
	s := &Session{ID: "s", Lanes: []*Lane{l}}
	Derive(s, 2000)
	if op.Shares[0].Ms+op.Shares[1].Ms+op.Shares[2].Ms != 600 {
		t.Fatalf("shares must sum to the wall clock: %+v", op.Shares)
	}
	var seen []string
	for _, sg := range l.Segments {
		if sg.Op == "c1" {
			seen = append(seen, string(sg.Phase)+":"+sg.Sub+":"+map[bool]string{true: "est", false: "meas"}[sg.Shared])
		}
	}
	// deps 200 → [200,400) crosses nothing; compile 200 → [400,600); test 200 → [600,800): the
	// test slice (priority 80) outranks the concurrent go build (70) from 700 to 800
	if got := strings.Join(seen, " "); got != "build:deps:est build:compile:est test::est" {
		t.Fatalf("slices %q", got)
	}
	if l.ByPhase[classify.Build] != 200+200+50 || l.ByPhase[classify.Test] != 200 || l.ByPhaseShared[classify.Build] != 400 {
		t.Fatalf("by_phase %+v shared %+v", l.ByPhase, l.ByPhaseShared)
	}
	for k, v := range l.ByPhaseShared {
		if v > l.ByPhase[k] {
			t.Fatalf("shared %s %d > by_phase %d", k, v, l.ByPhase[k])
		}
	}
	var total int64
	for _, sg := range l.Segments {
		total += sg.End - sg.Start
	}
	if total != 1000 {
		t.Fatalf("partition %d", total)
	}
	// a live op: its shares follow the wall clock on every refresh
	live := &Lane{ID: "L2", Path: "/root", Started: 0, Ended: 500}
	live.Turns = []*Turn{{ID: "t1", Start: 100, End: 500, Status: "open"}}
	o := mkOp("c1", classify.Test, 200, 200, "running", "")
	o.Turn, o.Open, o.Shares = "t1", true, SharesOf(classify.Command("go build ./... && go test ./...", "").Parts)
	live.Ops = []*Operation{o}
	Derive(&Session{ID: "s2", Lanes: []*Lane{live}}, 1000)
	first := o.Shares[0].Ms + o.Shares[1].Ms
	Derive(&Session{ID: "s2", Lanes: []*Lane{live}}, 1400)
	if second := o.Shares[0].Ms + o.Shares[1].Ms; first != 800 || second != 1200 || o.Shares[0].Ms != 600 {
		t.Fatalf("live shares %d then %d (%+v)", first, second, o.Shares)
	}
	// A short first refresh must not destroy the requested sleep duration used by later ones.
	sleep := mkOp("c2", classify.Test, 200, 200, "running", "")
	sleep.Open, sleep.Shares = true, SharesOf(classify.Command("sleep 1 && go test ./...", "").Parts)
	shareWallClock(sleep, 700)
	shareWallClock(sleep, 1400)
	if sleep.Shares[0].Ms != 1000 || sleep.Shares[1].Ms != 200 {
		t.Fatalf("literal sleep changed across refreshes: %+v", sleep.Shares)
	}
}
