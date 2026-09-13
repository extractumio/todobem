# Normalized event schema

Every source adapter emits the same structures. All times are Unix milliseconds (UTC).

## Session
```
Session {
  id            string     // root thread id
  source        string     // "codex"
  title         string     // thread name from index, else first user message
  cwd, branch   string
  started, ended int64     // ms; ended = last event (or now while live)
  live          bool       // root file grew during the last refresh
  lanes         Lane[]     // lanes[0] is the root
  groups        Group[]    // retry / iteration groups (session-wide)
  totals        Totals     // root-lane exclusive wall-clock accounting
  parallel      { agent_ms, wall_ms }  // sub-agent time, reported separately
  version       string     // changes when any file grew
}
```

## Lane (= one agent thread)
```
Lane {
  id        string   // thread id
  path      string   // "/root", "/root/design_review", "/root/a/b"
  parent    string   // parent lane id ("" for root)
  role, nickname, model string
  depth     int
  file      string   // rollout path
  started, ended int64
  turns     Turn[]      // {id, start, end, status: completed|aborted|open|orphaned, trigger: user|system}
  ops       Operation[] // may overlap (parallel commands)
  segments  Segment[]   // exclusive partition of [started, ended]
  stages    Stage[]     // coalesced view of segments (think attributed to next op)
  markers   Marker[]    // point events
  active    [start,end][] // sub-agent: intervals between started/interacted and completed/interrupted
}
```

## Operation
```
Operation {
  id       string    // source item id / call id
  lane     string
  turn     string
  phase    Phase     // see table
  kind     string    // sub-kind, see table
  start, end int64
  open     bool      // no end recorded yet
  status   string    // completed | failed | aborted | running | recorded
  exit     *int      // exit code when known
  title    string    // short human label (command head / file / tool)
  detail   string    // full command / file list (≤ 2 KB)
  identity string    // "<cwd>\n<normalized command>" for test/build/release ops ("" otherwise)
  group    string    // group id when member of a retry group
  attempt  int       // attempt number within the group (test/build/release ops)
  parallel int       // number of sibling commands started in the same tool call
  background bool    // outlived its turn (dev server, watcher): thin bar, excluded from totals
  src      {file, off, len}   // exact source line for the inspector
}
```

### Phases
| phase | meaning | rule source |
|---|---|---|
| `llm` | the model generating: `Reasoning`/`AgentMessage` items (verified, `by_kind.llm_verified`) plus uncovered in-turn time (convention, `by_kind.llm_gap`); includes the generation of every patch | turn boundaries + item timestamps |
| `code` | everything about writing code: reading/searching sources, listing, web/MCP lookups, file edits, local VCS, formatting, scripted reads/writes | Codex `parsed_cmd`, `FileChange`, `apply_patch`, command table |
| `build` | compiling / bundling | command table |
| `test` | running tests, simulators, UI automation | command table |
| `release` | push, PR/MR, CI, deploy, device install, publish | command table |
| `infra` | containers, remote hosts, processes, cleanup, packages | command table |
| `wait_worker` | waiting for sub-agents, CI, remote leases, sleeps, process polls; also idle time before a harness-triggered turn (goal loop) | tool names, command table (`sleep` loops, `--watch`), `Turn.trigger` |
| `wait_user` | outside a turn on the root lane, before a user-triggered turn | turn boundaries |
| `idle` | sub-agent outside a turn (waiting for the parent) | state machine |
| `compaction` | context compaction by the harness | `ContextCompaction` |
| `no_telemetry` | open turn with no events yet (`open_turn`), or turn never closed (`orphaned_turn`) | state machine |
| `unknown` | command matched no rule, opaque scripts | classifier fallback |

### Kinds used by the "inside testing" breakdown
Attempts of a retry group: `first`, `retry_after_failure` (previous attempt failed), `rerun`
(previous passed/aborted), `parallel` (started before the previous attempt ended).
Other ops on the same lane between a failed attempt and its retry, when exactly one group is
in that state: `fix` (code), `infra_recovery` (infra), `worker_queue` (wait_worker).
`remote` flag when the command targets a remote host (`ssh`, `*_REMOTE_HOST=`, `--remote`);
`queued` flag when a `while … sleep` loop precedes the work inside one command.
The kind is stored as `"<tool kind>|<role>"`.

### Script bodies (heredocs) — literal signals only
A `python3 - <<PY` / `node --input-type=module <<JS` body is classified by, in priority order:
literal commands passed to `subprocess.*` / `os.system` / `execSync` (classified with the same
table, one level deep, kind `script→<inner kind>`); a file written with a literal body
(`Path(f).write_text(...)`, `cat > f <<EOF`) and executed by a later segment (`bash f`, `./f`)
is classified by that body; assertion statements or a playwright/puppeteer import →
`test/script-check`; file writes → `code/script-write`; only reads/queries/probes (no DML,
no writes) → `code/script-read`; anything else → `unknown`.
Only interpreter source is inspected this way. Heredocs consumed as data (for example by
`cat`, or by Python running a named script) remain opaque unless a written script is later
executed in the same command.

### Replayed history in forked sub-agents
A sub-agent file forked from its parent (`fork_turns: all`) starts with the parent's
`task_started`/`task_complete` lines (parent turn ids, same timestamp as the file, no items).
Only child turns with no operations, final answer, or markers beyond the start and injected
context are eligible for removal: superseded within 5 s, or completed within 1 s. Injected
context moves to the real turn when superseded. Root turns and turns with user messages,
plans, or questions are retained. A `task_complete` for a turn the file never opened is ignored.

### Pending tool envelopes
An operation awaiting output advances to the current time while its turn is open. Its
recorded output replaces that display endpoint when it arrives, including buffered output
whose timestamp precedes the previous refresh. The exclusive partition stays within lane bounds.

### Provisional ops
When an `exec` call returns (`yield_time_ms`) before its commands finished, an op is synthesized
from the literal `cmd:` strings in the JS input with status `running`; the real
`CommandExecution` item (same command text) replaces it when it arrives.

### Background processes
An op whose end lies past the end of the turn it started in (1 s tolerance), or that started
outside any turn, is `background`: the log proves the agent moved on while the process kept
running (dev servers, watchers, a leftover `npm start`). Background ops are drawn as a thin
bar under the lane, summed in `totals.background_ms / background_ops`, and never overlay the
partition, the raw sums or retry groups.

## Segment / Stage
```
Segment { start, end, phase, op }   // exclusive; op = the winning op id or ""
Stage   { start, end, phase, ops:int, turn }
```

## Marker
```
Marker { t, kind, lane, turn, text, ref }
kind: user_message | system_message (harness-injected, ref = tag) | question | final_answer |
      agent_started | agent_interacted | agent_completed | agent_interrupted |
      result_returned | message_sent | message_received | plan | turn_start | turn_end |
      compaction | interrupted | resumed | goal
```

## Group
```
Group { id, identity, phase, title, start, end, attempts:int, failed:int, members: op ids[] }
```
Command identity preserves quoted arguments and their internal whitespace. It removes
supported output redirections and trailing cosmetic pipe stages; complex shell syntax is
kept verbatim when safe normalization is uncertain.

## Totals
```
Totals { elapsed_ms, in_turn_ms, raw_ops_ms, by_phase: {phase: ms}, raw_by_phase: {phase: ms},
         by_kind: {kind: ms}, ops, turns, user_messages, system_messages, questions,
         compactions, failed_ops, background_ms, background_ops }
```
`sum(by_phase) == elapsed_ms` always (partition property, tested). `raw_ops_ms` is the plain
sum of op durations and is larger when commands ran in parallel inside one lane.

## Command classification table (head word → phase)
Rules are evaluated per top-level shell segment; the operation takes the highest-priority
phase found. Priority: release > test > build > workers > infra > code > unknown.
The table lives in `internal/classify/classify.go` and is the single source of truth; the
`/api/rules` endpoint exposes it so the UI "How to read" page shows the live table.
